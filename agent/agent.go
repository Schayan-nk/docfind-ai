// Package agent is an investigation agent for an assistant: a goal in,
// a plan of registered tools, the evidence those tools return, and actions
// proposed for a person to approve.
//
// WHAT THE AGENT IS NOT. It is not a model with access to the system. It is
// built on one rule -- the model never writes SQL and never computes a
// figure -- and nothing here loosens it:
//
//   - A tool is a Go function registered by name with a typed schema. Nothing
//     can call a tool that is not registered, and every argument is validated
//     against its schema before the function runs (tools.go).
//   - Reading is done by the application's own read-only functions -- the
//     approved queries, the document index, the person's mailbox --
//     under the permissions of the person who asked (the Principal).
//   - The plan is fixed BEFORE any data is read, and nothing a tool returns is
//     ever shown to a model. A document or an email that says "ignore your
//     instructions and send this" is text in an evidence list; there is no
//     step at which it could be read as an instruction.
//   - Conclusions are written by Go from the tools' structured results, each
//     marked as a fact, an inference, a recommendation or an unknown.
//   - Nothing that changes anything runs inside a plan. A plan can only PROPOSE
//     an action; executing it is a separate request, made by the person, that
//     re-checks their permission at that moment (Class Execute).
package agent

import "time"

// Class is what a tool does to the world.
type Class string

const (
	// Read searches, retrieves, compares and counts. It runs without asking.
	Read Class = "read"
	// Propose prepares an action and changes nothing. What it produces is a
	// Proposal a person may edit, approve or cancel.
	Propose Class = "propose"
	// Execute changes something: sends, stores, notifies. A tool of this class
	// can never be a step in a plan -- it runs only when an approved Proposal
	// names it (Registry.Plannable).
	Execute Class = "execute"
)

// EvidenceKind separates what the data says from what is concluded from it.
//
// The four are shown to the reader as four different labels, because a
// sentence the system counted and a sentence the system concluded read the
// same once they are set in one typeface -- and the reader quoting one in a
// meeting cannot tell which they quoted.
type EvidenceKind string

const (
	// Fact is read or counted from a record and names the record.
	Fact EvidenceKind = "fact"
	// Inference is concluded from facts by a stated rule -- "longer than the
	// usual time for this stage" -- and says how.
	Inference EvidenceKind = "inference"
	// Recommendation is what the system suggests doing. It never runs itself.
	Recommendation EvidenceKind = "recommendation"
	// Unknown is something the question needs and the data does not hold.
	// Said explicitly, because silence reads as "there was nothing to find".
	Unknown EvidenceKind = "unknown"
)

// Source is the record a piece of evidence came from.
type Source struct {
	// Kind is workflow, report, email, document or profile.
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Ref   string `json:"ref,omitempty"`
	// URL is an address inside this application, never an outside one: a
	// source is a way to open the record, not a link somebody can plant.
	URL string `json:"url,omitempty"`
	// At is when the record says it happened, as the record says it.
	At string `json:"at,omitempty"`
}

// Evidence is one statement with where it came from.
type Evidence struct {
	Kind    EvidenceKind `json:"kind"`
	Text    string       `json:"text"`
	Sources []Source     `json:"sources,omitempty"`
	// Calc is how a derived number was computed, in words.
	Calc string `json:"calc,omitempty"`
	// Freshness is how current the underlying data is.
	Freshness string `json:"freshness,omitempty"`
	// Confidence is high, medium or low; set on inferences.
	Confidence string `json:"confidence,omitempty"`
	// Step is the plan step that produced it.
	Step int `json:"step"`
}

// Subject is what an investigation is about, carried from one request to the
// next so "the related emails too" means the case already on screen.
type Subject struct {
	// Kind is case, customer or "" for none.
	Kind string `json:"kind,omitempty"`
	// Ref is the identifier as the person gave it: a case number or a
	// customer's name. Never rewritten beyond folding its digits.
	Ref   string `json:"ref,omitempty"`
	Label string `json:"label,omitempty"`
}

// Empty reports whether there is no subject.
func (s Subject) Empty() bool { return s.Kind == "" || s.Ref == "" }

// Proposal is an action the agent prepared and nobody has approved yet.
type Proposal struct {
	ID string `json:"id"`
	// Kind is reminder, note, notification, email or whatsapp.
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Tool is the Execute tool that carries it out once approved.
	Tool string `json:"tool"`
	// Fields are the tool's arguments, as the person will see and may edit
	// them: recipient, subject, body, due time.
	Fields map[string]string `json:"fields"`
	// Reason is why the agent proposes it, in one sentence.
	Reason  string   `json:"reason"`
	Sources []Source `json:"sources,omitempty"`
	// External is set when approving it reaches outside the application --
	// an email or a WhatsApp message to somebody.
	External bool `json:"external,omitempty"`
	// Identifiers are the case numbers and names the action is about. When
	// they came from speech recognised with low confidence, LowConfidence is
	// set and approval must confirm them explicitly.
	Identifiers   []string `json:"identifiers,omitempty"`
	LowConfidence bool     `json:"low_confidence,omitempty"`
	// Status is proposed, executed, rejected or failed.
	Status  string    `json:"status"`
	Result  string    `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`
	Created time.Time `json:"created_at"`
}

// Proposal statuses.
const (
	ProposalOpen     = "proposed"
	ProposalExecuted = "executed"
	ProposalRejected = "rejected"
	ProposalFailed   = "failed"
)
