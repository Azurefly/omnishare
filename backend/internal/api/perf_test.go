package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"omnishare/internal/config"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

// newLargeStateServer materializes a realistic dataset on disk (bypassing the
// quadratic write path) and returns a handler bound to it.
func newLargeStateServer(tb testing.TB, notes, files, pads, shares, audits int) http.Handler {
	tb.Helper()
	dir := tb.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)

	payload := map[string]interface{}{
		"schema_version": 2,
		"notes":          make([]model.QuickNote, 0, notes),
		"files":          make([]model.FileAsset, 0, files),
		"pads":           make([]model.PadDocument, 0, pads),
		"shares":         make([]model.ShareLink, 0, shares),
		"audits":         make([]model.AuditEvent, 0, audits),
	}
	noteList := make([]model.QuickNote, 0, notes)
	for i := 0; i < notes; i++ {
		noteList = append(noteList, model.QuickNote{
			ID:        fmt.Sprintf("n_%06d", i),
			Content:   fmt.Sprintf("benchmark note %d #tag%d", i, i%17),
			Tags:      []string{fmt.Sprintf("tag%d", i%17)},
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	fileList := make([]model.FileAsset, 0, files)
	for i := 0; i < files; i++ {
		storagePath := fmt.Sprintf("uploads/blobs/blob_%d", i)
		fileList = append(fileList, model.FileAsset{
			ID:          fmt.Sprintf("f_%06d", i),
			FileName:    fmt.Sprintf("file_%d.bin", i),
			FileSize:    4096,
			MIMEType:    "application/octet-stream",
			StoragePath: storagePath,
			FileHash:    fmt.Sprintf("%064d", i),
			CreatedAt:   now,
		})
		if err := os.MkdirAll(filepath.Join(dir, "uploads", "blobs"), 0o700); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, storagePath), make([]byte, 4096), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	padList := make([]model.PadDocument, 0, pads)
	for i := 0; i < pads; i++ {
		padList = append(padList, model.PadDocument{
			ID:        fmt.Sprintf("pad_%06d", i),
			Title:     fmt.Sprintf("pad %d", i),
			Content:   "content",
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	shareList := make([]model.ShareLink, 0, shares)
	for i := 0; i < shares; i++ {
		objectType, objectID := "note", fmt.Sprintf("n_%06d", i%notes)
		if i%3 == 1 {
			objectType, objectID = "file", fmt.Sprintf("f_%06d", i%files)
		} else if i%3 == 2 {
			objectType, objectID = "pad", fmt.Sprintf("pad_%06d", i%pads)
		}
		shareList = append(shareList, model.ShareLink{
			ID:         fmt.Sprintf("shr_%06d", i),
			Token:      fmt.Sprintf("token-%06d", i),
			ObjectType: objectType,
			ObjectID:   objectID,
			Name:       "share",
			CreatedAt:  now,
		})
	}
	payload["notes"] = noteList
	payload["files"] = fileList
	payload["pads"] = padList
	payload["shares"] = shareList

	raw, err := json.Marshal(payload)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "omnishare.json"), raw, 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "uploads", ".staging"), 0o700); err != nil {
		tb.Fatal(err)
	}

	cfg, err := config.New(dir, 8081, "bench-node")
	if err != nil {
		tb.Fatal(err)
	}
	store, err := storage.New(dir)
	if err != nil {
		tb.Fatal(err)
	}
	return New(store, cfg).Handler()
}

func benchGet(b *testing.B, handler http.Handler, path string) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "127.0.0.1:8081"
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("%s: status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func BenchmarkDashboardLargeState(b *testing.B) {
	handler := newLargeStateServer(b, 20000, 300, 500, 2000, 500)
	benchGet(b, handler, "/api/v1/dashboard")
}

func BenchmarkListNotesLargeState(b *testing.B) {
	handler := newLargeStateServer(b, 20000, 300, 500, 2000, 500)
	benchGet(b, handler, "/api/v1/notes")
}

func BenchmarkListSharesLargeState(b *testing.B) {
	handler := newLargeStateServer(b, 20000, 300, 500, 2000, 500)
	benchGet(b, handler, "/api/v1/shares")
}

func BenchmarkDownloadSmallFileLargeState(b *testing.B) {
	handler := newLargeStateServer(b, 20000, 300, 500, 2000, 500)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/files/f_000000/download", nil)
		r.Host = "127.0.0.1:8081"
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("download status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func newBenchServer(b *testing.B) *Server {
	b.Helper()
	dir := b.TempDir()
	cfg, err := config.New(dir, 8081, "bench-node")
	if err != nil {
		b.Fatal(err)
	}
	store, err := storage.New(dir)
	if err != nil {
		b.Fatal(err)
	}
	return New(store, cfg)
}

// Host validation runs on every request before routing, so its lookup has to
// stay lock-free and allocation-free. A regression here shows up as a global
// serialization point under concurrent load, not as a slow single call.
func BenchmarkLocalAddressLookup(b *testing.B) {
	s := newBenchServer(b)
	s.localNetwork() // warm the snapshot
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ip := net.ParseIP("127.0.0.1")
		for pb.Next() {
			if !s.isLocalAddress(ip) {
				b.Fatal("loopback address rejected")
			}
		}
	})
}
