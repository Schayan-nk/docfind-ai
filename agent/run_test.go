package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLimits() Limits {
	l := DefaultLimits
	l.MaxDuration, l.Keep = 3*time.Second, time.Minute
	return l
}

func readTool(name string, run func(context.Context, *Call) (*Output, error)) Tool {
	return Tool{Name: name, Title: name, Class: Read, Permissions: []string{"read"},
		Audit: "agent." + name, Timeout: time.Second, Run: run,
		Params: []Param{{Name: "q", Label: "q", Kind: Text}}}
}

func finish(t *testing.T, rn *Runner, run Run) Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := rn.Wait(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAStepThePersonMayNotDoIsSkippedAndSaidSo(t *testing.T) {
	reg := NewRegistry()
	called := false
	reg.MustRegister(Tool{Name: "secret", Title: "گزارش محرمانه", Class: Read, Permissions: []string{"admin"},
		Audit: "a", Run: func(context.Context, *Call) (*Output, error) { called = true; return nil, nil }})
	rn := NewRunner(reg, testLimits())
	var events []ToolEvent
	var mu sync.Mutex
	rn.Audit = func(e ToolEvent) { mu.Lock(); events = append(events, e); mu.Unlock() }

	run, err := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "secret", Title: "x"}}}, Meta{})
	if err != nil {
		t.Fatal(err)
	}
	run = finish(t, rn, run)
	if called {
		t.Fatal("a tool ran for somebody without its permission")
	}
	if run.Steps[0].Status != StepSkipped {
		t.Errorf("step status %q", run.Steps[0].Status)
	}
	if len(run.Evidence) == 0 || run.Evidence[0].Kind != Unknown {
		t.Errorf("the gap is not said: %+v", run.Evidence)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Result != "denied" {
		t.Errorf("the refusal is not audited: %+v", events)
	}
}

func TestAPlanNamingAnUnregisteredOrWritingToolIsRefusedWhole(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(readTool("look", noop))
	reg.MustRegister(Tool{Name: "send_email", Title: "t", Class: Execute, Confirm: true,
		Permissions: []string{"read"}, Audit: "a", Run: noop})
	rn := NewRunner(reg, testLimits())
	for _, p := range []Plan{
		{Steps: []PlannedStep{{Tool: "look"}, {Tool: "delete_everything"}}},
		{Steps: []PlannedStep{{Tool: "look"}, {Tool: "send_email"}}},
		{Steps: []PlannedStep{{Tool: "look", Args: map[string]string{"sql": "SELECT 1"}}}},
	} {
		if _, err := rn.Start(person("read"), p, Meta{}); !errors.Is(err, ErrBadPlan) {
			t.Errorf("plan %+v: %v", p.Steps, err)
		}
	}
	long := Plan{}
	for range rn.Limits().MaxSteps + 1 {
		long.Steps = append(long.Steps, PlannedStep{Tool: "look"})
	}
	if _, err := rn.Start(person("read"), long, Meta{}); !errors.Is(err, ErrBadPlan) {
		t.Errorf("a plan over the step limit ran: %v", err)
	}
}

func TestARunThatOverrunsItsTimeStopsAndSaysSo(t *testing.T) {
	reg := NewRegistry()
	slow := readTool("slow", func(ctx context.Context, _ *Call) (*Output, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	slow.Timeout = time.Minute
	reg.MustRegister(slow)
	reg.MustRegister(readTool("after", noop))
	l := testLimits()
	l.MaxDuration = 150 * time.Millisecond
	rn := NewRunner(reg, l)
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "slow"}, {Tool: "after"}}}, Meta{})
	run = finish(t, rn, run)
	if run.Status != RunTimeout {
		t.Errorf("status %q", run.Status)
	}
	if run.Steps[1].Status != StepCancelled {
		t.Errorf("the step after the timeout was %q", run.Steps[1].Status)
	}
	if run.Answer == nil {
		t.Error("a timed-out run has no answer at all")
	}
}

func TestCancellingStopsTheStepInProgressAndEveryStepAfter(t *testing.T) {
	reg := NewRegistry()
	started := make(chan struct{})
	var stopped atomic.Bool
	block := readTool("block", func(ctx context.Context, _ *Call) (*Output, error) {
		close(started)
		<-ctx.Done()
		stopped.Store(true)
		return nil, ctx.Err()
	})
	block.Timeout = time.Minute
	reg.MustRegister(block)
	var laterRan atomic.Bool
	reg.MustRegister(readTool("later", func(context.Context, *Call) (*Output, error) {
		laterRan.Store(true)
		return nil, nil
	}))
	rn := NewRunner(reg, testLimits())
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "block"}, {Tool: "later"}}}, Meta{})
	<-started
	if !rn.Cancel(run.ID) {
		t.Fatal("cancel found no run")
	}
	run = finish(t, rn, run)
	if run.Status != RunCancelled || !stopped.Load() || laterRan.Load() {
		t.Errorf("status %q, step stopped %v, later ran %v", run.Status, stopped.Load(), laterRan.Load())
	}
}

func TestOnlyATransientFailureIsTriedAgain(t *testing.T) {
	reg := NewRegistry()
	var flaky, broken atomic.Int32
	f := readTool("flaky", func(context.Context, *Call) (*Output, error) {
		if flaky.Add(1) < 2 {
			return nil, ErrTransient
		}
		return &Output{Summary: "ok"}, nil
	})
	f.Retries = 3
	b := readTool("broken", func(context.Context, *Call) (*Output, error) {
		broken.Add(1)
		return nil, errors.New("refused")
	})
	b.Retries = 3
	reg.MustRegister(f)
	reg.MustRegister(b)
	rn := NewRunner(reg, testLimits())
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "flaky"}, {Tool: "broken"}}}, Meta{})
	run = finish(t, rn, run)
	if run.Steps[0].Status != StepDone || flaky.Load() != 2 {
		t.Errorf("flaky: %q after %d calls", run.Steps[0].Status, flaky.Load())
	}
	if run.Steps[1].Status != StepFailed || broken.Load() != 1 {
		t.Errorf("a refusal was retried: %d calls", broken.Load())
	}
	if run.Status != RunDone {
		t.Errorf("one failed step failed the whole run: %q", run.Status)
	}
}

func TestTheToolCallCeilingHolds(t *testing.T) {
	reg := NewRegistry()
	var calls atomic.Int32
	loop := readTool("loop", func(context.Context, *Call) (*Output, error) {
		calls.Add(1)
		return nil, ErrTransient
	})
	loop.Retries = 2
	reg.MustRegister(loop)
	l := testLimits()
	l.MaxToolCalls, l.MaxRetries = 4, 2
	rn := NewRunner(reg, l)
	p := Plan{}
	for range 5 {
		p.Steps = append(p.Steps, PlannedStep{Tool: "loop"})
	}
	run, _ := rn.Start(person("read"), p, Meta{})
	run = finish(t, rn, run)
	if calls.Load() > 4 || run.ToolCalls > 4 {
		t.Errorf("%d calls past a ceiling of 4", calls.Load())
	}
}

func TestAPanickingToolFailsItsStepNotTheServer(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(readTool("boom", func(context.Context, *Call) (*Output, error) { panic("nil map") }))
	rn := NewRunner(reg, testLimits())
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "boom"}}}, Meta{})
	run = finish(t, rn, run)
	if run.Steps[0].Status != StepFailed {
		t.Errorf("status %q", run.Steps[0].Status)
	}
}

func TestAMissingFactSkipsTheStepThatNeededIt(t *testing.T) {
	reg := NewRegistry()
	var got string
	reg.MustRegister(readTool("uses", func(_ context.Context, c *Call) (*Output, error) {
		got = c.Args["q"]
		return nil, nil
	}))
	reg.MustRegister(readTool("finds", func(context.Context, *Call) (*Output, error) {
		return &Output{Facts: map[string]string{"name": "بازرگانی آلفا"}}, nil
	}))
	rn := NewRunner(reg, testLimits())
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{
		{Tool: "uses", From: map[string]string{"q": "name"}},
		{Tool: "finds"},
		{Tool: "uses", From: map[string]string{"q": "name"}},
	}}, Meta{})
	run = finish(t, rn, run)
	if run.Steps[0].Status != StepSkipped || run.Steps[2].Status != StepDone || got != "بازرگانی آلفا" {
		t.Errorf("steps %q/%q, argument %q", run.Steps[0].Status, run.Steps[2].Status, got)
	}
}

func TestAToolCannotSmuggleInAProposalNothingCouldCarryOut(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(readTool("sly", func(context.Context, *Call) (*Output, error) {
		return &Output{Proposals: []Proposal{
			{Kind: "whatsapp", Tool: "sly"},         // a Read tool is not an action
			{Kind: "email", Tool: "not_registered"}, // nor is a name nobody registered
		}}, nil
	}))
	rn := NewRunner(reg, testLimits())
	run, _ := rn.Start(person("read"), Plan{Steps: []PlannedStep{{Tool: "sly"}}}, Meta{})
	run = finish(t, rn, run)
	if len(run.Proposals) != 0 {
		t.Errorf("proposals kept: %+v", run.Proposals)
	}
}

func TestOnePersonCannotFillTheRunner(t *testing.T) {
	reg := NewRegistry()
	release := make(chan struct{})
	wait := readTool("wait", func(ctx context.Context, _ *Call) (*Output, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	})
	wait.Timeout = time.Minute
	reg.MustRegister(wait)
	l := testLimits()
	l.PerUser = 2
	rn := NewRunner(reg, l)
	p := Plan{Steps: []PlannedStep{{Tool: "wait"}}}
	var ids []string
	for range 2 {
		r, err := rn.Start(person("read"), p, Meta{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if _, err := rn.Start(person("read"), p, Meta{}); !errors.Is(err, ErrBusy) {
		t.Errorf("a third run started: %v", err)
	}
	other := who{name: "someone-else", perms: map[string]bool{"read": true}}
	if r, err := rn.Start(other, p, Meta{}); err != nil {
		t.Errorf("somebody else was refused: %v", err)
	} else {
		ids = append(ids, r.ID)
	}
	close(release)
	for _, id := range ids {
		finish(t, rn, Run{ID: id})
	}
}

func TestTheAuditTrailNeverHoldsAMessageBody(t *testing.T) {
	tool := &Tool{Params: []Param{{Name: "body", Kind: Text}, {Name: "q", Kind: Text}, {Name: "ref", Kind: Ref}}}
	got := redactArgs(tool, map[string]string{"body": "متن محرمانهٔ ایمیل", "q": strings.Repeat("ب", 200), "ref": "1234"})
	if strings.Contains(got["body"], "محرمانه") || len([]rune(got["q"])) > 81 || got["ref"] != "1234" {
		t.Errorf("redacted wrongly: %v", got)
	}
}
