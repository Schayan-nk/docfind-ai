package dependencies

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// clock is a settable time for the breaker's cooldowns.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newReg(c *clock, s Spec) *Registry {
	r := New()
	r.now = c.now
	r.Register(s)
	return r
}

var errDown = errors.New("connection refused")

func fail(context.Context) error { return errDown }
func ok(context.Context) error   { return nil }

// Three failures open the breaker; while open, calls are refused without
// being made; after the cooldown one probe goes through, and its failure
// doubles the cooldown.
func TestTheBreakerOpensFailsFastAndBacksOff(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)}
	r := newReg(c, Spec{Name: "pm", Cooldown: 10 * time.Second, MaxCooldown: 25 * time.Second})
	ctx := context.Background()
	for range 3 {
		r.Do(ctx, "pm", fail)
	}
	calls := 0
	err := r.Do(ctx, "pm", func(context.Context) error { calls++; return nil })
	if !errors.Is(err, ErrOpen) || calls != 0 {
		t.Fatalf("open breaker: err %v, %d calls made", err, calls)
	}
	if s, _ := r.Status("pm"); s.Breaker != Open || s.State != Offline || s.ErrorClass != "refused" {
		t.Errorf("status = %+v", s)
	}

	c.add(11 * time.Second) // cooldown over: one probe
	if err := r.Do(ctx, "pm", fail); !errors.Is(err, errDown) {
		t.Fatalf("the probe was not made: %v", err)
	}
	c.add(15 * time.Second) // 20 s now: still open
	if err := r.Do(ctx, "pm", ok); !errors.Is(err, ErrOpen) {
		t.Errorf("after a failed probe the cooldown did not double: %v", err)
	}
	c.add(6 * time.Second)
	if err := r.Do(ctx, "pm", ok); err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if s, _ := r.Status("pm"); s.Breaker != Closed || s.State != Live || s.Failures != 0 {
		t.Errorf("after a good probe: %+v", s)
	}
}

// While one probe is in flight nobody else gets through.
func TestOnlyOneProbeAtATime(t *testing.T) {
	c := &clock{t: time.Now()}
	r := newReg(c, Spec{Name: "pg", FailThreshold: 1})
	ctx := context.Background()
	r.Do(ctx, "pg", fail)
	c.add(time.Minute)
	release := make(chan struct{})
	started := make(chan struct{})
	go r.Do(ctx, "pg", func(context.Context) error { close(started); <-release; return nil })
	<-started
	if err := r.Do(ctx, "pg", ok); !errors.Is(err, ErrOpen) {
		t.Errorf("a second caller got through during the probe: %v", err)
	}
	if r.Allow("pg") {
		t.Error("Allow said yes during the probe")
	}
	close(release)
}

// A caller who gives up has not seen the dependency fail.
func TestACancelledCallerDoesNotCountAsAFailure(t *testing.T) {
	c := &clock{t: time.Now()}
	r := newReg(c, Spec{Name: "ai", FailThreshold: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Do(ctx, "ai", func(ctx context.Context) error { return ctx.Err() })
	if s, _ := r.Status("ai"); s.Failures != 0 || s.Breaker != Closed {
		t.Errorf("status = %+v", s)
	}
}

// A call slower than MaxWait is cut off, and a slow success is "degraded".
func TestMaxWaitAndSlowness(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "oa", MaxWait: 20 * time.Millisecond, SlowAfter: 5 * time.Millisecond})
	err := r.Do(context.Background(), "oa", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if Classify(err) != "timeout" {
		t.Errorf("slow call: %v (%s)", err, Classify(err))
	}
	r.Do(context.Background(), "oa", func(context.Context) error { time.Sleep(8 * time.Millisecond); return nil })
	if s, _ := r.Status("oa"); s.State != Degraded {
		t.Errorf("slow success is %s", s.State)
	}
}

// Failing but with a recent answer is "stale"; with none, "offline".
func TestStaleThenOffline(t *testing.T) {
	c := &clock{t: time.Now()}
	r := newReg(c, Spec{Name: "pm", FailThreshold: 1, MaxStale: time.Hour})
	ctx := context.Background()
	r.Do(ctx, "pm", ok)
	c.add(time.Minute)
	r.Do(ctx, "pm", fail)
	if s, _ := r.Status("pm"); s.State != Stale || s.DataAgeSec != 60 {
		t.Errorf("after a failure: %s, age %d", s.State, s.DataAgeSec)
	}
	c.add(2 * time.Hour)
	if s, _ := r.Status("pm"); s.State != Offline {
		t.Errorf("two hours later: %s", s.State)
	}
}

// Fetch answers from the cache, marked as such, when the live call fails.
func TestFetchFallsBackToTheLastGoodAnswer(t *testing.T) {
	c := &clock{t: time.Now()}
	r := newReg(c, Spec{Name: "pm", FailThreshold: 1})
	var cache Cached[int]
	ctx := context.Background()
	if _, _, err := Fetch(ctx, r, "pm", &cache, func(context.Context) (int, error) { return 0, errDown }); err == nil {
		t.Fatal("no cache and no answer, but no error")
	}
	c.add(time.Minute) // past the cooldown
	v, m, err := Fetch(ctx, r, "pm", &cache, func(context.Context) (int, error) { return 42, nil })
	if err != nil || v != 42 || m.Source != FromLive {
		t.Fatalf("live: %d %+v %v", v, m, err)
	}
	took := m.At
	c.add(time.Minute)
	v, m, err = Fetch(ctx, r, "pm", &cache, func(context.Context) (int, error) { return 0, errDown })
	if err != nil || v != 42 || m.Source != FromCache || !m.At.Equal(took) || m.Err == nil {
		t.Errorf("fallback: %d %+v %v", v, m, err)
	}
}

// Background checks run on the breaker's schedule.
func TestCheckAllRecordsEveryDependency(t *testing.T) {
	r := New()
	r.Register(Spec{Name: "a", Check: ok})
	r.Register(Spec{Name: "b", Check: fail})
	r.Register(Spec{Name: "c"}) // nothing to check with
	r.CheckAll(context.Background())
	got := map[string]State{}
	for _, s := range r.All() {
		got[s.Name] = s.State
	}
	if got["a"] != Live || got["b"] != Degraded || got["c"] != Unknown {
		t.Errorf("states = %v", got)
	}
}

func TestClassify(t *testing.T) {
	for want, err := range map[string]error{
		"timeout":      context.DeadlineExceeded,
		"dns":          &net.DNSError{Err: "no such host", Name: "db.local"},
		"refused":      errors.New("dial tcp 192.168.77.4:3306: connectex: No connection could be made because the target machine actively refused it."),
		"auth":         errors.New("Error 1045: Access denied for user 'docfind'"),
		"tls":          errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"),
		"circuit_open": &OpenError{Name: "pm"},
		"other":        errors.New("something else"),
	} {
		if got := Classify(err); got != want {
			t.Errorf("Classify(%v) = %s, want %s", err, got, want)
		}
	}
}
