package frontend

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web UI under frontend/web is the source of truth; this package embeds a
// copy so the single Go binary can serve it. When the two drift the shipped UI
// silently regresses to stale behaviour, which is easy to miss because the
// source and the embedded copy look identical in review.
func TestEmbeddedAssetsMatchWebSource(t *testing.T) {
	source := filepath.Join("..", "..", "..", "frontend", "web")
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		t.Fatalf("web source directory unavailable at %s: %v", source, err)
	}

	if err := filepath.Walk(source, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		// Editor and OS metadata must not be embedded.
		if strings.HasPrefix(name, ".") {
			return nil
		}

		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := fs.ReadFile(assets, "dist/"+name)
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Errorf("embedded asset dist/%s is stale; copy %s into it", name, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Every embedded asset is served, so the copy must not gain extra entries that
// no longer exist in the source tree.
func TestEmbeddedAssetsHaveNoExtras(t *testing.T) {
	entries, err := fs.Glob(assets, "dist/*")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := strings.TrimPrefix(entry, "dist/")
		if _, err := os.Stat(filepath.Join("..", "..", "..", "frontend", "web", filepath.FromSlash(name))); err != nil {
			t.Errorf("embedded asset %s has no web source; it is dead weight in the binary", entry)
		}
	}
}
