package transform

import (
	"strings"
	"testing"
)

func BenchmarkHideLargeDocument(b *testing.B) {
	e := newEngine(b, true)
	doc := strings.Repeat("Matthew Wiltzius lives in Austin; call 512-555-0199 or jane@example.org. {{pi.x}} filler text here.\n", 10000)
	b.SetBytes(int64(len(doc)))
	for b.Loop() {
		g := e.Hide(doc)
		e.Reveal(g)
	}
}
