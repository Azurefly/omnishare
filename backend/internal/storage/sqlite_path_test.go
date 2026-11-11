package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestSQLiteDataDirectoryPathsPersistAcrossReopen(t *testing.T) {
	names := []string{"directory with spaces", "中文数据", "account#notes", "percent%2Fnotes", "mixed 中文 #100%"}
	if runtime.GOOS != "windows" {
		// '?' is a legal filename character on Unix, but not on Windows.
		names = append(names, "account?notes")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			assertSQLitePathPersistence(t, dir)
		})
	}
}

func TestSQLiteRelativeDataDirectoryRemainsRelative(t *testing.T) {
	t.Chdir(t.TempDir())
	assertSQLitePathPersistence(t, "relative 中文 #100%")
}

func assertSQLitePathPersistence(t *testing.T, dir string) {
	t.Helper()
	now := time.Now().UTC()
	note := model.QuickNote{ID: "path-note", Content: "持久化内容 #100%", ContentType: "text", CreatedAt: now, UpdatedAt: now}
	for attempt := 0; attempt < 2; attempt++ {
		store, err := NewSQLite(dir)
		if err != nil {
			t.Fatalf("open attempt %d: %v", attempt, err)
		}
		closed := false
		t.Cleanup(func() {
			if !closed {
				_ = store.Close()
			}
		})
		if attempt == 0 {
			if err := store.CreateNote(note); err != nil {
				t.Fatalf("write note: %v", err)
			}
		}
		expected := filepath.Join(dir, "omnishare.db")
		file, err := os.Stat(expected)
		if err != nil {
			t.Fatalf("database was not created in the selected data directory: %v", err)
		}
		var seq int
		var database, actual string
		if err := store.writeDB.QueryRow("PRAGMA database_list").Scan(&seq, &database, &actual); err != nil {
			t.Fatalf("inspect actual database: %v", err)
		}
		actualFile, err := os.Stat(actual)
		if err != nil || !os.SameFile(file, actualFile) {
			t.Fatalf("SQLite opened %q instead of %q: %v", actual, expected, err)
		}
		got, err := store.GetNote(note.ID)
		if err != nil || got.Content != note.Content {
			t.Fatalf("read attempt %d: note=%+v, err=%v", attempt, got, err)
		}
		if _, err := store.readDB.Exec("UPDATE quick_notes SET content='changed' WHERE id=?", note.ID); err == nil {
			t.Fatal("path parsing disabled the read-only connection pool")
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close attempt %d: %v", attempt, err)
		}
		closed = true
	}
}
