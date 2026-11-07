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

func TestOpenStorageDoesNotFallbackFromExistingSQLite(t *testing.T) {
	dir := t.TempDir()
	seedLegacyState(t, dir)
	backend, _, err := openStorage(dir, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := backend.CreateNote(model.QuickNote{ID: "sqlite-only", Content: "newer data", ContentType: "text", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	// The migration source still contains only n1. A database error must not
	// silently select that stale source and appear to lose all newer writes.
	dbPath := filepath.Join(dir, "omnishare.db")
	if err := os.WriteFile(dbPath, []byte("damaged database"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, _, err := openStorage(dir, "sqlite")
	if fallback != nil {
		_ = fallback.Close()
	}
	if err == nil {
		t.Fatal("existing SQLite failure silently selected the stale migration JSON")
	}
	if !strings.Contains(err.Error(), "existing SQLite") {
		t.Fatalf("database failure should explain why fallback is unsafe: %v", err)
	}
}

func TestOpenStorageRejectsUnknownEngine(t *testing.T) {
	dir := t.TempDir()
	backend, _, err := openStorage(dir, "sqltie")
	if backend != nil {
		_ = backend.Close()
	}
	if err == nil {
		t.Fatal("unknown storage engine was silently treated as sqlite")
	}
	if _, err := os.Stat(filepath.Join(dir, "omnishare.db")); !os.IsNotExist(err) {
		t.Fatalf("invalid engine selection created a database: %v", err)
	}
}

func TestOpenStorageSuccessfulSQLiteIsNotPending(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		dir := t.TempDir()
		if migrate {
			seedLegacyState(t, dir)
		}
		for attempt := 0; attempt < 2; attempt++ {
			backend, state, err := openStorage(dir, "sqlite")
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			if state.MigrationPending || state.MigrationFailed {
				t.Fatalf("successful startup falsely reports pending migration (legacy=%t, attempt=%d): %+v", migrate, attempt, state)
			}
		}
	}
}
