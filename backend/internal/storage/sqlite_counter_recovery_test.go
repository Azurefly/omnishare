package storage

import (
	"encoding/json"
	"testing"
	"time"

	"omnishare/internal/model"
)

func counterRecoveryStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLite(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Keep these regressions independent of the background flush timer.
	close(store.statsStop)
	<-store.statsDone
	store.statsStop = nil
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	if err := store.CreateNote(model.QuickNote{ID: "n1", Content: "hello", ContentType: "text", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateFile(model.FileAsset{ID: "f1", FileName: "a.txt", StoragePath: "uploads/a.txt", FileSize: 5, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return store
}

func bufferRecoveryCounters(t *testing.T, store *SQLiteStore) {
	t.Helper()
	if _, err := store.ReadNote("n1"); err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementDownload("f1"); err != nil {
		t.Fatal(err)
	}
}

func requireRecoveryCounters(t *testing.T, store *SQLiteStore, want int) {
	t.Helper()
	note, err := store.GetNote("n1")
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFile("f1")
	if err != nil {
		t.Fatal(err)
	}
	if note.ReadCount != want || file.DownloadCount != want {
		t.Fatalf("counts = %d/%d, want %d/%d", note.ReadCount, file.DownloadCount, want, want)
	}
}

func TestCounterFlushRetainsFailedBatch(t *testing.T) {
	store := counterRecoveryStore(t)
	bufferRecoveryCounters(t, store)
	if _, err := store.writeDB.Exec("CREATE TRIGGER reject_counter_flush BEFORE UPDATE OF download_count ON file_assets BEGIN SELECT RAISE(ABORT, 'counter flush blocked'); END"); err != nil {
		t.Fatal(err)
	}
	store.FlushStats()
	requireRecoveryCounters(t, store, 0)
	if _, err := store.writeDB.Exec("DROP TRIGGER reject_counter_flush"); err != nil {
		t.Fatal(err)
	}
	bufferRecoveryCounters(t, store)
	store.FlushStats()
	requireRecoveryCounters(t, store, 2)
}

func TestRestoreDiscardsPreviousBufferedCounters(t *testing.T) {
	store := counterRecoveryStore(t)
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	bufferRecoveryCounters(t, store)
	if err := store.ReplaceStateJSON(snapshot); err != nil {
		t.Fatal(err)
	}
	store.FlushStats()
	requireRecoveryCounters(t, store, 0)
}

func TestFailedRestorePreservesBufferedCounters(t *testing.T) {
	store := counterRecoveryStore(t)
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err := json.Unmarshal(snapshot, &st); err != nil {
		t.Fatal(err)
	}
	st.Notes = append(st.Notes, st.Notes[0])
	invalid, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	bufferRecoveryCounters(t, store)
	if err := store.ReplaceStateJSON(invalid); err == nil {
		t.Fatal("restore with duplicate note ID succeeded")
	}
	store.FlushStats()
	requireRecoveryCounters(t, store, 1)
}

func TestSnapshotPreservesBufferedCountersOnRollback(t *testing.T) {
	store := counterRecoveryStore(t)
	before, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	bufferRecoveryCounters(t, store)
	rollback, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var st state
	if err := json.Unmarshal(rollback, &st); err != nil {
		t.Fatal(err)
	}
	if st.Notes[0].ReadCount != 1 || st.Files[0].DownloadCount != 1 {
		t.Fatalf("snapshot counts = %d/%d, want 1/1", st.Notes[0].ReadCount, st.Files[0].DownloadCount)
	}
	// Snapshotting must not consume the batch or introduce a database write.
	requireRecoveryCounters(t, store, 0)
	// Simulate a restore that replaces storage, then rolls back after a later
	// stage fails. The rollback must restore the buffered counts exactly once.
	if err := store.ReplaceStateJSON(before); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceStateJSON(rollback); err != nil {
		t.Fatal(err)
	}
	store.FlushStats()
	requireRecoveryCounters(t, store, 1)
}
