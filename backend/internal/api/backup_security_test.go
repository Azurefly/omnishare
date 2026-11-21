package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"omnishare/internal/config"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

func postRestore(t *testing.T, h http.Handler, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	return postRestoreFrom(t, h, "127.0.0.1:55", archive)
}

func postRestoreFrom(t *testing.T, h http.Handler, remote string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("backup", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/restore", &body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-OmniShare-Confirm", "RESTORE-BACKUP")
	applyTestAuth(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func uploadFrom(t *testing.T, h http.Handler, remote, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.WriteField("ttl_seconds", "3600")
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", &body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", mw.FormDataContentType())
	applyTestAuth(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func zipEntry(t *testing.T, src []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatalf("zip entry %s missing", name)
	return nil
}

// rewriteZipEntry rebuilds the archive with one entry replaced.
func rewriteZipEntry(t *testing.T, src []byte, name string, data []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if f.Name == name {
			continue
		}
		hdr := &zip.FileHeader{Name: f.Name, Method: f.Method}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			t.Fatal(err)
		}
		_ = rc.Close()
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A state file can be edited by hand or produced by an older build; the
// download path must refuse to follow it outside uploads/.
func TestDownloadRejectsStatePathEscapingUploads(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := []byte("outside-the-data-dir")
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	seeded := map[string]interface{}{
		"schema_version": 2,
		"notes":          []interface{}{}, "pads": []interface{}{}, "shares": []interface{}{}, "audits": []interface{}{},
		"files": []map[string]interface{}{{
			"id": "f_traversal", "file_name": "secret.txt", "file_size": len(secret),
			"mime_type": "text/plain", "storage_path": "../secret.txt", "created_at": time.Now().UTC(),
		}},
	}
	raw, err := json.Marshal(seeded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "omnishare.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(dataDir, 8081, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, cfg)
	testAuthToken = s.InitToken()
	w := request(t, s.Handler(), "GET", "/api/v1/files/f_traversal/download", nil, "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("traversal download served: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), string(secret)) {
		t.Fatal("download leaked bytes outside the uploads directory")
	}
}

// The other entry: a crafted backup state must be rejected by the storage layer
// rather than installed and served later.
func TestReplaceStateRejectsEscapingStoragePath(t *testing.T) {
	s, _, _ := newTestServer(t)
	raw, err := s.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["files"] = []map[string]interface{}{{
		"id": "f_crafted", "file_name": "x.txt", "file_size": 1, "mime_type": "text/plain",
		"storage_path": "../../secret.txt", "created_at": time.Now().UTC(),
	}}
	crafted, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.ReplaceStateJSON(crafted); err == nil {
		t.Fatal("storage accepted a state file pointing outside uploads")
	}
}

// A real backup must round-trip a file blob, not just metadata.
func TestBackupRestorePreservesFileBlobs(t *testing.T) {
	s, h, _ := newTestServer(t)
	content := []byte("blob survives the round trip")
	resp := upload(t, h, "keep.txt", content, "")
	if resp.Code != http.StatusCreated {
		t.Fatalf("upload=%d %s", resp.Code, resp.Body.String())
	}
	asset := decodeData[model.FileAsset](t, resp)
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup=%d %s", backup.Code, backup.Body.String())
	}
	// Destroy the live blob so only the archive can bring it back.
	if err := os.Remove(filepath.Join(s.dataDir, filepath.FromSlash(asset.StoragePath))); err != nil {
		t.Fatal(err)
	}
	if w := postRestore(t, h, backup.Body.Bytes()); w.Code != http.StatusOK {
		t.Fatalf("restore=%d %s", w.Code, w.Body.String())
	}
	got := request(t, h, "GET", "/api/v1/files/"+asset.ID+"/download", nil, "", "")
	if got.Code != http.StatusOK || got.Body.String() != string(content) {
		t.Fatalf("restored download=%d %q", got.Code, got.Body.String())
	}
}

// A state file that references a blob absent from the manifest passes checksum
// validation; the restore must detect it and roll back instead of reporting
// success with a broken library.
func TestRestoreRollsBackOnMissingReferencedBlob(t *testing.T) {
	_, h, _ := newTestServer(t)
	note := decodeData[model.QuickNote](t, request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"survives rollback"}`), "", "application/json"))
	resp := upload(t, h, "original.txt", []byte("original blob"), "")
	if resp.Code != http.StatusCreated {
		t.Fatalf("upload=%d %s", resp.Code, resp.Body.String())
	}
	asset := decodeData[model.FileAsset](t, resp)
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup=%d", backup.Code)
	}
	var stateDoc map[string]interface{}
	if err := json.Unmarshal(zipEntry(t, backup.Body.Bytes(), "omnishare.json"), &stateDoc); err != nil {
		t.Fatal(err)
	}
	files, _ := stateDoc["files"].([]interface{})
	stateDoc["files"] = append(files, map[string]interface{}{
		"id": "f_missing", "file_name": "missing.bin", "file_size": 10, "mime_type": "application/octet-stream",
		"storage_path": "uploads/blobs/zz/not-in-archive", "created_at": time.Now().UTC(),
	})
	stateJSON, err := json.Marshal(stateDoc)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON := zipEntry(t, backup.Body.Bytes(), "backup-manifest.json")
	var manifest model.BackupManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.StateSHA256 = hexDigest(stateJSON)
	changedManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive := rewriteZipEntry(t, backup.Body.Bytes(), "omnishare.json", stateJSON)
	archive = rewriteZipEntry(t, archive, "backup-manifest.json", changedManifest)

	if w := postRestore(t, h, archive); w.Code != http.StatusConflict {
		t.Fatalf("restore accepted dangling state: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, "GET", "/api/v1/notes/"+note.ID+"/manage", nil, "", ""); w.Code != http.StatusOK {
		t.Fatalf("rollback lost the note: %d", w.Code)
	}
	if w := request(t, h, "GET", "/api/v1/files/"+asset.ID+"/download", nil, "", ""); w.Code != http.StatusOK {
		t.Fatalf("rollback lost the file: %d", w.Code)
	}
}

// The restore entry must reject unsafe paths too, not only downloads.
func TestRestoreRejectsUnsafeManifestPath(t *testing.T) {
	_, h, _ := newTestServer(t)
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup=%d", backup.Code)
	}
	manifestJSON := zipEntry(t, backup.Body.Bytes(), "backup-manifest.json")
	var manifest model.BackupManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Files = append(manifest.Files, model.BackupFileManifest{ID: "f_escape", ArchivePath: "../secret.txt", Size: 1, SHA256: "x"})
	changed, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	archive := rewriteZipEntry(t, backup.Body.Bytes(), "backup-manifest.json", changed)
	if w := postRestore(t, h, archive); w.Code != http.StatusConflict {
		t.Fatalf("restore accepted unsafe manifest path: %d %s", w.Code, w.Body.String())
	}
}

// Uploads and restores both rewrite uploads/, so they must be serialised: no
// request may fail because its blob vanished mid-flight, and the store must stay
// readable afterwards. Run with -race to catch lock ordering mistakes.
func TestConcurrentUploadsAndRestore(t *testing.T) {
	_, h, _ := newTestServer(t)
	if w := upload(t, h, "seed.txt", []byte("seed"), ""); w.Code != http.StatusCreated {
		t.Fatalf("seed upload=%d", w.Code)
	}
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup=%d", backup.Code)
	}
	archive := backup.Body.Bytes()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan string, 16)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// Distinct loopback addresses avoid the per-IP concurrency gate so
			// this test exercises the uploads lock, not the rate limiter.
			if w := uploadFrom(t, h, "127.0.0.10:"+strconv.Itoa(i+1), "concurrent.txt", bytes.Repeat([]byte("x"), 32*1024)); w.Code != http.StatusCreated {
				errs <- w.Body.String()
			}
		}(i)
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if w := postRestoreFrom(t, h, "127.0.0.20:"+strconv.Itoa(i+1), archive); w.Code != http.StatusOK {
				errs <- w.Body.String()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("concurrent request failed: %s", msg)
	}
	if w := request(t, h, "GET", "/api/v1/files", nil, "", ""); w.Code != http.StatusOK {
		t.Fatalf("store unreadable after concurrent restore: %d", w.Code)
	}
}

// Export and restore must agree on backup_max_mb; an archive above the limit is
// refused instead of being half-applied.
func TestBackupLimitAppliesToExportAndRestore(t *testing.T) {
	_, h, cfg := newTestServer(t)
	current := cfg.Get()
	current.BackupMaxMB = 1
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	if resp := upload(t, h, "big.bin", bytes.Repeat([]byte("x"), 2<<20), ""); resp.Code != http.StatusCreated {
		t.Fatalf("upload=%d %s", resp.Code, resp.Body.String())
	}
	if backup := request(t, h, "GET", "/api/v1/backup", nil, "", ""); backup.Code != http.StatusInternalServerError {
		t.Fatalf("export above the limit succeeded: %d %s", backup.Code, backup.Body.String())
	}
	// A larger archive must be rejected by restore under the same limit.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"omnishare.json", "config.json", "backup-manifest.json"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("{}"))
	}
	w, err := zw.Create("uploads/blobs/big")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(bytes.Repeat([]byte("y"), 2<<20))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := postRestore(t, h, buf.Bytes()); got.Code != http.StatusConflict {
		t.Fatalf("restore above the limit succeeded: %d %s", got.Code, got.Body.String())
	}
}
