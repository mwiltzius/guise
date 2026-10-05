package transform

import (
	"fmt"
	"regexp"
)

// A Detector flags text that looks sensitive even though it is not in the
// vault. Detector matches become unreviewed placeholders named after the
// detector's kind, e.g. {{pi.unreviewed.email.1}}.
type Detector interface {
	// Kind names what the detector finds ("email", "phone", ...).
	Kind() string
	// Find returns byte ranges [start, end) of sensitive text.
	Find(text string) [][2]int
}

// RegexDetector flags every match of a regular expression, optionally
// filtered by Valid.
type RegexDetector struct {
	Name  string
	Re    *regexp.Regexp
	Valid func(string) bool
}

func (d RegexDetector) Kind() string { return d.Name }

func (d RegexDetector) Find(text string) [][2]int {
	var out [][2]int
	for _, loc := range d.Re.FindAllStringIndex(text, -1) {
		if d.Valid == nil || d.Valid(text[loc[0]:loc[1]]) {
			out = append(out, [2]int{loc[0], loc[1]})
		}
	}
	return out
}

// NewPatternDetector compiles a user-supplied pattern. Its name becomes the
// kind of the unreviewed placeholders it produces.
func NewPatternDetector(name, pattern string) (RegexDetector, error) {
	if !ValidName(name) {
		return RegexDetector{}, fmt.Errorf("invalid pattern name %q: use letters, digits, '_', '-' and '.'", name)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return RegexDetector{}, fmt.Errorf("pattern %q: %w", name, err)
	}
	return RegexDetector{Name: name, Re: re}, nil
}

// BuiltinDetectors returns detectors for common kinds of personal data.
func BuiltinDetectors() []Detector {
	return []Detector{
		RegexDetector{Name: "email", Re: regexp.MustCompile(
			`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)},
		RegexDetector{Name: "phone", Re: regexp.MustCompile(
			`(?:\+\d{1,3}[ .-]?)?(?:\(\d{3}\)|\b\d{3})[ .-]?\d{3}[ .-]?\d{4}\b`)},
		RegexDetector{Name: "phone", Re: regexp.MustCompile(
			`\+\d{1,3}(?:[ .-]?\(?\d{1,4}\)?){2,5}\b`), Valid: digitsBetween(8, 15)},
		RegexDetector{Name: "ssn", Re: regexp.MustCompile(
			`\b\d{3}-\d{2}-\d{4}\b`)},
		RegexDetector{Name: "card", Re: regexp.MustCompile(
			`\b\d(?:[ -]?\d){12,18}\b`), Valid: luhn},
	}
}

func digitsBetween(lo, hi int) func(string) bool {
	return func(s string) bool {
		n := 0
		for i := 0; i < len(s); i++ {
			if s[i] >= '0' && s[i] <= '9' {
				n++
			}
		}
		return n >= lo && n <= hi
	}
}

// luhn reports whether the digits in s pass the Luhn checksum used by
// payment card numbers.
func luhn(s string) bool {
	sum, n, double := 0, 0, false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
		n++
	}
	return n >= 13 && sum%10 == 0
}
