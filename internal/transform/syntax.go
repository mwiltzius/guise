// Package transform converts text between its original form and its guise.
//
// In hide mode, values from a vault (and anything the detectors flag) are
// replaced with placeholders such as {{pi.lastname}}, and placeholders are
// rendered back into values on the way in. In fill mode the target holds
// placeholders and the guise holds values.
package transform

import "strings"

// Syntax describes how placeholders are written: Open + Prefix + "." + name
// [":" modifier] + Close, e.g. {{pi.lastname}} or {{pi.lastname:upper}}.
//
// Dots in a name separate different values of the same kind (lastname,
// lastname.2). A modifier selects a spelling of the same value: "upper",
// "lower", or a recorded variant number (lastname:2).
//
// A literal placeholder-like sequence in hide-mode target text is escaped by
// adding a backslash after the prefix: "{{pi.x}}" becomes "{{pi\.x}}", and
// "{{pi\.x}}" becomes "{{pi\\.x}}". Only Open+Prefix followed by backslashes
// and a dot is affected, so the escape is always reversible.
type Syntax struct {
	Open   string
	Close  string
	Prefix string
}

// DefaultSyntax is {{pi.name}}.
var DefaultSyntax = Syntax{Open: "{{", Close: "}}", Prefix: "pi"}

// Modifiers record how a matched value's spelling differed from the vault
// value. Besides these, a decimal number N >= 2 names a recorded variant.
const (
	ModNone  = ""
	ModUpper = "upper"
	ModLower = "lower"
)

func (s Syntax) head() string { return s.Open + s.Prefix }

// Placeholder renders a placeholder for name with an optional modifier.
func (s Syntax) Placeholder(name, mod string) string {
	if mod != ModNone {
		return s.head() + "." + name + ":" + mod + s.Close
	}
	return s.head() + "." + name + s.Close
}

// ValidName reports whether name can appear in a placeholder.
func ValidName(name string) bool {
	if name == "" || name[0] == '.' || name[len(name)-1] == '.' || strings.Contains(name, "..") {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isNameByte(name[i]) {
			return false
		}
	}
	return true
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '.'
}

func validMod(mod string) bool {
	return mod == ModUpper || mod == ModLower || variantNumber(mod) > 0
}

// variantNumber parses a variant modifier ("2", "3", ...), returning 0 if mod
// is not one.
func variantNumber(mod string) int {
	if mod == "" || len(mod) > 6 || mod[0] == '0' {
		return 0
	}
	n := 0
	for i := 0; i < len(mod); i++ {
		if mod[i] < '0' || mod[i] > '9' {
			return 0
		}
		n = n*10 + int(mod[i]-'0')
	}
	if n < 2 {
		return 0
	}
	return n
}

// token is one piece of scanned text.
type token struct {
	kind tokenKind
	text string // the exact source text of the token
	name string // placeholder name (tokPlaceholder)
	mod  string // placeholder modifier (tokPlaceholder)
	lit  string // unescaped text (tokEscape)
}

type tokenKind int

const (
	tokText        tokenKind = iota // ordinary text
	tokPlaceholder                  // {{pi.name}} / {{pi.name:mod}}
	tokEscape                       // {{pi\.  (one or more backslashes)
)

// scan splits text into ordinary text, placeholders, and escape sequences.
func (s Syntax) scan(text string) []token {
	head := s.head()
	var toks []token
	emit := func(t token) {
		if t.kind == tokText && len(toks) > 0 && toks[len(toks)-1].kind == tokText {
			toks[len(toks)-1].text += t.text
			return
		}
		toks = append(toks, t)
	}
	for {
		i := strings.Index(text, head)
		if i < 0 {
			if text != "" {
				emit(token{kind: tokText, text: text})
			}
			return toks
		}
		if i > 0 {
			emit(token{kind: tokText, text: text[:i]})
		}
		rest := text[i+len(head):]
		k := 0
		for k < len(rest) && rest[k] == '\\' {
			k++
		}
		switch {
		case k < len(rest) && rest[k] == '.' && k > 0:
			n := len(head) + k + 1
			emit(token{kind: tokEscape, text: text[i : i+n], lit: head + strings.Repeat(`\`, k-1) + "."})
			text = text[i+n:]
		case k == 0 && len(rest) > 0 && rest[0] == '.':
			if t, n, ok := s.parsePlaceholder(text[i:]); ok {
				emit(t)
				text = text[i+n:]
			} else {
				emit(token{kind: tokText, text: head})
				text = text[i+len(head):]
			}
		default:
			emit(token{kind: tokText, text: head})
			text = text[i+len(head):]
		}
	}
}

// parsePlaceholder parses a placeholder at the start of text, which is known
// to begin with Open+Prefix+".". It returns the token and its length.
func (s Syntax) parsePlaceholder(text string) (token, int, bool) {
	body := text[len(s.head())+1:]
	end := strings.Index(body, s.Close)
	if end < 0 {
		return token{}, 0, false
	}
	inner := body[:end]
	name, mod := inner, ModNone
	if c := strings.IndexByte(inner, ':'); c >= 0 {
		name, mod = inner[:c], inner[c+1:]
		if !validMod(mod) {
			return token{}, 0, false
		}
	}
	if !ValidName(name) {
		return token{}, 0, false
	}
	n := len(s.head()) + 1 + end + len(s.Close)
	return token{kind: tokPlaceholder, text: text[:n], name: name, mod: mod}, n, true
}

// triggers returns the byte ranges in text that hide mode must escape:
// Open+Prefix followed by zero or more backslashes and a dot.
func (s Syntax) triggers(text string) []span {
	head := s.head()
	var out []span
	for off := 0; ; {
		i := strings.Index(text[off:], head)
		if i < 0 {
			return out
		}
		start := off + i
		j := start + len(head)
		for j < len(text) && text[j] == '\\' {
			j++
		}
		if j < len(text) && text[j] == '.' {
			out = append(out, span{start: start, end: j + 1})
			off = j + 1
		} else {
			off = start + len(head)
		}
	}
}

// escape adds one backslash to a trigger sequence.
func (s Syntax) escape(trigger string) string {
	h := s.head()
	return h + `\` + trigger[len(h):]
}
