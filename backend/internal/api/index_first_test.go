package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Observe actual source-node HTTP requests as well as receiver disk state.
// Binding and polling must never fetch, stage or replicate original file bytes.
func TestWorkspaceIndexOnlyUntilExplicitFileRead(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	var media, tickets atomic.Int32
	original := b.http.Config.Handler
	b.http.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/media/") || strings.HasSuffix(r.URL.Path, "/download") || strings.HasSuffix(r.URL.Path, "/stream") {
			media.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/ticket") {
			tickets.Add(1)
		}
		original.ServeHTTP(w, r)
	})
	payload := bytes.Repeat([]byte("origin-file-not-replicated"), 4096)
	var body bytes.Buffer
	multipartBody := multipart.NewWriter(&body)
	part, err := multipartBody.CreateFormFile("file", "source.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = multipartBody.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, b.http.URL+"/api/v1/files/upload", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", multipartBody.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var packet map[string]interface{}
	if err = json.NewDecoder(response.Body).Decode(&packet); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatal(packet)
	}
	id := packet["data"].(map[string]interface{})["id"].(string)
	assertNotCopied := func() {
		t.Helper()
		if len(a.s.store.ListFiles("", "")) != 0 {
			t.Fatal("remote file was replicated into local database")
		}
		err := filepath.WalkDir(filepath.Join(a.cfg.Get().DataDir, "uploads"), func(path string, d os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !d.IsDir() {
				t.Errorf("file bytes appeared in receiver uploads: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	wsBind(t, a, b, "")
	for round := 0; round < 3; round++ {
		for _, resource := range []string{"dashboard", "files", "notes", "pads", "shares", "trash", "audit"} {
			status, result, _ := wsCall(t, a, http.MethodGet, "/api/v1/"+resource+"?scope=all", nil, "")
			if status != http.StatusOK {
				t.Fatalf("%s: %v", resource, result)
			}
			if resource == "files" {
				files := result["data"].([]interface{})
				if len(files) != 1 {
					t.Fatal("source index missing")
				}
				file := files[0].(map[string]interface{})
				if file["id"] != qualifyID(b.cfg.Get().NodeID, id) {
					t.Fatal("index lost source identity")
				}
				if file["file_size"] != float64(len(payload)) {
					t.Fatal("index metadata differs")
				}
			}
		}
	}
	if media.Load() != 0 || tickets.Load() != 0 {
		t.Fatalf("background content fetch: media=%d tickets=%d", media.Load(), tickets.Load())
	}
	assertNotCopied()
	// Even issuing a user-requested ticket is metadata only; the actual GET is lazy.
	status, result, _ := wsCall(t, a, http.MethodPost, "/api/v1/files/"+qualifyID(b.cfg.Get().NodeID, id)+"/ticket", map[string]string{"disposition": "attachment"}, "")
	if status != http.StatusCreated {
		t.Fatal(result)
	}
	if tickets.Load() != 1 || media.Load() != 0 {
		t.Fatal("ticket prefetched file data")
	}
	path := result["data"].(map[string]interface{})["url"].(string)
	request, err = http.NewRequest(http.MethodGet, a.http.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=2-5")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || !bytes.Equal(got, payload[2:6]) {
		t.Fatalf("on-demand range mismatch: %d %q", response.StatusCode, got)
	}
	if media.Load() != 1 {
		t.Fatalf("expected one explicit file read, got %d", media.Load())
	}
	assertNotCopied()
}
