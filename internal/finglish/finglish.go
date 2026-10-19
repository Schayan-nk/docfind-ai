// Package finglish reads Persian typed in Latin letters -- "vaziate sefaresh
// 1234 chie?" for «وضعیت سفارش ۱۲۳۴ چیه؟» -- so an assistant can understand it
// as the Persian it is.
//
// WHY A TABLE. Transliterating NAMES cannot be done by table: the vowels
// Persian leaves unwritten are unknowable, and the right answer is often a
// translation. This renders the few hundred words people actually ask an
// assistant with -- «sefaresh», «gheymat», «chie», «emruz» -- whose Persian is
// not in doubt. Everything else, names included, is left exactly as typed: a
// name spelled in Latin is found in Latin.
//
// WHAT IT IS FOR. An assistant's rules are usually Persian: intent words,
// keywords, readers of order and case numbers. A Finglish question meets none
// of them. Rendered, it meets all of them the way the same question typed in
// Persian would.
//
// WHAT IT MUST NOT DO is fire on English or German, which is most of the mail
// in an office that trades abroad. Is only says yes when the words are
// distinctively Persian (see the threshold there), and ToPersian is only ever
// applied after it.
package finglish

import "strings"

const zwnj = "‌"

// Instruction is the sentence a model is given beside a person's own words.
//
// "Whatever you write for the person is Persian", rather than "answer in
// Persian", because the prompts it goes into ask for JSON and nothing else,
// and one of them says in so many words to write no answer. A model handed
// both would have to pick one to disobey.
//
// Names, numbers and codes are named because a model told to write Persian
// will otherwise translate an argument -- "Siemens" into «زیمنس» -- and a
// search for the Persian spelling misses mail written in the Latin one.
const Instruction = "کاربر گاهی فارسی را با حروف لاتین می‌نویسد (فینگلیش)، مثل «vaziate sefaresh chie»؛ " +
	"چنین متنی را همان فارسی بخوان. هر چه برای کاربر می‌نویسی همیشه فارسی باشد، " +
	"ولی نام‌ها، شماره‌ها و کدها را همان‌طور که نوشته شده نگه دار."

// Rendering is the Persian a text should be matched as, and whether it was
// Finglish at all. Text that is not Finglish comes back unchanged -- the
// callers match on the result either way, so Persian and English questions
// reach every rule byte for byte as they did before.
func Rendering(text string) (string, bool) {
	if !Is(text) {
		return text, false
	}
	return ToPersian(text), true
}

// PromptLine is the rendering as one line for a prompt, or "" when the text is
// not Finglish. The original goes to the model as well: a local model reads
// the Persian, and a larger one can still check it against what was typed.
func PromptLine(text string) string {
	fa, ok := Rendering(text)
	if !ok {
		return ""
	}
	return "همین متن به خط فارسی: «" + fa + "»"
}

// Is reports whether text is Finglish.
//
// THE THRESHOLD. Every Latin word is one of four things: distinctive (only
// Finglish writes it), ambiguous or neutral (Finglish, but also English or
// German: «in», «to», «order», «check»), or unknown. The ambiguous and neutral
// words are left out of the count altogether, because English sentences are
// dense with them. Of the rest, the text is Finglish when
//
//   - at least one word is distinctive,
//   - the distinctive words are at least HALF of them, and
//   - the Latin words are at least as many as the Persian-script ones.
//
// Half, not most, so that «sefareshaye Golzar Steel» -- two Persian words
// and a Latin customer name -- still counts; an English sentence carries far
// more unknown words than that ("the", "of", "for", "attached") and a real
// one with a Persian name in it («Dear Mr. Vali, the invoice is attached»)
// comes nowhere near. Numbers and codes («204518», «KX1234567») are not
// words and count for neither side.
func Is(text string) bool {
	distinct, unknown, latin, script := 0, 0, 0, 0
	for _, p := range tokenize(text) {
		switch p.kind {
		case kindScript:
			script++
		case kindWord:
			latin++
			if neutral[canon(p.text)] {
				continue
			}
			e, ok := lookup(p.text)
			switch {
			case !ok:
				unknown++
			case e.distinct:
				distinct++
			}
		}
	}
	return distinct > 0 && distinct >= unknown && latin >= script
}

// ToPersian renders the Finglish words it knows in Persian script and leaves
// everything else -- names, codes, numbers, English, punctuation, Persian --
// as it was. It does not decide whether the text is Finglish; see Rendering.
func ToPersian(text string) string {
	ps := tokenize(text)
	var out []string
	orphan := false // a dropped word with no space before it to take along
	for i := 0; i < len(ps); i++ {
		p := ps[i]
		if p.kind != kindWord {
			if !(orphan && p.kind == kindSep && strings.TrimSpace(p.text) == "") {
				out = append(out, p.text)
			}
			orphan = false
			continue
		}
		orphan = false
		fa, known, last := renderAt(ps, i)
		if !known {
			out = append(out, p.text)
			continue
		}
		i = last
		if fa == "" {
			// An ezafe typed apart: not written in Persian. It takes the
			// space before it with it, or the one after when it opens the text.
			var dropped bool
			out, dropped = dropSpace(out)
			orphan = !dropped
			continue
		}
		// «sefaresh haye», «pish factor ha»: the plural typed as a word of
		// its own joins the word before it, as Persian writes it.
		if j := nextWord(ps, i); j > 0 && isPlural(ps[j].text) {
			pl, _ := lookup(ps[j].text)
			fa, i = fa+zwnj+pl.fa, j
		}
		out = append(out, fa)
	}
	return strings.Join(out, "")
}

// renderAt renders the word at i, as a two-word phrase when it starts one,
// and returns the index of the last word used.
func renderAt(ps []piece, i int) (string, bool, int) {
	if j := nextWord(ps, i); j > 0 {
		if fa, ok := phrase(ps[i].text, ps[j].text); ok {
			return fa, true, j
		}
	}
	e, ok := lookup(ps[i].text)
	return e.fa, ok, i
}

// phrase renders two words Persian writes as one, keeping a suffix on the
// second: «zemanat namehaye» is «ضمانت‌نامه‌های».
func phrase(a, b string) (string, bool) {
	ca, cb := canon(a), canon(b)
	if fa, ok := phrases[ca+" "+cb]; ok {
		return fa, true
	}
	for _, s := range suffixes {
		if stem, ok := strings.CutSuffix(cb, s.latin); ok {
			if fa, ok := phrases[ca+" "+stem]; ok {
				return join(fa, stem, s.fa), true
			}
		}
	}
	return "", false
}

// nextWord is the index of the word after i when only spaces or a hyphen
// separate them, or -1.
func nextWord(ps []piece, i int) int {
	j := i + 1
	if j < len(ps) && ps[j].kind == kindSep && strings.Trim(ps[j].text, " -") == "" {
		j++
	}
	if j == i+1 || j >= len(ps) || ps[j].kind != kindWord {
		return -1
	}
	return j
}

func isPlural(word string) bool {
	switch canon(word) {
	case "ha", "haye", "hay":
		return true
	}
	return false
}

// dropSpace removes the space before a dropped word, and reports whether
// there was one.
func dropSpace(out []string) ([]string, bool) {
	if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
		return out[:n-1], true
	}
	return out, false
}
