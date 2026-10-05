package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() { scryptWorkFactor = 10 } // keep tests fast

func passphrase(p string) PassphraseFunc {
	return func() ([]byte, error) { return []byte(p), nil }
}

func noPassphrase(t *testing.T) PassphraseFunc {
	return func() ([]byte, error) {
		t.Helper()
		t.Fatal("passphrase requested for a plain vault")
		return nil, nil
	}
}

func TestPlainRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "letter.toml")
	v, err := Create(path, false, noPassphrase(t))
	if err != nil {
		t.Fatal(err)
	}
	must(t, v.Set("name", "Alice"))
	must(t, v.Set("address.city", "Austin"))
	must(t, v.SetPattern("employee_id", `EMP-\d{6}`))
	must(t, v.Save(path))

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `name = "Alice"`) {
		t.Fatalf("plain vault not human-readable:\n%s", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}

	v2, err := Load(path, noPassphrase(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v2.Get("address.city"); got != "Austin" {
		t.Fatalf("address.city = %q", got)
	}
	if v2.Encrypted {
		t.Fatal("plain vault loaded as encrypted")
	}
}

func TestEncryptedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.age")
	v, err := Create(path, true, passphrase("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, v.Set("lastname", "Wiltzius"))
	must(t, v.Save(path))

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "Wiltzius") {
		t.Fatal("encrypted vault contains plaintext")
	}
	if _, err := Load(path, passphrase("wrong")); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	v2, err := Load(path, passphrase("correct horse"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := v2.Get("lastname"); got != "Wiltzius" {
		t.Fatalf("lastname = %q", got)
	}
	// Saving again reuses the unlocked passphrase.
	must(t, v2.Set("city", "Austin"))
	must(t, v2.Save(path))
	if _, err := Load(path, passphrase("correct horse")); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	if _, err := Create(path, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, false, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestRejectsInvalidEntries(t *testing.T) {
	v := newVault(false)
	if err := v.Set("bad name", "x"); err == nil {
		t.Error("bad name accepted")
	}
	if err := v.Set("unreviewed.3", "x"); err == nil {
		t.Error("reserved name accepted")
	}
	if err := v.SetPattern("p", "("); err == nil {
		t.Error("bad regex accepted")
	}
	path := filepath.Join(t.TempDir(), "v.toml")
	os.WriteFile(path, []byte("[values]\n\"bad name\" = \"x\"\n"), 0o600)
	if _, err := Load(path, nil); err == nil {
		t.Error("invalid file accepted")
	}
}

// Detections are persisted with the vault, and review moves them into the
// vault or onto the ignore list.
func TestUnreviewedLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	v, err := Create(path, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := v.Engine(Hide)
	if err != nil {
		t.Fatal(err)
	}
	g := e.Hide("mail jane@example.org or support@example.com")
	if g != "mail {{pi.unreviewed.email.1}} or {{pi.unreviewed.email.2}}" {
		t.Fatalf("got %q", g)
	}
	must(t, v.Save(path))

	v2, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	must(t, v2.Promote("unreviewed.email.1", "email"))
	must(t, v2.Dismiss("unreviewed.email.2"))
	must(t, v2.Save(path))

	v3, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	e3, err := v3.Engine(Hide)
	if err != nil {
		t.Fatal(err)
	}
	if g := e3.Hide("mail jane@example.org or support@example.com"); g != "mail {{pi.email}} or support@example.com" {
		t.Fatalf("after review: %q", g)
	}
	if n := len(v3.Unreviewed().Entries()); n != 0 {
		t.Fatalf("%d unreviewed entries left", n)
	}
}

func TestVariantsPersistAndReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	v, _ := Create(path, false, nil)
	must(t, v.Set("lastname", "Wiltzius"))
	e, _ := v.Engine(Hide)
	if g := e.Hide("WiLtZiUs"); g != "{{pi.lastname:2}}" {
		t.Fatalf("got %q", g)
	}
	if !v.Dirty() || v.Dirty() {
		t.Fatal("Dirty should report the new variant exactly once")
	}
	must(t, v.Save(path))

	v2, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := v2.Engine(Hide)
	if r, _ := e2.Reveal("{{pi.lastname:2}}"); r != "WiLtZiUs" {
		t.Fatalf("variant not persisted: %q", r)
	}
	must(t, v2.Set("lastname", "WILTZIUS")) // case change keeps variants
	if _, ok := v2.Variants().Lookup("lastname", 2); !ok {
		t.Fatal("variant dropped on case-only change")
	}
	must(t, v2.Set("lastname", "Doe")) // new value drops them
	if _, ok := v2.Variants().Lookup("lastname", 2); ok {
		t.Fatal("stale variant kept after value change")
	}

	os.WriteFile(path, []byte("[values]\nlastname = \"Doe\"\n[variants.lastname]\n2 = \"Smith\"\n"), 0o600)
	if _, err := Load(path, nil); err == nil {
		t.Fatal("variant that is not a case variant accepted")
	}
}

func TestSuggestName(t *testing.T) {
	v := newVault(false)
	if got := v.SuggestName("unreviewed.email.1"); got != "email" {
		t.Fatalf("got %q", got)
	}
	must(t, v.Set("email", "a@example.org"))
	must(t, v.Set("email.2", "b@example.org"))
	if got := v.SuggestName("unreviewed.email.7"); got != "email.3" {
		t.Fatalf("got %q", got)
	}
	// Promoting onto an existing name must not overwrite it.
	e, _ := v.Engine(Hide)
	e.Hide("c@example.org")
	if err := v.Promote("unreviewed.email.1", "email"); err == nil {
		t.Fatal("Promote overwrote an existing value")
	}
	must(t, v.Promote("unreviewed.email.1", v.SuggestName("unreviewed.email.1")))
	if got, _ := v.Get("email.3"); got != "c@example.org" {
		t.Fatalf("email.3 = %q", got)
	}
}

func TestUserPatterns(t *testing.T) {
	v := newVault(false)
	must(t, v.SetPattern("employee_id", `EMP-\d{6}`))
	e, err := v.Engine(Hide)
	if err != nil {
		t.Fatal(err)
	}
	if g := e.Hide("badge EMP-123456"); g != "badge {{pi.unreviewed.employee_id.1}}" {
		t.Fatalf("got %q", g)
	}
	fe, err := v.Engine(Fill)
	if err != nil {
		t.Fatal(err)
	}
	if g := fe.Unfill("badge EMP-123456"); g != "badge EMP-123456" {
		t.Fatalf("fill mode ran detectors: %q", g)
	}
}

func TestCustomPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.toml")
	v, _ := Create(path, false, nil)
	must(t, v.Set("name", "Alice"))
	must(t, v.SetPrefix("to"))
	must(t, v.Save(path))
	v2, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := v2.Engine(Fill)
	if out, _ := e.Fill("Dear {{to.name}},"); out != "Dear Alice," {
		t.Fatalf("got %q", out)
	}
	if err := v2.SetPrefix("bad.prefix"); err == nil {
		t.Fatal("dotted prefix accepted")
	}
}

func TestResolve(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv(EnvVar, "")
	project := filepath.Join(root, "project")
	deep := filepath.Join(project, "a", "b")
	must(t, os.MkdirAll(deep, 0o755))

	check := func(flag, recorded string, wantPath string, wantSrc Source) {
		t.Helper()
		p, src, err := Resolve(flag, recorded, deep)
		if err != nil {
			t.Fatal(err)
		}
		if p != wantPath || src != wantSrc {
			t.Fatalf("Resolve(%q, %q) = %q (%s), want %q (%s)", flag, recorded, p, src, wantPath, wantSrc)
		}
	}

	check("", "", filepath.Join(root, "config", "guise", "vault.age"), FromDefault)

	pv := filepath.Join(project, ProjectDir, "vault.toml")
	must(t, os.MkdirAll(filepath.Dir(pv), 0o700))
	must(t, os.WriteFile(pv, nil, 0o600))
	check("", "", pv, FromProject)

	env := filepath.Join(root, "env.age")
	t.Setenv(EnvVar, env)
	check("", "", env, FromEnv)

	rec := filepath.Join(root, "recorded.toml")
	check("", rec, rec, FromGuise)

	fl := filepath.Join(root, "flag.toml")
	check(fl, rec, fl, FromFlag)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
