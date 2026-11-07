package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnishare/internal/model"
)

func writeLegacyState(t *testing.T, dir string, st state) string {
	t.Helper()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "omnishare.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Migration failure must leave the legacy JSON untouched, clean up half-created
// SQLite artifacts, and let the JSON engine keep serving.
func TestMigrationFailureRollsBackAndKeepsJSON(t *testing.T) {
	dir := t.TempDir()
	st := state{SchemaVersion: 2}
	appendAudit(&st, "note", "note", "n1", "created", nil)
	// Corrupt the hash chain so the pre-import verification fails.
	st.Audits[0].Hash = "deadbeef"
	jsonPath := writeLegacyState(t, dir, st)
	before, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewSQLite(dir); err == nil {
		t.Fatal("expected migration to fail on a broken audit chain")
	}
	after, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("legacy json disappeared after failed migration: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("failed migration modified the source json")
	}
	if _, err := os.Stat(filepath.Join(dir, "omnishare.db")); !os.IsNotExist(err) {
		t.Fatal("failed migration left a database file behind")
	}
	// The timestamped backup is still produced, because it is the first step.
	backups, _ := filepath.Glob(jsonPath + ".*.migrated.bak")
	if len(backups) != 1 {
		t.Fatalf("expected one migration backup, got %v", backups)
	}
	// The fallback engine can still read the original state.
	legacy, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.ListNotes("", "")) != 0 || len(legacy.ListAudits(10)) != 1 {
		t.Fatal("legacy engine could not read the untouched state")
	}
}

// Read paths (note read, download counter, list/get) must not commit writes.
// Burn-after-read is an intentional exception and is covered separately.
func TestReadPathsDoNotWrite(t *testing.T) {
	store, err := NewSQLite(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	note := model.QuickNote{ID: "n1", Content: "hello", ContentType: "text", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateNote(note); err != nil {
		t.Fatal(err)
	}
	asset := model.FileAsset{ID: "f1", FileName: "a.txt", StoragePath: "uploads/a.txt", FileSize: 5, CreatedAt: time.Now().UTC()}
	if err := store.CreateFile(asset); err != nil {
		t.Fatal(err)
	}

	before := store.WriteTransactions()
	for i := 0; i < 50; i++ {
		if _, err := store.ReadNote("n1"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		if err := store.IncrementDownload("f1"); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.ListNotes("", "")
	_ = store.ListFiles("", "")
	_, _ = store.GetFile("f1")
	_ = store.ListAudits(10)
	_ = store.Stats()
	if got := store.WriteTransactions(); got != before {
		t.Fatalf("read paths committed %d writes, want 0", got-before)
	}

	// Flushing folds all buffered counters into one transaction.
	store.FlushStats()
	if got := store.WriteTransactions(); got != before+1 {
		t.Fatalf("expected exactly one batched flush write, got %d", got-before)
	}
	files := store.ListFiles("", "")
	if len(files) != 1 || files[0].DownloadCount != 20 {
		t.Fatalf("batched download count not persisted: %+v", files)
	}
	notes, err := store.GetNote("n1")
	if err != nil {
		t.Fatal(err)
	}
	if notes.ReadCount != 50 {
		t.Fatalf("batched read count not persisted: %d", notes.ReadCount)
	}
}

// Retention must delete only whole anchored segments and leave the surviving
// chain verifiable.
func TestAuditRetentionKeepsAnchorsVerifiable(t *testing.T) {
	store, err := NewSQLite(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for i := 0; i < 600; i++ {
		if err := store.AddAudit("note", "note", "n1", "created", map[string]interface{}{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	anchors, err := store.loadAnchors()
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors) != 2 {
		t.Fatalf("expected 2 anchors for 600 events, got %d", len(anchors))
	}
	if anchors[0].EventCount != auditAnchorEvery || anchors[1].PrevAnchorHash != anchors[0].AnchorHash {
		t.Fatal("anchor chain was not linked")
	}

	removed, err := store.PruneAudits(90, 50)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2*auditAnchorEvery {
		t.Fatalf("expected %d pruned events (two full anchored segments), got %d", 2*auditAnchorEvery, removed)
	}
	if got := len(store.ListAudits(1000)); got != 600-removed {
		t.Fatalf("retained events = %d, want %d", got, 600-removed)
	}
	verified, err := store.VerifyAuditChain()
	if err != nil {
		t.Fatalf("chain no longer verifiable after pruning: %v", err)
	}
	if verified != 600-removed {
		t.Fatalf("verified events = %d, want %d", verified, 600-removed)
	}
	// Anchors covering the deleted range must survive.
	anchors, _ = store.loadAnchors()
	if len(anchors) != 2 {
		t.Fatalf("anchors for pruned range were deleted: %d", len(anchors))
	}
	// A second capacity-only prune with a tiny cap can only remove anchored data,
	// so the 88-event tail stays until it is anchored.
	store.PruneAudits(90, 10)
	if got := len(store.ListAudits(1000)); got != 88 {
		t.Fatalf("tail below one anchor segment must be kept, got %d", got)
	}
}
