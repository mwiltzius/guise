package guisefs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"

	"guise/internal/registry"
	"guise/internal/rules"
	"guise/internal/vault"
)

type env struct {
	t       *testing.T
	root    string
	config  string
	scratch string
	vault   string
	trashed []string
}

func newEnv(t *testing.T, values map[string]string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{t: t, root: root, config: filepath.Join(root, "config"),
		scratch: filepath.Join(root, "scratch"), vault: filepath.Join(root, "vault.toml")}
	v, err := vault.Create(e.vault, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, val := range values {
		if err := v.Set(k, val); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Save(e.vault); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) read(p string) string {
	e.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

func (e *env) add(target, kind, mode string, r rules.Config) string {
	e.t.Helper()
	var id string
	err := registry.Update(e.config, func(reg *registry.Registry) error {
		g, err := reg.Add(registry.Guise{Target: target, Path: filepath.Join(e.root, "links", filepath.Base(target)),
			Kind: kind, Mode: mode, Vaults: []string{e.vault}, Rules: r})
		id = g.ID
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) fs() *FS {
	e.t.Helper()
	f, err := New(Options{ConfigDir: e.config, ScratchDir: e.scratch,
		PassFor:    func(string) vault.PassphraseFunc { return nil },
		Trash:      func(p string) error { e.trashed = append(e.trashed, p); return os.Remove(p) },
		Logf:       e.t.Logf,
		FlushDelay: time.Hour, // tests flush explicitly via Close
		SaveDelay:  time.Hour,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return f
}

func readFile(t *testing.T, f billy.Filesystem, name string) string {
	t.Helper()
	h, err := f.Open(name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer h.Close()
	b, err := io.ReadAll(h)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, f billy.Filesystem, name, content string) {
	t.Helper()
	h, err := f.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if _, err := h.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	h.Close()
}

func names(t *testing.T, f billy.Filesystem, dir string) []string {
	t.Helper()
	infos, err := f.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, fi := range infos {
		out = append(out, fi.Name())
	}
	sort.Strings(out)
	return out
}

var person = map[string]string{"firstname": "Matthew", "lastname": "Wiltzius", "city": "Austin"}

func TestFileGuiseReadWrite(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/resume.md", "Matthew Wiltzius\nAustin\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	f := e.fs()

	if got := names(t, f, "/"); len(got) != 1 || got[0] != id {
		t.Fatalf("root = %v", got)
	}
	g := readFile(t, f, id+"/resume.md")
	if g != "{{pi.firstname}} {{pi.lastname}}\n{{pi.city}}\n" {
		t.Fatalf("guise = %q", g)
	}
	fi, _ := f.Stat(id + "/resume.md")
	if fi.Size() != int64(len(g)) {
		t.Fatalf("size %d, want %d", fi.Size(), len(g))
	}
	// An AI moves and duplicates placeholders.
	writeFile(t, f, id+"/resume.md", "{{pi.city}}: {{pi.lastname}}, {{pi.firstname}} {{pi.lastname}}\n")
	if e.read(target) != "Matthew Wiltzius\nAustin\n" {
		t.Fatal("written back before flush")
	}
	f.Close()
	if got := e.read(target); got != "Austin: Wiltzius, Matthew Wiltzius\n" {
		t.Fatalf("target = %q", got)
	}
}

func TestFileGuiseAtomicSaveAndBackup(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/resume.md", "Hi, I'm Matthew.\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	f := e.fs()

	// Temp file + rename over the guised file.
	writeFile(t, f, id+"/.resume.md.tmp123", "Hello, {{pi.firstname}}!\n")
	if err := f.Rename(id+"/.resume.md.tmp123", id+"/resume.md"); err != nil {
		t.Fatal(err)
	}
	if got := e.read(target); got != "Hello, Matthew!\n" {
		t.Fatalf("after atomic save: %q", got)
	}
	if got := names(t, f, id); len(got) != 1 || got[0] != "resume.md" {
		t.Fatalf("dir = %v", got)
	}

	// Backup-then-write (rename original away, create a new one).
	if err := f.Rename(id+"/resume.md", id+"/resume.md~"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(id + "/resume.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renamed-away file still visible: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("target deleted by rename")
	}
	writeFile(t, f, id+"/resume.md", "Bye, {{pi.firstname}}.\n")
	f.Close()
	if got := e.read(target); got != "Bye, Matthew.\n" {
		t.Fatalf("after backup save: %q", got)
	}
	if got := readFile(t, f, id+"/resume.md~"); got != "Hello, {{pi.firstname}}!\n" {
		t.Fatalf("backup = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target), "resume.md~")); err == nil {
		t.Fatal("backup leaked into the target directory")
	}
}

func TestFileGuiseRemoveNeverDeletes(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/resume.md", "Matthew\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	f := e.fs()
	if err := f.Remove(id + "/resume.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("target deleted")
	}
	if got := names(t, f, id); len(got) != 0 {
		t.Fatalf("dir = %v", got)
	}
}

func TestDirGuise(t *testing.T) {
	e := newEnv(t, person)
	dir := filepath.Join(e.root, "job")
	e.write("job/cover.md", "Dear team, I'm Matthew from Austin.\n")
	e.write("job/notes/todo.txt", "call Wiltzius\n")
	e.write("job/photo.png", "\x89PNG\x00\x01binary")
	e.write("job/.guise/config.toml", "")
	os.Symlink("/etc/hosts", filepath.Join(dir, "escape"))
	id := e.add(dir, registry.KindDir, registry.ModeHide, rules.Config{})
	f := e.fs()

	got := names(t, f, id)
	if strings.Join(got, ",") != "cover.md,notes" {
		t.Fatalf("listing = %v (binary, .guise and symlinks must be hidden)", got)
	}
	for _, hidden := range []string{"photo.png", "escape", ".guise/config.toml"} {
		if _, err := f.Open(id + "/" + hidden); err == nil {
			t.Errorf("%s is reachable", hidden)
		}
	}
	if g := readFile(t, f, id+"/notes/todo.txt"); g != "call {{pi.lastname}}\n" {
		t.Fatalf("nested = %q", g)
	}

	// New files are created in the target with values revealed.
	writeFile(t, f, id+"/notes/new.md", "From {{pi.city}}")
	// macOS metadata goes to scratch, never into the target.
	writeFile(t, f, id+"/._cover.md", "appledouble")
	writeFile(t, f, id+"/.DS_Store", "ds")
	f.Close()
	if got := e.read(filepath.Join(dir, "notes/new.md")); got != "From Austin" {
		t.Fatalf("new file = %q", got)
	}
	for _, junk := range []string{"._cover.md", ".DS_Store"} {
		if _, err := os.Stat(filepath.Join(dir, junk)); err == nil {
			t.Errorf("%s written into target", junk)
		}
	}
	if got := names(t, f, id); !contains(got, "._cover.md") {
		t.Errorf("scratch entries not listed: %v", got)
	}

	// Renames are real; deletes go to the Trash.
	if err := f.Rename(id+"/notes/new.md", id+"/notes/renamed.md"); err != nil {
		t.Fatal(err)
	}
	if got := e.read(filepath.Join(dir, "notes/renamed.md")); got != "From Austin" {
		t.Fatalf("renamed = %q", got)
	}
	if err := f.Remove(id + "/notes/renamed.md"); err != nil {
		t.Fatal(err)
	}
	if len(e.trashed) != 1 || filepath.Base(e.trashed[0]) != "renamed.md" {
		t.Fatalf("trashed = %v", e.trashed)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestFillMode(t *testing.T) {
	e := newEnv(t, map[string]string{"name": "Alice"})
	target := e.write("letter.md", "Dear {{pi.name}},\nThanks!\n")
	id := e.add(target, registry.KindFile, registry.ModeFill, rules.Config{})
	f := e.fs()
	if g := readFile(t, f, id+"/letter.md"); g != "Dear Alice,\nThanks!\n" {
		t.Fatalf("filled = %q", g)
	}
	writeFile(t, f, id+"/letter.md", "Dear Alice,\nThanks, Alice!\n")
	f.Close()
	if got := e.read(target); got != "Dear {{pi.name}},\nThanks, {{pi.name}}!\n" {
		t.Fatalf("template = %q", got)
	}
}

func TestExternalChangesAndVaultEdits(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/a.md", "Matthew in Austin\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	f := e.fs()
	readFile(t, f, id+"/a.md")

	e.write("docs/a.md", "Wiltzius now\n")
	if g := readFile(t, f, id+"/a.md"); g != "{{pi.lastname}} now\n" {
		t.Fatalf("after target edit: %q", g)
	}

	before, _ := f.Stat(id + "/a.md")
	v, _ := vault.Load(e.vault, nil)
	v.Set("word", "now")
	v.Save(e.vault)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(e.vault, future, future)
	f.mu.Lock()
	f.lastCheck = time.Time{}
	f.mu.Unlock()
	if g := readFile(t, f, id+"/a.md"); g != "{{pi.lastname}} {{pi.word}}\n" {
		t.Fatalf("after vault edit: %q", g)
	}
	after, _ := f.Stat(id + "/a.md")
	if !after.ModTime().After(before.ModTime()) {
		t.Fatal("mtime not bumped after the guise view changed")
	}
}

func TestDetectionsPersistAndExposeRule(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/a.md", "Matthew, Austin, jane@example.org\n")
	id := e.add(target, registry.KindFile, registry.ModeHide,
		rules.Config{Rules: rules.Rules{Expose: map[string]bool{"city": true}}})
	f := e.fs()
	if g := readFile(t, f, id+"/a.md"); g != "{{pi.firstname}}, Austin, {{pi.unreviewed.email.1}}\n" {
		t.Fatalf("guise = %q", g)
	}
	f.Close()
	v, err := vault.Load(e.vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v.Unreviewed().Lookup("unreviewed.email.1"); got != "jane@example.org" {
		t.Fatalf("unreviewed not saved: %v", v.Unreviewed().Entries())
	}
}

func TestUnavailableVaultFailsClosed(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/a.md", "Matthew\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	os.Remove(e.vault)
	f := e.fs()
	if _, err := f.Open(id + "/a.md"); err == nil {
		t.Fatal("guise readable without its vault")
	}
}

func TestReopenAfterUnlock(t *testing.T) {
	e := newEnv(t, person)
	target := e.write("docs/a.md", "Matthew\n")
	id := e.add(target, registry.KindFile, registry.ModeHide, rules.Config{})
	f, err := New(Options{ConfigDir: e.config, ScratchDir: e.scratch, Logf: t.Logf,
		PassFor: func(string) vault.PassphraseFunc { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate guises whose vault was locked at startup.
	f.mu.Lock()
	for _, g := range f.guises {
		g.stack = nil
	}
	f.mu.Unlock()
	if got := f.Unavailable(); len(got) != 1 {
		t.Fatalf("unavailable = %v", got)
	}
	if err := f.Reopen(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f, id+"/a.md"); got != "{{pi.firstname}}\n" {
		t.Fatalf("after reopen: %q", got)
	}
}
