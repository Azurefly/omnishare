package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestNormalizeFolderPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"   ///  ", "", true},
		{`\工作\2026\方案/`, "工作/2026/方案", true},
		{"a/b/c/d/e/f", "a/b/c/d/e/f", true},
		{"a/b/c/d/e/f/g", "", false},
		{"../secret", "", false},
		{"工作/./方案", "", false},
		{"工作//方案", "", false},
		{"__root__", "", false},
		{"工作:资料", "", false},
		{"bad*name", "", false},
		{"ctrl\nline", "", false},
	}
	for _, tc := range cases {
		got, err := NormalizeFolderPath(tc.in)
		if tc.ok && err != nil {
			t.Fatalf("NormalizeFolderPath(%q) unexpected error: %v", tc.in, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("NormalizeFolderPath(%q) expected error, got %q", tc.in, got)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("NormalizeFolderPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFolderOperationsAcrossBackends(t *testing.T) {
	run := func(t *testing.T, store Backend) {
		t.Helper()
		now := time.Now().UTC()

		if err := store.EnsureFolder("notes", `工作\2026\v1_draft`); err != nil {
			t.Fatalf("EnsureFolder notes: %v", err)
		}
		if err := store.EnsureFolder("notes", "生活"); err != nil {
			t.Fatalf("EnsureFolder notes root2: %v", err)
		}
		if err := store.EnsureFolder("files", "设计/素材"); err != nil {
			t.Fatalf("EnsureFolder files: %v", err)
		}
		if err := store.EnsureFolder("pads", "团队/周报"); err != nil {
			t.Fatalf("EnsureFolder pads: %v", err)
		}

		// Create notes across root, parent, and child folders.
		if err := store.CreateNote(model.QuickNote{ID: NewID("n"), Content: "root note", ContentType: "text/plain", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.CreateNote(model.QuickNote{ID: NewID("n"), Content: "parent note", ContentType: "text/plain", Folder: "工作", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		childNoteID := NewID("n")
		if err := store.CreateNote(model.QuickNote{ID: childNoteID, Content: "child note", ContentType: "text/plain", Folder: "工作/2026/v1_draft", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}

		if got := len(store.ListNotesInFolder("", "", FolderRootFilter, false)); got != 1 {
			t.Fatalf("root filter got %d notes, want 1", got)
		}
		if got := len(store.ListNotesInFolder("", "", "工作", false)); got != 1 {
			t.Fatalf("non-recursive folder filter got %d notes, want 1", got)
		}
		if got := len(store.ListNotesInFolder("", "", "工作", true)); got != 2 {
			t.Fatalf("recursive folder filter got %d notes, want 2", got)
		}

		// SQLite wildcard _ must not match unrelated v1Xdraft folders.
		if err := store.CreateNote(model.QuickNote{ID: NewID("n"), Content: "similar name", ContentType: "text/plain", Folder: "工作/2026/v1Xdraft", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}

		// Rename folder with underscore; check self-nesting guard first.
		if _, err := store.RenameFolder("notes", "工作/2026", "工作/2026/sub"); err == nil {
			t.Fatal("expected error when moving folder into itself")
		}
		moved, err := store.RenameFolder("notes", "工作/2026/v1_draft", "归档/第一版")
		if err != nil {
			t.Fatalf("RenameFolder: %v", err)
		}
		if moved != 1 {
			t.Fatalf("RenameFolder moved %d items, want 1 (wildcard leaked?)", moved)
		}
		renamedNote, err := store.GetNote(childNoteID)
		if err != nil || renamedNote.Folder != "归档/第一版" {
			t.Fatalf("moved note folder = %q, err=%v", renamedNote.Folder, err)
		}

		// Files: create, move, update name+folder.
		fID := NewID("f")
		if err := store.CreateFile(model.FileAsset{ID: fID, FileName: "spec.pdf", FileSize: 12, MIMEType: "application/pdf", Folder: "设计/素材", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		newName := "spec-v2.pdf"
		newFolder := "设计"
		uf, err := store.UpdateFile(fID, &newName, &newFolder)
		if err != nil || uf.FileName != newName || uf.Folder != newFolder {
			t.Fatalf("UpdateFile = %+v, err=%v", uf, err)
		}
		if got := len(store.ListFilesInFolder("", "", "设计", false)); got != 1 {
			t.Fatalf("ListFilesInFolder exact got %d, want 1", got)
		}

		// Pads: create, update folder with optimistic lock, bulk move, and delete folder move_to_root.
		pID := NewID("pad")
		if err := store.CreatePad(model.PadDocument{ID: pID, Title: "Plan", Content: "body", Folder: "团队/周报", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		targetPadFolder := "团队/归档"
		if _, err := store.UpdatePadWithFolder(pID, "Plan", "body", &targetPadFolder, 99); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("expected ErrVersionConflict, got %v", err)
		}
		up, err := store.UpdatePadWithFolder(pID, "Plan", "body v2", &targetPadFolder, 1)
		if err != nil || up.Folder != targetPadFolder || up.Version != 2 {
			t.Fatalf("UpdatePadWithFolder = %+v, err=%v", up, err)
		}
		if _, err := store.DeleteFolder("pads", "团队", "move_to_root"); err != nil {
			t.Fatalf("DeleteFolder move_to_root: %v", err)
		}
		gotPad, err := store.GetPad(pID)
		if err != nil || gotPad.Folder != "" {
			t.Fatalf("pad after DeleteFolder move_to_root = %+v, err=%v", gotPad, err)
		}

		// BulkMove & DeleteFolder trash_items + RestoreTrash restores ancestor folders.
		if cnt, err := store.MoveItems("pads", []string{pID}, "重建/文档"); err != nil || cnt != 1 {
			t.Fatalf("MoveItems pads cnt=%d err=%v", cnt, err)
		}
		if _, err := store.DeleteFolder("pads", "重建", "trash_items"); err != nil {
			t.Fatalf("DeleteFolder trash_items: %v", err)
		}
		if got := len(store.ListPadsInFolder("", "", true)); got != 0 {
			t.Fatalf("expected 0 active pads after trash_items, got %d", got)
		}
		if err := store.RestoreTrash("pad", pID); err != nil {
			t.Fatalf("RestoreTrash pad: %v", err)
		}
		folders, err := store.ListFolders("pads")
		if err != nil || len(folders) < 2 {
			t.Fatalf("expected ancestor folders restored, got %+v err=%v", folders, err)
		}

		// Snapshot & ReplaceStateJSON round-trip (plus legacy snapshot without folders field).
		snap, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(snap, &raw); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceStateJSON(snap); err != nil {
			t.Fatalf("ReplaceStateJSON round-trip: %v", err)
		}
		delete(raw, "folders")
		legacySnap, _ := json.Marshal(raw)
		if err := store.ReplaceStateJSON(legacySnap); err != nil {
			t.Fatalf("ReplaceStateJSON legacy compatibility: %v", err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		st, err := NewSQLite(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})

	t.Run("json", func(t *testing.T) {
		st, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		run(t, st)
	})
}

func TestOpenSQLiteMigratesLegacySchemaWithoutFolderColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "omnishare.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := `
	CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE TABLE quick_notes (
		id TEXT PRIMARY KEY, content TEXT NOT NULL, content_redacted INTEGER NOT NULL DEFAULT 0,
		content_type TEXT NOT NULL DEFAULT 'text', tags TEXT NOT NULL DEFAULT '[]',
		pinned INTEGER NOT NULL DEFAULT 0, is_burn_after_read INTEGER NOT NULL DEFAULT 0,
		read_count INTEGER NOT NULL DEFAULT 0, max_read_count INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT, deleted_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	);
	CREATE TABLE file_assets (
		id TEXT PRIMARY KEY, file_name TEXT NOT NULL, file_size INTEGER NOT NULL, mime_type TEXT NOT NULL,
		storage_path TEXT NOT NULL DEFAULT '', file_hash TEXT NOT NULL DEFAULT '',
		download_count INTEGER NOT NULL DEFAULT 0, is_video INTEGER NOT NULL DEFAULT 0,
		download_url TEXT NOT NULL DEFAULT '', stream_url TEXT NOT NULL DEFAULT '',
		expires_at TEXT, deleted_at TEXT, created_at TEXT NOT NULL
	);
	CREATE TABLE pad_documents (
		id TEXT PRIMARY KEY, title TEXT NOT NULL, content TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL DEFAULT 1, deleted_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	);`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO quick_notes(id, content, created_at, updated_at) VALUES ('n_legacy', 'old note', ?, ?)`, nowStr, nowStr); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	st, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("OpenSQLite on legacy db failed: %v", err)
	}
	defer st.Close()
	notes := st.ListNotesInFolder("", "", FolderRootFilter, false)
	if len(notes) != 1 || notes[0].ID != "n_legacy" {
		t.Fatalf("migrated legacy note lookup failed: %+v", notes)
	}
}
