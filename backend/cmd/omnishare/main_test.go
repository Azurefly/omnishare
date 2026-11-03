package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnishare/internal/model"
	"omnishare/internal/storage"
)

func seedLegacyState(t *testing.T, dir string) {
	t.Helper()
	legacy, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.CreateNote(model.QuickNote{ID: "n1", Content: "hello", ContentType: "text", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.AddAudit("note", "note", "n1", "created", nil); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStorageMigratesThenFallsBackOnCorruption(t *testing.T) {
	dir := t.TempDir()
	seedLegacyState(t, dir)

	backend, state, err := openStorage(dir, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if state.Engine != "sqlite" || state.MigrationFailed {
		t.Fatalf("unexpected state after migration: %+v", state)
	}
	if notes := backend.ListNotes("", ""); len(notes) != 1 {
		t.Fatalf("migrated notes = %d", len(notes))
	}
	_ = backend.Close()

	// Corrupt the audit hash in a fresh legacy-only directory. The migration
	// must fail, keep the JSON untouched, and boot on the JSON engine with a
	// clear "not migrated" status.
	brokenDir := t.TempDir()
	seedLegacyState(t, brokenDir)
	jsonPath := filepath.Join(brokenDir, "omnishare.json")
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := strings.Replace(string(raw), `"hash": "`, `"hash": "deadbeef`, 1)
	if corrupted == string(raw) {
		t.Fatal("could not locate the audit hash to corrupt")
	}
	if err := os.WriteFile(jsonPath, []byte(corrupted), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, state, err := openStorage(brokenDir, "sqlite")
	if err != nil {
		t.Fatalf("openStorage should fall back instead of failing: %v", err)
	}
	defer fallback.Close()
	if state.Engine != "json" || !state.MigrationFailed || state.MigrationError == "" {
		t.Fatalf("fallback status = %+v", state)
	}
	if notes := fallback.ListNotes("", ""); len(notes) != 1 {
		t.Fatalf("fallback notes = %d", len(notes))
	}
}
