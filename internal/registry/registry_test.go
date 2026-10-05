package registry

import (
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func guise(target, path string) Guise {
	return Guise{Target: target, Path: path, Kind: KindFile, Mode: ModeHide, Vaults: []string{"/v.age"}}
}

func TestAddRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var id string
	err := Update(dir, func(r *Registry) error {
		g, err := r.Add(guise("/docs/My Resume (2026).md", "/ai/resume.md"))
		id = g.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^my-resume-2026-[0-9a-f]{4}$`).MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	r, _ := Load(dir)
	g, ok := r.ByPath("/ai/resume.md")
	if !ok || g.ID != id || g.Target != "/docs/My Resume (2026).md" || g.Created.IsZero() {
		t.Fatalf("loaded %+v", g)
	}
	if _, ok := r.Remove("/ai/resume.md"); !ok || len(r.Guises) != 0 {
		t.Fatal("remove failed")
	}
}

func TestAddValidation(t *testing.T) {
	r := &Registry{}
	if _, err := r.Add(guise("/docs/a.md", "/ai/a.md")); err != nil {
		t.Fatal(err)
	}
	bad := []Guise{
		guise("/docs/b.md", "/ai/a.md"),  // duplicate path
		guise("docs/b.md", "/ai/b.md"),   // relative
		guise("/docs", "/docs/sub/link"), // inside target
		{Target: "/x", Path: "/y", Kind: KindFile, Mode: "show", Vaults: []string{"/v"}},
		{Target: "/x", Path: "/y", Kind: KindFile, Mode: ModeHide},
	}
	for _, g := range bad {
		if _, err := r.Add(g); err == nil {
			t.Errorf("Add(%+v) succeeded", g)
		}
	}
}

func TestConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Update(dir, func(r *Registry) error {
				_, err := r.Add(guise("/docs/a.md", filepath.Join("/ai", string(rune('a'+i)))))
				return err
			})
		}()
	}
	wg.Wait()
	r, _ := Load(dir)
	if len(r.Guises) != 20 {
		t.Fatalf("%d guises, want 20", len(r.Guises))
	}
}
