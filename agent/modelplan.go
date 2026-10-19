package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Schayan-nk/docfind-ai/internal/fa"
	"github.com/Schayan-nk/docfind-ai/internal/finglish"
)

// The model planner: for a goal the rules could not place, a language model
// chooses among the registered tools. Among them, and nothing else.
//
// WHAT THE MODEL SEES. The goal -- which the person typed or said -- and the
// list of tools this person may use, with their parameters. No row, no email,
// no document: the plan is made before anything is read.
//
// WHAT IT CAN DO WITH IT. Return a list of tool names and arguments, which is
// then held to exactly the rules a hand-written plan is: every tool
// registered and plannable, every argument declared and valid, no more steps
// than the limit. One invented tool or one Execute tool refuses the WHOLE
// plan -- the steps around an invented one were chosen by the same
// misunderstanding -- and the caller falls back to the rules.

// LanguageModel is the model behind the planner. llm.Model fits it, so the
// provider chain's fallback order, its circuit breakers and its "which
// provider answered" all carry over.
type LanguageModel interface {
	// Complete sends one prompt and returns the text and the provider that
	// answered it.
	Complete(ctx context.Context, system, user string) (text, provider string, err error)
}

// ModelPlanner plans with a language model.
type ModelPlanner struct {
	Model    LanguageModel
	Registry *Registry
	MaxSteps int
}

// Reading is a goal as it was read before planning.
type Reading struct {
	Goal string // with spoken numbers written as digits
	// Heard reports that number words were turned into digits, so a page
	// can show the person the identifier that was actually used.
	Heard bool
	// ReadAs is the Persian a goal typed in Finglish was read as -- «vaziate
	// sefaresh 1234 chie» as «وضعیت سفارش 1234 چیه» -- and "" for any other
	// goal. Goal stays as typed: it is what the person wrote.
	ReadAs  string
	Intents []string
	Subject Subject
	// Continued is set when the subject came from the conversation rather
	// than from these words.
	Continued bool
}

// ReadGoal reads a goal the way every plan starts: number words a speech
// recogniser wrote out turned into digits (SpokenNumbers), Persian and Arabic
// digits into ASCII, and a goal typed in Finglish rendered into the Persian
// it means. What the goal asks for -- its intents and its subject -- is the
// application's to add, because that depends on its tools and its records.
func ReadGoal(goal string) Reading {
	text, heard := SpokenNumbers(goal)
	text = LatinDigits(text)
	r := Reading{Goal: text, Heard: heard}
	if match, isFinglish := finglish.Rendering(text); isFinglish {
		r.ReadAs = match
	}
	return r
}

// Folded is the text an application's rules should match: the Persian a
// Finglish goal was read as, or the goal itself, with letter forms, digits,
// zero-width joiners and short vowels folded so one spelling matches all.
func (r Reading) Folded() string {
	if r.ReadAs != "" {
		return fa.Fold(r.ReadAs)
	}
	return fa.Fold(r.Goal)
}

// ErrModelPlan is any reason a model's plan was not used.
var ErrModelPlan = errors.New("agent: the model's plan was refused")

// modelPlanSystem carries finglish.Instruction BEFORE the output rule rather
// than after it: the last thing a small model reads is the thing it obeys, and
// that has to stay "JSON only".
const modelPlanSystem = "تو برنامه‌ریز یک سامانهٔ اداری هستی. فقط از ابزارهای فهرست‌شده استفاده کن و " +
	"هیچ ابزاری از خودت نساز. هیچ عددی نساز و هیچ پاسخی ننویس؛ فقط برنامه. " +
	finglish.Instruction + " خروجی فقط و فقط " +
	`یک شیء JSON به این شکل است: {"steps":[{"tool":"نام","args":{"پارامتر":"مقدار"}}]}. ` +
	"اگر هیچ ابزاری به کار این هدف نمی‌آید، steps را خالی برگردان؛ این پاسخ درست است."

// Plan asks the model for a plan and validates it.
func (mp ModelPlanner) Plan(ctx context.Context, who Principal, r Reading) (Plan, error) {
	if mp.Model == nil || mp.Registry == nil {
		return Plan{}, fmt.Errorf("%w: no model", ErrModelPlan)
	}
	max := mp.MaxSteps
	if max <= 0 {
		max = 6
	}
	var list strings.Builder
	for _, t := range mp.Registry.Tools() {
		if t.Class == Execute || !t.Allowed(who) {
			continue
		}
		fmt.Fprintf(&list, "- %s: %s\n", t.Name, t.Description)
		for _, p := range t.Params {
			req := ""
			if p.Required {
				req = "، الزامی"
			}
			fmt.Fprintf(&list, "    %s (%s%s): %s\n", p.Name, p.Kind, req, p.Label)
		}
	}
	goal := "هدف: " + clip(r.Goal, 500)
	if r.ReadAs != "" {
		// Both: the rendering is what a small local model can read, and the
		// goal as typed is what a larger one can check the rendering against.
		goal += "\nهمان هدف به خط فارسی: " + clip(r.ReadAs, 500)
	}
	user := goal + "\n\nابزارها:\n" + list.String() +
		fmt.Sprintf("\nحداکثر %d مرحله.", max)

	raw, provider, err := mp.Model.Complete(ctx, modelPlanSystem, user)
	if err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrModelPlan, err)
	}
	steps, err := mp.parse(raw, who, max)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Goal: r.Goal, Subject: r.Subject, PlannedBy: "model", Provider: provider,
		Intents: r.Intents, Steps: steps}
	for _, s := range steps {
		if !slices.Contains(p.Specialists, s.Specialist) {
			p.Specialists = append(p.Specialists, s.Specialist)
		}
	}
	return p, nil
}

func (mp ModelPlanner) parse(raw string, who Principal, max int) ([]PlannedStep, error) {
	obj, ok := jsonObject(raw)
	if !ok {
		return nil, fmt.Errorf("%w: not JSON", ErrModelPlan)
	}
	var out struct {
		Steps []struct {
			Tool string         `json:"tool"`
			Args map[string]any `json:"args"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(obj), &out); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModelPlan, err)
	}
	if len(out.Steps) == 0 {
		return nil, fmt.Errorf("%w: no steps", ErrModelPlan)
	}
	if len(out.Steps) > max {
		return nil, fmt.Errorf("%w: %d steps, at most %d", ErrModelPlan, len(out.Steps), max)
	}
	var steps []PlannedStep
	for _, s := range out.Steps {
		t, err := mp.Registry.Plannable(s.Tool)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrModelPlan, err)
		}
		if !t.Allowed(who) {
			return nil, fmt.Errorf("%w: %q is not permitted", ErrModelPlan, s.Tool)
		}
		args := map[string]string{}
		for k, v := range s.Args {
			args[k] = strings.TrimSpace(fmt.Sprint(v))
		}
		if _, err := t.Validate(args); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrModelPlan, s.Tool, err)
		}
		steps = append(steps, PlannedStep{Tool: t.Name, Args: args, Title: t.Title,
			Specialist: t.Specialist})
	}
	return steps, nil
}

// jsonObject finds the first balanced {...} in s, skipping braces in strings.
// Models wrap JSON in prose and code fences often enough that this is needed.
func jsonObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
