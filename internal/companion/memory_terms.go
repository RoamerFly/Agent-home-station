package companion

import (
	"sort"
	"strings"
	"unicode"
)

// Chinese bigrams and normalized Latin words share a single exact inverted
// index. No model/network call is needed for the lexical fallback.
func memoryTerms(text string) []string {
	seen := map[string]bool{}
	word := []rune{}
	var prev rune
	flush := func() {
		if len(word) > 0 {
			seen[string(word)] = true
			word = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) {
			flush()
			seen[string(r)] = true
			if prev != 0 {
				seen[string([]rune{prev, r})] = true
			}
			prev = r
		} else {
			prev = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
