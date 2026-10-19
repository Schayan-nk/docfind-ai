package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"
)

// Running a plan: in the background, bounded, stoppable, and recorded.
//
// A plan is a fixed list of steps. It cannot grow while it runs -- no step can
// add a step -- so a run cannot loop; the limits below are the second guard,
// for the day somebody writes a planner that produces a long list.

// Limits bound one run and the runner as a whole.
type Limits struct {
	MaxSteps     int
	MaxToolCalls int // counts every attempt, retries included
	MaxRetries   int // ceiling on any tool's own Retries
	MaxDuration  time.Duration
	PerUser      int // runs one person may have going at once
	Global       int // runs going at once, everybody together
	// Keep is how long a finished run stays in memory for its page to read.
	Keep time.Duration
}

// DefaultLimits are the limits a runner uses unless given others.
var DefaultLimits = Limits{
	MaxSteps: 12, MaxToolCalls: 20, MaxRetries: 2,
	MaxDuration: 2 * time.Minute, PerUser: 2, Global: 8,
	Keep: 15 * time.Minute,
}

// Runner executes plans. One per server.
type Runner struct {
	reg    *Registry
	limits Limits
	// Save is told about every change to a run; the store behind it keeps
	// the run for a page that lists runs and for the next request's context.
	Save func(Run)
	// Audit is told about every tool call.
	Audit func(ToolEvent)
	// Finish writes the answer once the steps are done (compose.go).
	Finish func(*Run)

	mu      sync.Mutex
	live    map[string]*liveRun
	perUser map[string]int
	active  int
}

type liveRun struct {
	run    *Run
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRunner returns a runner over reg.
func NewRunner(reg *Registry, limits Limits) *Runner {
	if limits.MaxSteps == 0 {
		limits = DefaultLimits
	}
	return &Runner{reg: reg, limits: limits, Finish: Compose,
		live: map[string]*liveRun{}, perUser: map[string]int{}}
}

// Errors from Start.
var (
	ErrBusy    = errors.New("agent: too many investigations are running")
	ErrBadPlan = errors.New("agent: the plan is not valid")
	ErrNoRun   = errors.New("agent: no such run")
)

// Registry returns the tools this runner runs.
func (rn *Runner) Registry() *Registry { return rn.reg }

// Limits returns the runner's limits.
func (rn *Runner) Limits() Limits { return rn.limits }

// Check validates a plan without running it: every step a plannable tool,
// every fixed argument valid, and no more steps than the limit.
func (rn *Runner) Check(p Plan) error {
	if len(p.Steps) > rn.limits.MaxSteps {
		return fmt.Errorf("%w: %d steps, at most %d", ErrBadPlan, len(p.Steps), rn.limits.MaxSteps)
	}
	for i, s := range p.Steps {
		t, err := rn.reg.Plannable(s.Tool)
		if err != nil {
			return fmt.Errorf("%w: step %d: %v", ErrBadPlan, i+1, err)
		}
		if err := t.validatePlanned(s.Args, s.From); err != nil {
			return fmt.Errorf("%w: step %d: %v", ErrBadPlan, i+1, err)
		}
	}
	return nil
}

// Start begins a run in the background and returns its first snapshot.
func (rn *Runner) Start(who Principal, p Plan, meta Meta) (Run, error) {
	if who == nil {
		return Run{}, ErrForbidden
	}
	if err := rn.Check(p); err != nil {
		return Run{}, err
	}
	user := who.Username()
	rn.mu.Lock()
	if rn.active >= rn.limits.Global || rn.perUser[user] >= rn.limits.PerUser {
		rn.mu.Unlock()
		return Run{}, ErrBusy
	}
	rn.active++
	rn.perUser[user]++

	now := time.Now().UTC()
	run := &Run{
		ID: NewID(), Owner: user, Goal: p.Goal, Source: meta.Source,
		Confidence: meta.Confidence, Status: RunRunning, Plan: p,
		Steps: make([]StepState, len(p.Steps)), Evidence: []Evidence{},
		Proposals: []Proposal{}, Started: now, Facts: map[string]string{},
		outputs: make([]*Output, len(p.Steps)), sender: meta.Sender,
	}
	if run.Source == "" {
		run.Source = "text"
	}
	for i, s := range p.Steps {
		run.Steps[i] = StepState{Index: i, Tool: s.Tool, Title: s.Title,
			Specialist: s.Specialist, Args: copyMap(s.Args), Status: StepPending}
	}
	// Not the request's context: the request returns at once with the run's
	// id, and the run must outlive it. Bounded by its own clock instead.
	ctx, cancel := context.WithTimeout(context.Background(), rn.limits.MaxDuration)
	l := &liveRun{run: run, cancel: cancel, done: make(chan struct{})}
	rn.live[run.ID] = l
	snap := rn.snapshotLocked(run)
	rn.mu.Unlock()

	rn.save(snap)
	go rn.execute(ctx, l, who)
	return snap, nil
}

// Get returns a snapshot of a run still in memory.
func (rn *Runner) Get(id string) (Run, bool) {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	l, ok := rn.live[id]
	if !ok {
		return Run{}, false
	}
	return rn.snapshotLocked(l.run), true
}

// Cancel stops a run: the step in progress has its context cancelled and no
// further step starts.
func (rn *Runner) Cancel(id string) bool {
	rn.mu.Lock()
	l, ok := rn.live[id]
	rn.mu.Unlock()
	if !ok {
		return false
	}
	l.cancel()
	return true
}

// Wait blocks until the run finishes or ctx ends. For tests and shutdown.
func (rn *Runner) Wait(ctx context.Context, id string) (Run, error) {
	rn.mu.Lock()
	l, ok := rn.live[id]
	rn.mu.Unlock()
	if !ok {
		return Run{}, ErrNoRun
	}
	select {
	case <-l.done:
	case <-ctx.Done():
		return Run{}, ctx.Err()
	}
	r, _ := rn.Get(id)
	return r, nil
}

// Active counts the runs going now.
func (rn *Runner) Active() int {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	return rn.active
}

func (rn *Runner) execute(ctx context.Context, l *liveRun, who Principal) {
	run := l.run
	defer func() {
		rn.mu.Lock()
		rn.active--
		rn.perUser[run.Owner]--
		if rn.perUser[run.Owner] <= 0 {
			delete(rn.perUser, run.Owner)
		}
		rn.mu.Unlock()
		l.cancel()
		close(l.done)
		// Kept for the page to read its last state, then dropped: the store
		// has it from here.
		time.AfterFunc(rn.limits.Keep, func() {
			rn.mu.Lock()
			delete(rn.live, run.ID)
			rn.mu.Unlock()
		})
	}()

	for i, step := range run.Plan.Steps {
		if ctx.Err() != nil {
			break
		}
		rn.runStep(ctx, run, i, step, who)
	}

	rn.mu.Lock()
	status := RunDone
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		status, run.Error = RunTimeout, "زمان بررسی به پایان رسید؛ نتیجه تا همین‌جا نشان داده شده است."
	case errors.Is(ctx.Err(), context.Canceled):
		status, run.Error = RunCancelled, "بررسی متوقف شد."
	}
	for i := range run.Steps {
		if run.Steps[i].Status == StepPending || run.Steps[i].Status == StepRunning {
			run.Steps[i].Status = StepCancelled
		}
	}
	if rn.Finish != nil {
		rn.Finish(run)
	}
	now := time.Now().UTC()
	// The reasoning's proposals get the same treatment as a tool's: a name,
	// a status, and dropped if no Execute tool could carry them out.
	kept := run.Proposals[:0]
	for _, p := range run.Proposals {
		if p.ID == "" {
			if t, ok := rn.reg.Get(p.Tool); !ok || t.Class != Execute {
				continue
			}
			p.ID, p.Status, p.Created = NewID(), ProposalOpen, now
		}
		kept = append(kept, p)
	}
	run.Proposals = kept
	run.Status, run.Finished = status, &now
	snap := rn.snapshotLocked(run)
	rn.mu.Unlock()
	rn.save(snap)
}

func (rn *Runner) runStep(ctx context.Context, run *Run, i int, step PlannedStep, who Principal) {
	tool, err := rn.reg.Plannable(step.Tool)
	if err != nil {
		rn.endStep(run, i, StepFailed, "", "این ابزار ثبت نشده است.", nil)
		return
	}
	if !tool.Allowed(who) {
		// Not a failure of the run: this person may not see this, so it was
		// not looked at, and the answer says so rather than leaving a gap.
		rn.audit(run, i, tool, nil, 0, "denied", "", 0)
		rn.endStep(run, i, StepSkipped, "", "دسترسی لازم برای این بخش را ندارید.", &Output{
			Evidence: []Evidence{{Kind: Unknown,
				Text: "«" + tool.Title + "» بررسی نشد، چون دسترسی لازم برای آن را ندارید."}},
		})
		return
	}
	raw := copyMap(step.Args)
	if raw == nil {
		raw = map[string]string{}
	}
	rn.mu.Lock()
	for arg, fact := range step.From {
		v, ok := run.Facts[fact]
		if !ok || v == "" {
			rn.mu.Unlock()
			rn.endStep(run, i, StepSkipped, "", "داده‌ای که این مرحله لازم داشت در مرحله‌های پیش پیدا نشد.", nil)
			return
		}
		raw[arg] = v
	}
	facts := copyMap(run.Facts)
	rn.mu.Unlock()

	args, err := tool.Validate(raw)
	if err != nil {
		rn.audit(run, i, tool, raw, 0, "error", err.Error(), 0)
		rn.endStep(run, i, StepFailed, "", err.Error(), nil)
		return
	}
	now := time.Now().UTC()
	rn.mu.Lock()
	run.Steps[i].Status, run.Steps[i].Started, run.Steps[i].Args = StepRunning, &now, copyMap(args)
	snap := rn.snapshotLocked(run)
	rn.mu.Unlock()
	rn.save(snap)

	tries := min(tool.Retries, rn.limits.MaxRetries)
	var out *Output
	for attempt := 0; attempt <= tries; attempt++ {
		rn.mu.Lock()
		if run.ToolCalls >= rn.limits.MaxToolCalls {
			rn.mu.Unlock()
			rn.endStep(run, i, StepFailed, "", "سقف تعداد فراخوانی ابزارها در این بررسی پر شد.", nil)
			return
		}
		run.ToolCalls++
		run.Steps[i].Attempts = attempt + 1
		rn.mu.Unlock()

		start := time.Now()
		out, err = callTool(ctx, tool, &Call{Tool: tool, Args: args, Who: who, Facts: facts, Step: i})
		if err == nil {
			rn.audit(run, i, tool, args, attempt+1, "ok", "", time.Since(start))
			break
		}
		rn.audit(run, i, tool, args, attempt+1, "error", err.Error(), time.Since(start))
		if !errors.Is(err, ErrTransient) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		msg := "این بخش انجام نشد."
		if ctx.Err() != nil {
			msg = "پیش از پایان این بخش، بررسی متوقف شد."
		} else if errors.Is(err, ErrForbidden) {
			msg = "دسترسی لازم برای این بخش را ندارید."
		}
		rn.endStep(run, i, StepFailed, "", msg, &Output{Evidence: []Evidence{{Kind: Unknown,
			Text: "«" + tool.Title + "» انجام نشد، پس آنچه این بخش باید نشان می‌داد نامعلوم است."}}})
		return
	}
	rn.endStep(run, i, StepDone, out.Summary, "", out)
}

// callTool runs one tool under its own deadline, and turns a panic into an
// error: one broken tool must not take the server down with it.
func callTool(ctx context.Context, t *Tool, c *Call) (out *Output, err error) {
	sctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, fmt.Errorf("agent: tool %s panicked: %v", t.Name, p)
		}
	}()
	out, err = t.Run(sctx, c)
	if err == nil && out == nil {
		out = &Output{}
	}
	if err == nil && sctx.Err() != nil {
		err = sctx.Err()
	}
	return out, err
}

func (rn *Runner) endStep(run *Run, i int, status, summary, msg string, out *Output) {
	now := time.Now().UTC()
	rn.mu.Lock()
	st := &run.Steps[i]
	st.Status, st.Summary, st.Error, st.Finished = status, summary, msg, &now
	if out != nil {
		run.outputs[i] = out
		for _, e := range out.Evidence {
			e.Step = i
			run.Evidence = append(run.Evidence, e)
		}
		maps.Copy(run.Facts, out.Facts)
		for _, p := range out.Proposals {
			if t, ok := rn.reg.Get(p.Tool); !ok || t.Class != Execute {
				continue // a proposal nothing could carry out is a bug, not an action
			}
			p.ID, p.Status, p.Created = NewID(), ProposalOpen, now
			run.Proposals = append(run.Proposals, p)
		}
	}
	snap := rn.snapshotLocked(run)
	rn.mu.Unlock()
	rn.save(snap)
}

func (rn *Runner) audit(run *Run, step int, t *Tool, args map[string]string, attempt int, result, msg string, d time.Duration) {
	if rn.Audit == nil {
		return
	}
	rn.Audit(ToolEvent{RunID: run.ID, User: run.Owner, Tool: t.Name, Audit: t.Audit,
		Args: redactArgs(t, args), Step: step, Attempt: attempt, Result: result,
		Error: clip(msg, 200), Duration: d})
}

func (rn *Runner) save(r Run) {
	if rn.Save != nil {
		rn.Save(r)
	}
}

// redactArgs keeps what an auditor needs and nothing more: identifiers
// whole, free text clipped, and a message body never.
func redactArgs(t *Tool, args map[string]string) map[string]string {
	if args == nil {
		return nil
	}
	kinds := map[string]ParamKind{}
	for _, p := range t.Params {
		kinds[p.Name] = p.Kind
	}
	out := map[string]string{}
	for k, v := range args {
		switch {
		case k == "body" || k == "message":
			out[k] = fmt.Sprintf("(%d نویسه)", len([]rune(v)))
		case kinds[k] == Text:
			out[k] = clip(v, 80)
		default:
			out[k] = v
		}
	}
	return out
}

func (rn *Runner) snapshotLocked(r *Run) Run {
	c := *r
	c.Steps = append([]StepState(nil), r.Steps...)
	c.Evidence = append([]Evidence(nil), r.Evidence...)
	c.Proposals = append([]Proposal(nil), r.Proposals...)
	c.Facts = copyMap(r.Facts)
	if r.Answer != nil {
		a := *r.Answer
		c.Answer = &a
	}
	return c
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

// NewID is a random identifier for a run or a proposal: 16 bytes, hex.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system has no randomness left, which nothing survives
	}
	return hex.EncodeToString(b[:])
}
