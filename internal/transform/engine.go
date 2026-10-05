package transform

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Config configures an Engine.
type Config struct {
	Syntax Syntax
	// Values maps placeholder names to the values they stand for.
	Values map[string]string
	// Detectors flag sensitive text that is not in Values (hide mode only).
	Detectors []Detector
	// Unreviewed holds detector matches awaiting review. Nil means a fresh,
	// empty set.
	Unreviewed *Unreviewed
	// Variants holds recorded spellings of vault values. Nil means a fresh,
	// empty set. Hide records new spellings; Unfill only uses existing ones.
	Variants *Variants
	// Ignore lists exact text that detectors should not flag, e.g. matches
	// the user reviewed and marked as not sensitive.
	Ignore []string
}

// Engine converts text in both directions. It is safe for concurrent use.
type Engine struct {
	syntax     Syntax
	values     map[string]string
	matchers   []valueMatcher
	detectors  []Detector
	unreviewed *Unreviewed
	variants   *Variants
	ignore     map[string]bool
}

// Report describes placeholders that could not be rendered.
type Report struct {
	// Unknown lists placeholders (name[:modifier]) that matched neither a
	// vault value, a recorded variant, nor an unreviewed entry. They are left
	// in the output verbatim.
	Unknown []string
}

// New validates cfg and returns an Engine.
func New(cfg Config) (*Engine, error) {
	s := cfg.Syntax
	if s == (Syntax{}) {
		s = DefaultSyntax
	}
	if s.Open == "" || s.Close == "" {
		return nil, errors.New("placeholder delimiters must not be empty")
	}
	if !ValidName(s.Prefix) || strings.Contains(s.Prefix, ".") {
		return nil, fmt.Errorf("invalid placeholder prefix %q", s.Prefix)
	}
	e := &Engine{syntax: s, values: map[string]string{}, detectors: cfg.Detectors,
		unreviewed: cfg.Unreviewed, variants: cfg.Variants}
	if e.unreviewed == nil {
		e.unreviewed = NewUnreviewed(nil)
	}
	if e.variants == nil {
		e.variants = NewVariants(nil)
	}
	e.ignore = make(map[string]bool, len(cfg.Ignore))
	for _, s := range cfg.Ignore {
		e.ignore[s] = true
	}
	names := make([]string, 0, len(cfg.Values))
	for name := range cfg.Values {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic matcher order
	for _, name := range names {
		value := cfg.Values[name]
		if err := CheckEntry(s, name, value); err != nil {
			return nil, err
		}
		e.values[name] = value
		e.matchers = append(e.matchers, newValueMatcher(name, value))
	}
	return e, nil
}

// CheckEntry reports whether name and value can be stored in a vault that
// uses syntax s.
func CheckEntry(s Syntax, name, value string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '_', '-' and '.'", name)
	}
	if IsUnreviewedName(name) {
		return fmt.Errorf("name %q is reserved", name)
	}
	if value == "" {
		return fmt.Errorf("value for %q is empty", name)
	}
	if strings.Contains(value, s.head()) {
		return fmt.Errorf("value for %q contains the placeholder prefix %q", name, s.head())
	}
	return nil
}

// Syntax returns the placeholder syntax in use.
func (e *Engine) Syntax() Syntax { return e.syntax }

// Unreviewed returns the engine's unreviewed set.
func (e *Engine) Unreviewed() *Unreviewed { return e.unreviewed }

// Variants returns the engine's recorded spellings.
func (e *Engine) Variants() *Variants { return e.variants }

// Hide converts original text to its guise: vault values become named
// placeholders (irregular spellings are recorded as variants), detector
// matches become unreviewed placeholders, and literal placeholder-like text
// is escaped. Reveal(Hide(t)) == t for any t.
func (e *Engine) Hide(text string) string {
	reserved := e.syntax.triggers(text)
	var cands []match
	for _, m := range e.matchers {
		cands = append(cands, m.find(text)...)
	}
	for _, d := range e.detectors {
		for _, loc := range d.Find(text) {
			if e.ignore[text[loc[0]:loc[1]]] {
				continue
			}
			cands = append(cands, match{span: span{loc[0], loc[1]}, priority: prioDetector, kind: d.Kind()})
		}
	}
	sel := selectMatches(len(text), cands, reserved)

	var b strings.Builder
	b.Grow(len(text))
	pos, ti := 0, 0
	literal := func(end int) { // copy text[pos:end], escaping triggers
		for ti < len(reserved) && reserved[ti].end <= end {
			r := reserved[ti]
			b.WriteString(text[pos:r.start])
			b.WriteString(e.syntax.escape(text[r.start:r.end]))
			pos = r.end
			ti++
		}
		b.WriteString(text[pos:end])
		pos = end
	}
	for _, m := range sel {
		literal(m.start)
		got := text[m.start:m.end]
		switch {
		case m.name == "":
			b.WriteString(e.syntax.Placeholder(e.unreviewed.assign(m.kind, got), ModNone))
		case m.irregular:
			b.WriteString(e.syntax.Placeholder(m.name, strconv.Itoa(e.variants.assign(m.name, got))))
		default:
			b.WriteString(e.syntax.Placeholder(m.name, m.mod))
		}
		pos = m.end
	}
	literal(len(text))
	return b.String()
}

// Reveal converts a guise back to original text: placeholders are rendered
// and escapes removed. Unknown placeholders are kept verbatim and reported.
func (e *Engine) Reveal(text string) (string, Report) {
	var b strings.Builder
	var rep Report
	for _, t := range e.syntax.scan(text) {
		switch t.kind {
		case tokText:
			b.WriteString(t.text)
		case tokEscape:
			b.WriteString(t.lit)
		case tokPlaceholder:
			v, ok := e.render(t, true)
			if !ok {
				rep.Unknown = append(rep.Unknown, unknownName(t))
				b.WriteString(t.text)
				continue
			}
			b.WriteString(v)
		}
	}
	return b.String(), rep
}

// render resolves a placeholder token, including unreviewed names if asked.
func (e *Engine) render(t token, unreviewed bool) (string, bool) {
	v, ok := e.values[t.name]
	if !ok && unreviewed {
		v, ok = e.unreviewed.Lookup(t.name)
	}
	if !ok {
		return "", false
	}
	if n := variantNumber(t.mod); n > 0 {
		return e.variants.Lookup(t.name, n)
	}
	return applyMod(v, t.mod), true
}

func unknownName(t token) string {
	if t.mod != ModNone {
		return t.name + ":" + t.mod
	}
	return t.name
}

// Fill renders a template: placeholders for known vault names become values.
// Everything else, including escapes and unknown placeholders, is kept
// verbatim so that Unfill can restore it.
func (e *Engine) Fill(text string) (string, Report) {
	var b strings.Builder
	var rep Report
	for _, t := range e.syntax.scan(text) {
		if t.kind != tokPlaceholder {
			b.WriteString(t.text)
			continue
		}
		v, ok := e.render(t, false)
		if !ok {
			rep.Unknown = append(rep.Unknown, unknownName(t))
			b.WriteString(t.text)
			continue
		}
		b.WriteString(v)
	}
	return b.String(), rep
}

// Unfill converts filled text back to a template: vault values become
// placeholders. Placeholder-like text already present is left untouched.
// Unfill(Fill(t)) == t unless t contains a vault value as literal text.
func (e *Engine) Unfill(text string) string {
	var reserved []span
	off := 0
	for _, t := range e.syntax.scan(text) {
		if t.kind != tokText {
			reserved = append(reserved, span{off, off + len(t.text)})
		}
		off += len(t.text)
	}
	reserved = append(reserved, e.syntax.triggers(text)...)
	var cands []match
	for _, m := range e.matchers {
		for _, c := range m.find(text) {
			if c.irregular { // only spellings already recorded are restored
				n := e.variants.find(c.name, text[c.start:c.end])
				if n == 0 {
					continue
				}
				c.mod = strconv.Itoa(n)
			}
			cands = append(cands, c)
		}
	}
	var b strings.Builder
	pos := 0
	for _, m := range selectMatches(len(text), cands, reserved) {
		b.WriteString(text[pos:m.start])
		b.WriteString(e.syntax.Placeholder(m.name, m.mod))
		pos = m.end
	}
	b.WriteString(text[pos:])
	return b.String()
}

func applyMod(v, mod string) string {
	switch mod {
	case ModUpper:
		return strings.ToUpper(v)
	case ModLower:
		return strings.ToLower(v)
	}
	return v
}
