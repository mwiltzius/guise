package docx

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/mwiltzius/guise/internal/transform"
)

// build makes a .docx from part name → content. Entries are written in the
// given order (the first is [Content_Types].xml, as Word expects).
func build(t *testing.T, parts ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i := 0; i < len(parts); i += 2 {
		f, err := w.Create(parts[i])
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(parts[i+1]))
	}
	w.Close()
	return buf.Bytes()
}

func part(t *testing.T, doc []byte, name string) string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if f.Name == name {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			return string(b)
		}
	}
	t.Fatalf("no part %s", name)
	return ""
}

var tText = regexp.MustCompile(`<w:(?:t|delText)(?:\s[^>]*)?>([^<]*)</w:(?:t|delText)>`)

// paragraphs returns each paragraph's visible text.
func paragraphs(xml string) []string {
	var out []string
	for _, p := range strings.Split(xml, "</w:p>") {
		if !strings.Contains(p, "<w:p") {
			continue
		}
		var b strings.Builder
		for _, m := range tText.FindAllStringSubmatch(p, -1) {
			b.WriteString(unescape(m[1]))
		}
		out = append(out, b.String())
	}
	return out
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`

func body(paras ...string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		strings.Join(paras, "") + `</w:body></w:document>`
}

// para builds a paragraph whose runs hold the given pieces of text, each
// with its own formatting, as Word does after spell-checking or edits.
func para(runs ...string) string {
	var b strings.Builder
	b.WriteString(`<w:p><w:pPr><w:pStyle w:val="Normal"/></w:pPr>`)
	for i, r := range runs {
		if i%2 == 1 {
			b.WriteString(`<w:proofErr w:type="spellStart"/>`)
		}
		b.WriteString(`<w:r><w:rPr><w:b w:val="` + []string{"0", "1"}[i%2] + `"/></w:rPr><w:t>` + r + `</w:t></w:r>`)
	}
	b.WriteString(`</w:p>`)
	return b.String()
}

func engine(t *testing.T) *transform.Engine {
	t.Helper()
	e, err := transform.New(transform.Config{
		Values:    map[string]string{"firstname": "Peter", "lastname": "Parker", "alias": "Spiderman"},
		Detectors: transform.BuiltinDetectors(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func hide(e *transform.Engine) Options {
	return Options{Edit: e.HideEdits, Leaks: e.Leaks}
}

func reveal(e *transform.Engine) Options {
	return Options{Edit: func(s string) []transform.Edit { ed, _ := e.RevealEdits(s); return ed }}
}

func TestSplitRunsRoundTrip(t *testing.T) {
	e := engine(t)
	doc := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(
			para("My name is Pe", "ter Par", "ker", ", a.k.a. Spider", "man."),
			para("Call 212-555-0187 &amp; ask for Peter"),
		),
		"word/media/image1.png", "\x89PNG binary bytes",
	)
	g, err := Transform(doc, hide(e))
	if err != nil {
		t.Fatal(err)
	}
	got := paragraphs(part(t, g, "word/document.xml"))
	want := []string{
		"My name is {{pi.firstname}} {{pi.lastname}}, a.k.a. {{pi.alias}}.",
		"Call {{pi.unreviewed.phone.1}} & ask for {{pi.firstname}}",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("guise paragraphs\n got  %q\n want %q", got, want)
	}
	if part(t, g, "word/media/image1.png") != "\x89PNG binary bytes" {
		t.Fatal("binary part changed")
	}
	if !strings.Contains(part(t, g, "word/document.xml"), `<w:b w:val="1"/>`) {
		t.Fatal("run formatting lost")
	}

	// An AI rewrites the sentence, splitting placeholders across runs the
	// way Word would after editing.
	edited := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(
			para("I am {{pi.first", "name}} {{pi.lastname:upper}}!"),
			para("Call {{pi.unreviewed.phone.1}} & ask for {{pi.firstname}}"),
		),
	)
	back, err := Transform(edited, reveal(e))
	if err != nil {
		t.Fatal(err)
	}
	got = paragraphs(part(t, back, "word/document.xml"))
	want = []string{"I am Peter PARKER!", "Call 212-555-0187 & ask for Peter"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("revealed paragraphs\n got  %q\n want %q", got, want)
	}
}

func TestRevealOfHideKeepsText(t *testing.T) {
	e := engine(t)
	orig := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(
			para("Peter", " Parker ", "lives at {{pi.x}} &lt;home&gt;"),
			para("  leading and trailing spaces  "),
		),
	)
	g, err := Transform(orig, hide(e))
	if err != nil {
		t.Fatal(err)
	}
	back, err := Transform(g, reveal(e))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := paragraphs(part(t, orig, "word/document.xml")), paragraphs(part(t, back, "word/document.xml")); strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatalf("text changed\n orig %q\n back %q", a, b)
	}
	if !strings.Contains(part(t, g, "word/document.xml"), `xml:space="preserve"`) {
		t.Fatal("rewritten runs must preserve spaces")
	}
}

func TestOtherPlacesPIHides(t *testing.T) {
	e := engine(t)
	doc := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(
			`<w:p><w:del w:id="1" w:author="Peter Parker" w:date="2026-01-01T00:00:00Z"><w:r><w:delText>Spiderman</w:delText></w:r></w:del></w:p>`,
			`<w:p><w:r><w:drawing><wp:docPr id="1" name="Picture 1" descr="Photo of Peter Parker"/></w:drawing></w:r></w:p>`,
			`<w:p><w:fldSimple w:instr=" HYPERLINK &quot;mailto:peter@example.com&quot; "><w:r><w:t>email</w:t></w:r></w:fldSimple></w:p>`,
		),
		"word/header1.xml", body(para("Peter Parker — Résumé")),
		"word/footnotes.xml", body(para("Parker, P. (2026)")),
		"word/comments.xml", `<w:comments><w:comment w:id="0" w:author="Peter Parker" w:initials="PP">`+para("nice")+`</w:comment></w:comments>`,
		"docProps/core.xml", `<cp:coreProperties><dc:creator>Peter Parker</dc:creator><cp:lastModifiedBy>Parker</cp:lastModifiedBy></cp:coreProperties>`,
		"word/_rels/document.xml.rels", `<Relationships><Relationship Id="rId9" Type="hyperlink" Target="mailto:peter@example.com" TargetMode="External"/><Relationship Id="rId1" Type="styles" Target="styles.xml"/></Relationships>`,
	)
	g, err := Transform(doc, hide(e))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"word/document.xml", "word/header1.xml", "word/footnotes.xml", "word/comments.xml", "docProps/core.xml", "word/_rels/document.xml.rels"} {
		p := part(t, g, name)
		for _, leak := range []string{"Peter", "Parker", "Spiderman", "peter@example.com"} {
			if strings.Contains(p, leak) {
				t.Errorf("%s still contains %q:\n%s", name, leak, p)
			}
		}
	}
	if p := part(t, g, "word/_rels/document.xml.rels"); !strings.Contains(p, `Target="styles.xml"`) {
		t.Errorf("internal relationship changed: %s", p)
	}
}

func TestLeakInUnhandledPartFailsClosed(t *testing.T) {
	e := engine(t)
	doc := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(para("hello")),
		"word/webextensions/taskpane.xml", `<wetp:taskpanes><wetp:taskpane note="Peter's add-in"/></wetp:taskpanes>`,
	)
	_, err := Transform(doc, hide(e))
	var le *LeakError
	if !errors.As(err, &le) || le.Part != "word/webextensions/taskpane.xml" {
		t.Fatalf("err = %v, want a leak in the taskpane part", err)
	}
}

func TestFillSplitPlaceholders(t *testing.T) {
	e := engine(t)
	tmpl := build(t,
		"[Content_Types].xml", contentTypes,
		"word/document.xml", body(para("Dear {{pi.first", "name}},", " regards")),
	)
	filled, err := Transform(tmpl, Options{Edit: func(s string) []transform.Edit { ed, _ := e.FillEdits(s); return ed }})
	if err != nil {
		t.Fatal(err)
	}
	if got := paragraphs(part(t, filled, "word/document.xml")); got[0] != "Dear Peter, regards" {
		t.Fatalf("filled = %q", got)
	}
	back, err := Transform(filled, Options{Edit: e.UnfillEdits})
	if err != nil {
		t.Fatal(err)
	}
	if got := paragraphs(part(t, back, "word/document.xml")); got[0] != "Dear {{pi.firstname}}, regards" {
		t.Fatalf("unfilled = %q", got)
	}
}

func TestIsAndInvalid(t *testing.T) {
	doc := build(t, "[Content_Types].xml", contentTypes, "word/document.xml", body())
	if !Is("CV.DOCX", doc) || Is("cv.txt", doc) || Is("cv.docx", []byte("not a zip")) {
		t.Fatal("Is misclassified")
	}
	if _, err := Transform([]byte("PK\x03\x04 truncated"), Options{Edit: engine(t).HideEdits}); err == nil {
		t.Fatal("truncated file accepted")
	}
}
