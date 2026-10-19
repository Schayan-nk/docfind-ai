package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Schayan-nk/docfind-ai/internal/finglish"
)

// Persian and English goals are read as they are: no rendering, and nothing
// said about one.
func TestOnlyAFinglishGoalIsRendered(t *testing.T) {
	for _, g := range []string{"وضعیت سفارش ۱۲۳۴ چیه؟", "why is order 1234 late", "status of case 1234/1"} {
		if r := ReadGoal(g); r.ReadAs != "" {
			t.Errorf("%q was read as %q", g, r.ReadAs)
		}
	}
}

// A goal typed in Finglish is kept as typed, for the page and the
// conversation, and its Persian is said beside it.
func TestAFinglishGoalIsKeptAsTypedAndRendered(t *testing.T) {
	r := ReadGoal("vaziate sefaresh 1234 chie?")
	if r.Goal != "vaziate sefaresh 1234 chie?" {
		t.Errorf("the goal was changed to %q", r.Goal)
	}
	if !strings.Contains(r.ReadAs, "سفارش") {
		t.Errorf("read as %q, want the Persian it means", r.ReadAs)
	}
	if r.Folded() == "" || !strings.Contains(r.Folded(), "سفارش") {
		t.Errorf("Folded() = %q, want the folded rendering", r.Folded())
	}
}

// Number words a speech recogniser wrote out become digits before anything
// else reads the goal, and the reading says so.
func TestASpokenNumberIsReadAsDigits(t *testing.T) {
	r := ReadGoal("وضعیت سفارش هزار و دویست و سی و چهار")
	if !strings.Contains(r.Goal, "1234") || !r.Heard {
		t.Errorf("read as %q (heard %v), want the number in digits", r.Goal, r.Heard)
	}
	if r := ReadGoal("نه"); r.Goal != "نه" || r.Heard {
		t.Errorf("a lone «نه» became %q: a refusal must never turn into a digit", r.Goal)
	}
}

type promptModel struct{ system, user string }

func (m *promptModel) Complete(_ context.Context, system, user string) (string, string, error) {
	m.system, m.user = system, user
	return `{"steps":[{"tool":"search_documents","args":{"query":"x"}}]}`, "ollama/qwen", nil
}

// docsRegistry is one plannable tool, search_documents, for whoever holds
// "docs".
func docsRegistry() *Registry {
	reg := NewRegistry()
	reg.MustRegister(Tool{Name: "search_documents", Title: "جست‌وجوی اسناد",
		Description: "search the documents for a phrase", Specialist: "documents",
		Class: Read, Permissions: []string{"docs"}, Audit: "agent.search_documents",
		Timeout: time.Second, Run: noop,
		Params: []Param{{Name: "query", Label: "عبارت", Kind: Text, Required: true}}})
	return reg
}

// The model planner is told that Persian may come in Latin letters, and a
// Finglish goal reaches it with its rendering beside it.
func TestTheModelPlannerIsGivenTheRendering(t *testing.T) {
	m := &promptModel{}
	r := ReadGoal("chera moshtaria narazi hastan?")
	p, err := (ModelPlanner{Model: m, Registry: docsRegistry()}).Plan(context.Background(), person("docs"), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 || p.Steps[0].Specialist != "documents" || p.Provider != "ollama/qwen" {
		t.Errorf("plan %+v, want the one step, its tool's specialist and the provider", p)
	}
	if !strings.Contains(m.system, finglish.Instruction) {
		t.Error("the planner's system prompt does not carry the Finglish instruction")
	}
	if !strings.Contains(m.user, "chera moshtaria narazi hastan?") || !strings.Contains(m.user, "چرا مشتری‌ها ناراضی هستند") {
		t.Errorf("the goal or its rendering is missing from the prompt:\n%s", m.user)
	}
	// The JSON-only rule stays the last word.
	if !strings.HasSuffix(m.system, "این پاسخ درست است.") || strings.Index(m.system, finglish.Instruction) > strings.Index(m.system, "JSON") {
		t.Error("the Finglish instruction was placed after the output rule")
	}

	persian := &promptModel{}
	pr := ReadGoal("چرا مشتری‌ها ناراضی هستند؟")
	if _, err := (ModelPlanner{Model: persian, Registry: docsRegistry()}).Plan(context.Background(), person("docs"), pr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persian.user, "به خط فارسی") {
		t.Errorf("a Persian goal was given a rendering:\n%s", persian.user)
	}
}

// A model that names a tool the person may not use gets no plan at all.
func TestTheModelPlannerRefusesAToolThePersonMayNotUse(t *testing.T) {
	m := &promptModel{}
	_, err := (ModelPlanner{Model: m, Registry: docsRegistry()}).Plan(context.Background(), person("other"), ReadGoal("x"))
	if err == nil {
		t.Fatal("a plan naming a tool the person may not use was accepted")
	}
	if strings.Contains(m.user, "search_documents") {
		t.Error("a tool the person may not use was offered to the model")
	}
}

// A goal the rules leave unread is summed up by the first fact found; a
// reasoning's own summary wins.
func TestComposeWritesTheFourParts(t *testing.T) {
	run := &Run{Evidence: []Evidence{
		{Kind: Fact, Text: "سفارش ۱۲۳۴ در مرحلهٔ ارسال است"},
		{Kind: Inference, Text: "این مرحله دو برابر معمول طول کشیده است"},
		{Kind: Recommendation, Text: "با مسئول ارسال تماس بگیرید"},
	}}
	Compose(run)
	a := run.Answer
	if a == nil || a.Summary != "سفارش ۱۲۳۴ در مرحلهٔ ارسال است" || a.Conclusion == "" || a.Recommendation == "" {
		t.Fatalf("answer %+v", a)
	}
	if !strings.Contains(a.Spoken, a.Summary) || !strings.Contains(a.Spoken, "جزئیات") {
		t.Errorf("spoken %q, want the summary and the offer of details", a.Spoken)
	}
	run.SetSummary("خلاصهٔ استدلال")
	Compose(run)
	if run.Answer.Summary != "خلاصهٔ استدلال" {
		t.Errorf("the reasoning's summary was not used: %q", run.Answer.Summary)
	}
}
