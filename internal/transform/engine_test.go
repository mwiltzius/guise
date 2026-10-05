package transform

import (
	"reflect"
	"strings"
	"testing"
)

var testValues = map[string]string{
	"firstname": "Matthew",
	"lastname":  "Wiltzius",
	"fullname":  "Matthew Wiltzius",
	"city":      "Austin",
	"street":    "123 Main St.",
}

func newEngine(t testing.TB, detectors bool) *Engine {
	t.Helper()
	cfg := Config{Values: testValues}
	if detectors {
		cfg.Detectors = BuiltinDetectors()
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func reveal(t *testing.T, e *Engine, s string) string {
	t.Helper()
	out, rep := e.Reveal(s)
	if len(rep.Unknown) > 0 {
		t.Fatalf("Reveal(%q): unknown placeholders %v", s, rep.Unknown)
	}
	return out
}

func TestHide(t *testing.T) {
	e := newEngine(t, false)
	tests := []struct{ in, want string }{
		{"Matthew Wiltzius", "{{pi.fullname}}"},                                  // longest match wins
		{"Dear Matthew,", "Dear {{pi.firstname}},"},                              // single value
		{"WILTZIUS & wiltzius", "{{pi.lastname:upper}} & {{pi.lastname:lower}}"}, // case variants
		{"Austinite in Austin", "Austinite in {{pi.city}}"},                      // whole words only
		{"matt.wiltzius@example.com", "matt.{{pi.lastname:lower}}@example.com"},
		{"at 123 Main St. today", "at {{pi.street}} today"}, // value ending in punctuation
		{"no secrets here", "no secrets here"},
	}
	for _, tt := range tests {
		if got := e.Hide(tt.in); got != tt.want {
			t.Errorf("Hide(%q)\n got  %q\n want %q", tt.in, got, tt.want)
		}
	}
}

// Spellings upper/lower cannot reproduce are recorded as numbered variants,
// so the placeholder still says what the value is.
func TestHideIrregularCaseRecordsVariant(t *testing.T) {
	e := newEngine(t, false)
	got := e.Hide("WiLtZiUs, wILTZIUS, WiLtZiUs")
	if got != "{{pi.lastname:2}}, {{pi.lastname:3}}, {{pi.lastname:2}}" {
		t.Fatalf("got %q", got)
	}
	if !e.Variants().TakeDirty() {
		t.Fatal("new variants not marked dirty")
	}
	if r := reveal(t, e, got); r != "WiLtZiUs, wILTZIUS, WiLtZiUs" {
		t.Fatalf("round trip: %q", r)
	}
	// An AI can reuse a variant anywhere.
	if r := reveal(t, e, "{{pi.lastname:3}} / {{pi.lastname}}"); r != "wILTZIUS / Wiltzius" {
		t.Fatalf("reuse: %q", r)
	}
	if _, rep := e.Reveal("{{pi.lastname:9}}"); len(rep.Unknown) != 1 || rep.Unknown[0] != "lastname:9" {
		t.Fatalf("unknown variant not reported: %v", rep.Unknown)
	}
}

func TestMultipleValuesOfOneKind(t *testing.T) {
	e, err := New(Config{Values: map[string]string{"lastname": "Wiltzius", "lastname.2": "Doe"}})
	if err != nil {
		t.Fatal(err)
	}
	g := e.Hide("Wiltzius and DOE")
	if g != "{{pi.lastname}} and {{pi.lastname.2:upper}}" {
		t.Fatalf("got %q", g)
	}
	if r := reveal(t, e, g); r != "Wiltzius and DOE" {
		t.Fatalf("round trip: %q", r)
	}
}

// Placeholders are matched by name, so moving, duplicating, or adding them in
// the guise renders the right value wherever they end up.
func TestRevealMovedAndDuplicatedPlaceholders(t *testing.T) {
	e := newEngine(t, false)
	orig := "Name: Matthew Wiltzius\nCity: Austin\n"
	g := e.Hide(orig)
	if g != "Name: {{pi.fullname}}\nCity: {{pi.city}}\n" {
		t.Fatalf("Hide: %q", g)
	}
	edited := "City: {{pi.city}}\nName: {{pi.fullname}}\nSigned, {{pi.firstname}} ({{pi.lastname:upper}}), {{pi.city}}\n"
	want := "City: Austin\nName: Matthew Wiltzius\nSigned, Matthew (WILTZIUS), Austin\n"
	if got := reveal(t, e, edited); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestEscaping(t *testing.T) {
	e := newEngine(t, false)
	tests := []struct{ in, want string }{
		{"literal {{pi.firstname}}", `literal {{pi\.firstname}}`},
		{`already {{pi\.x}}`, `already {{pi\\.x}}`},
		{"{{pi without dot", "{{pi without dot"},
		{"{{pi.Matthew}}", `{{pi\.{{pi.firstname}}}}`},
	}
	for _, tt := range tests {
		got := e.Hide(tt.in)
		if got != tt.want {
			t.Errorf("Hide(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if r := reveal(t, e, got); r != tt.in {
			t.Errorf("round trip %q -> %q -> %q", tt.in, got, r)
		}
	}
}

func TestRevealUnknownPlaceholder(t *testing.T) {
	e := newEngine(t, false)
	out, rep := e.Reveal("Hi {{pi.middlename}} {{pi.lastname}}")
	if out != "Hi {{pi.middlename}} Wiltzius" {
		t.Fatalf("got %q", out)
	}
	if !reflect.DeepEqual(rep.Unknown, []string{"middlename"}) {
		t.Fatalf("unknown = %v", rep.Unknown)
	}
}

func TestRevealMalformedIsLiteral(t *testing.T) {
	e := newEngine(t, false)
	for _, s := range []string{"{{pi.", "{{pi.lastname", "{{pi.last name}}", "{{pi.lastname:shout}}",
		"{{pi.lastname:1}}", "{{pi.lastname:02}}", "{{pi.}}"} {
		if got := reveal(t, e, s); got != s {
			t.Errorf("Reveal(%q) = %q", s, got)
		}
	}
}

func TestDetectors(t *testing.T) {
	e := newEngine(t, true)
	orig := "Call 512-555-0199 or mail jane.doe@example.org. Card 4111 1111 1111 1111, SSN 123-45-6789. Not a card: 1234 5678 9012 3456."
	g := e.Hide(orig)
	for _, leaked := range []string{"512-555-0199", "jane.doe@example.org", "4111 1111 1111 1111", "123-45-6789"} {
		if strings.Contains(g, leaked) {
			t.Errorf("guise leaks %q: %q", leaked, g)
		}
	}
	if !strings.Contains(g, "1234 5678 9012 3456") {
		t.Errorf("non-Luhn number was redacted: %q", g)
	}
	if r := reveal(t, e, g); r != orig {
		t.Fatalf("round trip:\n got  %q\n want %q", r, orig)
	}
}

func TestDetectorContainingVaultValueWins(t *testing.T) {
	e := newEngine(t, true)
	g := e.Hide("matthew.wiltzius@example.com")
	if g != "{{pi.unreviewed.email.1}}" {
		t.Fatalf("whole email should be hidden, got %q", g)
	}
}

func TestUnreviewedNamesAreStable(t *testing.T) {
	u := NewUnreviewed(map[string]string{"unreviewed.email.4": "jane@example.org"})
	e, err := New(Config{Detectors: BuiltinDetectors(), Unreviewed: u})
	if err != nil {
		t.Fatal(err)
	}
	g1 := e.Hide("a@example.org jane@example.org a@example.org 512-555-0199")
	if g1 != "{{pi.unreviewed.email.5}} {{pi.unreviewed.email.4}} {{pi.unreviewed.email.5}} {{pi.unreviewed.phone.1}}" {
		t.Fatalf("got %q", g1)
	}
	if !u.TakeDirty() || u.TakeDirty() {
		t.Fatal("dirty flag not set once")
	}
	if g2 := e.Hide("jane@example.org a@example.org"); g2 != "{{pi.unreviewed.email.4}} {{pi.unreviewed.email.5}}" {
		t.Fatalf("names changed between reads: %q", g2)
	}
}

func TestFillAndUnfill(t *testing.T) {
	e := newEngine(t, false)
	tmpl := "Dear {{pi.firstname}},\n{{pi.city}} is lovely. Keep {{pi.unknown}} and {{pi\\.literal}}.\n{{pi.lastname:upper}}"
	filled, rep := e.Fill(tmpl)
	want := "Dear Matthew,\nAustin is lovely. Keep {{pi.unknown}} and {{pi\\.literal}}.\nWILTZIUS"
	if filled != want {
		t.Fatalf("Fill:\n got  %q\n want %q", filled, want)
	}
	if !reflect.DeepEqual(rep.Unknown, []string{"unknown"}) {
		t.Fatalf("unknown = %v", rep.Unknown)
	}
	if back := e.Unfill(filled); back != tmpl {
		t.Fatalf("Unfill:\n got  %q\n want %q", back, tmpl)
	}
	// Editing the filled letter flows back into the template.
	edited := strings.Replace(filled, "is lovely", "is lovely in spring, Matthew", 1)
	if back := e.Unfill(edited); !strings.Contains(back, "in spring, {{pi.firstname}}") {
		t.Fatalf("edit not unfilled: %q", back)
	}
}

func TestFillUsesRecordedVariants(t *testing.T) {
	v := NewVariants(map[string]map[int]string{"lastname": {2: "WiLtZiUs"}})
	e, err := New(Config{Values: testValues, Variants: v})
	if err != nil {
		t.Fatal(err)
	}
	tmpl := "{{pi.lastname:2}} {{pi.lastname}}"
	filled, _ := e.Fill(tmpl)
	if filled != "WiLtZiUs Wiltzius" {
		t.Fatalf("Fill: %q", filled)
	}
	if back := e.Unfill(filled); back != tmpl {
		t.Fatalf("Unfill: %q", back)
	}
	// Unfill never records new spellings.
	if back := e.Unfill("wIlTzIuS"); back != "wIlTzIuS" || v.TakeDirty() {
		t.Fatalf("Unfill recorded a variant: %q", back)
	}
}

func TestCustomSyntax(t *testing.T) {
	e, err := New(Config{Syntax: Syntax{Open: "<<", Close: ">>", Prefix: "v"}, Values: testValues})
	if err != nil {
		t.Fatal(err)
	}
	g := e.Hide("Matthew in Austin, {{pi.x}} stays")
	if g != "<<v.firstname>> in <<v.city>>, {{pi.x}} stays" {
		t.Fatalf("got %q", g)
	}
}

func TestNewRejectsBadEntries(t *testing.T) {
	bad := []map[string]string{
		{"bad name": "x"},
		{"unreviewed.1": "x"},
		{"empty": ""},
		{"sneaky": "a {{pi.b}}"},
		{".dot": "x"},
	}
	for _, v := range bad {
		if _, err := New(Config{Values: v}); err == nil {
			t.Errorf("New(%v) succeeded", v)
		}
	}
	if _, err := New(Config{Syntax: Syntax{Open: "{{", Close: "}}", Prefix: "a.b"}}); err == nil {
		t.Error("dotted prefix accepted")
	}
}

func FuzzHideRevealRoundTrip(f *testing.F) {
	seeds := []string{
		"", "Matthew Wiltzius", "{{pi.firstname}}", `{{pi\.x}}`, `{{pi\\\.`, "{{{pi.x}}}",
		"{{piMatthew", "{{pi.Matthew}}", "x{{pi.", "WiLtZiUs at jane@example.org 512-555-0199",
		"123 Main St.{{pi.city}}", "Austin}}{{pi", "ǅ ǆ K K ſ s", "aUSTIN {{pi.city:2}} wiLTZIUS",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	e := newEngine(f, true)
	f.Fuzz(func(t *testing.T, s string) {
		g := e.Hide(s)
		r, rep := e.Reveal(g)
		if r != s {
			t.Fatalf("round trip failed\n in    %q\n guise %q\n out   %q", s, g, r)
		}
		if len(rep.Unknown) > 0 {
			t.Fatalf("Hide produced unknown placeholders %v for %q", rep.Unknown, s)
		}
		// Whole-word occurrences of any vault value (in any case) must not
		// survive into the guise.
		for _, m := range e.matchers {
			if found := m.find(g); len(found) > 0 {
				t.Fatalf("guise leaks %s: %q -> %q", m.name, s, g)
			}
		}
	})
}

func TestIgnoreSkipsDetectorMatches(t *testing.T) {
	e, err := New(Config{Detectors: BuiltinDetectors(), Ignore: []string{"support@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if g := e.Hide("support@example.com, jane@example.org"); g != "support@example.com, {{pi.unreviewed.email.1}}" {
		t.Fatalf("got %q", g)
	}
}
