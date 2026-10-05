// Package docx transforms the text of Word documents (.docx and friends)
// without disturbing anything else in the file.
//
// A .docx is a zip of XML parts. Text lives in runs (<w:t>), and Word splits
// words across runs freely (spell-check marks, formatting history), so a
// value like "Peter" may be stored as "Pe" + "ter". Each paragraph's runs are
// therefore joined, transformed as one string, and the edits mapped back
// onto the runs: replacement text goes into the run where the change starts
// (taking that run's formatting), and text it replaces is removed from the
// following runs.
//
// XML is edited as raw bytes; only changed text and attribute values are
// rewritten. Unchanged zip entries are copied byte for byte.
package docx

import (
	"archive/zip"
	"bytes"

	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/mwiltzius/guise/internal/transform"
)

// EditFunc returns the edits to make to a piece of text.
type EditFunc func(text string) []transform.Edit

// Options configures Transform.
type Options struct {
	Edit EditFunc
	// Leaks, if set, is run over every text node and attribute value of the
	// result; any names it returns abort the transform with a LeakError.
	// Hide mode uses it so values in parts this package does not handle
	// can never reach a guise.
	Leaks func(text string) []string
}

// LeakError reports values left in the document after transforming.
type LeakError struct {
	Part  string
	Names []string
}

func (e *LeakError) Error() string {
	return fmt.Sprintf("%s still contains %s after transforming", e.Part, strings.Join(e.Names, ", "))
}

// Is reports whether a file looks like a Word document.
func Is(name string, data []byte) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx", ".docm", ".dotx", ".dotm":
	default:
		return false
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return false
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

// Transform rewrites the document's text with opt.Edit and returns the new
// file. It fails (rather than guessing) on anything it cannot parse.
func Transform(data []byte, opt Options) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid .docx: %w", err)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range r.File {
		kind := partKind(f.Name)
		if kind == partOther && opt.Leaks == nil {
			if err := w.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		raw, err := readAll(f)
		if err != nil {
			return nil, err
		}
		next := raw
		switch kind {
		case partRuns:
			next = transformRuns(raw, opt.Edit)
			next = transformAttrs(next, runAttrs, opt.Edit)
		case partProps:
			next = transformElementText(raw, opt.Edit)
		case partRels:
			next = transformRels(raw, opt.Edit)
		}
		if opt.Leaks != nil && isXML(f.Name) {
			if names := leaks(next, opt.Leaks); len(names) > 0 {
				return nil, &LeakError{Part: f.Name, Names: names}
			}
		}
		if bytes.Equal(next, raw) {
			if err := w.Copy(f); err != nil {
				return nil, err
			}
			continue
		}
		hdr := f.FileHeader
		hdr.Method = zip.Deflate
		hdr.CRC32, hdr.CompressedSize64, hdr.UncompressedSize64 = 0, 0, 0
		hdr.CompressedSize, hdr.UncompressedSize = 0, 0
		hdr.Extra = nil
		fw, err := w.CreateHeader(&hdr)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(next); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

type kind int

const (
	partOther kind = iota
	partRuns       // WordprocessingML with runs: body, headers, footers, notes, comments
	partProps      // simple XML whose element text is all content: document properties
	partRels       // relationships: external link targets
)

func isXML(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".xml") || strings.HasSuffix(n, ".rels")
}

func partKind(name string) kind {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".rels"):
		return partRels
	case strings.HasPrefix(n, "word/") && strings.HasSuffix(n, ".xml"):
		return partRuns
	case strings.HasPrefix(n, "docprops/") && strings.HasSuffix(n, ".xml"),
		strings.HasPrefix(n, "customxml/item") && strings.HasSuffix(n, ".xml") && !strings.Contains(n, "props"):
		return partProps
	}
	return partOther
}

// --- runs ------------------------------------------------------------------

// runToken finds, in document order: paragraph starts and ends, tabs and
// breaks (which split text), and text elements with their content.
var runToken = regexp.MustCompile(
	`<w:p[ >]|</w:p>|<w:(?:tab|br|cr)\b[^>]*/>` +
		`|<(w:t|w:delText|w:instrText)(\s[^>]*)?>([^<]*)</(w:t|w:delText|w:instrText)>`)

type textNode struct {
	start, end int    // the whole element in the raw part
	name       string // w:t, w:delText or w:instrText
	attrs      string // raw attributes, including leading space
	text       string // decoded content
}

// transformRuns joins consecutive text elements of the same kind within a
// paragraph, transforms them together, and writes the changes back.
func transformRuns(raw []byte, edit EditFunc) []byte {
	var repl []replacement
	var group []textNode
	flush := func() {
		repl = append(repl, editGroup(group, edit)...)
		group = group[:0]
	}
	for _, m := range runToken.FindAllSubmatchIndex(raw, -1) {
		if m[2] < 0 { // a boundary
			flush()
			continue
		}
		name, closing := string(raw[m[2]:m[3]]), string(raw[m[8]:m[9]])
		if name != closing {
			flush()
			continue
		}
		attrs := ""
		if m[4] >= 0 {
			attrs = string(raw[m[4]:m[5]])
		}
		n := textNode{start: m[0], end: m[1], name: name, attrs: attrs, text: unescape(string(raw[m[6]:m[7]]))}
		if len(group) > 0 && group[0].name != n.name {
			flush()
		}
		group = append(group, n)
		if name == "w:instrText" { // field codes stand alone
			flush()
		}
	}
	flush()
	return apply(raw, repl)
}

// editGroup transforms the joined text of a group and returns the element
// rewrites needed.
func editGroup(group []textNode, edit EditFunc) []replacement {
	if len(group) == 0 {
		return nil
	}
	var joined strings.Builder
	bounds := make([]int, len(group)+1) // node i covers [bounds[i], bounds[i+1])
	for i, n := range group {
		bounds[i] = joined.Len()
		joined.WriteString(n.text)
	}
	bounds[len(group)] = joined.Len()
	text := joined.String()
	edits := edit(text)
	if len(edits) == 0 {
		return nil
	}

	owner := func(pos int) int { // node holding byte pos (last node at the end)
		i := sort.Search(len(group), func(i int) bool { return bounds[i+1] > pos })
		if i == len(group) {
			i = len(group) - 1
		}
		return i
	}
	out := make([]strings.Builder, len(group))
	keep := func(from, to int) {
		for from < to {
			i := owner(from)
			end := min(to, bounds[i+1])
			out[i].WriteString(text[from:end])
			from = end
		}
	}
	pos := 0
	for _, ed := range edits {
		keep(pos, ed.Start)
		out[owner(ed.Start)].WriteString(ed.Text)
		pos = ed.End
	}
	keep(pos, len(text))

	var repl []replacement
	for i, n := range group {
		if s := out[i].String(); s != n.text {
			repl = append(repl, replacement{n.start, n.end, element(n, s)})
		}
	}
	return repl
}

var spaceAttr = regexp.MustCompile(`\sxml:space="[^"]*"`)

// element renders a text element with new content. xml:space="preserve"
// keeps Word from trimming spaces at the edges of the new text.
func element(n textNode, text string) []byte {
	attrs := spaceAttr.ReplaceAllString(n.attrs, "")
	return []byte("<" + n.name + attrs + ` xml:space="preserve">` + escapeText(text) + "</" + n.name + ">")
}

// --- attributes --------------------------------------------------------------

// runAttrs are attribute local names that hold people's names or free text
// in Word parts: revision and comment authors, people (w15:userId is often
// an email), image alt text, and simple field instructions (hyperlinks).
var runAttrs = map[string]bool{"author": true, "initials": true, "userId": true, "descr": true, "title": true, "instr": true}

var (
	tagRe  = regexp.MustCompile(`<[^>!?]+>`)
	attrRe = regexp.MustCompile(`\s((?:[A-Za-z0-9_]+:)?([A-Za-z0-9_]+))="([^"]*)"`)
)

// transformAttrs transforms the values of attributes whose local name is in
// names. Each value is transformed on its own.
func transformAttrs(raw []byte, names map[string]bool, edit EditFunc) []byte {
	var repl []replacement
	for _, t := range tagRe.FindAllIndex(raw, -1) {
		tag := raw[t[0]:t[1]]
		for _, a := range attrRe.FindAllSubmatchIndex(tag, -1) {
			if !names[string(tag[a[4]:a[5]])] {
				continue
			}
			if r, ok := editValue(raw, t[0]+a[6], t[0]+a[7], edit, escapeAttr); ok {
				repl = append(repl, r)
			}
		}
	}
	return apply(raw, repl)
}

// transformRels transforms external relationship targets (mailto: links and
// URLs that may contain names or addresses).
func transformRels(raw []byte, edit EditFunc) []byte {
	var repl []replacement
	for _, t := range tagRe.FindAllIndex(raw, -1) {
		tag := raw[t[0]:t[1]]
		if !bytes.Contains(tag, []byte(`TargetMode="External"`)) {
			continue
		}
		for _, a := range attrRe.FindAllSubmatchIndex(tag, -1) {
			if string(tag[a[2]:a[3]]) != "Target" {
				continue
			}
			if r, ok := editValue(raw, t[0]+a[6], t[0]+a[7], edit, escapeAttr); ok {
				repl = append(repl, r)
			}
		}
	}
	return apply(raw, repl)
}

// --- element text (document properties) --------------------------------------

var elemText = regexp.MustCompile(`>([^<]+)<`)

func transformElementText(raw []byte, edit EditFunc) []byte {
	var repl []replacement
	for _, m := range elemText.FindAllSubmatchIndex(raw, -1) {
		if r, ok := editValue(raw, m[2], m[3], edit, escapeText); ok {
			repl = append(repl, r)
		}
	}
	return transformAttrs(apply(raw, repl), runAttrs, edit)
}

// editValue transforms one escaped value occupying raw[start:end].
func editValue(raw []byte, start, end int, edit EditFunc, esc func(string) string) (replacement, bool) {
	text := unescape(string(raw[start:end]))
	edits := edit(text)
	if len(edits) == 0 {
		return replacement{}, false
	}
	return replacement{start, end, []byte(esc(transform.Apply(text, edits)))}, true
}

// --- leak check ---------------------------------------------------------------

// leaks runs check over every text node and attribute value of an XML part.
func leaks(raw []byte, check func(string) []string) []string {
	found := map[string]bool{}
	scan := func(s string) {
		for _, n := range check(unescape(s)) {
			found[n] = true
		}
	}
	// Run text joined per paragraph, so values split across runs are caught.
	var joined strings.Builder
	for _, m := range runToken.FindAllSubmatchIndex(raw, -1) {
		if m[2] < 0 {
			scan(joined.String())
			joined.Reset()
			continue
		}
		joined.WriteString(string(raw[m[6]:m[7]]))
	}
	scan(joined.String())
	for _, m := range elemText.FindAllSubmatchIndex(raw, -1) {
		scan(string(raw[m[2]:m[3]]))
	}
	for _, t := range tagRe.FindAll(raw, -1) {
		for _, a := range attrRe.FindAllSubmatch(t, -1) {
			scan(string(a[3]))
		}
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// --- helpers ------------------------------------------------------------------

type replacement struct {
	start, end int
	with       []byte
}

// apply performs non-overlapping replacements on raw.
func apply(raw []byte, repl []replacement) []byte {
	if len(repl) == 0 {
		return raw
	}
	sort.Slice(repl, func(i, j int) bool { return repl[i].start < repl[j].start })
	var out bytes.Buffer
	pos := 0
	for _, r := range repl {
		out.Write(raw[pos:r.start])
		out.Write(r.with)
		pos = r.end
	}
	out.Write(raw[pos:])
	return out.Bytes()
}

var entity = regexp.MustCompile(`&(#x[0-9A-Fa-f]+|#[0-9]+|amp|lt|gt|quot|apos);`)

func unescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return entity.ReplaceAllStringFunc(s, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&apos;":
			return "'"
		}
		var r rune
		if strings.HasPrefix(e, "&#x") {
			fmt.Sscanf(e[3:len(e)-1], "%x", &r)
		} else {
			fmt.Sscanf(e[2:len(e)-1], "%d", &r)
		}
		return string(r)
	})
}

var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

func escapeText(s string) string { return textEscaper.Replace(s) }
func escapeAttr(s string) string { return attrEscaper.Replace(s) }
