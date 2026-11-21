package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"omnishare/internal/model"
)

// The read-only prechecks added in front of the cleanup worker must never
// suppress real work. Pinned notes, retention windows and trash purging each
// take a different path, so all of them are exercised here.
func TestCleanupWorkerPrechecksStillMoveExpiredWork(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	stale := now.Add(-800 * time.Hour)           // older than the 24 day retention window
	expired := now.Add(-time.Minute)             // TTL expiry
	purgeableDeleted := now.Add(-48 * time.Hour) // already in trash past the purge window

	if err := s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "pinned stale", Tags: []string{"keep"}, Pinned: true, CreatedAt: stale, UpdatedAt: stale}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "retention target", CreatedAt: stale, UpdatedAt: stale}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "ttl target", ExpiresAt: &expired, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFile(model.FileAsset{ID: NewID("f"), FileName: "stale.bin", FileSize: 1, MIMEType: "application/octet-stream", StoragePath: "uploads/blobs/aa/stale", FileHash: "aa", CreatedAt: stale}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFile(model.FileAsset{ID: NewID("f"), FileName: "already-purgeable.bin", FileSize: 1, MIMEType: "application/octet-stream", StoragePath: "uploads/blobs/bb/stale", FileHash: "bb", CreatedAt: stale, DeletedAt: &purgeableDeleted}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePad(model.PadDocument{ID: NewID("pad"), Title: "stale pad", Content: "x", Version: 1, CreatedAt: stale, UpdatedAt: stale}); err != nil {
		t.Fatal(err)
	}

	moved, err := s.CleanupExpired(24)
	if err != nil {
		t.Fatal(err)
	}
	// Two notes, one file and one pad are past the retention window; one more
	// note expires by TTL. The pinned note is exempt and the already deleted
	// file is not counted as moved.
	if moved != 4 {
		t.Fatalf("cleanup moved %d items, want 4", moved)
	}
	// A second call must be a no-op instead of cloning and rewriting the dataset.
	if moved, err := s.CleanupExpired(24); err != nil || moved != 0 {
		t.Fatalf("idle cleanup=%d %v", moved, err)
	}

	files, purged, err := s.PurgeExpiredTrash(1)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 || len(files) != 1 {
		t.Fatalf("purge=%d files=%d, want 1 and 1", purged, len(files))
	}
	// The pinned note survives every pass, so exactly one note must remain.
	remaining := s.ListNotes("", "")
	if len(remaining) != 1 || remaining[0].Content != "pinned stale" {
		t.Fatalf("remaining notes=%+v", remaining)
	}
	s.mu.RLock()
	live := s.state
	s.mu.RUnlock()
	// Only the file whose trash entry is older than the purge window went away;
	// the work CleanupExpired moved a moment ago is still sitting in the trash.
	active := func(deletedAt *time.Time) bool { return deletedAt == nil }
	liveNotes, liveFiles, livePads := 0, 0, 0
	for i := range live.Notes {
		if active(live.Notes[i].DeletedAt) {
			liveNotes++
		}
	}
	for i := range live.Files {
		if active(live.Files[i].DeletedAt) {
			liveFiles++
		}
	}
	for i := range live.Pads {
		if active(live.Pads[i].DeletedAt) {
			livePads++
		}
	}
	if liveNotes != 1 || liveFiles != 0 || livePads != 0 {
		t.Fatalf("active objects after cleanup: notes=%d files=%d pads=%d, want 1/0/0", liveNotes, liveFiles, livePads)
	}
	if len(live.Files) != 1 || live.Files[0].FileName != "stale.bin" {
		// The just-moved file must still be in the trash: the purge only removed
		// the one whose trash entry is older than the purge window.
		t.Fatalf("unexpected live files: %+v", live.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "omnishare.json")); err != nil {
		t.Fatalf("state file missing after cleanup: %v", err)
	}
}

// cloneState replaced a JSON round-trip. Rollback semantics depend on every
// reference reachable from state being copied, otherwise a failed mutation
// leaks changes into the live dataset (or vice versa).
func TestCloneStateIsDeep(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	expires := now.Add(time.Hour)
	deleted := now.Add(-time.Hour)

	if err := s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "original", Tags: []string{"alpha", "beta"}, ExpiresAt: &expires, DeletedAt: &deleted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAudit("test", "system", "n_1", "summary", map[string]interface{}{"kind": "value"}); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	live := s.state
	clone := cloneState(live)
	s.mu.RUnlock()

	// Creating the note and the audit both append an audit entry; inspect the
	// one this test wrote rather than assuming a position.
	index := -1
	for i := range clone.Audits {
		if _, ok := clone.Audits[i].Metadata["kind"]; ok {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("audit with metadata not found: %+v", clone.Audits)
	}

	// Mutate every reference type reachable from the clone.
	clone.Notes[0].Content = "mutated"
	clone.Notes[0].Tags[0] = "mutated"
	*clone.Notes[0].ExpiresAt = now.Add(-time.Hour)
	*clone.Notes[0].DeletedAt = now
	clone.Audits[index].Metadata["kind"] = "mutated"
	clone.Audits[index].Metadata["extra"] = "added"

	s.mu.RLock()
	after := s.state
	s.mu.RUnlock()
	if !reflect.DeepEqual(live, after) {
		t.Fatalf("mutating the clone changed the live state:\nbefore=%+v\nafter=%+v", live, after)
	}
	if live.Notes[0].Content != "original" || live.Notes[0].Tags[0] != "alpha" {
		t.Fatalf("note was mutated through the clone: %+v", live.Notes[0])
	}
	if live.Audits[index].Metadata["kind"] != "value" {
		t.Fatal("audit metadata map is shared with the clone")
	}
	if _, exists := live.Audits[index].Metadata["extra"]; exists {
		t.Fatal("audit metadata map is shared with the clone")
	}
}

// A rolled back mutation must leave the live dataset untouched. The apply
// closure receives a private copy, so any alias would silently commit work.
func TestMutateRollbackLeavesStateIntact(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.CreateNote(model.QuickNote{ID: NewID("n"), Content: "keep me", Tags: []string{"alpha"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	s.mu.RLock()
	before := s.state
	s.mu.RUnlock()

	if err := s.mutate(func(st *state) error {
		st.Notes[0].Content = "rolled back"
		st.Notes[0].Tags[0] = "rolled back"
		st.Notes[0].CreatedAt = time.Time{}
		return os.ErrClosed
	}); err == nil {
		t.Fatal("mutate reported success for a failing apply")
	}

	s.mu.RLock()
	after := s.state
	s.mu.RUnlock()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback leaked mutations:\nbefore=%+v\nafter=%+v", before, after)
	}
}

// Writers must take the SQLite write lock when the transaction starts. A
// deferred begin defers the lock until the first write, and a transaction that
// reads before it writes then has to upgrade mid-flight; SQLite answers that
// upgrade with SQLITE_BUSY immediately rather than waiting, because retrying
// could deadlock. That surfaced as a sporadic "database is locked" on slow CI
// runners while passing on a fast local disk.
func TestWritePoolTakesLockAtBegin(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lock.db")

	first, err := sql.Open("sqlite", sqliteWriteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetMaxOpenConns(1)
	if _, err := first.Exec(sqliteSchema); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	second, err := sql.Open("sqlite", sqliteWriteDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetMaxOpenConns(1)

	// Opening a write transaction without executing a statement must already
	// hold the lock, otherwise the second begin below returns immediately.
	held, err := first.Begin()
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan error, 1)
	go func() {
		tx, beginErr := second.Begin()
		if beginErr == nil {
			_ = tx.Rollback()
		}
		acquired <- beginErr
	}()

	select {
	case err := <-acquired:
		_ = held.Rollback()
		t.Fatalf("second begin returned before the first transaction ended (%v); the write pool is using deferred transactions", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := held.Rollback(); err != nil {
		t.Fatalf("rollback first transaction: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second begin failed after the lock was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second begin never acquired the write lock after release")
	}
}

// The DSN is the only place these settings live, so assert the contract
// directly rather than relying on a behavioural test to infer it.
func TestWriteDSNConfiguresContentionHandling(t *testing.T) {
	dsn := sqliteWriteDSN(filepath.Join("data", "omnishare.db"))
	query := dsn[strings.Index(dsn, "?")+1:]
	params, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse write DSN %q: %v", dsn, err)
	}
	if got := params.Get("_txlock"); got != "immediate" {
		t.Fatalf("write DSN _txlock = %q, want immediate", got)
	}
	if got, want := params.Get("_pragma"), fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS); got != want {
		t.Fatalf("write DSN _pragma = %q, want %q", got, want)
	}

	readDSN := sqliteReadDSN(filepath.Join("data", "omnishare.db"))
	readQuery := readDSN[strings.Index(readDSN, "?")+1:]
	readParams, err := url.ParseQuery(readQuery)
	if err != nil {
		t.Fatalf("parse read DSN %q: %v", readDSN, err)
	}
	if got := readParams.Get("mode"); got != "ro" {
		t.Fatalf("read DSN mode = %q, want ro", got)
	}
	if got := readParams.Get("_txlock"); got != "" {
		t.Fatalf("read DSN should keep deferred transactions, got _txlock=%q", got)
	}
}
