package agent

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The tool registry: the only things the agent can do, each declared once
// with its schema, its permission and what it does to the world.
//
// NOTHING OUTSIDE IT EXISTS. A plan naming a tool that is not registered is
// refused whole rather than run in part -- a model that invents a tool has
// misunderstood the job, and the steps around the invented one were chosen by
// the same misunderstanding.

// ParamKind is the type of one argument.
type ParamKind string

const (
	// Text is free text: a search phrase, a name. Control characters are
	// refused rather than stripped, and length is counted in runes.
	Text ParamKind = "text"
	// Int is a whole number within Min and Max. Persian digits are accepted.
	Int ParamKind = "int"
	// Ref is an identifier -- a case, order, tender or guarantee number. Its
	// digits are folded to ASCII and NOTHING ELSE is changed: separators and
	// letters stay as given, because "6383638/9" and "63836389" are shown back
	// to the person as they said them.
	Ref ParamKind = "ref"
	// Email is one address, with no line break anywhere in it.
	Email ParamKind = "email"
	// Phone is a WhatsApp number: 8 to 20 digits, optionally a leading +.
	Phone ParamKind = "phone"
	// Time is an RFC 3339 instant.
	Time ParamKind = "time"
	// Choice is one of Choices.
	Choice ParamKind = "choice"
)

// Param is one declared argument.
type Param struct {
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Kind     ParamKind `json:"kind"`
	Required bool      `json:"required,omitempty"`
	// Max is the longest text in runes, or the largest Int.
	Max     int      `json:"max,omitempty"`
	Min     int      `json:"min,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// Principal is the person a run acts for. Every tool runs with their
// permissions and no others; the agent has none of its own.
type Principal interface {
	Username() string
	// Can answers the application's own permission question, the same one
	// every route asks.
	Can(permission string) bool
}

// Call is one invocation of a tool.
type Call struct {
	Tool *Tool
	Args Args
	// Who is asking. Never nil.
	Who Principal
	// Facts are the named values earlier steps established -- the case
	// number a lookup resolved, the customer's name as the records spell it.
	Facts map[string]string
	// Step is the index of this step in its plan.
	Step int
}

// Output is what a tool returns.
type Output struct {
	// Summary is one line for the plan view: what the step found.
	Summary  string
	Evidence []Evidence
	// Facts are named values later steps may use (PlannedStep.From).
	Facts map[string]string
	// Data is the structured result an application's reasoning reads (see
	// Compose and Run.Outputs). Never
	// shown to a model.
	Data      any
	Proposals []Proposal
}

// Tool is one registered capability.
type Tool struct {
	Name        string
	Title       string // Persian, for the plan view
	Description string // for the model planner, and the documentation
	// Specialist names the part of the system the tool belongs to -- "email",
	// "documents" -- and a plan lists the specialists its steps use.
	Specialist string
	Params     []Param
	// Output names what Data holds, for the documentation.
	Output string
	// Permissions must ALL be held by the Principal.
	Permissions []string
	Class       Class
	// Confirm says the person must approve before it runs. Always true for
	// Execute; Register refuses an Execute tool without it.
	Confirm bool
	// Audit is the event recorded when it runs.
	Audit   string
	Timeout time.Duration
	// Retries is how many times a transient failure (ErrTransient) is tried
	// again. Zero for anything that writes.
	Retries int
	Run     func(ctx context.Context, c *Call) (*Output, error)
}

// ErrTransient marks a failure worth one more try: a timeout reaching the
// reporting database, not a refusal.
var ErrTransient = errors.New("agent: transient failure")

// Errors a caller branches on.
var (
	ErrUnknownTool  = errors.New("agent: no such tool")
	ErrForbidden    = errors.New("agent: not permitted")
	ErrNotPlannable = errors.New("agent: this tool cannot be a plan step")
)

// Registry is the set of tools. Build it once at start-up; it is read-only
// afterwards and safe for concurrent use.
type Registry struct {
	tools map[string]*Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]*Tool{}} }

var (
	toolName  = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)
	paramName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
)

// Register adds a tool, refusing one that is malformed or would weaken a rule.
func (r *Registry) Register(t Tool) error {
	switch {
	case !toolName.MatchString(t.Name):
		return fmt.Errorf("agent: bad tool name %q", t.Name)
	case r.tools[t.Name] != nil:
		return fmt.Errorf("agent: tool %q registered twice", t.Name)
	case t.Run == nil:
		return fmt.Errorf("agent: tool %q has no function", t.Name)
	case t.Class != Read && t.Class != Propose && t.Class != Execute:
		return fmt.Errorf("agent: tool %q has no class", t.Name)
	case t.Class == Execute && !t.Confirm:
		return fmt.Errorf("agent: tool %q changes something and does not ask first", t.Name)
	case t.Class == Execute && t.Retries > 0:
		// A write retried after a lost answer is a write done twice.
		return fmt.Errorf("agent: tool %q writes and retries", t.Name)
	case len(t.Permissions) == 0:
		return fmt.Errorf("agent: tool %q names no permission", t.Name)
	case t.Audit == "":
		return fmt.Errorf("agent: tool %q records no audit event", t.Name)
	}
	seen := map[string]bool{}
	for _, p := range t.Params {
		if !paramName.MatchString(p.Name) {
			return fmt.Errorf("agent: tool %q has a bad parameter name %q", t.Name, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("agent: tool %q declares %q twice", t.Name, p.Name)
		}
		seen[p.Name] = true
	}
	if t.Timeout <= 0 {
		t.Timeout = 20 * time.Second
	}
	tt := t
	r.tools[t.Name] = &tt
	return nil
}

// MustRegister is Register for start-up code, where a bad tool is a bug.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Plannable returns a tool that may be a step in a plan: registered, and not
// one that changes anything.
func (r *Registry) Plannable(name string) (*Tool, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	if t.Class == Execute {
		return nil, fmt.Errorf("%w: %q", ErrNotPlannable, name)
	}
	return t, nil
}

// Tools lists every tool, by name.
func (r *Registry) Tools() []*Tool {
	out := make([]*Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Allowed reports whether who holds every permission t needs.
func (t *Tool) Allowed(who Principal) bool {
	if who == nil {
		return false
	}
	for _, p := range t.Permissions {
		if !who.Can(p) {
			return false
		}
	}
	return true
}

// Args are validated arguments.
type Args map[string]string

// Int is an Int argument; Validate has already bounded it.
func (a Args) Int(name string) int {
	n, _ := strconv.Atoi(a[name])
	return n
}

// Validate checks raw arguments against t's schema and returns clean ones.
//
// Strict in both directions: an argument the schema does not declare is an
// error, not something to ignore, because a model that sends one is telling
// us it thinks the tool does something it does not.
func (t *Tool) Validate(raw map[string]string) (Args, error) {
	declared := map[string]Param{}
	for _, p := range t.Params {
		declared[p.Name] = p
	}
	for k := range raw {
		if _, ok := declared[k]; !ok {
			return nil, fmt.Errorf("ابزار «%s» پارامتری به نام «%s» ندارد", t.Title, clip(k, 40))
		}
	}
	out := Args{}
	for _, p := range t.Params {
		v, ok := raw[p.Name]
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			if p.Required {
				return nil, fmt.Errorf("«%s» لازم است", p.Label)
			}
			continue
		}
		clean, err := validValue(p, v)
		if err != nil {
			return nil, err
		}
		out[p.Name] = clean
	}
	return out, nil
}

// validatePlanned checks a plan step before anything runs: every argument
// named is declared, every fixed value is valid, and every required argument
// is either fixed or filled from an earlier step's fact. The values from facts
// are validated when the step runs, once they exist.
func (t *Tool) validatePlanned(fixed, from map[string]string) error {
	declared := map[string]Param{}
	for _, p := range t.Params {
		declared[p.Name] = p
	}
	for k := range from {
		if _, ok := declared[k]; !ok {
			return fmt.Errorf("ابزار «%s» پارامتری به نام «%s» ندارد", t.Title, clip(k, 40))
		}
	}
	for k, v := range fixed {
		if _, filled := from[k]; filled {
			continue
		}
		p, ok := declared[k]
		if !ok {
			return fmt.Errorf("ابزار «%s» پارامتری به نام «%s» ندارد", t.Title, clip(k, 40))
		}
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if _, err := validValue(p, v); err != nil {
			return err
		}
	}
	for _, p := range t.Params {
		if _, filled := from[p.Name]; filled || !p.Required {
			continue
		}
		if strings.TrimSpace(fixed[p.Name]) == "" {
			return fmt.Errorf("«%s» لازم است", p.Label)
		}
	}
	return nil
}

var phoneShape = regexp.MustCompile(`^\+?[0-9]{8,20}$`)

func validValue(p Param, v string) (string, error) {
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' {
			return "", fmt.Errorf("«%s» نویسهٔ نامعتبر دارد", p.Label)
		}
	}
	max := p.Max
	switch p.Kind {
	case Text:
		if max <= 0 {
			max = 200
		}
		if utf8.RuneCountInString(v) > max {
			return "", fmt.Errorf("«%s» بیش از %d نویسه است", p.Label, max)
		}
		return v, nil
	case Int:
		n, err := strconv.Atoi(LatinDigits(v))
		if err != nil {
			return "", fmt.Errorf("«%s» باید یک عدد باشد", p.Label)
		}
		if n < p.Min || (p.Max != 0 && n > p.Max) {
			return "", fmt.Errorf("«%s» باید میان %d و %d باشد", p.Label, p.Min, p.Max)
		}
		return strconv.Itoa(n), nil
	case Ref:
		v = LatinDigits(v)
		if utf8.RuneCountInString(v) > 40 {
			return "", fmt.Errorf("«%s» بیش از حد بلند است", p.Label)
		}
		digits := 0
		for _, r := range v {
			switch {
			case r >= '0' && r <= '9':
				digits++
			case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '/', r == '-', r == '.', r == ' ':
			default:
				return "", fmt.Errorf("«%s» فقط رقم، حرف لاتین و جداکننده می‌پذیرد", p.Label)
			}
		}
		if digits < 3 {
			return "", fmt.Errorf("«%s» دست‌کم سه رقم لازم دارد", p.Label)
		}
		return v, nil
	case Email:
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("«%s» نشانی درستی نیست", p.Label)
		}
		a, err := mail.ParseAddress(v)
		if err != nil || a.Address != v {
			return "", fmt.Errorf("«%s» نشانی ایمیل درستی نیست", p.Label)
		}
		return v, nil
	case Phone:
		v = strings.NewReplacer(" ", "", "-", "").Replace(LatinDigits(v))
		if !phoneShape.MatchString(v) {
			return "", fmt.Errorf("«%s» شمارهٔ درستی نیست", p.Label)
		}
		return v, nil
	case Time:
		at, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return "", fmt.Errorf("«%s» زمان درستی نیست", p.Label)
		}
		return at.UTC().Format(time.RFC3339), nil
	case Choice:
		if slices.Contains(p.Choices, v) {
			return v, nil
		}
		return "", fmt.Errorf("«%s» یکی از گزینه‌های مجاز نیست", p.Label)
	}
	return "", fmt.Errorf("«%s» نوع ناشناخته‌ای دارد", p.Label)
}

// LatinDigits folds Persian and Arabic-Indic digits to ASCII and leaves
// everything else alone.
func LatinDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
