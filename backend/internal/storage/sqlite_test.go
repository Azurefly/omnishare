package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestSQLiteBasicLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer store.Close()

	// Integrity check
	check, err := store.IntegrityCheck()
	if err != nil || check != "ok" {
		t.Fatalf("IntegrityCheck: %s, %v", check, err)
	}

	// 1. Note CRUD
	now := time.Now()
	n := model.QuickNote{
		ID:        NewID("n"),
		Content:   "SQLite WAL test #tag",
		Tags:      []string{"tag"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreateNote(n); err != nil {
		t.Fatalf("CreateNote: %v", err)
	}

	notes := store.ListNotes("WAL", "tag")
	if len(notes) != 1 || notes[0].ID != n.ID {
		t.Fatalf("ListNotes failed: %+v", notes)
	}

	updated, err := store.UpdateNote(n.ID, func(item *model.QuickNote) error {
		item.Content = "Updated content"
		item.Pinned = true
		return nil
	})
	if err != nil || !updated.Pinned || updated.Content != "Updated content" {
		t.Fatalf("UpdateNote failed: %+v, %v", updated, err)
	}

	// 2. Pad CRUD
	pad := model.PadDocument{
		ID:        NewID("pad"),
		Title:     "Doc 1",
		Content:   "Content 1",
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.CreatePad(pad); err != nil {
		t.Fatalf("CreatePad: %v", err)
	}
	pads := store.ListPads("Doc")
	if len(pads) != 1 {
		t.Fatalf("ListPads: %d", len(pads))
	}

	// 3. File CRUD & Trash
	f := model.FileAsset{
		ID:          NewID("f"),
		FileName:    "test.txt",
		FileSize:    100,
		MIMEType:    "text/plain",
		StoragePath: "uploads/blobs/test.txt",
		FileHash:    "dummyhash",
		CreatedAt:   now,
	}
	if err := store.CreateFile(f); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}

	delFiles, err := store.DeleteFiles([]string{f.ID})
	if err != nil || len(delFiles) != 1 {
		t.Fatalf("DeleteFiles: %v", err)
	}
	trash := store.ListTrash("file")
	if len(trash) != 1 || trash[0].ID != f.ID {
		t.Fatalf("ListTrash: %+v", trash)
	}

	// 4. Share CRUD
	share := model.ShareLink{
		ID:         NewID("shr"),
		Token:      NewToken(),
		ObjectType: "pad",
		ObjectID:   pad.ID,
		Name:       "Pad Share",
		CreatedAt:  now,
	}
	if err := store.CreateShare(share); err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	gotShare, err := store.GetShareByToken(share.Token)
	if err != nil || gotShare.Status != "active" {
		t.Fatalf("GetShareByToken: %+v, %v", gotShare, err)
	}

	// 5. Audits
	audits := store.ListAudits(10)
	if len(audits) < 4 {
		t.Fatalf("expected >= 4 audits, got %d", len(audits))
	}
}

func TestSQLiteConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer store.Close()

	const readers = 50
	const writers = 50
	var wg sync.WaitGroup
	errCh := make(chan error, readers+writers)

	// Launch concurrent readers
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = store.ListNotes("", "")
				_ = store.Stats()
				time.Sleep(1 * time.Millisecond)
			}
		}(i)
	}

	// Launch concurrent writers
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			now := time.Now()
			n := model.QuickNote{
				ID:        NewID("n"),
				Content:   fmt.Sprintf("Concurrent note from worker %d", id),
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := store.CreateNote(n); err != nil {
				errCh <- fmt.Errorf("writer %d: %w", id, err)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent error: %v", err)
	}

	notes := store.ListNotes("", "")
	if len(notes) != writers {
		t.Fatalf("expected %d notes, got %d", writers, len(notes))
	}

	// Checkpoint WAL
	if err := store.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
}

func TestSQLiteMigrationAndDump(t *testing.T) {
	dir := t.TempDir()

	// 1. Populate a JSON store first
	jsonStore, err := New(dir)
	if err != nil {
		t.Fatalf("New JSON Store: %v", err)
	}

	now := time.Now().UTC()
	note := model.QuickNote{ID: NewID("n"), Content: "Legacy note", CreatedAt: now, UpdatedAt: now}
	if err := jsonStore.CreateNote(note); err != nil {
		t.Fatal(err)
	}
	pad := model.PadDocument{ID: NewID("pad"), Title: "Legacy pad", Content: "Pad body", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := jsonStore.CreatePad(pad); err != nil {
		t.Fatal(err)
	}
	share := model.ShareLink{ID: NewID("shr"), Token: NewToken(), ObjectType: "note", ObjectID: note.ID, Name: "Share", CreatedAt: now}
	if err := jsonStore.CreateShare(share); err != nil {
		t.Fatal(err)
	}

	jsonSnap, err := jsonStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}

	// 2. Open SQLite Store on the same directory -> should trigger automatic migration
	sqlStore, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("NewSQLite migration: %v", err)
	}
	defer sqlStore.Close()

	// Check that json backup exists
	if _, err := os.Stat(filepath.Join(dir, "omnishare.json.bak")); err != nil {
		t.Fatalf("expected omnishare.json.bak: %v", err)
	}

	// Verify migrated data
	migratedNote, err := sqlStore.GetNote(note.ID)
	if err != nil || migratedNote.Content != "Legacy note" {
		t.Fatalf("migrated note mismatch: %+v, %v", migratedNote, err)
	}
	migratedPad, err := sqlStore.GetPad(pad.ID)
	if err != nil || migratedPad.Title != "Legacy pad" {
		t.Fatalf("migrated pad mismatch: %+v, %v", migratedPad, err)
	}

	// Verify DumpState round-trip
	dumped, err := sqlStore.DumpState()
	if err != nil {
		t.Fatalf("DumpState: %v", err)
	}
	if len(dumped.Notes) != 1 || len(dumped.Pads) != 1 || len(dumped.Shares) != 1 {
		t.Fatalf("dumped state counts mismatch: notes=%d, pads=%d, shares=%d", len(dumped.Notes), len(dumped.Pads), len(dumped.Shares))
	}

	// Verify ReplaceStateJSON
	if err := sqlStore.ReplaceStateJSON(jsonSnap); err != nil {
		t.Fatalf("ReplaceStateJSON: %v", err)
	}
	afterReplaceNote, err := sqlStore.GetNote(note.ID)
	if err != nil || afterReplaceNote.Content != "Legacy note" {
		t.Fatalf("after replace note mismatch: %+v, %v", afterReplaceNote, err)
	}
}

func TestSQLiteReadOnlyPoolRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := NewSQLite(dir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	defer store.Close()

	// Attempt to execute an insert on readDB directly
	_, err = store.readDB.Exec("INSERT INTO schema_meta(key, value, updated_at) VALUES ('hack', 'fail', 'now')")
	if err == nil {
		t.Fatalf("expected readDB write to fail due to mode=ro, but succeeded")
	}
}
