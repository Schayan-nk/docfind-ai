package agent

import (
	"strings"

	"github.com/Schayan-nk/docfind-ai/internal/fa"
)

// Reading a spoken or typed confirmation.
//
// ONLY AN UNQUALIFIED YES IS A YES. «بله» sends the email; «آره شاید» does
// not, and neither does «فکر کنم» or «بفرست ولی نه الان». The cost of the two
// mistakes is not symmetrical: asking again costs a sentence, and sending an
// email to a customer on a misheard "maybe" cannot be taken back.
//
// So the utterance must consist ONLY of words from the yes list (and a few
// courtesy words that change nothing, like «لطفا»). One hedge, one negation,
// or one word this does not know makes it Unclear -- which the caller answers
// by asking again, never by acting.

// Verdict is how a confirmation was understood.
type Verdict string

const (
	Yes     Verdict = "yes"
	No      Verdict = "no"
	Unclear Verdict = "unclear"
)

var (
	// strongYes are the words that mean yes on their own. At least one must
	// be present: «کن» or "it" alone agrees to nothing.
	strongYes = wordSet("بله", "بلی", "آره", "اره", "آری", "حتما", "حتماً", "تایید", "تأیید",
		"ارسال", "بفرست", "بفرستش", "بفرستید", "درسته", "yes", "yeah", "yep", "confirm",
		"confirmed", "send", "ok", "okay")
	// weakYes only complete a strong one: «ارسال کن»، «تأیید می‌کنم»، "send it".
	weakYes = wordSet("میکنم", "می‌کنم", "کن", "کنید", "بشه", "انجام", "بده", "بدید", "ثبت",
		"it", "go", "ahead", "do")
	// Words that are only politeness and change no meaning.
	courtesy = wordSet("لطفا", "لطفاً", "خب", "خوب", "please", "sure")
	noWords  = wordSet("نه", "خیر", "نخیر", "نفرست", "نفرستید", "نکن", "نکنید", "لغو", "کنسل",
		"بیخیال", "بی‌خیال", "صبر", "بعدا", "بعداً", "no", "nope", "cancel", "stop", "dont",
		"don't", "wait", "later")
	hedges = wordSet("شاید", "احتمالا", "احتمالاً", "فکر", "کنم", "نمیدونم", "نمی‌دونم",
		"مطمئن", "نیستم", "ببینم", "maybe", "perhaps", "probably", "think", "guess", "not")
)

// Confirmation reads one utterance as yes, no or unclear.
func Confirmation(utterance string) Verdict {
	words := strings.FieldsFunc(fa.Fold(utterance), func(r rune) bool {
		return r == ' ' || r == '.' || r == '،' || r == ',' || r == '!' || r == '؟' || r == '?' || r == '؛'
	})
	if len(words) == 0 {
		return Unclear
	}
	yes, no := 0, 0
	for _, w := range words {
		switch {
		case hedges[w]:
			return Unclear
		case noWords[w]:
			no++
		case strongYes[w]:
			yes++
		case weakYes[w], courtesy[w]:
		default:
			// A word this does not know is a word that might have changed the
			// meaning. Asking again is cheap.
			return Unclear
		}
	}
	switch {
	case no > 0 && yes == 0:
		return No
	case yes > 0 && no == 0:
		return Yes
	}
	return Unclear
}

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[fa.Fold(w)] = true
	}
	return m
}
