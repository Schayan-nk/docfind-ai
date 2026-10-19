// Package dependencies keeps one record per thing a server depends on -- a
// database, the language-model providers, a mailbox, the network -- and
// decides, for each call, whether it is worth making.
//
// Every dependency has a state a person can read (live, degraded, stale,
// offline), the facts behind it (last success, last attempt, latency, what
// kind of error), and a circuit breaker: after a few failures in a row calls
// stop being made for a cooldown that doubles on each failed retry, up to a
// cap; then one probe is let through, and its answer closes the breaker or
// opens it again. A request somebody is waiting for therefore fails in
// microseconds while a dependency is known to be down, and goes to the cache
// (Fetch) instead of hanging for its full timeout.
package dependencies

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// State is what a person is told about a dependency.
type State string

const (
	Live     State = "live"     // answering, and quickly
	Degraded State = "degraded" // answering, but slowly or after recent failures
	Stale    State = "stale"    // not answering; what it last said is still recent
	Offline  State = "offline"  // not answering, and nothing recent to show instead
	Unknown  State = "unknown"  // not asked yet
)

// Spec declares a dependency: how to ask whether it is there, how long a
// person may wait for it, and what they get instead when it is not.
type Spec struct {
	Name  string // stable id: "postgres", "reports", ...
	Label string // what the status bar calls it
	// Check asks the dependency whether it is there. It is also the probe a
	// half-open breaker lets through.
	Check func(ctx context.Context) error
	// Interval between background checks while healthy. Default 1 minute.
	Interval time.Duration
	// MaxWait is the most a user-facing call waits. Default 3 seconds.
	MaxWait time.Duration
	// SlowAfter: a success slower than this is "degraded". Default MaxWait/2.
	SlowAfter time.Duration
	// MaxStale: this long without a success and "stale" becomes "offline".
	// Default 1 hour.
	MaxStale time.Duration
	// FailThreshold consecutive failures open the breaker. Default 3.
	FailThreshold int
	// Cooldown is the first open period; each failed probe doubles it up to
	// MaxCooldown. Defaults 10 seconds and 5 minutes.
	Cooldown, MaxCooldown time.Duration
	// Fallback says what people get while it is down, for the status page.
	Fallback string
	// Downgrade names the capabilities lost while it is down.
	Downgrade []string
}

func (s *Spec) defaults() {
	if s.Interval <= 0 {
		s.Interval = time.Minute
	}
	if s.MaxWait <= 0 {
		s.MaxWait = 3 * time.Second
	}
	if s.SlowAfter <= 0 {
		s.SlowAfter = s.MaxWait / 2
	}
	if s.MaxStale <= 0 {
		s.MaxStale = time.Hour
	}
	if s.FailThreshold <= 0 {
		s.FailThreshold = 3
	}
	if s.Cooldown <= 0 {
		s.Cooldown = 10 * time.Second
	}
	if s.MaxCooldown < s.Cooldown {
		s.MaxCooldown = max(5*time.Minute, s.Cooldown)
	}
}

// Breaker positions.
const (
	Closed   = "closed"
	Open     = "open"
	HalfOpen = "half-open"
)

// ErrOpen is returned, without calling anything, while a breaker is open.
var ErrOpen = errors.New("dependencies: circuit open")

// OpenError says which dependency refused and until when.
type OpenError struct {
	Name  string
	Until time.Time
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("%s is unavailable (circuit open until %s)", e.Name, e.Until.Format("15:04:05"))
}

func (e *OpenError) Is(target error) bool { return target == ErrOpen }

// Status is one dependency as the status page and the health checks see it.
type Status struct {
	Name        string    `json:"name"`
	Label       string    `json:"label"`
	State       State     `json:"state"`
	Breaker     string    `json:"breaker"`
	LastSuccess time.Time `json:"last_success"`
	LastAttempt time.Time `json:"last_attempt"`
	LatencyMs   int64     `json:"latency_ms"`
	ErrorClass  string    `json:"error_class,omitempty"`
	Error       string    `json:"error,omitempty"`
	// DataAgeSec is how old the newest answer is; -1 when there has been none.
	DataAgeSec int64     `json:"data_age_sec"`
	Failures   int       `json:"failures"`
	OpenUntil  time.Time `json:"open_until,omitempty"`
	Fallback   string    `json:"fallback,omitempty"`
	Downgrade  []string  `json:"downgrade,omitempty"`
}

type dep struct {
	spec        Spec
	lastSuccess time.Time
	lastAttempt time.Time
	latency     time.Duration
	err         error
	failures    int
	breaker     string
	openUntil   time.Time
	cooldown    time.Duration
	probing     bool // a half-open probe is in flight
	nextCheck   time.Time
	checking    bool // a background check is in flight
}

// Registry is every dependency.
type Registry struct {
	mu    sync.Mutex
	deps  map[string]*dep
	order []string
	now   func() time.Time
	sims  map[string]Simulation // resilience test mode (simulate.go)
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{deps: map[string]*dep{}, now: time.Now}
}

// Register adds a dependency, or replaces one of the same name.
func (r *Registry) Register(s Spec) {
	s.defaults()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.deps[s.Name]; !ok {
		r.order = append(r.order, s.Name)
	}
	r.deps[s.Name] = &dep{spec: s, breaker: Closed, cooldown: s.Cooldown}
}

// Ensure registers a dependency unless one of that name already exists: for
// callers that meet their dependencies as they go, such as the AI chain,
// whose providers come from settings that change while running.
func (r *Registry) Ensure(s Spec) {
	r.mu.Lock()
	_, ok := r.deps[s.Name]
	r.mu.Unlock()
	if !ok {
		r.Register(s)
	}
}

// Do makes one call to a dependency on somebody's behalf: refused at once
// while its breaker is open, otherwise run with the dependency's MaxWait.
// The outcome feeds the breaker. An unregistered name is simply called.
func (r *Registry) Do(ctx context.Context, name string, fn func(context.Context) error) error {
	r.mu.Lock()
	d := r.deps[name]
	if d == nil {
		r.mu.Unlock()
		return fn(ctx)
	}
	if s, ok := r.simulated(name); ok {
		r.mu.Unlock()
		return &OpenError{Name: name, Until: s.Until}
	}
	if err := r.admit(d); err != nil {
		r.mu.Unlock()
		return err
	}
	wait := d.spec.MaxWait
	r.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	started := r.now()
	err := fn(cctx)
	took := r.now().Sub(started)
	// The caller giving up is not the dependency failing.
	if err != nil && ctx.Err() != nil {
		r.mu.Lock()
		if d.probing {
			d.probing = false
		}
		r.mu.Unlock()
		return err
	}
	r.record(d, err, took)
	return err
}

// Allow reports whether a call would be made now, without making one.
func (r *Registry) Allow(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.deps[name]
	if d == nil {
		return true
	}
	if _, ok := r.simulated(name); ok {
		return false
	}
	switch d.breaker {
	case Closed:
		return true
	case Open:
		return !r.now().Before(d.openUntil)
	default:
		return !d.probing
	}
}

// admit decides whether a call goes ahead; r.mu is held.
func (r *Registry) admit(d *dep) error {
	switch d.breaker {
	case Open:
		if r.now().Before(d.openUntil) {
			return &OpenError{Name: d.spec.Name, Until: d.openUntil}
		}
		d.breaker = HalfOpen
		fallthrough
	case HalfOpen:
		if d.probing {
			return &OpenError{Name: d.spec.Name, Until: d.openUntil}
		}
		d.probing = true
	}
	return nil
}

func (r *Registry) record(d *dep, err error, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	d.lastAttempt, d.latency, d.err = now, took, err
	wasProbe := d.probing
	d.probing = false
	if err == nil {
		d.lastSuccess = now
		d.failures = 0
		d.breaker = Closed
		d.cooldown = d.spec.Cooldown
		return
	}
	d.failures++
	switch {
	case wasProbe:
		// The probe failed: open again, for twice as long.
		d.cooldown = min(2*d.cooldown, d.spec.MaxCooldown)
		d.breaker, d.openUntil = Open, now.Add(d.cooldown)
	case d.breaker == Closed && d.failures >= d.spec.FailThreshold:
		d.breaker, d.openUntil = Open, now.Add(d.cooldown)
	}
}

// Status is one dependency's current record.
func (r *Registry) Status(name string) (Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.deps[name]
	if d == nil {
		return Status{}, false
	}
	return r.status(d), true
}

// All is every dependency's record, in registration order.
func (r *Registry) All() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.status(r.deps[n]))
	}
	return out
}

func (r *Registry) status(d *dep) Status {
	now := r.now()
	s := Status{
		Name: d.spec.Name, Label: d.spec.Label, Breaker: d.breaker,
		LastSuccess: d.lastSuccess, LastAttempt: d.lastAttempt, LatencyMs: d.latency.Milliseconds(),
		Failures: d.failures, Fallback: d.spec.Fallback, Downgrade: slices.Clone(d.spec.Downgrade),
		DataAgeSec: -1,
	}
	if !d.lastSuccess.IsZero() {
		s.DataAgeSec = int64(now.Sub(d.lastSuccess).Seconds())
	}
	if d.breaker != Closed {
		s.OpenUntil = d.openUntil
	}
	if d.err != nil {
		s.Error, s.ErrorClass = d.err.Error(), Classify(d.err)
	}
	switch {
	case d.lastAttempt.IsZero():
		s.State = Unknown
	case d.err == nil && d.latency > d.spec.SlowAfter:
		s.State = Degraded
	case d.err == nil:
		s.State = Live
	case d.breaker == Closed:
		// Failing, but not yet often enough to stop trying.
		s.State = Degraded
	case !d.lastSuccess.IsZero() && now.Sub(d.lastSuccess) <= d.spec.MaxStale:
		s.State = Stale
	default:
		s.State = Offline
	}
	// A rehearsal says so: everything downstream -- the banner, the status
	// line, a monitoring page, an alert -- sees the outage the
	// fallbacks are being tested against, and the words that it is a test.
	if sim, ok := r.simulated(d.spec.Name); ok {
		s.State, s.Breaker, s.OpenUntil = Offline, "simulated", sim.Until
		s.Error, s.ErrorClass = "آزمایش تاب‌آوری: قطعی شبیه‌سازی‌شده تا "+sim.Until.Format("15:04"), "simulated"
	}
	return s
}
