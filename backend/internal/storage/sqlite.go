package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"omnishare/internal/model"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements the SQLite WAL storage engine with dual connection pools.
type SQLiteStore struct {
	dataDir string
	dbPath  string
	writeDB *sql.DB
	readDB  *sql.DB
	writeMu sync.Mutex // Serializes write operations in the application layer
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS quick_notes (
    id TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    content_redacted INTEGER NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT 'text',
    tags TEXT NOT NULL DEFAULT '[]',
    pinned INTEGER NOT NULL DEFAULT 0,
    is_burn_after_read INTEGER NOT NULL DEFAULT 0,
    read_count INTEGER NOT NULL DEFAULT 0,
    max_read_count INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT,
    deleted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notes_pinned_updated ON quick_notes(pinned, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_notes_deleted_expires ON quick_notes(deleted_at, expires_at);

CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,
    file_name TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    mime_type TEXT NOT NULL,
    storage_path TEXT NOT NULL DEFAULT '',
    file_hash TEXT NOT NULL DEFAULT '',
    download_count INTEGER NOT NULL DEFAULT 0,
    is_video INTEGER NOT NULL DEFAULT 0,
    download_url TEXT NOT NULL DEFAULT '',
    stream_url TEXT NOT NULL DEFAULT '',
    expires_at TEXT,
    deleted_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_hash ON file_assets(file_hash);
CREATE INDEX IF NOT EXISTS idx_files_deleted_created ON file_assets(deleted_at, created_at DESC);

CREATE TABLE IF NOT EXISTS pad_documents (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pads_deleted_updated ON pad_documents(deleted_at, updated_at DESC);

CREATE TABLE IF NOT EXISTS share_links (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    access_count INTEGER NOT NULL DEFAULT 0,
    max_access_count INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT,
    revoked_at TEXT,
    last_accessed_at TEXT,
    created_at TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active'
);
CREATE INDEX IF NOT EXISTS idx_shares_token ON share_links(token);
CREATE INDEX IF NOT EXISTS idx_shares_target ON share_links(object_type, object_id);

CREATE TABLE IF NOT EXISTS audit_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    action TEXT NOT NULL,
    object TEXT NOT NULL,
    object_id TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '{}',
    prev_hash TEXT NOT NULL DEFAULT '',
    hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audits_created ON audit_events(created_at ASC);
`

// NewSQLite creates and initializes a SQLiteStore with WAL mode, dual connection pools, and migration.
func NewSQLite(dataDir string) (*SQLiteStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	for _, sub := range []string{"uploads/blobs", "uploads/.staging", "uploads/.purging"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o700); err != nil {
			return nil, err
		}
	}

	dbPath := filepath.Join(dataDir, "omnishare.db")
	jsonPath := filepath.Join(dataDir, "omnishare.json")

	// Determine if initial migration is required
	needsMigration := false
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		if _, jsonErr := os.Stat(jsonPath); jsonErr == nil {
			needsMigration = true
		}
	}

	// 1. Setup Write Connection Pool
	// DSN parameters: _journal_mode=WAL, _busy_timeout=5000, _foreign_keys=ON, etc.
	writeParams := url.Values{}
	writeParams.Set("_pragma", "busy_timeout(5000)")
	writeDSN := fmt.Sprintf("file:%s?%s", dbPath, writeParams.Encode())

	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)
	writeDB.SetConnMaxLifetime(0)

	// Configure PRAGMAs on write pool
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA wal_autocheckpoint=1000;",
	}
	for _, p := range pragmas {
		if _, err := writeDB.Exec(p); err != nil {
			writeDB.Close()
			return nil, fmt.Errorf("exec pragma %s: %w", p, err)
		}
	}

	// Initialize tables in a transaction
	tx, err := writeDB.Begin()
	if err != nil {
		writeDB.Close()
		return nil, err
	}
	if _, err := tx.Exec(sqliteSchema); err != nil {
		_ = tx.Rollback()
		writeDB.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		writeDB.Close()
		return nil, err
	}

	store := &SQLiteStore{
		dataDir: dataDir,
		dbPath:  dbPath,
		writeDB: writeDB,
	}

	// 2. Perform Migration if omnishare.json exists and DB was newly created
	if needsMigration {
		if err := store.migrateFromJSON(jsonPath); err != nil {
			writeDB.Close()
			_ = CleanDirtyDBArtifacts(dbPath)
			return nil, fmt.Errorf("migration from json failed: %w", err)
		}
	}

	// 3. Setup Read Connection Pool (read-only with mode=ro)
	readParams := url.Values{}
	readParams.Set("mode", "ro")
	readParams.Set("_pragma", "busy_timeout(5000)")
	readDSN := fmt.Sprintf("file:%s?%s", dbPath, readParams.Encode())

	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open sqlite read db: %w", err)
	}
	readDB.SetMaxOpenConns(16)
	readDB.SetMaxIdleConns(8)
	readDB.SetConnMaxLifetime(time.Hour)

	store.readDB = readDB
	return store, nil
}

func (s *SQLiteStore) Close() error {
	var errs []error
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (s *SQLiteStore) DataDir() string {
	return s.dataDir
}

// CleanDirtyDBArtifacts safely removes the target SQLite database file along with WAL and SHM files.
func CleanDirtyDBArtifacts(basePath string) error {
	for _, ext := range []string{"", "-wal", "-shm"} {
		path := basePath + ext
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// VerifyAuditChain traverses the audit_events table in strict physical sequence order (seq ASC)
// and mathematically validates that the Merkle prev_hash chain and SHA-256 hashes are 100% unbroken.
func (s *SQLiteStore) VerifyAuditChain() (int, error) {
	rows, err := s.readDB.Query(`SELECT id, action, object, object_id, summary, metadata, prev_hash, hash, created_at FROM audit_events ORDER BY seq ASC`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var verifyPrev string
	var count int
	for rows.Next() {
		var a model.AuditEvent
		var metaStr, createdStr string
		if err := rows.Scan(&a.ID, &a.Action, &a.Object, &a.ObjectID, &a.Summary, &metaStr, &a.PrevHash, &a.Hash, &createdStr); err != nil {
			return count, err
		}
		_ = json.Unmarshal([]byte(metaStr), &a.Metadata)
		a.CreatedAt = mustParseTime(createdStr)
		if a.PrevHash != verifyPrev {
			return count, fmt.Errorf("audit chain broken at seq %d (id %s): prev_hash %q != expected %q", count+1, a.ID, a.PrevHash, verifyPrev)
		}
		expectedHash := auditHash(a)
		if a.Hash != expectedHash {
			return count, fmt.Errorf("audit hash mismatch at seq %d (id %s): hash %q != expected %q", count+1, a.ID, a.Hash, expectedHash)
		}
		verifyPrev = a.Hash
		count++
	}
	return count, rows.Err()
}

// Checkpoint manually executes a WAL checkpoint (PASSIVE, FULL, RESTART, or TRUNCATE).
func (s *SQLiteStore) Checkpoint() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.Exec("PRAGMA wal_checkpoint(TRUNCATE);")
	return err
}

// IntegrityCheck runs SQLite PRAGMA integrity_check.
func (s *SQLiteStore) IntegrityCheck() (string, error) {
	var res string
	err := s.readDB.QueryRow("PRAGMA integrity_check;").Scan(&res)
	return res, err
}

// --- Helpers for scanning and serialization ---

func timeToString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func parseTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, *s)
		if err != nil {
			return nil
		}
	}
	t = t.UTC()
	return &t
}

func mustParseTime(s string) time.Time {
	t := parseTime(&s)
	if t == nil {
		return time.Now().UTC()
	}
	return *t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(i int) bool {
	return i != 0
}

// --- Migration & State Serialization (AC 2.1 & 2.2) ---

func (s *SQLiteStore) migrateFromJSON(jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("unmarshal source json: %w", err)
	}

	// Verify existing audit hash chain before importing
	prev := ""
	for _, a := range st.Audits {
		if a.PrevHash != prev {
			return fmt.Errorf("source audit chain broken at id %s", a.ID)
		}
		computed := auditHash(a)
		if a.Hash != computed {
			return fmt.Errorf("source audit hash mismatch at id %s", a.ID)
		}
		prev = a.Hash
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec("INSERT OR REPLACE INTO schema_meta(key, value, updated_at) VALUES ('schema_version', '2', ?)", nowStr); err != nil {
		return err
	}

	// Insert Notes
	noteStmt, err := tx.Prepare(`INSERT INTO quick_notes(
		id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer noteStmt.Close()

	for _, n := range st.Notes {
		tagsJSON, _ := json.Marshal(n.Tags)
		if _, err := noteStmt.Exec(
			n.ID, n.Content, boolToInt(n.ContentRedacted), n.ContentType, string(tagsJSON),
			boolToInt(n.Pinned), boolToInt(n.IsBurnAfterRead), n.ReadCount, n.MaxReadCount,
			timeToString(n.ExpiresAt), timeToString(n.DeletedAt),
			n.CreatedAt.UTC().Format(time.RFC3339Nano), n.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert note %s: %w", n.ID, err)
		}
	}

	// Insert Files
	fileStmt, err := tx.Prepare(`INSERT INTO file_assets(
		id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer fileStmt.Close()

	for _, f := range st.Files {
		if _, err := fileStmt.Exec(
			f.ID, f.FileName, f.FileSize, f.MIMEType, f.StoragePath, f.FileHash,
			f.DownloadCount, boolToInt(f.IsVideo), f.DownloadURL, f.StreamURL,
			timeToString(f.ExpiresAt), timeToString(f.DeletedAt),
			f.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert file %s: %w", f.ID, err)
		}
	}

	// Insert Pads
	padStmt, err := tx.Prepare(`INSERT INTO pad_documents(
		id, title, content, version, deleted_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer padStmt.Close()

	for _, p := range st.Pads {
		if _, err := padStmt.Exec(
			p.ID, p.Title, p.Content, p.Version, timeToString(p.DeletedAt),
			p.CreatedAt.UTC().Format(time.RFC3339Nano), p.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert pad %s: %w", p.ID, err)
		}
	}

	// Insert Shares
	shareStmt, err := tx.Prepare(`INSERT INTO share_links(
		id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer shareStmt.Close()

	for _, sLink := range st.Shares {
		status := sLink.Status
		if status == "" {
			status = "active"
		}
		if _, err := shareStmt.Exec(
			sLink.ID, sLink.Token, sLink.ObjectType, sLink.ObjectID, sLink.Name, sLink.URL,
			sLink.AccessCount, sLink.MaxAccessCount, timeToString(sLink.ExpiresAt),
			timeToString(sLink.RevokedAt), timeToString(sLink.LastAccessedAt),
			sLink.CreatedAt.UTC().Format(time.RFC3339Nano), status,
		); err != nil {
			return fmt.Errorf("insert share %s: %w", sLink.ID, err)
		}
	}

	// Insert Audits sequentially to maintain seq & hash chain
	auditStmt, err := tx.Prepare(`INSERT INTO audit_events(
		id, action, object, object_id, summary, metadata, prev_hash, hash, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer auditStmt.Close()

	for _, a := range st.Audits {
		metaJSON, _ := json.Marshal(a.Metadata)
		if _, err := auditStmt.Exec(
			a.ID, a.Action, a.Object, a.ObjectID, a.Summary, string(metaJSON),
			a.PrevHash, a.Hash, a.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert audit %s: %w", a.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil

	// Verify imported audit chain
	rows, err := s.writeDB.Query("SELECT id, action, object, object_id, summary, metadata, prev_hash, hash, created_at FROM audit_events ORDER BY seq ASC")
	if err != nil {
		return err
	}
	defer rows.Close()

	var verifyPrev string
	var count int
	for rows.Next() {
		var a model.AuditEvent
		var metaStr, createdStr string
		if err := rows.Scan(&a.ID, &a.Action, &a.Object, &a.ObjectID, &a.Summary, &metaStr, &a.PrevHash, &a.Hash, &createdStr); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(metaStr), &a.Metadata)
		a.CreatedAt = mustParseTime(createdStr)
		if a.PrevHash != verifyPrev {
			return fmt.Errorf("migrated audit chain broken at %s", a.ID)
		}
		if a.Hash != auditHash(a) {
			return fmt.Errorf("migrated audit hash mismatch at %s", a.ID)
		}
		verifyPrev = a.Hash
		count++
	}
	if count != len(st.Audits) {
		return fmt.Errorf("migrated audit count mismatch: got %d want %d", count, len(st.Audits))
	}

	// Rename omnishare.json to omnishare.json.bak
	_ = os.Rename(jsonPath, jsonPath+".bak")
	return nil
}

// DumpState exports the entire SQLite state into a Go `state` structure for backup or JSON fallback.
func (s *SQLiteStore) DumpState() (state, error) {
	var st state
	st.SchemaVersion = currentStateSchema
	st.Notes = []model.QuickNote{}
	st.Files = []model.FileAsset{}
	st.Pads = []model.PadDocument{}
	st.Shares = []model.ShareLink{}
	st.Audits = []model.AuditEvent{}

	// Query Notes
	nRows, err := s.readDB.Query(`SELECT id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at FROM quick_notes ORDER BY rowid ASC`)
	if err != nil {
		return st, err
	}
	defer nRows.Close()
	for nRows.Next() {
		var n model.QuickNote
		var redacted, pinned, burn int
		var tagsStr, createdStr, updatedStr string
		var expStr, delStr *string
		if err := nRows.Scan(&n.ID, &n.Content, &redacted, &n.ContentType, &tagsStr, &pinned, &burn,
			&n.ReadCount, &n.MaxReadCount, &expStr, &delStr, &createdStr, &updatedStr); err != nil {
			return st, err
		}
		n.ContentRedacted = intToBool(redacted)
		n.Pinned = intToBool(pinned)
		n.IsBurnAfterRead = intToBool(burn)
		_ = json.Unmarshal([]byte(tagsStr), &n.Tags)
		n.ExpiresAt = parseTime(expStr)
		n.DeletedAt = parseTime(delStr)
		n.CreatedAt = mustParseTime(createdStr)
		n.UpdatedAt = mustParseTime(updatedStr)
		st.Notes = append(st.Notes, n)
	}

	// Query Files
	fRows, err := s.readDB.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at FROM file_assets ORDER BY rowid ASC`)
	if err != nil {
		return st, err
	}
	defer fRows.Close()
	for fRows.Next() {
		var f model.FileAsset
		var isVideo int
		var expStr, delStr *string
		var createdStr string
		if err := fRows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err != nil {
			return st, err
		}
		f.IsVideo = intToBool(isVideo)
		f.ExpiresAt = parseTime(expStr)
		f.DeletedAt = parseTime(delStr)
		f.CreatedAt = mustParseTime(createdStr)
		st.Files = append(st.Files, f)
	}

	// Query Pads
	pRows, err := s.readDB.Query(`SELECT id, title, content, version, deleted_at, created_at, updated_at FROM pad_documents ORDER BY rowid ASC`)
	if err != nil {
		return st, err
	}
	defer pRows.Close()
	for pRows.Next() {
		var p model.PadDocument
		var delStr *string
		var createdStr, updatedStr string
		if err := pRows.Scan(&p.ID, &p.Title, &p.Content, &p.Version, &delStr, &createdStr, &updatedStr); err != nil {
			return st, err
		}
		p.DeletedAt = parseTime(delStr)
		p.CreatedAt = mustParseTime(createdStr)
		p.UpdatedAt = mustParseTime(updatedStr)
		st.Pads = append(st.Pads, p)
	}

	// Query Shares
	sRows, err := s.readDB.Query(`SELECT id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status FROM share_links ORDER BY rowid ASC`)
	if err != nil {
		return st, err
	}
	defer sRows.Close()
	for sRows.Next() {
		var sLink model.ShareLink
		var expStr, revStr, lastStr *string
		var createdStr string
		if err := sRows.Scan(&sLink.ID, &sLink.Token, &sLink.ObjectType, &sLink.ObjectID, &sLink.Name, &sLink.URL,
			&sLink.AccessCount, &sLink.MaxAccessCount, &expStr, &revStr, &lastStr, &createdStr, &sLink.Status); err != nil {
			return st, err
		}
		sLink.ExpiresAt = parseTime(expStr)
		sLink.RevokedAt = parseTime(revStr)
		sLink.LastAccessedAt = parseTime(lastStr)
		sLink.CreatedAt = mustParseTime(createdStr)
		st.Shares = append(st.Shares, sLink)
	}

	// Query Audits
	aRows, err := s.readDB.Query(`SELECT id, action, object, object_id, summary, metadata, prev_hash, hash, created_at FROM audit_events ORDER BY seq ASC`)
	if err != nil {
		return st, err
	}
	defer aRows.Close()
	for aRows.Next() {
		var a model.AuditEvent
		var metaStr, createdStr string
		if err := aRows.Scan(&a.ID, &a.Action, &a.Object, &a.ObjectID, &a.Summary, &metaStr, &a.PrevHash, &a.Hash, &createdStr); err != nil {
			return st, err
		}
		_ = json.Unmarshal([]byte(metaStr), &a.Metadata)
		a.CreatedAt = mustParseTime(createdStr)
		st.Audits = append(st.Audits, a)
	}

	return st, nil
}

// Snapshot returns the JSON representation of the database state.
func (s *SQLiteStore) Snapshot() ([]byte, error) {
	st, err := s.DumpState()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(st, "", "  ")
}

// WithSnapshot holds a read lock (conceptually via consistent DumpState) for backup creation.
func (s *SQLiteStore) WithSnapshot(fn func(stateJSON []byte, files []model.FileAsset) error) error {
	st, err := s.DumpState()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fn(data, st.Files)
}

// ReplaceStateJSON replaces all data in SQLite with the given JSON state atomically.
func (s *SQLiteStore) ReplaceStateJSON(data []byte) error {
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Notes == nil || st.Files == nil || st.Pads == nil || st.Shares == nil || st.Audits == nil {
		return errors.New("backup state is incomplete")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	tables := []string{"quick_notes", "file_assets", "pad_documents", "share_links", "audit_events"}
	for _, t := range tables {
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}

	// Insert Notes
	for _, n := range st.Notes {
		tagsJSON, _ := json.Marshal(n.Tags)
		if _, err := tx.Exec(`INSERT INTO quick_notes(
			id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
			read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, n.Content, boolToInt(n.ContentRedacted), n.ContentType, string(tagsJSON),
			boolToInt(n.Pinned), boolToInt(n.IsBurnAfterRead), n.ReadCount, n.MaxReadCount,
			timeToString(n.ExpiresAt), timeToString(n.DeletedAt),
			n.CreatedAt.UTC().Format(time.RFC3339Nano), n.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}

	// Insert Files
	for _, f := range st.Files {
		if _, err := tx.Exec(`INSERT INTO file_assets(
			id, file_name, file_size, mime_type, storage_path, file_hash,
			download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.FileName, f.FileSize, f.MIMEType, f.StoragePath, f.FileHash,
			f.DownloadCount, boolToInt(f.IsVideo), f.DownloadURL, f.StreamURL,
			timeToString(f.ExpiresAt), timeToString(f.DeletedAt),
			f.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}

	// Insert Pads
	for _, p := range st.Pads {
		if _, err := tx.Exec(`INSERT INTO pad_documents(
			id, title, content, version, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Title, p.Content, p.Version, timeToString(p.DeletedAt),
			p.CreatedAt.UTC().Format(time.RFC3339Nano), p.UpdatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}

	// Insert Shares
	for _, sLink := range st.Shares {
		status := sLink.Status
		if status == "" {
			status = "active"
		}
		if _, err := tx.Exec(`INSERT INTO share_links(
			id, token, object_type, object_id, name, url,
			access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sLink.ID, sLink.Token, sLink.ObjectType, sLink.ObjectID, sLink.Name, sLink.URL,
			sLink.AccessCount, sLink.MaxAccessCount, timeToString(sLink.ExpiresAt),
			timeToString(sLink.RevokedAt), timeToString(sLink.LastAccessedAt),
			sLink.CreatedAt.UTC().Format(time.RFC3339Nano), status,
		); err != nil {
			return err
		}
	}

	// Rebuild and insert audits
	prev := ""
	for _, a := range st.Audits {
		a.PrevHash = prev
		a.Hash = auditHash(a)
		prev = a.Hash
		metaJSON, _ := json.Marshal(a.Metadata)
		if _, err := tx.Exec(`INSERT INTO audit_events(
			id, action, object, object_id, summary, metadata, prev_hash, hash, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Action, a.Object, a.ObjectID, a.Summary, string(metaJSON),
			a.PrevHash, a.Hash, a.CreatedAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

// --- Audit append helper within write transactions ---

func (s *SQLiteStore) appendAuditTx(tx *sql.Tx, action, object, objectID, summary string, metadata map[string]interface{}) error {
	var prevHash string
	row := tx.QueryRow("SELECT hash FROM audit_events ORDER BY seq DESC LIMIT 1")
	if err := row.Scan(&prevHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e := model.AuditEvent{
		ID:        newID("evt"),
		Action:    action,
		Object:    object,
		ObjectID:  objectID,
		Summary:   summary,
		Metadata:  metadata,
		PrevHash:  prevHash,
		CreatedAt: time.Now().UTC(),
	}
	e.Hash = auditHash(e)
	metaJSON, _ := json.Marshal(e.Metadata)
	_, err := tx.Exec(`INSERT INTO audit_events(id, action, object, object_id, summary, metadata, prev_hash, hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Action, e.Object, e.ObjectID, e.Summary, string(metaJSON), e.PrevHash, e.Hash, e.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) AddAudit(action, object, objectID, summary string, metadata map[string]interface{}) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()
	if err := s.appendAuditTx(tx, action, object, objectID, summary, metadata); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) ListAudits(limit int) []model.AuditEvent {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.readDB.Query(`SELECT id, action, object, object_id, summary, metadata, prev_hash, hash, created_at
		FROM audit_events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return []model.AuditEvent{}
	}
	defer rows.Close()

	out := make([]model.AuditEvent, 0, limit)
	for rows.Next() {
		var a model.AuditEvent
		var metaStr, createdStr string
		if err := rows.Scan(&a.ID, &a.Action, &a.Object, &a.ObjectID, &a.Summary, &metaStr, &a.PrevHash, &a.Hash, &createdStr); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(metaStr), &a.Metadata)
		a.CreatedAt = mustParseTime(createdStr)
		out = append(out, a)
	}
	return out
}

// --- Quick Notes API ---

func (s *SQLiteStore) ListNotes(query, tag string) []model.QuickNote {
	query = strings.ToLower(strings.TrimSpace(query))
	tag = strings.ToLower(strings.TrimSpace(tag))
	now := time.Now().UTC()

	rows, err := s.readDB.Query(`SELECT id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
		FROM quick_notes WHERE deleted_at IS NULL ORDER BY pinned DESC, updated_at DESC`)
	if err != nil {
		return []model.QuickNote{}
	}
	defer rows.Close()

	out := make([]model.QuickNote, 0)
	for rows.Next() {
		var n model.QuickNote
		var redacted, pinned, burn int
		var tagsStr, createdStr, updatedStr string
		var expStr, delStr *string
		if err := rows.Scan(&n.ID, &n.Content, &redacted, &n.ContentType, &tagsStr, &pinned, &burn,
			&n.ReadCount, &n.MaxReadCount, &expStr, &delStr, &createdStr, &updatedStr); err != nil {
			continue
		}
		n.ExpiresAt = parseTime(expStr)
		if expired(n.ExpiresAt, now) {
			continue
		}
		n.ContentRedacted = intToBool(redacted)
		n.Pinned = intToBool(pinned)
		n.IsBurnAfterRead = intToBool(burn)
		_ = json.Unmarshal([]byte(tagsStr), &n.Tags)
		n.CreatedAt = mustParseTime(createdStr)
		n.UpdatedAt = mustParseTime(updatedStr)

		if query != "" && !strings.Contains(strings.ToLower(n.Content), query) && !containsTag(n.Tags, query) {
			continue
		}
		if tag != "" && !containsTag(n.Tags, tag) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func (s *SQLiteStore) GetNote(id string) (model.QuickNote, error) {
	now := time.Now().UTC()
	var n model.QuickNote
	var redacted, pinned, burn int
	var tagsStr, createdStr, updatedStr string
	var expStr, delStr *string

	err := s.readDB.QueryRow(`SELECT id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
		FROM quick_notes WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&n.ID, &n.Content, &redacted, &n.ContentType, &tagsStr, &pinned, &burn,
		&n.ReadCount, &n.MaxReadCount, &expStr, &delStr, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.QuickNote{}, ErrNotFound
		}
		return model.QuickNote{}, err
	}
	n.ExpiresAt = parseTime(expStr)
	if expired(n.ExpiresAt, now) {
		return model.QuickNote{}, ErrNotFound
	}
	n.ContentRedacted = intToBool(redacted)
	n.Pinned = intToBool(pinned)
	n.IsBurnAfterRead = intToBool(burn)
	_ = json.Unmarshal([]byte(tagsStr), &n.Tags)
	n.CreatedAt = mustParseTime(createdStr)
	n.UpdatedAt = mustParseTime(updatedStr)
	return n, nil
}

func (s *SQLiteStore) CreateNote(n model.QuickNote) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	tagsJSON, _ := json.Marshal(n.Tags)
	_, err = tx.Exec(`INSERT INTO quick_notes(
		id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Content, boolToInt(n.ContentRedacted), n.ContentType, string(tagsJSON),
		boolToInt(n.Pinned), boolToInt(n.IsBurnAfterRead), n.ReadCount, n.MaxReadCount,
		timeToString(n.ExpiresAt), timeToString(n.DeletedAt),
		n.CreatedAt.UTC().Format(time.RFC3339Nano), n.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}

	if err := s.appendAuditTx(tx, "create", "note", n.ID, "note created", nil); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) UpdateNote(id string, apply func(*model.QuickNote) error) (result model.QuickNote, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return model.QuickNote{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	var n model.QuickNote
	var redacted, pinned, burn int
	var tagsStr, createdStr, updatedStr string
	var expStr, delStr *string

	err = tx.QueryRow(`SELECT id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
		FROM quick_notes WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&n.ID, &n.Content, &redacted, &n.ContentType, &tagsStr, &pinned, &burn,
		&n.ReadCount, &n.MaxReadCount, &expStr, &delStr, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.QuickNote{}, ErrNotFound
		}
		return model.QuickNote{}, err
	}
	n.ExpiresAt = parseTime(expStr)
	if expired(n.ExpiresAt, now) {
		return model.QuickNote{}, ErrNotFound
	}
	n.ContentRedacted = intToBool(redacted)
	n.Pinned = intToBool(pinned)
	n.IsBurnAfterRead = intToBool(burn)
	_ = json.Unmarshal([]byte(tagsStr), &n.Tags)
	n.CreatedAt = mustParseTime(createdStr)
	n.UpdatedAt = mustParseTime(updatedStr)

	if err := apply(&n); err != nil {
		return model.QuickNote{}, err
	}
	n.UpdatedAt = now

	tagsJSON, _ := json.Marshal(n.Tags)
	_, err = tx.Exec(`UPDATE quick_notes SET content = ?, content_redacted = ?, content_type = ?, tags = ?,
		pinned = ?, is_burn_after_read = ?, read_count = ?, max_read_count = ?, expires_at = ?, updated_at = ?
		WHERE id = ?`,
		n.Content, boolToInt(n.ContentRedacted), n.ContentType, string(tagsJSON),
		boolToInt(n.Pinned), boolToInt(n.IsBurnAfterRead), n.ReadCount, n.MaxReadCount,
		timeToString(n.ExpiresAt), n.UpdatedAt.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return model.QuickNote{}, err
	}

	if err := s.appendAuditTx(tx, "update", "note", id, "note updated", nil); err != nil {
		return model.QuickNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.QuickNote{}, err
	}
	tx = nil
	return n, nil
}

func (s *SQLiteStore) ReadNote(id string) (result model.QuickNote, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return model.QuickNote{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	var n model.QuickNote
	var redacted, pinned, burn int
	var tagsStr, createdStr, updatedStr string
	var expStr, delStr *string

	err = tx.QueryRow(`SELECT id, content, content_redacted, content_type, tags, pinned, is_burn_after_read,
		read_count, max_read_count, expires_at, deleted_at, created_at, updated_at
		FROM quick_notes WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&n.ID, &n.Content, &redacted, &n.ContentType, &tagsStr, &pinned, &burn,
		&n.ReadCount, &n.MaxReadCount, &expStr, &delStr, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.QuickNote{}, ErrNotFound
		}
		return model.QuickNote{}, err
	}
	n.ExpiresAt = parseTime(expStr)
	if expired(n.ExpiresAt, now) {
		return model.QuickNote{}, ErrNotFound
	}
	n.ContentRedacted = intToBool(redacted)
	n.Pinned = intToBool(pinned)
	n.IsBurnAfterRead = intToBool(burn)
	_ = json.Unmarshal([]byte(tagsStr), &n.Tags)
	n.CreatedAt = mustParseTime(createdStr)
	n.UpdatedAt = mustParseTime(updatedStr)

	n.ReadCount++
	remove := n.IsBurnAfterRead || (n.MaxReadCount > 0 && n.ReadCount >= n.MaxReadCount)

	if remove {
		if _, err := tx.Exec("DELETE FROM quick_notes WHERE id = ?", id); err != nil {
			return model.QuickNote{}, err
		}
		nowStr := now.Format(time.RFC3339Nano)
		if _, err := tx.Exec("UPDATE share_links SET revoked_at = ? WHERE object_type = 'note' AND object_id = ? AND revoked_at IS NULL", nowStr, id); err != nil {
			return model.QuickNote{}, err
		}
	} else {
		if _, err := tx.Exec("UPDATE quick_notes SET read_count = ? WHERE id = ?", n.ReadCount, id); err != nil {
			return model.QuickNote{}, err
		}
	}

	if err := s.appendAuditTx(tx, "read", "note", id, "note read", map[string]interface{}{"removed": remove}); err != nil {
		return model.QuickNote{}, err
	}

	if err := tx.Commit(); err != nil {
		return model.QuickNote{}, err
	}
	tx = nil
	return n, nil
}

func (s *SQLiteStore) DeleteNotes(ids []string) (deleted int, err error) {
	if len(ids) == 0 {
		return 0, ErrNotFound
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	for _, id := range ids {
		res, err := tx.Exec("UPDATE quick_notes SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL", nowStr, id)
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			deleted++
			if err := s.appendAuditTx(tx, "trash", "note", id, "note moved to trash", nil); err != nil {
				return 0, err
			}
		}
	}
	if deleted == 0 {
		return 0, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	tx = nil
	return deleted, nil
}

// --- Files API ---

func (s *SQLiteStore) ListFiles(query, kind string) []model.FileAsset {
	query = strings.ToLower(strings.TrimSpace(query))
	kind = strings.ToLower(strings.TrimSpace(kind))
	now := time.Now().UTC()

	rows, err := s.readDB.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE deleted_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return []model.FileAsset{}
	}
	defer rows.Close()

	out := make([]model.FileAsset, 0)
	for rows.Next() {
		var f model.FileAsset
		var isVideo int
		var expStr, delStr *string
		var createdStr string
		if err := rows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err != nil {
			continue
		}
		f.ExpiresAt = parseTime(expStr)
		if expired(f.ExpiresAt, now) {
			continue
		}
		f.IsVideo = intToBool(isVideo)
		f.CreatedAt = mustParseTime(createdStr)

		if query != "" && !strings.Contains(strings.ToLower(f.FileName), query) && !strings.Contains(strings.ToLower(f.MIMEType), query) {
			continue
		}
		if kind == "video" && !f.IsVideo {
			continue
		}
		if kind == "image" && !strings.HasPrefix(f.MIMEType, "image/") {
			continue
		}
		if kind == "document" && (f.IsVideo || strings.HasPrefix(f.MIMEType, "image/")) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (s *SQLiteStore) AllFiles() []model.FileAsset {
	rows, err := s.readDB.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets ORDER BY rowid ASC`)
	if err != nil {
		return []model.FileAsset{}
	}
	defer rows.Close()

	out := make([]model.FileAsset, 0)
	for rows.Next() {
		var f model.FileAsset
		var isVideo int
		var expStr, delStr *string
		var createdStr string
		if err := rows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err != nil {
			continue
		}
		f.IsVideo = intToBool(isVideo)
		f.ExpiresAt = parseTime(expStr)
		f.DeletedAt = parseTime(delStr)
		f.CreatedAt = mustParseTime(createdStr)
		out = append(out, f)
	}
	return out
}

func (s *SQLiteStore) GetFile(id string) (model.FileAsset, error) {
	now := time.Now().UTC()
	var f model.FileAsset
	var isVideo int
	var expStr, delStr *string
	var createdStr string

	err := s.readDB.QueryRow(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
		&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.FileAsset{}, ErrNotFound
		}
		return model.FileAsset{}, err
	}
	f.ExpiresAt = parseTime(expStr)
	if expired(f.ExpiresAt, now) {
		return model.FileAsset{}, ErrNotFound
	}
	f.IsVideo = intToBool(isVideo)
	f.CreatedAt = mustParseTime(createdStr)
	return f, nil
}

func (s *SQLiteStore) GetTrashedFile(id string) (model.FileAsset, error) {
	var f model.FileAsset
	var isVideo int
	var expStr, delStr *string
	var createdStr string

	err := s.readDB.QueryRow(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(
		&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
		&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.FileAsset{}, ErrNotFound
		}
		return model.FileAsset{}, err
	}
	f.IsVideo = intToBool(isVideo)
	f.ExpiresAt = parseTime(expStr)
	f.DeletedAt = parseTime(delStr)
	f.CreatedAt = mustParseTime(createdStr)
	return f, nil
}

func (s *SQLiteStore) FindFileByHash(hash string, size int64) (model.FileAsset, error) {
	now := time.Now().UTC()
	rows, err := s.readDB.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE file_hash = ? AND file_size = ? AND deleted_at IS NULL`, hash, size)
	if err != nil {
		return model.FileAsset{}, ErrNotFound
	}
	defer rows.Close()

	candidates := make([]model.FileAsset, 0)
	for rows.Next() {
		var f model.FileAsset
		var isVideo int
		var expStr, delStr *string
		var createdStr string
		if err := rows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err != nil {
			continue
		}
		f.ExpiresAt = parseTime(expStr)
		if expired(f.ExpiresAt, now) {
			continue
		}
		f.IsVideo = intToBool(isVideo)
		f.CreatedAt = mustParseTime(createdStr)
		candidates = append(candidates, f)
	}

	for _, f := range candidates {
		path := filepath.Join(s.dataDir, f.StoragePath)
		if info, err := os.Stat(path); err != nil || info.Size() != size {
			continue
		}
		actual, err := hashFile(path)
		if err == nil && subtleEqualHex(actual, hash) {
			return f, nil
		}
	}
	return model.FileAsset{}, ErrNotFound
}

func (s *SQLiteStore) CreateFile(f model.FileAsset) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`INSERT INTO file_assets(
		id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.FileName, f.FileSize, f.MIMEType, f.StoragePath, f.FileHash,
		f.DownloadCount, boolToInt(f.IsVideo), f.DownloadURL, f.StreamURL,
		timeToString(f.ExpiresAt), timeToString(f.DeletedAt),
		f.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}

	if err := s.appendAuditTx(tx, "upload", "file", f.ID, "file uploaded", map[string]interface{}{"name": f.FileName, "size": f.FileSize, "hash": f.FileHash}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) RenameFile(id, name string) (result model.FileAsset, err error) {
	name = sanitizeFilename(name)
	if name == "" {
		return model.FileAsset{}, errors.New("file name is required")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return model.FileAsset{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.Exec("UPDATE file_assets SET file_name = ? WHERE id = ? AND deleted_at IS NULL", name, id)
	if err != nil {
		return model.FileAsset{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.FileAsset{}, ErrNotFound
	}

	if err := s.appendAuditTx(tx, "rename", "file", id, "file renamed", map[string]interface{}{"name": name}); err != nil {
		return model.FileAsset{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.FileAsset{}, err
	}
	tx = nil

	return s.GetFile(id)
}

func (s *SQLiteStore) IncrementDownload(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.writeDB.Exec("UPDATE file_assets SET download_count = download_count + 1 WHERE id = ? AND deleted_at IS NULL", id)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteFiles(ids []string) (deleted []model.FileAsset, err error) {
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	for _, id := range ids {
		var f model.FileAsset
		var isVideo int
		var expStr *string
		var createdStr string
		err := tx.QueryRow(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
			download_count, is_video, download_url, stream_url, expires_at, created_at
			FROM file_assets WHERE id = ? AND deleted_at IS NULL`, id).Scan(
			&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &createdStr,
		)
		if err != nil {
			continue
		}
		f.IsVideo = intToBool(isVideo)
		f.ExpiresAt = parseTime(expStr)
		f.DeletedAt = &now
		f.CreatedAt = mustParseTime(createdStr)

		if _, err := tx.Exec("UPDATE file_assets SET deleted_at = ? WHERE id = ?", nowStr, id); err != nil {
			return nil, err
		}
		deleted = append(deleted, f)
		if err := s.appendAuditTx(tx, "trash", "file", f.ID, "file moved to trash", map[string]interface{}{"name": f.FileName}); err != nil {
			return nil, err
		}
	}

	if len(deleted) == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return deleted, nil
}

func (s *SQLiteStore) StorageReferencedByOthers(storagePath, excludingID string) bool {
	var count int
	err := s.readDB.QueryRow("SELECT count(*) FROM file_assets WHERE storage_path = ? AND id != ?", storagePath, excludingID).Scan(&count)
	return err == nil && count > 0
}

// --- Pads API ---

func (s *SQLiteStore) ListPads(query string) []model.PadDocument {
	query = strings.ToLower(strings.TrimSpace(query))
	rows, err := s.readDB.Query(`SELECT id, title, content, version, deleted_at, created_at, updated_at
		FROM pad_documents WHERE deleted_at IS NULL ORDER BY updated_at DESC`)
	if err != nil {
		return []model.PadDocument{}
	}
	defer rows.Close()

	out := make([]model.PadDocument, 0)
	for rows.Next() {
		var p model.PadDocument
		var delStr *string
		var createdStr, updatedStr string
		if err := rows.Scan(&p.ID, &p.Title, &p.Content, &p.Version, &delStr, &createdStr, &updatedStr); err != nil {
			continue
		}
		p.CreatedAt = mustParseTime(createdStr)
		p.UpdatedAt = mustParseTime(updatedStr)
		if query != "" && !strings.Contains(strings.ToLower(p.Title), query) && !strings.Contains(strings.ToLower(p.Content), query) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func (s *SQLiteStore) GetPad(id string) (model.PadDocument, error) {
	var p model.PadDocument
	var delStr *string
	var createdStr, updatedStr string

	err := s.readDB.QueryRow(`SELECT id, title, content, version, deleted_at, created_at, updated_at
		FROM pad_documents WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&p.ID, &p.Title, &p.Content, &p.Version, &delStr, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.PadDocument{}, ErrNotFound
		}
		return model.PadDocument{}, err
	}
	p.CreatedAt = mustParseTime(createdStr)
	p.UpdatedAt = mustParseTime(updatedStr)
	return p, nil
}

func (s *SQLiteStore) CreatePad(p model.PadDocument) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.Exec(`INSERT INTO pad_documents(
		id, title, content, version, deleted_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Title, p.Content, p.Version, timeToString(p.DeletedAt),
		p.CreatedAt.UTC().Format(time.RFC3339Nano), p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}

	if err := s.appendAuditTx(tx, "create", "pad", p.ID, "document created", map[string]interface{}{"title": p.Title}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) UpdatePad(id, title, content string, expectedVersion int) (result model.PadDocument, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return model.PadDocument{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var p model.PadDocument
	var delStr *string
	var createdStr, updatedStr string

	err = tx.QueryRow(`SELECT id, title, content, version, deleted_at, created_at, updated_at
		FROM pad_documents WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&p.ID, &p.Title, &p.Content, &p.Version, &delStr, &createdStr, &updatedStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.PadDocument{}, ErrNotFound
		}
		return model.PadDocument{}, err
	}

	if expectedVersion <= 0 || expectedVersion != p.Version {
		return model.PadDocument{}, ErrVersionConflict
	}

	if strings.TrimSpace(title) != "" {
		p.Title = cleanText(title, 200)
	}
	p.Content = content
	p.Version++
	p.UpdatedAt = time.Now().UTC()

	_, err = tx.Exec("UPDATE pad_documents SET title = ?, content = ?, version = ?, updated_at = ? WHERE id = ?",
		p.Title, p.Content, p.Version, p.UpdatedAt.Format(time.RFC3339Nano), id)
	if err != nil {
		return model.PadDocument{}, err
	}

	if err := s.appendAuditTx(tx, "update", "pad", id, "document updated", map[string]interface{}{"version": p.Version}); err != nil {
		return model.PadDocument{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PadDocument{}, err
	}
	tx = nil
	p.CreatedAt = mustParseTime(createdStr)
	return p, nil
}

func (s *SQLiteStore) DeletePad(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var title string
	err = tx.QueryRow("SELECT title FROM pad_documents WHERE id = ? AND deleted_at IS NULL", id).Scan(&title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec("UPDATE pad_documents SET deleted_at = ? WHERE id = ?", nowStr, id); err != nil {
		return err
	}

	if err := s.appendAuditTx(tx, "trash", "pad", id, "document moved to trash", map[string]interface{}{"title": title}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

// --- Shares API ---

func (s *SQLiteStore) CreateShare(link model.ShareLink) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	status := link.Status
	if status == "" {
		status = "active"
	}
	_, err = tx.Exec(`INSERT INTO share_links(
		id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID, link.Token, link.ObjectType, link.ObjectID, link.Name, link.URL,
		link.AccessCount, link.MaxAccessCount, timeToString(link.ExpiresAt),
		timeToString(link.RevokedAt), timeToString(link.LastAccessedAt),
		link.CreatedAt.UTC().Format(time.RFC3339Nano), status,
	)
	if err != nil {
		return err
	}

	if err := s.appendAuditTx(tx, "share", link.ObjectType, link.ObjectID, "share created", map[string]interface{}{"share_id": link.ID, "max_access_count": link.MaxAccessCount}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) calculateShareStatus(link model.ShareLink, now time.Time) string {
	if link.RevokedAt != nil {
		return "revoked"
	}
	if expired(link.ExpiresAt, now) {
		return "expired"
	}
	if link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
		return "exhausted"
	}
	switch link.ObjectType {
	case "note":
		var delStr *string
		var expStr *string
		err := s.readDB.QueryRow("SELECT deleted_at, expires_at FROM quick_notes WHERE id = ?", link.ObjectID).Scan(&delStr, &expStr)
		if err != nil {
			return "target_missing"
		}
		if delStr != nil {
			return "suspended"
		}
		if expired(parseTime(expStr), now) {
			return "target_expired"
		}
		return "active"
	case "file":
		var delStr *string
		var expStr *string
		err := s.readDB.QueryRow("SELECT deleted_at, expires_at FROM file_assets WHERE id = ?", link.ObjectID).Scan(&delStr, &expStr)
		if err != nil {
			return "target_missing"
		}
		if delStr != nil {
			return "suspended"
		}
		if expired(parseTime(expStr), now) {
			return "target_expired"
		}
		return "active"
	case "pad":
		var delStr *string
		err := s.readDB.QueryRow("SELECT deleted_at FROM pad_documents WHERE id = ?", link.ObjectID).Scan(&delStr)
		if err != nil {
			return "target_missing"
		}
		if delStr != nil {
			return "suspended"
		}
		return "active"
	}
	return "target_missing"
}

func (s *SQLiteStore) ListShares() []model.ShareLink {
	now := time.Now().UTC()
	rows, err := s.readDB.Query(`SELECT id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
		FROM share_links ORDER BY created_at DESC`)
	if err != nil {
		return []model.ShareLink{}
	}
	defer rows.Close()

	out := make([]model.ShareLink, 0)
	for rows.Next() {
		var link model.ShareLink
		var expStr, revStr, lastStr *string
		var createdStr string
		if err := rows.Scan(&link.ID, &link.Token, &link.ObjectType, &link.ObjectID, &link.Name, &link.URL,
			&link.AccessCount, &link.MaxAccessCount, &expStr, &revStr, &lastStr, &createdStr, &link.Status); err != nil {
			continue
		}
		link.ExpiresAt = parseTime(expStr)
		link.RevokedAt = parseTime(revStr)
		link.LastAccessedAt = parseTime(lastStr)
		link.CreatedAt = mustParseTime(createdStr)
		link.Status = s.calculateShareStatus(link, now)
		out = append(out, link)
	}
	return out
}

func (s *SQLiteStore) GetShareByToken(token string) (model.ShareLink, error) {
	return s.LookupShareByToken(token, false)
}

func (s *SQLiteStore) LookupShareByToken(token string, allowExhausted bool) (model.ShareLink, error) {
	now := time.Now().UTC()
	var link model.ShareLink
	var expStr, revStr, lastStr *string
	var createdStr string

	err := s.readDB.QueryRow(`SELECT id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
		FROM share_links WHERE token = ?`, token).Scan(
		&link.ID, &link.Token, &link.ObjectType, &link.ObjectID, &link.Name, &link.URL,
		&link.AccessCount, &link.MaxAccessCount, &expStr, &revStr, &lastStr, &createdStr, &link.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ShareLink{}, ErrNotFound
		}
		return model.ShareLink{}, err
	}
	link.ExpiresAt = parseTime(expStr)
	link.RevokedAt = parseTime(revStr)
	link.LastAccessedAt = parseTime(lastStr)
	link.CreatedAt = mustParseTime(createdStr)

	if link.RevokedAt != nil {
		return model.ShareLink{}, ErrShareRevoked
	}
	if expired(link.ExpiresAt, now) {
		return model.ShareLink{}, ErrShareExpired
	}
	if !allowExhausted && link.MaxAccessCount > 0 && link.AccessCount >= link.MaxAccessCount {
		return model.ShareLink{}, ErrShareExhausted
	}
	link.Status = s.calculateShareStatus(link, now)
	if link.Status != "active" && !(allowExhausted && link.Status == "exhausted") {
		return model.ShareLink{}, ErrNotFound
	}
	return link, nil
}

func (s *SQLiteStore) ConsumeShare(token string) (result model.ShareLink, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return model.ShareLink{}, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	var link model.ShareLink
	var expStr, revStr, lastStr *string
	var createdStr string

	err = tx.QueryRow(`SELECT id, token, object_type, object_id, name, url,
		access_count, max_access_count, expires_at, revoked_at, last_accessed_at, created_at, status
		FROM share_links WHERE token = ?`, token).Scan(
		&link.ID, &link.Token, &link.ObjectType, &link.ObjectID, &link.Name, &link.URL,
		&link.AccessCount, &link.MaxAccessCount, &expStr, &revStr, &lastStr, &createdStr, &link.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ShareLink{}, ErrNotFound
		}
		return model.ShareLink{}, err
	}
	link.ExpiresAt = parseTime(expStr)
	link.RevokedAt = parseTime(revStr)
	link.LastAccessedAt = parseTime(lastStr)
	link.CreatedAt = mustParseTime(createdStr)

	if err := validateShare(link, now); err != nil {
		return model.ShareLink{}, err
	}
	status := s.calculateShareStatus(link, now)
	if status != "active" {
		return model.ShareLink{}, ErrNotFound
	}

	link.AccessCount++
	link.LastAccessedAt = &now
	nowStr := now.Format(time.RFC3339Nano)

	_, err = tx.Exec("UPDATE share_links SET access_count = ?, last_accessed_at = ? WHERE token = ?",
		link.AccessCount, nowStr, token)
	if err != nil {
		return model.ShareLink{}, err
	}

	if err := s.appendAuditTx(tx, "access", "share", link.ID, "share accessed", map[string]interface{}{"count": link.AccessCount}); err != nil {
		return model.ShareLink{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ShareLink{}, err
	}
	tx = nil
	link.Status = "active"
	return link, nil
}

func (s *SQLiteStore) RevokeShare(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var revokedAt *string
	err = tx.QueryRow("SELECT revoked_at FROM share_links WHERE id = ?", id).Scan(&revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if revokedAt == nil {
		now := time.Now().UTC()
		nowStr := now.Format(time.RFC3339Nano)
		if _, err := tx.Exec("UPDATE share_links SET revoked_at = ?, status = 'revoked' WHERE id = ?", nowStr, id); err != nil {
			return err
		}
		if err := s.appendAuditTx(tx, "revoke", "share", id, "share revoked", nil); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

// --- Trash & Purge API ---

func (s *SQLiteStore) ListTrash(kind string) []model.TrashItem {
	kind = strings.ToLower(strings.TrimSpace(kind))
	out := []model.TrashItem{}

	if kind == "" || kind == "note" {
		rows, err := s.readDB.Query("SELECT id, content, deleted_at, created_at FROM quick_notes WHERE deleted_at IS NOT NULL")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, content, createdStr string
				var delStr *string
				if err := rows.Scan(&id, &content, &delStr, &createdStr); err == nil && delStr != nil {
					out = append(out, model.TrashItem{
						ObjectType: "note",
						ID:         id,
						Name:       compact(content, 80),
						DeletedAt:  *parseTime(delStr),
						CreatedAt:  mustParseTime(createdStr),
					})
				}
			}
		}
	}
	if kind == "" || kind == "file" {
		rows, err := s.readDB.Query("SELECT id, file_name, file_size, deleted_at, created_at FROM file_assets WHERE deleted_at IS NOT NULL")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, name, createdStr string
				var size int64
				var delStr *string
				if err := rows.Scan(&id, &name, &size, &delStr, &createdStr); err == nil && delStr != nil {
					out = append(out, model.TrashItem{
						ObjectType: "file",
						ID:         id,
						Name:       name,
						Size:       size,
						DeletedAt:  *parseTime(delStr),
						CreatedAt:  mustParseTime(createdStr),
					})
				}
			}
		}
	}
	if kind == "" || kind == "pad" {
		rows, err := s.readDB.Query("SELECT id, title, deleted_at, created_at FROM pad_documents WHERE deleted_at IS NOT NULL")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, title, createdStr string
				var delStr *string
				if err := rows.Scan(&id, &title, &delStr, &createdStr); err == nil && delStr != nil {
					out = append(out, model.TrashItem{
						ObjectType: "pad",
						ID:         id,
						Name:       title,
						DeletedAt:  *parseTime(delStr),
						CreatedAt:  mustParseTime(createdStr),
					})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out
}

func (s *SQLiteStore) RestoreTrash(kind, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "note":
		res, err := tx.Exec("UPDATE quick_notes SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL", id)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		if err := s.appendAuditTx(tx, "restore", "note", id, "note restored", nil); err != nil {
			return err
		}
	case "file":
		var storagePath, name string
		err := tx.QueryRow("SELECT storage_path, file_name FROM file_assets WHERE id = ? AND deleted_at IS NOT NULL", id).Scan(&storagePath, &name)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := os.Stat(filepath.Join(s.dataDir, storagePath)); err != nil {
			return fmt.Errorf("file entity is missing: %w", err)
		}
		if _, err := tx.Exec("UPDATE file_assets SET deleted_at = NULL WHERE id = ?", id); err != nil {
			return err
		}
		if err := s.appendAuditTx(tx, "restore", "file", id, "file restored", map[string]interface{}{"name": name}); err != nil {
			return err
		}
	case "pad":
		res, err := tx.Exec("UPDATE pad_documents SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL", id)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		if err := s.appendAuditTx(tx, "restore", "pad", id, "document restored", nil); err != nil {
			return err
		}
	default:
		return errors.New("unsupported trash type")
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func (s *SQLiteStore) PurgeTrash(kind, id string) (removed *model.FileAsset, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "note":
		res, err := tx.Exec("DELETE FROM quick_notes WHERE id = ? AND deleted_at IS NOT NULL", id)
		if err != nil {
			return nil, err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return nil, ErrNotFound
		}
		_, _ = tx.Exec("UPDATE share_links SET revoked_at = ? WHERE object_type = 'note' AND object_id = ? AND revoked_at IS NULL", nowStr, id)
		if err := s.appendAuditTx(tx, "purge", "note", id, "note permanently deleted", nil); err != nil {
			return nil, err
		}
	case "file":
		var f model.FileAsset
		var isVideo int
		var expStr, delStr *string
		var createdStr string
		err := tx.QueryRow(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
			download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
			FROM file_assets WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(
			&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
			&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr,
		)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		f.IsVideo = intToBool(isVideo)
		f.ExpiresAt = parseTime(expStr)
		f.DeletedAt = parseTime(delStr)
		f.CreatedAt = mustParseTime(createdStr)
		removed = &f

		if _, err := tx.Exec("DELETE FROM file_assets WHERE id = ?", id); err != nil {
			return nil, err
		}
		_, _ = tx.Exec("UPDATE share_links SET revoked_at = ? WHERE object_type = 'file' AND object_id = ? AND revoked_at IS NULL", nowStr, id)
		if err := s.appendAuditTx(tx, "purge", "file", id, "file permanently deleted", map[string]interface{}{"name": f.FileName}); err != nil {
			return nil, err
		}
	case "pad":
		res, err := tx.Exec("DELETE FROM pad_documents WHERE id = ? AND deleted_at IS NOT NULL", id)
		if err != nil {
			return nil, err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return nil, ErrNotFound
		}
		_, _ = tx.Exec("UPDATE share_links SET revoked_at = ? WHERE object_type = 'pad' AND object_id = ? AND revoked_at IS NULL", nowStr, id)
		if err := s.appendAuditTx(tx, "purge", "pad", id, "document permanently deleted", nil); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported trash type")
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return removed, nil
}

func (s *SQLiteStore) EmptyTrash() (files []model.FileAsset, deletedCount int, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)

	// Notes
	nRes, err := tx.Exec("DELETE FROM quick_notes WHERE deleted_at IS NOT NULL")
	if err != nil {
		return nil, 0, err
	}
	nAff, _ := nRes.RowsAffected()
	deletedCount += int(nAff)

	// Files
	rows, err := tx.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE deleted_at IS NOT NULL`)
	if err == nil {
		for rows.Next() {
			var f model.FileAsset
			var isVideo int
			var expStr, delStr *string
			var createdStr string
			if err := rows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
				&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err == nil {
				f.IsVideo = intToBool(isVideo)
				f.ExpiresAt = parseTime(expStr)
				f.DeletedAt = parseTime(delStr)
				f.CreatedAt = mustParseTime(createdStr)
				files = append(files, f)
			}
		}
		rows.Close()
	}
	fRes, err := tx.Exec("DELETE FROM file_assets WHERE deleted_at IS NOT NULL")
	if err != nil {
		return nil, 0, err
	}
	fAff, _ := fRes.RowsAffected()
	deletedCount += int(fAff)

	// Pads
	pRes, err := tx.Exec("DELETE FROM pad_documents WHERE deleted_at IS NOT NULL")
	if err != nil {
		return nil, 0, err
	}
	pAff, _ := pRes.RowsAffected()
	deletedCount += int(pAff)

	if deletedCount == 0 {
		return nil, 0, ErrNotFound
	}

	// Revoke affected shares
	_, _ = tx.Exec(`UPDATE share_links SET revoked_at = ? WHERE revoked_at IS NULL AND (
		(object_type = 'note' AND object_id NOT IN (SELECT id FROM quick_notes)) OR
		(object_type = 'file' AND object_id NOT IN (SELECT id FROM file_assets)) OR
		(object_type = 'pad' AND object_id NOT IN (SELECT id FROM pad_documents))
	)`, nowStr)

	if err := s.appendAuditTx(tx, "purge", "trash", "", "trash emptied", map[string]interface{}{"count": deletedCount, "files": len(files)}); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	tx = nil
	return files, deletedCount, nil
}

// --- Dashboard Stats ---

func (s *SQLiteStore) Stats() model.DashboardStats {
	now := time.Now().UTC()
	var st model.DashboardStats

	// Notes
	var activeNotes, trashNotes int
	rows, err := s.readDB.Query("SELECT expires_at, deleted_at FROM quick_notes")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var expStr, delStr *string
			if err := rows.Scan(&expStr, &delStr); err == nil {
				if delStr != nil {
					trashNotes++
				} else if !expired(parseTime(expStr), now) {
					activeNotes++
				}
			}
		}
	}
	st.NotesCount = activeNotes
	st.TrashCount += trashNotes

	// Files & Storage used
	blobSizes := map[string]int64{}
	var activeFiles, videos, trashFiles int
	fRows, err := s.readDB.Query("SELECT file_size, storage_path, is_video, expires_at, deleted_at FROM file_assets")
	if err == nil {
		defer fRows.Close()
		for fRows.Next() {
			var size int64
			var path string
			var isVideo int
			var expStr, delStr *string
			if err := fRows.Scan(&size, &path, &isVideo, &expStr, &delStr); err == nil {
				if delStr != nil {
					trashFiles++
					continue
				}
				if expired(parseTime(expStr), now) {
					continue
				}
				activeFiles++
				blobSizes[path] = size
				if isVideo != 0 {
					videos++
				}
			}
		}
	}
	st.FilesCount = activeFiles
	st.VideosCount = videos
	st.TrashCount += trashFiles
	for _, sz := range blobSizes {
		st.StorageUsed += sz
	}

	// Pads
	var activePads, trashPads int
	pRows, err := s.readDB.Query("SELECT deleted_at FROM pad_documents")
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var delStr *string
			if err := pRows.Scan(&delStr); err == nil {
				if delStr != nil {
					trashPads++
				} else {
					activePads++
				}
			}
		}
	}
	st.PadsCount = activePads
	st.TrashCount += trashPads

	// Shares
	shares := s.ListShares()
	for _, sl := range shares {
		if sl.Status == "active" {
			st.ActiveShares++
		}
	}

	return st
}

// --- Expiry & Periodic Cleanup ---

func (s *SQLiteStore) CleanupExpired(retentionDays int) (moved int, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	var cutoffStr string
	if retentionDays > 0 {
		cutoffStr = now.Add(-time.Duration(retentionDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	}

	// Notes: expired(ExpiresAt) or (cutoff and UpdatedAt < cutoff and not pinned)
	nQuery := "UPDATE quick_notes SET deleted_at = ? WHERE deleted_at IS NULL AND (expires_at IS NOT NULL AND expires_at <= ?"
	args := []interface{}{nowStr, nowStr}
	if cutoffStr != "" {
		nQuery += " OR (updated_at < ? AND pinned = 0)"
		args = append(args, cutoffStr)
	}
	nQuery += ")"
	res, err := tx.Exec(nQuery, args...)
	if err != nil {
		return 0, err
	}
	aff, _ := res.RowsAffected()
	moved += int(aff)

	// Files: expired(ExpiresAt) or (cutoff and CreatedAt < cutoff)
	fQuery := "UPDATE file_assets SET deleted_at = ? WHERE deleted_at IS NULL AND (expires_at IS NOT NULL AND expires_at <= ?"
	fArgs := []interface{}{nowStr, nowStr}
	if cutoffStr != "" {
		fQuery += " OR created_at < ?"
		fArgs = append(fArgs, cutoffStr)
	}
	fQuery += ")"
	fRes, err := tx.Exec(fQuery, fArgs...)
	if err != nil {
		return 0, err
	}
	fAff, _ := fRes.RowsAffected()
	moved += int(fAff)

	// Pads: cutoff and UpdatedAt < cutoff
	if cutoffStr != "" {
		pRes, err := tx.Exec("UPDATE pad_documents SET deleted_at = ? WHERE deleted_at IS NULL AND updated_at < ?", nowStr, cutoffStr)
		if err != nil {
			return 0, err
		}
		pAff, _ := pRes.RowsAffected()
		moved += int(pAff)
	}

	if moved == 0 {
		return 0, nil
	}

	if err := s.appendAuditTx(tx, "cleanup", "system", "", "expired items moved to trash", map[string]interface{}{"count": moved}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	tx = nil
	return moved, nil
}

func (s *SQLiteStore) PurgeExpiredTrash(days int) (files []model.FileAsset, purged int, err error) {
	if days <= 0 {
		return nil, 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writeDB.Begin()
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)

	// Notes
	nRes, err := tx.Exec("DELETE FROM quick_notes WHERE deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
	if err != nil {
		return nil, 0, err
	}
	nAff, _ := nRes.RowsAffected()
	purged += int(nAff)

	// Files
	rows, err := tx.Query(`SELECT id, file_name, file_size, mime_type, storage_path, file_hash,
		download_count, is_video, download_url, stream_url, expires_at, deleted_at, created_at
		FROM file_assets WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err == nil {
		for rows.Next() {
			var f model.FileAsset
			var isVideo int
			var expStr, delStr *string
			var createdStr string
			if err := rows.Scan(&f.ID, &f.FileName, &f.FileSize, &f.MIMEType, &f.StoragePath, &f.FileHash,
				&f.DownloadCount, &isVideo, &f.DownloadURL, &f.StreamURL, &expStr, &delStr, &createdStr); err == nil {
				f.IsVideo = intToBool(isVideo)
				f.ExpiresAt = parseTime(expStr)
				f.DeletedAt = parseTime(delStr)
				f.CreatedAt = mustParseTime(createdStr)
				files = append(files, f)
			}
		}
		rows.Close()
	}
	fRes, err := tx.Exec("DELETE FROM file_assets WHERE deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
	if err != nil {
		return nil, 0, err
	}
	fAff, _ := fRes.RowsAffected()
	purged += int(fAff)

	// Pads
	pRes, err := tx.Exec("DELETE FROM pad_documents WHERE deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
	if err != nil {
		return nil, 0, err
	}
	pAff, _ := pRes.RowsAffected()
	purged += int(pAff)

	// Purge stale shares older than 30 days
	shareCutoff := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, _ = tx.Exec(`DELETE FROM share_links WHERE created_at < ? AND (
		revoked_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at <= ?) OR (max_access_count > 0 AND access_count >= max_access_count)
	)`, shareCutoff, nowStr)

	// Revoke shares of deleted objects
	_, _ = tx.Exec(`UPDATE share_links SET revoked_at = ? WHERE revoked_at IS NULL AND (
		(object_type = 'note' AND object_id NOT IN (SELECT id FROM quick_notes)) OR
		(object_type = 'file' AND object_id NOT IN (SELECT id FROM file_assets)) OR
		(object_type = 'pad' AND object_id NOT IN (SELECT id FROM pad_documents))
	)`, nowStr)

	if purged == 0 {
		return nil, 0, nil
	}

	if err := s.appendAuditTx(tx, "purge", "trash", "", "expired trash purged", map[string]interface{}{"count": purged, "files": len(files)}); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	tx = nil
	return files, purged, nil
}
