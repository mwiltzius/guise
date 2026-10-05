package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"guise/internal/rules"
)

func plainVault(t *testing.T, path string, values map[string]string) {
	t.Helper()
	v, err := Create(path, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, val := range values {
		must(t, v.Set(k, val))
	}
	must(t, v.Save(path))
}

func noPass(string) PassphraseFunc { return nil }

func TestStackPrecedenceAndRouting(t *testing.T) {
	dir := t.TempDir()
	personal := filepath.Join(dir, "personal.toml")
	work := filepath.Join(dir, "work.toml")
	plainVault(t, personal, map[string]string{"lastname": "Wiltzius", "email": "me@home.org"})
	plainVault(t, work, map[string]string{"email": "me@work.com", "employer": "Initech"})

	s, err := OpenStack([]string{personal, work}, noPass)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Collisions(), []string{"email"}) {
		t.Fatalf("collisions = %v", s.Collisions())
	}
	e, err := s.Engine(Hide, s.Rules())
	if err != nil {
		t.Fatal(err)
	}
	g := e.Hide("WiLtZiUs of InItEcH, me@home.org, me@work.com, jane@example.org")
	want := "{{pi.lastname:2}} of {{pi.employer:2}}, {{pi.email}}, {{pi.unreviewed.email.1}}, {{pi.unreviewed.email.2}}"
	if g != want {
		t.Fatalf("got  %q\nwant %q", g, want)
	}
	if !s.Dirty() {
		t.Fatal("not dirty")
	}
	must(t, s.Save())

	// Variants go to the vault owning the name; unreviewed to the primary.
	p, _ := Load(personal, nil)
	w, _ := Load(work, nil)
	if _, ok := p.Variants().Lookup("lastname", 2); !ok {
		t.Error("lastname variant not in personal vault")
	}
	if _, ok := w.Variants().Lookup("employer", 2); !ok {
		t.Error("employer variant not in work vault")
	}
	if len(p.Unreviewed().Entries()) != 2 || len(w.Unreviewed().Entries()) != 0 {
		t.Errorf("unreviewed: personal %v, work %v", p.Unreviewed().Entries(), w.Unreviewed().Entries())
	}
}

func TestStackRulesAndExpose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.toml")
	plainVault(t, path, map[string]string{"city": "Austin", "street": "123 Main St."})
	v, _ := Load(path, nil)
	must(t, v.SetPattern("badge", `EMP-\d+`))
	must(t, v.Save(path))

	s, err := OpenStack([]string{path}, noPass)
	if err != nil {
		t.Fatal(err)
	}
	r := rules.Config{Rules: s.Rules()}.Overlay(rules.Config{Rules: rules.Rules{
		Expose:    map[string]bool{"city": true},
		Detectors: map[string]bool{"phone": false},
	}}).For("x.md")
	e, err := s.Engine(Hide, r)
	if err != nil {
		t.Fatal(err)
	}
	g := e.Hide("123 Main St., Austin, EMP-42, 512-555-0199")
	if g != "{{pi.street}}, Austin, {{pi.unreviewed.badge.1}}, 512-555-0199" {
		t.Fatalf("got %q", g)
	}
}

// Edits made to a vault file elsewhere (e.g. by the CLI) survive the
// daemon saving its own additions.
func TestStackSaveMergesExternalEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	plainVault(t, path, map[string]string{"lastname": "Wiltzius"})
	s, err := OpenStack([]string{path}, noPass)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.Engine(Hide, s.Rules())
	e.Hide("WiLtZiUs jane@example.org")

	time.Sleep(10 * time.Millisecond) // ensure a distinct mtime
	other, _ := Load(path, nil)
	must(t, other.Set("city", "Austin"))
	must(t, other.Save(path))
	os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))

	must(t, s.Save())
	final, _ := Load(path, nil)
	if _, ok := final.Get("city"); !ok {
		t.Error("external edit lost")
	}
	if _, ok := final.Variants().Lookup("lastname", 2); !ok {
		t.Error("in-memory variant lost")
	}
	if len(final.Unreviewed().Entries()) != 1 {
		t.Errorf("unreviewed lost: %v", final.Unreviewed().Entries())
	}
}

// Entries promoted or dismissed elsewhere must not be resurrected by a
// running stack that still has them in memory.
func TestStackReloadKeepsRemovals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	plainVault(t, path, nil)
	s, err := OpenStack([]string{path}, noPass)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.Engine(Hide, s.Rules())
	e.Hide("jane@example.org")
	must(t, s.Save())

	cli, _ := Load(path, nil)
	must(t, cli.Dismiss("unreviewed.email.1"))
	must(t, cli.Save(path))
	future := time.Now().Add(time.Second)
	os.Chtimes(path, future, future)

	e.Hide("bob@example.org") // new, unsaved detection
	must(t, s.Reload())
	got := s.Primary().Unreviewed().Entries()
	if _, back := got["unreviewed.email.1"]; back {
		t.Fatal("dismissed entry resurrected")
	}
	if len(got) != 1 {
		t.Fatalf("unsaved detection lost: %v", got)
	}
}
