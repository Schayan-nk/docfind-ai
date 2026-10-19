package agent

import "time"

// The shapes a run is made of, as the page, the store and the audit trail see
// them. The machinery that fills them is run.go.

// PlannedStep is one step of a plan.
type PlannedStep struct {
	Tool string            `json:"tool"`
	Args map[string]string `json:"args,omitempty"`
	// From fills an argument from a fact an earlier step established:
	// {"customer": "customer_name"} takes the customer's name as the lookup
	// spelled it. A fact that never arrived skips the step and says so.
	From       map[string]string `json:"from,omitempty"`
	Title      string            `json:"title"`
	Specialist string            `json:"specialist"`
}

// Plan is what will be done, fixed before anything is read.
type Plan struct {
	Goal        string        `json:"goal"`
	Subject     Subject       `json:"subject"`
	Specialists []string      `json:"specialists"`
	Steps       []PlannedStep `json:"steps"`
	// PlannedBy is rules or model; Provider names the model when it is one.
	PlannedBy string `json:"planned_by"`
	Provider  string `json:"provider,omitempty"`
	// Note is said instead of running anything, when the goal asks for
	// something a plan cannot do -- "send it" is an approval, not a step.
	Note string `json:"note,omitempty"`
	// Intents are what the goal was read as, for the page and the tests.
	Intents []string `json:"intents,omitempty"`
}

// Step statuses.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepDone      = "done"
	StepFailed    = "failed"
	StepSkipped   = "skipped"
	StepCancelled = "cancelled"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunDone      = "completed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunTimeout   = "timeout"
)

// StepState is one step as it stands.
type StepState struct {
	Index      int               `json:"index"`
	Tool       string            `json:"tool"`
	Title      string            `json:"title"`
	Specialist string            `json:"specialist"`
	Args       map[string]string `json:"args,omitempty"`
	Status     string            `json:"status"`
	Summary    string            `json:"summary,omitempty"`
	Error      string            `json:"error,omitempty"`
	Attempts   int               `json:"attempts,omitempty"`
	Started    *time.Time        `json:"started_at,omitempty"`
	Finished   *time.Time        `json:"finished_at,omitempty"`
}

// Answer is the structured reply: the same four parts whether it is read or
// spoken, and the spoken form is short on purpose (compose.go).
type Answer struct {
	Summary        string `json:"summary"`
	Conclusion     string `json:"conclusion,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	Spoken         string `json:"spoken"`
}

// Run is one execution of a plan, as the page and the store see it.
type Run struct {
	ID     string `json:"id"`
	Owner  string `json:"owner"`
	Goal   string `json:"goal"`
	Source string `json:"source"` // text or voice
	// Confidence is the speech recogniser's, when Source is voice; 0 when it
	// gave none.
	Confidence float64     `json:"confidence,omitempty"`
	Status     string      `json:"status"`
	Plan       Plan        `json:"plan"`
	Steps      []StepState `json:"steps"`
	Evidence   []Evidence  `json:"evidence"`
	Proposals  []Proposal  `json:"proposals"`
	Answer     *Answer     `json:"answer,omitempty"`
	ToolCalls  int         `json:"tool_calls"`
	Error      string      `json:"error,omitempty"`
	Started    time.Time   `json:"started_at"`
	Finished   *time.Time  `json:"finished_at,omitempty"`
	// Facts are what the steps established, kept for the next request's
	// context and never sent to a model.
	Facts map[string]string `json:"facts,omitempty"`

	outputs []*Output
	sender  string
	// summary is the answer's first line when the reasoning wrote one from
	// the structured results, rather than the first fact found.
	summary string
}

// Outputs are the tools' structured results, by step; nil for a step that
// produced none. For the reasoning, which runs in this process.
func (r *Run) Outputs() []*Output { return r.outputs }

// SetSummary gives the answer its first line, for reasoning that wrote one
// from the structured results; without it the first fact found is used.
func (r *Run) SetSummary(s string) { r.summary = s }

// Sender is the person's display name the run was started with
// (Meta.Sender), for the signature of a draft a proposal carries.
func (r *Run) Sender() string { return r.sender }

// Meta is what the caller knows about a run that the plan does not say.
type Meta struct {
	Source     string
	Confidence float64
	// Sender is the person's display name, for the signature of a draft.
	Sender string
}

// ToolEvent is one tool call, for the audit trail.
type ToolEvent struct {
	RunID    string
	User     string
	Tool     string
	Audit    string
	Args     map[string]string
	Step     int
	Attempt  int
	Result   string // ok, error, denied, skipped
	Error    string
	Duration time.Duration
}
