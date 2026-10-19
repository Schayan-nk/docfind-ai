package agent

import (
	"strconv"
	"strings"

	"github.com/Schayan-nk/docfind-ai/internal/fa"
)

// Spoken numbers: «هزار و دویست و سی و چهار» is 1234.
//
// A speech recogniser writes Persian numbers as words about as often as it
// writes digits, and an order number spoken as words is still an order number.
// So words are turned into digits before the goal is read -- but carefully,
// because the same words are ordinary Persian too:
//
//   - «یک پاسخ آماده کن» is "prepare A reply", not "prepare 1 reply".
//   - «نه» on its own is "no", and turning a refusal into the digit 9 is the
//     worst thing this could do.
//
// So a lone small number word is left alone. Words become digits only when
// they are unmistakably a number: a compound joined by «و» (سی و چهار), a word
// that is only ever a number (بیست، صد، هزار), or three or more digits
// dictated one after another («شش هشت سه» is 683, the way people read a
// reference aloud).

var (
	faUnits = map[string]int{
		"صفر": 0, "یک": 1, "یه": 1, "دو": 2, "سه": 3, "چهار": 4, "پنج": 5,
		"شش": 6, "شیش": 6, "هفت": 7, "هشت": 8, "نه": 9,
	}
	faBig = map[string]int{
		"ده": 10, "یازده": 11, "دوازده": 12, "سیزده": 13, "چهارده": 14,
		"پانزده": 15, "پونزده": 15, "شانزده": 16, "شونزده": 16, "هفده": 17,
		"هیفده": 17, "هجده": 18, "هیجده": 18, "نوزده": 19,
		"بیست": 20, "سی": 30, "چهل": 40, "پنجاه": 50, "شصت": 60,
		"هفتاد": 70, "هشتاد": 80, "نود": 90,
		"صد": 100, "یکصد": 100, "دویست": 200, "سیصد": 300, "چهارصد": 400,
		"پانصد": 500, "پونصد": 500, "ششصد": 600, "شیشصد": 600, "هفتصد": 700,
		"هشتصد": 800, "نهصد": 900,
	}
	faMult = map[string]int{"هزار": 1000, "میلیون": 1_000_000, "میلیارد": 1_000_000_000}
)

func numberWord(tok string) bool {
	_, u := faUnits[tok]
	_, b := faBig[tok]
	_, m := faMult[tok]
	return u || b || m
}

// SpokenNumbers rewrites number words in s as digits and reports whether it
// changed anything. Everything that is not a number is returned as it was.
func SpokenNumbers(s string) (string, bool) {
	toks := strings.Fields(s)
	if len(toks) == 0 {
		return s, false
	}
	folded := make([]string, len(toks))
	for i, t := range toks {
		folded[i] = strings.Trim(fa.Fold(t), ".،,؟?!:؛")
	}
	var out []string
	changed := false
	for i := 0; i < len(toks); {
		if !numberWord(folded[i]) {
			out = append(out, toks[i])
			i++
			continue
		}
		// Gather the group: number words, and «و» between two of them.
		j, joined := i, false
		for j < len(toks) {
			if numberWord(folded[j]) {
				j++
				continue
			}
			if folded[j] == "و" && j+1 < len(toks) && numberWord(folded[j+1]) && j > i {
				joined = true
				j++
				continue
			}
			break
		}
		group := folded[i:j]
		if digits, ok := groupDigits(group, joined); ok {
			// Keep punctuation that was stuck to the last word.
			tail := trailingPunct(toks[j-1])
			out = append(out, digits+tail)
			changed = true
		} else {
			out = append(out, toks[i:j]...)
		}
		i = j
	}
	return strings.Join(out, " "), changed
}

// groupDigits turns one group of number words into digits, or declines.
func groupDigits(group []string, joined bool) (string, bool) {
	var words []string
	for _, g := range group {
		if g != "و" {
			words = append(words, g)
		}
	}
	allUnits := true
	for _, w := range words {
		if _, ok := faUnits[w]; !ok {
			allUnits = false
		}
	}
	switch {
	case allUnits && !joined && len(words) >= 3:
		// Dictated digit by digit.
		var b strings.Builder
		for _, w := range words {
			b.WriteString(strconv.Itoa(faUnits[w]))
		}
		return b.String(), true
	case allUnits && !joined:
		// One or two small words with nothing joining them: ordinary Persian
		// («یک پاسخ»، «نه»), left as it is.
		return "", false
	}
	total, cur := 0, 0
	for _, w := range words {
		if v, ok := faUnits[w]; ok {
			cur += v
		} else if v, ok := faBig[w]; ok {
			cur += v
		} else if m, ok := faMult[w]; ok {
			if cur == 0 {
				cur = 1
			}
			total += cur * m
			cur = 0
		}
	}
	return strconv.Itoa(total + cur), true
}

func trailingPunct(tok string) string {
	r := []rune(tok)
	i := len(r)
	for i > 0 && strings.ContainsRune(".،,؟?!:؛", r[i-1]) {
		i--
	}
	return string(r[i:])
}
