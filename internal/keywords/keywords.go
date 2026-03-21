// Package keywords provides lightweight keyword extraction using stop-word
// filtering and term frequency. Zero external dependencies.
package keywords

import (
	"sort"
	"strings"
	"unicode"
)

// stopWords is the set of common English words to exclude.
var stopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"do": {}, "does": {}, "did": {}, "will": {}, "would": {}, "could": {},
	"should": {}, "may": {}, "might": {}, "shall": {}, "can": {}, "need": {},
	"dare": {}, "ought": {}, "used": {}, "to": {}, "of": {}, "in": {},
	"for": {}, "on": {}, "with": {}, "at": {}, "by": {}, "from": {}, "as": {},
	"into": {}, "through": {}, "during": {}, "before": {}, "after": {},
	"above": {}, "below": {}, "between": {}, "out": {}, "off": {}, "over": {},
	"under": {}, "again": {}, "further": {}, "then": {}, "once": {}, "here": {},
	"there": {}, "when": {}, "where": {}, "why": {}, "how": {}, "all": {},
	"each": {}, "every": {}, "both": {}, "few": {}, "more": {}, "most": {},
	"other": {}, "some": {}, "such": {}, "no": {}, "nor": {}, "not": {},
	"only": {}, "own": {}, "same": {}, "so": {}, "than": {}, "too": {},
	"very": {}, "just": {}, "because": {}, "but": {}, "and": {}, "or": {},
	"if": {}, "while": {}, "that": {}, "this": {}, "these": {}, "those": {},
	"it": {}, "its": {}, "i": {}, "me": {}, "my": {}, "we": {}, "our": {},
	"you": {}, "your": {}, "he": {}, "him": {}, "his": {}, "she": {}, "her": {},
	"they": {}, "them": {}, "their": {}, "what": {}, "which": {}, "who": {}, "whom": {},
}

// stripPunctuation removes leading and trailing punctuation from a word.
func stripPunctuation(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Extract returns the top N keywords from the given text.
// Uses stop-word filtering and term frequency. No external dependencies.
func Extract(text string, topN int) []string {
	if strings.TrimSpace(text) == "" || topN <= 0 {
		return nil
	}

	freq := make(map[string]int)
	for _, raw := range strings.Fields(strings.ToLower(text)) {
		word := stripPunctuation(raw)
		if len(word) < 3 {
			continue
		}
		if _, isStop := stopWords[word]; isStop {
			continue
		}
		freq[word]++
	}

	type entry struct {
		word  string
		count int
	}
	entries := make([]entry, 0, len(freq))
	for w, c := range freq {
		entries = append(entries, entry{w, c})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].word < entries[j].word
	})

	if topN > len(entries) {
		topN = len(entries)
	}
	result := make([]string, topN)
	for i := 0; i < topN; i++ {
		result[i] = entries[i].word
	}
	return result
}

// ExtractFromPair extracts keywords from a user+assistant message pair.
// Concatenates both texts before extraction.
func ExtractFromPair(userText, assistantText string, topN int) []string {
	return Extract(userText+" "+assistantText, topN)
}
