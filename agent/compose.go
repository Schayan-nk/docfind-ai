package agent

import (
	"strings"
	"unicode/utf8"
)

// Writing the answer: the same four parts on the page and aloud.
//
//	خلاصه           the summary: the reasoning's, or the first fact found
//	شواهد           the evidence list itself, shown on the page
//	نتیجه           the first inference, or the first unknown when there is none
//	اقدام پیشنهادی  the first recommendation
//
// The spoken form is short on purpose. A table read aloud is a table nobody
// hears the end of, so the voice says the summary and the conclusion, says
// whether an action waits for approval, and offers the rest rather than
// reciting it.

// spokenMax bounds the spoken answer, in runes: about twenty seconds of speech.
const spokenMax = 360

// Compose is the runner's default Finish: it writes the answer from the
// evidence the steps returned.
//
// An application with reasoning of its own -- conclusions drawn in Go from the
// tools' structured results (Run.Outputs), each marked as a fact, an inference
// or a recommendation -- runs it first and then calls Compose:
//
//	runner.Finish = func(r *agent.Run) {
//		myReasoning(r) // appends to r.Evidence and r.Proposals, may SetSummary
//		agent.Compose(r)
//	}
func Compose(run *Run) {
	run.Answer = answerFor(run)
}

func answerFor(run *Run) *Answer {
	a := &Answer{}
	switch {
	case run.Plan.Note != "":
		a.Summary = run.Plan.Note
	case run.summary != "":
		a.Summary = run.summary
	default:
		a.Summary = firstOf(run, Fact)
	}
	if a.Summary == "" {
		a.Summary = "در داده‌هایی که بررسی شد، چیزی دربارهٔ این درخواست پیدا نشد."
	}
	a.Conclusion = firstOf(run, Inference)
	if a.Conclusion == "" {
		a.Conclusion = firstOf(run, Unknown)
	}
	a.Recommendation = firstOf(run, Recommendation)
	a.Spoken = spoken(run, a)
	return a
}

// firstOf is the text of the first piece of evidence of one kind, or "".
func firstOf(run *Run, kind EvidenceKind) string {
	for _, e := range run.Evidence {
		if e.Kind == kind && strings.TrimSpace(e.Text) != "" {
			return e.Text
		}
	}
	return ""
}

// spoken is the answer as it will be said.
func spoken(run *Run, a *Answer) string {
	parts := []string{sentence(a.Summary)}
	if a.Conclusion != "" && a.Conclusion != a.Summary {
		parts = append(parts, sentence(a.Conclusion))
	}
	if openProposals(run) > 0 {
		parts = append(parts, "یک اقدام پیشنهادی آماده است؛ تا تأیید نکنید، انجام نمی‌شود.")
	}
	out := strings.Join(parts, " ")
	if utf8.RuneCountInString(out) > spokenMax {
		out = clipSentence(out, spokenMax)
	}
	if len(run.Evidence) > 1 {
		out += " اگر بخواهید، جزئیات را هم نشان می‌دهم."
	}
	return out
}

func openProposals(run *Run) int {
	n := 0
	for _, p := range run.Proposals {
		if p.Status == "" || p.Status == ProposalOpen {
			n++
		}
	}
	return n
}

// sentence ends s with a full stop when it has no ending of its own.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch r, _ := utf8.DecodeLastRuneInString(s); r {
	case '.', '؟', '?', '!', '…':
		return s
	}
	return s + "."
}

// clipSentence cuts at the last sentence end before max runes, so speech
// never stops in the middle of a number.
func clipSentence(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := r[:max]
	for i := len(cut) - 1; i > max/2; i-- {
		if cut[i] == '.' || cut[i] == '؟' {
			return string(cut[:i+1])
		}
	}
	return string(cut) + "…"
}
