package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestStoreNoteLifecycleExpirationTrashAndRollback(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	n := model.QuickNote{ID: NewID("n"), Content: "deploy #release", Tags: []string{"release"}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateNote(n); err != nil {
		t.Fatal(err)
	}
	if got := s.ListNotes("deploy", ""); len(got) != 1 {
		t.Fatalf("search=%d", len(got))
	}
	updated, err := s.UpdateNote(n.ID, func(item *model.QuickNote) error { item.Content = ""; item.Pinned = true; return nil })
	if err != nil || updated.Content != "" || !updated.Pinned {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}

	// A failed durable write must not leak a mutation into memory.
	originalPath := s.path
	blocker := filepath.Join(s.dataDir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocker, "state.json")
	_, err = s.UpdateNote(n.ID, func(item *model.QuickNote) error { item.Content = "should rollback"; return nil })
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	s.path = originalPath
	current, err := s.GetNote(n.ID)
	if err != nil || current.Content == "should rollback" {
		t.Fatalf("rollback failed: %+v %v", current, err)
	}

	count, err := s.DeleteNotes([]string{n.ID})
	if err != nil || count != 1 {
		t.Fatalf("delete=%d err=%v", count, err)
	}
	if _, err := s.GetNote(n.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("hidden note: %v", err)
	}
	if err := s.RestoreTrash("note", n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNote(n.ID); err != nil {
		t.Fatal(err)
	}

	expired := time.Now().Add(-time.Second)
	e := model.QuickNote{ID: NewID("n"), Content: "expired", ExpiresAt: &expired, CreatedAt: now, UpdatedAt: now}
	_ = s.CreateNote(e)
	if _, err := s.ReadNote(e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired detail readable: %v", err)
	}
}

func TestBurnAfterReadMaxReadAndShareRevocation(t *testing.T) {
	s, _ := New(t.TempDir())
	now := time.Now()
	burn := model.QuickNote{ID: NewID("n"), Content: "secret", IsBurnAfterRead: true, CreatedAt: now, UpdatedAt: now}
	_ = s.CreateNote(burn)
	link := model.ShareLink{ID: NewID("shr"), Token: NewToken(), ObjectType: "note", ObjectID: burn.ID, Name: "secret", CreatedAt: now}
	_ = s.CreateShare(link)
	if _, err := s.ReadNote(burn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetShareByToken(link.Token); !errors.Is(err, ErrShareRevoked) {
		t.Fatalf("share not revoked: %v", err)
	}
	limited := model.QuickNote{ID: NewID("n"), Content: "twice", MaxReadCount: 2, CreatedAt: now, UpdatedAt: now}
	_ = s.CreateNote(limited)
	_, _ = s.ReadNote(limited.ID)
	if _, err := s.GetNote(limited.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = s.ReadNote(limited.ID)
	if _, err := s.GetNote(limited.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("max-read note remains")
	}
}

func TestFileValidationLogicalDedupReferencesAndTrash(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	blob := filepath.Join("uploads", "blobs", "aa", "blob")
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(blob)), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("same-content")
	if err := os.WriteFile(filepath.Join(dir, blob), content, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, _ := hashFile(filepath.Join(dir, blob))
	now := time.Now()
	f1 := model.FileAsset{ID: NewID("f"), FileName: "a.txt", FileSize: int64(len(content)), StoragePath: blob, FileHash: hash, CreatedAt: now}
	f2 := f1
	f2.ID = NewID("f")
	f2.FileName = "b.txt"
	if err := s.CreateFile(f1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFile(f2); err != nil {
		t.Fatal(err)
	}
	if found, err := s.FindFileByHash(hash, int64(len(content))); err != nil || found.FileHash != hash {
		t.Fatalf("find=%+v %v", found, err)
	}
	if !s.StorageReferencedByOthers(blob, f1.ID) {
		t.Fatal("shared blob reference not detected")
	}
	deleted, err := s.DeleteFiles([]string{f1.ID})
	if err != nil || len(deleted) != 1 {
		t.Fatalf("delete=%v %v", deleted, err)
	}
	if _, err := s.PurgeTrash("file", f1.ID); err != nil {
		t.Fatal(err)
	}
	if !s.StorageReferencedByOthers(blob, f1.ID) {
		t.Fatal("remaining reference lost")
	}
	if err := os.Remove(filepath.Join(dir, blob)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindFileByHash(hash, int64(len(content))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale metadata reused: %v", err)
	}
}

func TestPadConflictShareLifecycleAndAuditChain(t *testing.T) {
	s, _ := New(t.TempDir())
	now := time.Now()
	p := model.PadDocument{ID: NewID("pad"), Title: "Plan", Content: "v1", Version: 1, CreatedAt: now, UpdatedAt: now}
	_ = s.CreatePad(p)
	updated, err := s.UpdatePad(p.ID, "Plan 2", "v2", 1)
	if err != nil || updated.Version != 2 {
		t.Fatalf("update=%+v %v", updated, err)
	}
	if _, err := s.UpdatePad(p.ID, "", "stale", 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	link := model.ShareLink{ID: NewID("shr"), Token: NewToken(), ObjectType: "pad", ObjectID: p.ID, Name: "Plan", MaxAccessCount: 1, CreatedAt: now}
	_ = s.CreateShare(link)
	if _, err := s.GetShareByToken(link.Token); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ConsumeShare(link.Token); err != nil || got.AccessCount != 1 {
		t.Fatalf("consume=%+v %v", got, err)
	}
	if _, err := s.GetShareByToken(link.Token); !errors.Is(err, ErrShareExhausted) {
		t.Fatalf("not exhausted: %v", err)
	}
	audits := s.ListAudits(1000)
	if len(audits) == 0 {
		t.Fatal("no audits")
	}
	// ListAudits is newest-first; each newer event points to the next older hash.
	for i, e := range audits {
		if e.Hash == "" {
			t.Fatalf("missing hash at %d", i)
		}
		if i+1 < len(audits) && e.PrevHash != audits[i+1].Hash {
			t.Fatalf("broken chain at %d", i)
		}
	}
}

func TestCleanupOnlyPersistsChangesAndRecoveryBackup(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if moved, err := s.CleanupExpired(0); err != nil || moved != 0 {
		t.Fatalf("empty cleanup=%d %v", moved, err)
	}
	expired := time.Now().Add(-time.Minute)
	now := time.Now()
	_ = s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "expired", ExpiresAt: &expired, CreatedAt: now, UpdatedAt: now})
	if moved, err := s.CleanupExpired(0); err != nil || moved != 1 {
		t.Fatalf("cleanup=%d %v", moved, err)
	}
	old := time.Now().Add(-48 * time.Hour)
	s.mu.Lock()
	s.state.Notes[0].DeletedAt = &old
	candidate := s.state
	s.mu.Unlock()
	if err := s.persistCandidate(candidate); err != nil {
		t.Fatal(err)
	}
	if _, count, err := s.PurgeExpiredTrash(1); err != nil || count != 1 {
		t.Fatalf("purge=%d %v", count, err)
	}
	// Generate a backup generation, corrupt primary, and ensure New recovers.
	_ = s.AddAudit("test", "system", "", "generation", nil)
	if err := os.WriteFile(filepath.Join(dir, "omnishare.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err != nil {
		t.Fatalf("backup recovery failed: %v", err)
	}
}
