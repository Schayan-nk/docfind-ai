package finglish

import (
	"strings"
	"unicode"
)

// Spelling: the one word Finglish writes a dozen ways.
//
// There is no standard. «gheymat», «qeymat», «gheimat»; «emrooz», «emruz»;
// «hafte», «hafteh»; «vaziat», «vaziyat». canon brings the spellings people
// actually use to one key, and the tables in lexicon.go list only what it
// cannot bring together.

// canonical rewrites, applied left to right in one pass. A strings.Replacer
// tries its pairs in the order given at each position, so «ee» is seen before
// «ei» could be.
var canonical = strings.NewReplacer(
	"'", "", "’", "", "`", "", "ʼ", "", "‘", "",
	"oo", "u", "ou", "u", "ee", "i", "aa", "a",
	"ei", "ey", "iy", "i",
	"w", "v", "q", "gh", "x", "kh",
)

// accents are the vowels a phone keyboard offers with a mark on them.
var accents = strings.NewReplacer(
	"â", "a", "á", "a", "à", "a", "ā", "a", "ä", "a",
	"ê", "e", "é", "e", "è", "e", "ë", "e",
	"î", "i", "í", "i", "ì", "i", "ï", "i",
	"ô", "o", "ó", "o", "ò", "o", "ö", "o",
	"û", "u", "ú", "u", "ù", "u", "ü", "u",
)

// canon is the key a Latin word is looked up by.
//
//   - lower case, apostrophes gone («ta'min» is «tamin»), accents gone;
//   - the long vowels written doubled become single: oo/ou -> u, ee -> i,
//     aa -> a; «ei» is «ey» and «iy» is «i» («kheili», «vaziyat»);
//   - w -> v, q -> gh, x -> kh, the three letters people use for those sounds;
//   - a doubled consonant is single («barresi», «tahvill») -- except h, which
//     is never doubled inside a word but is at a suffix: «sefareshha»;
//   - a final «eh» is «e» («hafteh»), and a final y after a consonant is i
//     («moshtary»).
func canon(w string) string {
	w = canonical.Replace(accents.Replace(strings.ToLower(w)))
	var b strings.Builder
	var prev rune
	for _, r := range w {
		if r == prev && r != 'h' && !isVowel(r) {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	w = b.String()
	if s, ok := strings.CutSuffix(w, "eh"); ok && s != "" {
		w = s + "e"
	}
	if n := len(w); n > 2 && w[n-1] == 'y' && !isVowel(rune(w[n-2])) {
		w = w[:n-1] + "i"
	}
	return w
}

func isVowel(r rune) bool { return strings.ContainsRune("aeiou", r) }

// suffix is one ending Finglish attaches to a word, and its Persian.
type suffix struct{ latin, fa string }

// suffixes, longest first so «haye» is tried before «ha» and «e».
//
// The ezafe and the colloquial «is» («vaziate sefaresh», «sefareshe») are
// dropped rather than written: Persian script does not write the first, and
// «سفارشه» for the second would match no keyword where «سفارش» matches many.
//
// «aye» and «a» are the plural as it is spoken and then typed: «sefareshaye»,
// «moshtaria». «y» is «i» after a word ending in e, which canon has already
// turned from «ei» into «ey»: «jalasei» arrives here as «jalasey».
var suffixes = []suffix{
	{"haye", zwnj + "های"}, {"haro", zwnj + "ها رو"}, {"hay", zwnj + "های"}, {"ha", zwnj + "ها"},
	{"eshun", "شون"}, {"emun", "مون"}, {"etun", "تون"},
	{"aye", zwnj + "های"}, {"esh", "ش"}, {"am", "م"}, {"et", "ت"}, {"an", "ان"},
	{"ye", ""}, {"ro", " رو"}, {"o", " رو"}, {"e", ""}, {"i", "ی"}, {"y", "ی"}, {"a", zwnj + "ها"},
}

// lookup finds a word, whole or as a known word with a suffix.
func lookup(word string) (entry, bool) {
	c := canon(word)
	if e, ok := lexicon[c]; ok {
		return e, true
	}
	for _, s := range suffixes {
		stem, ok := strings.CutSuffix(c, s.latin)
		if !ok || len(stem) < 2 {
			continue
		}
		// A dropped word takes no suffix: «ye» is an ezafe, not a stem.
		if e, ok := lexicon[stem]; ok && e.fa != "" {
			return entry{fa: join(e.fa, stem, s.fa), distinct: e.distinct}, true
		}
	}
	return entry{}, false
}

// join writes a suffix onto a Persian word.
//
// A word whose Latin ends in «e» ends in a silent heh in Persian («hafte»,
// «jalase»), and a silent heh takes a suffix after a half-space, with an alef
// before a single-letter one: «جلسه‌ای», «هفته‌اش». A heh that is sounded --
// «mah», «ماه» -- takes it directly, which is why the Latin decides and not
// the last Persian letter.
func join(fa, latinStem, suf string) string {
	if suf == "" || strings.HasPrefix(suf, zwnj) || strings.HasPrefix(suf, " ") {
		return fa + suf
	}
	if strings.HasSuffix(latinStem, "e") && strings.HasSuffix(fa, "ه") {
		if len([]rune(suf)) == 1 {
			return fa + zwnj + "ا" + suf
		}
		return fa + zwnj + suf
	}
	return fa + suf
}

// Tokens. A piece is a run of one kind; the text is the pieces joined.
type kind int

const (
	kindSep    kind = iota // spaces, punctuation, anything else
	kindWord               // Latin letters only
	kindCode               // Latin letters and digits: 204518, KX1234567, S7
	kindScript             // Persian or Arabic letters
)

type piece struct {
	text string
	kind kind
}

// tokenize splits text into pieces. An apostrophe inside a Latin word is
// part of it («ta'min»); anywhere else it is punctuation.
func tokenize(text string) []piece {
	rs := []rune(text)
	var ps []piece
	for i := 0; i < len(rs); {
		j, k := i+1, classOf(rs, i)
		for j < len(rs) && sameRun(k, classOf(rs, j)) {
			j++
		}
		if k == kindWord && hasDigit(rs[i:j]) {
			k = kindCode
		}
		ps = append(ps, piece{text: string(rs[i:j]), kind: k})
		i = j
	}
	return ps
}

// classOf is the kind of the rune at i, with digits reported as kindCode so a
// run of letters and digits stays one piece.
func classOf(rs []rune, i int) kind {
	r := rs[i]
	switch {
	case r < unicode.MaxASCII && unicode.IsDigit(r):
		return kindCode
	case unicode.IsLetter(r) && unicode.Is(unicode.Latin, r):
		return kindWord
	case unicode.IsLetter(r) && unicode.Is(unicode.Arabic, r):
		return kindScript
	case strings.ContainsRune("'’`ʼ‘", r) && i > 0 && i+1 < len(rs) &&
		latinLetter(rs[i-1]) && latinLetter(rs[i+1]):
		// The neighbours are looked at as letters, not classified: asking
		// them for their class recursed between two apostrophes side by
		// side («''», common in pasted mail) until the stack overflowed --
		// which kills the process, not the request.
		return kindWord
	}
	return kindSep
}

func latinLetter(r rune) bool { return unicode.IsLetter(r) && unicode.Is(unicode.Latin, r) }

// sameRun reports whether two runes' kinds belong in one piece: letters and
// digits together make a code, and every other kind runs only with itself.
func sameRun(a, b kind) bool {
	latinish := func(k kind) bool { return k == kindWord || k == kindCode }
	return a == b || latinish(a) && latinish(b)
}

func hasDigit(rs []rune) bool {
	for _, r := range rs {
		if r < unicode.MaxASCII && unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
