package api

import (
	"net/http"
	"testing"
	"time"

	"omnishare/internal/config"
	"omnishare/internal/storage"
)

func newSQLiteTestServer(t *testing.T) (*Server, http.Handler, *storage.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.New(dir, 8081, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewSQLite(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := New(store, cfg)
	return s, s.Handler(), store
}

func requireAccessKey(t *testing.T, cfg *config.Manager) {
	t.Helper()
	current := cfg.Get()
	current.AccessKey = "test-key-0123456789"
	current.AccessKeyEnabled = nil // Exercise legacy implicit key configuration.
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
}

// A 401 storm must not turn each request into its own write transaction. The
// batch is written once when flushed.
func TestAuthDeniedStormDoesNotWritePerRequest(t *testing.T) {
	s, h, store := newSQLiteTestServer(t)
	requireAccessKey(t, s.cfg)
	// Keep the whole storm inside one aggregation window. Under -race the 50
	// PBKDF2 verifications can take over a minute, and a window rollover would
	// legitimately flush one batch mid-test.
	s.deniedWindow = time.Hour

	before := store.WriteTransactions()
	for i := 0; i < 50; i++ {
		w := request(t, h, "GET", "/api/v1/notes", nil, "wrong-key", "")
		if w.Code != 401 {
			t.Fatalf("expected 401, got %d", w.Code)
		}
	}
	if got := store.WriteTransactions(); got != before {
		t.Fatalf("401 storm wrote %d transactions, want 0", got-before)
	}
	s.FlushDeniedAudits()
	if got := store.WriteTransactions(); got != before+1 {
		t.Fatalf("expected one aggregated audit write, got %d", got-before)
	}
	audits := store.ListAudits(10)
	if len(audits) != 1 {
		t.Fatalf("expected one aggregated audit event, got %d", len(audits))
	}
	meta := audits[0].Metadata
	if audits[0].Action != "auth_denied" {
		t.Fatalf("unexpected audit action %q", audits[0].Action)
	}
	// JSON-decoded metadata is map[string]interface{} for in-memory engines too.
	if count, ok := meta["count"]; !ok || count == nil {
		t.Fatalf("aggregate metadata lost its count: %+v", audits[0].Metadata)
	}
}
