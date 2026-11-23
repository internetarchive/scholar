package cleaning

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeTitle folds a title down to what is worth comparing: Unicode
// compatibility-decomposed with combining marks dropped (so "Império" and
// "Imperio" agree), lowercased, every run of non-alphanumerics turned into a
// single space, trimmed.
func NormalizeTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true // suppresses a leading space
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// titlePrefixMinRunes is how long the shorter of two titles must be before
// "one is a prefix of the other" counts as a match. Below it, short generic
// titles ("Introduction", "Essays on growth") would match far too much.
const titlePrefixMinRunes = 40

// maxTitleRunes bounds the edit-distance computation, which is quadratic. Real
// titles are far shorter; this only keeps a pathological record from costing
// seconds.
const maxTitleRunes = 1000

// TitleSimilarity scores how alike two titles are, from 0 (nothing in common,
// or either is empty) to 1 (the same once normalized), using a normalized
// Levenshtein ratio over NormalizeTitle output.
//
// One title being a word-boundary prefix of the other also scores 1 when the
// shorter one is at least titlePrefixMinRunes long. Theses especially get
// catalogued with and without their subtitle, and edit distance alone punishes
// a dropped subtitle as heavily as an entirely different title.
func TitleSimilarity(a, b string) float64 {
	na, nb := []rune(NormalizeTitle(a)), []rune(NormalizeTitle(b))
	if len(na) == 0 || len(nb) == 0 {
		return 0
	}
	if len(na) > maxTitleRunes {
		na = na[:maxTitleRunes]
	}
	if len(nb) > maxTitleRunes {
		nb = nb[:maxTitleRunes]
	}

	short, long := na, nb
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) >= titlePrefixMinRunes && string(long[:len(short)]) == string(short) &&
		(len(long) == len(short) || long[len(short)] == ' ') {
		return 1
	}

	d := levenshtein(na, nb)
	return 1 - float64(d)/float64(len(long))
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
