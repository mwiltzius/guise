package rules

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergeAndPathRules(t *testing.T) {
	project := Config{
		Rules: Rules{Detectors: map[string]bool{"phone": false}, Ignore: []string{"a"}},
		Paths: []PathRules{{Match: "letters/**", Rules: Rules{Expose: map[string]bool{"city": true}}}},
	}
	guise := Config{
		Rules: Rules{Detectors: map[string]bool{"phone": true, "ssn": false}, Ignore: []string{"b"}},
		Paths: []PathRules{{Match: "**/*.md", Rules: Rules{Expose: map[string]bool{"city": false}}}},
	}
	c := project.Overlay(guise)

	base := c.For("notes.txt")
	if !base.DetectorEnabled("phone") || base.DetectorEnabled("ssn") || !base.DetectorEnabled("email") {
		t.Fatalf("detectors: %v", base.Detectors)
	}
	if !reflect.DeepEqual(base.Ignore, []string{"a", "b"}) {
		t.Fatalf("ignore: %v", base.Ignore)
	}
	if got := c.For("letters/alice.txt").ExposedNames(); !reflect.DeepEqual(got, []string{"city"}) {
		t.Fatalf("letters: %v", got)
	}
	// Later path layers win: the guise's *.md rule un-exposes city.
	if got := c.For("letters/alice.md").ExposedNames(); len(got) != 0 {
		t.Fatalf("letters md: %v", got)
	}
}

func TestFindProject(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	os.MkdirAll(deep, 0o755)
	if _, p, err := FindProject(deep); err != nil || p != "" {
		t.Fatalf("got %q, %v", p, err)
	}
	os.MkdirAll(filepath.Join(root, ".guise"), 0o755)
	cfgPath := filepath.Join(root, ".guise", ProjectFile)
	os.WriteFile(cfgPath, []byte(`
ignore = ["support@example.com"]
[expose]
city = true
[[path]]
match = "drafts/**"
[path.detectors]
phone = false
`), 0o600)
	c, p, err := FindProject(deep)
	if err != nil || p != cfgPath {
		t.Fatalf("got %q, %v", p, err)
	}
	if r := c.For("drafts/x.md"); r.DetectorEnabled("phone") || !r.Expose["city"] || r.Ignore[0] != "support@example.com" {
		t.Fatalf("rules: %+v", r)
	}
	os.WriteFile(cfgPath, []byte("[[path]]\nmatch = \"[\"\n"), 0o600)
	if _, _, err := FindProject(deep); err == nil {
		t.Fatal("invalid glob accepted")
	}
}
