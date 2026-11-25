package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalWorkbenchBoundary(t *testing.T) {
	for _, tc := range []struct {
		host, address, origin, peer, forwarded string
		allowed                                bool
	}{
		{"127.0.0.1:8081", "127.0.0.1:5000", "http://127.0.0.1:8081", "", "", true},
		{"localhost", "[::1]:5000", "", "", "", true},
		{"127.0.0.1", "192.0.2.1:5000", "", "", "", false},
		{"example.org", "127.0.0.1:5000", "", "", "", false},
		{"127.0.0.1", "127.0.0.1:5000", "https://example.org", "", "", false},
		{"127.0.0.1", "127.0.0.1:5000", "", "bound", "", false},
		{"127.0.0.1", "127.0.0.1:5000", "", "", "192.0.2.1", false},
	} {
		req := httptest.NewRequest("POST", "/", nil)
		req.Host, req.RemoteAddr = tc.host, tc.address
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("X-OmniShare-Peer", tc.peer)
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := localWorkbench(req); got != tc.allowed {
			t.Errorf("localWorkbench(%+v) = %t", tc, got)
		}
	}
}

func TestLocalFileSelectionAndCopy(t *testing.T) {
	node := newWorkspaceFixture(t, "file input")
	root := t.TempDir()
	dir := filepath.Join(root, "中文 资料", "内层")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "#报告 1.txt")
	const contents = "文件内容"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	uri := (&url.URL{Scheme: "file", Path: slash}).String()
	for _, source := range []string{uri, "'" + path + "'", path} {
		p, _, err := node.s.externalPath(source)
		if err != nil || filepath.Base(p) != filepath.Base(path) {
			t.Fatalf("parse %q: %q, %v", source, p, err)
		}
	}
	status, body, _ := wsCall(t, node, "POST", "/api/v1/local-files/selection", map[string]any{"paths": []string{filepath.Dir(dir)}}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	files := body["data"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["relative_folder"] != "中文 资料/内层" {
		t.Fatal(files)
	}
	for _, invalid := range []string{node.cfg.Get().DataDir, "file://other.example/docs/a", "relative/a", path + ".missing"} {
		if _, _, err := node.s.externalPath(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	postPath := func(route, value string, confirm bool) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		form := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"local_path": value, "folder": "目标", "ttl_seconds": "86400"} {
			if err := form.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", route, &buf)
		req.Host, req.RemoteAddr = "127.0.0.1:8081", "127.0.0.1:4567"
		req.Header.Set("Content-Type", form.FormDataContentType())
		if confirm {
			req.Header.Set("X-OmniShare-Confirm", "RESTORE-BACKUP")
		}
		w := httptest.NewRecorder()
		node.s.Handler().ServeHTTP(w, req)
		return w
	}
	if got := postPath("/api/v1/files/upload", uri, false); got.Code != 201 {
		t.Fatal(got.Code, got.Body.String())
	}
	saved := node.s.store.AllFiles()
	if len(saved) != 1 || saved[0].FileName != filepath.Base(path) || saved[0].Folder != "目标" || saved[0].ExpiresAt == nil ||
		saved[0].FileHash != hexDigest([]byte(contents)) || shortcutBlob(saved[0].StoragePath) {
		t.Fatal(saved)
	}
	backup := downloadBackup(t, node.s.Handler(), "")
	if backup.Code != 200 {
		t.Fatal(backup.Code, backup.Body.String())
	}
	archive := filepath.Join(root, "恢复 备份.zip")
	if err := os.WriteFile(archive, backup.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if got := postPath("/api/v1/restore", archive, false); got.Code != http.StatusBadRequest {
		t.Fatal(got.Code)
	}
	if got := postPath("/api/v1/restore", archive, true); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("original backup lost: %v", err)
	}
}
