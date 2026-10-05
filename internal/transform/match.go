package transform

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// span is a half-open byte range [start, end) in a piece of text.
type span struct {
	start, end int
}

func (s span) len() int { return s.end - s.start }

// match is a candidate replacement found in the original text.
type match struct {
	span
	priority  int    // lower wins ties between equal-length matches
	name      string // vault entry name; empty for detector matches
	mod       string
	irregular bool   // case differs in a way upper/lower cannot reproduce
	kind      string // detector kind, for detector matches
}

const (
	prioVault = iota
	prioDetector
)

// valueMatcher finds occurrences of one vault value, case-insensitively and
// on whole-word boundaries.
type valueMatcher struct {
	name  string
	value string
	re    *regexp.Regexp
}

func newValueMatcher(name, value string) valueMatcher {
	return valueMatcher{name: name, value: value, re: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(value))}
}

// find returns matches of the value in text. A match whose case differs from
// the vault value gets an upper/lower modifier when one reproduces it
// exactly; otherwise it is marked irregular and the caller records it as a
// variant spelling.
func (m valueMatcher) find(text string) []match {
	var out []match
	for _, loc := range m.re.FindAllStringIndex(text, -1) {
		s := span{loc[0], loc[1]}
		if !wordBounded(text, s) {
			continue
		}
		got := text[s.start:s.end]
		mt := match{span: s, priority: prioVault, name: m.name}
		switch {
		case got == m.value:
		case got == strings.ToUpper(m.value):
			mt.mod = ModUpper
		case got == strings.ToLower(m.value):
			mt.mod = ModLower
		default:
			mt.irregular = true
		}
		out = append(out, mt)
	}
	return out
}

// wordBounded reports whether s does not split a word: if the match starts
// (ends) with a word character, the preceding (following) character must not
// be one.
func wordBounded(text string, s span) bool {
	first, _ := utf8.DecodeRuneInString(text[s.start:])
	if isWordRune(first) && s.start > 0 {
		prev, _ := utf8.DecodeLastRuneInString(text[:s.start])
		if isWordRune(prev) {
			return false
		}
	}
	last, _ := utf8.DecodeLastRuneInString(text[:s.end])
	if isWordRune(last) && s.end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[s.end:])
		if isWordRune(next) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// selectMatches picks a non-overlapping set of matches. Longer matches win,
// then lower priority, then earlier position. Matches overlapping a reserved
// span are discarded. The result is sorted by position.
func selectMatches(textLen int, cands []match, reserved []span) []match {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.len() != b.len() {
			return a.len() > b.len()
		}
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		return a.start < b.start
	})
	taken := make([]bool, textLen)
	for _, r := range reserved {
		for i := r.start; i < r.end; i++ {
			taken[i] = true
		}
	}
	var out []match
next:
	for _, c := range cands {
		if c.len() == 0 {
			continue
		}
		for i := c.start; i < c.end; i++ {
			if taken[i] {
				continue next
			}
		}
		for i := c.start; i < c.end; i++ {
			taken[i] = true
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}
