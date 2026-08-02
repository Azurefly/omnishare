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
	"strings"
	"testing"
	"time"

	"omnishare/internal/config"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func newTestServer(t *testing.T) (*Server, http.Handler, *config.Manager) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.New(dir, 8081, "test-node")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, cfg)
	return s, s.Handler(), cfg
}
func request(t *testing.T, h http.Handler, method, path string, body io.Reader, key, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:12345"
	if key != "" {
		r.Header.Set("X-OmniShare-Key", key)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func decodeData[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, w.Body.String())
	}
	var data T
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatal(err)
		}
	}
	return data
}
func upload(t *testing.T, h http.Handler, name string, content []byte, key string) *httptest.ResponseRecorder {
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
	return request(t, h, "POST", "/api/v1/files/upload", &body, key, mw.FormDataContentType())
}

func TestSecurityHeadersCORSHostAndStrictJSON(t *testing.T) {
	_, h, _ := newTestServer(t)
	w := request(t, h, "GET", "/api/v1/health", nil, "", "")
	if w.Code != 200 {
		t.Fatalf("health=%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "object-src 'none'") {
		t.Fatal("CSP missing")
	}
	data := decodeData[map[string]interface{}](t, w)
	if _, ok := data["node_name"]; ok {
		t.Fatal("public health leaked node identity")
	}
	r := httptest.NewRequest("PUT", "http://127.0.0.1:8081/api/v1/config", strings.NewReader(`{"node_name":"pwn"}`))
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:2"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	cw := httptest.NewRecorder()
	h.ServeHTTP(cw, r)
	if cw.Code != 403 {
		t.Fatalf("CORS=%d %s", cw.Code, cw.Body.String())
	}
	badHost := httptest.NewRequest("GET", "http://attacker.example/api/v1/health", nil)
	badHost.Host = "attacker.example"
	bw := httptest.NewRecorder()
	h.ServeHTTP(bw, badHost)
	if bw.Code != 400 {
		t.Fatalf("host=%d", bw.Code)
	}
	trailing := request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"a"}{"content":"b"}`), "", "application/json")
	if trailing.Code != 400 {
		t.Fatalf("trailing=%d", trailing.Code)
	}
}

func TestAuthProtectionNoQueryKeyAndRateLimit(t *testing.T) {
	s, h, cfg := newTestServer(t)
	current := cfg.Get()
	current.AccessKey = "0123456789abcdef"
	current.AllowLAN = true
	current.ListenAddress = "0.0.0.0"
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	if w := request(t, h, "GET", "/api/v1/dashboard?key=0123456789abcdef", nil, "", ""); w.Code != 401 {
		t.Fatalf("query key accepted=%d", w.Code)
	}
	if w := request(t, h, "GET", "/api/v1/dashboard", nil, "0123456789abcdef", ""); w.Code != 200 {
		t.Fatalf("authorized=%d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 5; i++ {
		_ = request(t, h, "POST", "/api/v1/auth/verify", nil, "bad-bad-bad-bad", "")
	}
	blocked := request(t, h, "POST", "/api/v1/auth/verify", nil, "bad-bad-bad-bad", "")
	if blocked.Code != 429 {
		t.Fatalf("rate limit=%d", blocked.Code)
	}
	if len(s.store.ListAudits(100)) < 5 {
		t.Fatal("auth attempts not audited")
	}
}

func TestNoteExpiryHeadAndCRUD(t *testing.T) {
	_, h, _ := newTestServer(t)
	body := `{"content":"hello #alpha","tags":["manual"],"pinned":true,"max_read_count":2,"ttl_seconds":60}`
	w := request(t, h, "POST", "/api/v1/notes", strings.NewReader(body), "", "application/json")
	if w.Code != 201 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	n := decodeData[model.QuickNote](t, w)
	head := request(t, h, "HEAD", "/api/v1/notes/"+n.ID, nil, "", "")
	if head.Code != 200 {
		t.Fatalf("head=%d", head.Code)
	}
	read := request(t, h, "GET", "/api/v1/notes/"+n.ID, nil, "", "")
	got := decodeData[model.QuickNote](t, read)
	if got.ReadCount != 1 {
		t.Fatalf("head consumed read: %+v", got)
	}
	empty := request(t, h, "PUT", "/api/v1/notes/"+n.ID, strings.NewReader(`{"content":"","is_burn_after_read":true,"max_read_count":3}`), "", "application/json")
	if empty.Code != 200 {
		t.Fatalf("empty update=%d %s", empty.Code, empty.Body.String())
	}
	exp := request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"short","ttl_seconds":1}`), "", "application/json")
	expired := decodeData[model.QuickNote](t, exp)
	time.Sleep(1100 * time.Millisecond)
	if w := request(t, h, "GET", "/api/v1/notes/"+expired.ID, nil, "", ""); w.Code != 404 {
		t.Fatalf("expired readable=%d", w.Code)
	}
}

func TestStreamingUploadPhysicalDedupTicketRangeAndBackup(t *testing.T) {
	s, h, _ := newTestServer(t)
	content := []byte("range-test-content")
	first := upload(t, h, "demo.txt", content, "")
	if first.Code != 201 {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	f1 := decodeData[model.FileAsset](t, first)
	second := upload(t, h, "other-name.txt", content, "")
	if second.Code != 201 {
		t.Fatalf("second=%d %s", second.Code, second.Body.String())
	}
	f2 := decodeData[model.FileAsset](t, second)
	if f1.ID == f2.ID || f1.FileHash != f2.FileHash {
		t.Fatalf("logical dedup wrong f1=%+v f2=%+v", f1, f2)
	}
	all := s.store.AllFiles()
	if len(all) != 2 || all[0].StoragePath != all[1].StoragePath {
		t.Fatalf("physical blob not deduped: %+v", all)
	}
	ticketResp := request(t, h, "POST", "/api/v1/files/"+f1.ID+"/ticket", strings.NewReader(`{"disposition":"inline"}`), "", "application/json")
	if ticketResp.Code != 201 {
		t.Fatalf("ticket=%d %s", ticketResp.Code, ticketResp.Body.String())
	}
	ticket := decodeData[map[string]interface{}](t, ticketResp)
	ticketURL := ticket["url"].(string)
	path := strings.TrimPrefix(ticketURL, "http://127.0.0.1:8081")
	r := httptest.NewRequest("GET", path, nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:3"
	r.Header.Set("Range", "bytes=0-4")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusPartialContent || rw.Body.String() != "range" {
		t.Fatalf("range=%d %q", rw.Code, rw.Body.String())
	}
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != 200 {
		t.Fatalf("backup=%d %s", backup.Code, backup.Body.String())
	}
	zipPath := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(zipPath, backup.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, entry := range zr.File {
		names[entry.Name] = true
	}
	for _, required := range []string{"omnishare.json", "config.json", "backup-manifest.json"} {
		if !names[required] {
			t.Fatalf("missing %s", required)
		}
	}
}

func TestShareHEADDoesNotConsumeRangeUsesSessionAndHostNotInjected(t *testing.T) {
	_, h, _ := newTestServer(t)
	noteResp := request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"share me safely"}`), "", "application/json")
	note := decodeData[model.QuickNote](t, noteResp)
	shareResp := request(t, h, "POST", "/api/v1/shares", strings.NewReader(`{"object_type":"note","object_id":"`+note.ID+`","ttl_seconds":3600,"max_access_count":1}`), "", "application/json")
	if shareResp.Code != 201 {
		t.Fatalf("share=%d %s", shareResp.Code, shareResp.Body.String())
	}
	share := decodeData[model.ShareLink](t, shareResp)
	if strings.Contains(share.URL, "attacker") || !strings.HasPrefix(share.URL, "http://127.0.0.1:8081/s/") {
		t.Fatalf("url=%s", share.URL)
	}
	head := request(t, h, "HEAD", "/s/"+share.Token, nil, "", "")
	if head.Code != 200 {
		t.Fatalf("head=%d", head.Code)
	}
	first := request(t, h, "GET", "/s/"+share.Token, nil, "", "")
	if first.Code != 200 || !strings.Contains(first.Body.String(), "share me safely") {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	cookie := first.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("share session cookie missing")
	}
	// Same browser session can re-fetch without consuming another access.
	r := httptest.NewRequest("GET", "/s/"+share.Token, nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:4"
	r.AddCookie(cookie[0])
	again := httptest.NewRecorder()
	h.ServeHTTP(again, r)
	if again.Code != 200 {
		t.Fatalf("session fetch=%d", again.Code)
	}
	// A new session is exhausted.
	if exhausted := request(t, h, "GET", "/s/"+share.Token, nil, "", ""); exhausted.Code != 410 {
		t.Fatalf("exhausted=%d", exhausted.Code)
	}
}

func TestPadConflictTrashAndRestoreBackup(t *testing.T) {
	_, h, _ := newTestServer(t)
	padResp := request(t, h, "POST", "/api/v1/pads", strings.NewReader(`{"Title":"Plan","Content":"v1"}`), "", "application/json")
	if padResp.Code != 201 {
		t.Fatalf("pad=%d %s", padResp.Code, padResp.Body.String())
	}
	p := decodeData[model.PadDocument](t, padResp)
	if w := request(t, h, "PUT", "/api/v1/pads/"+p.ID, strings.NewReader(`{"Title":"Plan","Content":"v2","Version":1}`), "", "application/json"); w.Code != 200 {
		t.Fatalf("save=%d %s", w.Code, w.Body.String())
	}
	if w := request(t, h, "PUT", "/api/v1/pads/"+p.ID, strings.NewReader(`{"Title":"Plan","Content":"stale","Version":1}`), "", "application/json"); w.Code != 409 {
		t.Fatalf("conflict=%d", w.Code)
	}
	_ = request(t, h, "DELETE", "/api/v1/pads/"+p.ID, nil, "", "")
	trash := request(t, h, "GET", "/api/v1/trash?type=pad", nil, "", "")
	items := decodeData[[]model.TrashItem](t, trash)
	if len(items) != 1 {
		t.Fatalf("trash=%+v", items)
	}
	if w := request(t, h, "POST", "/api/v1/trash/pad/"+p.ID+"/restore", nil, "", ""); w.Code != 200 {
		t.Fatalf("restore=%d", w.Code)
	}
}

func TestBackupRestoreIncludesConfigStateAndWorksWithoutFiles(t *testing.T) {
	_, h, cfg := newTestServer(t)
	current := cfg.Get()
	current.NodeName = "backup-node"
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	originalResp := request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"keep after restore"}`), "", "application/json")
	original := decodeData[model.QuickNote](t, originalResp)
	backup := request(t, h, "GET", "/api/v1/backup", nil, "", "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup=%d %s", backup.Code, backup.Body.String())
	}
	changed := cfg.Get()
	changed.NodeName = "changed-node"
	if err := cfg.Update(changed); err != nil {
		t.Fatal(err)
	}
	extraResp := request(t, h, "POST", "/api/v1/notes", strings.NewReader(`{"content":"remove after restore"}`), "", "application/json")
	extra := decodeData[model.QuickNote](t, extraResp)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("backup", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(backup.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/restore", &body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:55"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-OmniShare-Confirm", "RESTORE-BACKUP")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("restore=%d %s", w.Code, w.Body.String())
	}
	if cfg.Public().NodeName != "backup-node" {
		t.Fatalf("config not restored: %+v", cfg.Public())
	}
	if got := request(t, h, "GET", "/api/v1/notes/"+original.ID+"/manage", nil, "", ""); got.Code != http.StatusOK {
		t.Fatalf("original note missing after restore: %d", got.Code)
	}
	if got := request(t, h, "GET", "/api/v1/notes/"+extra.ID+"/manage", nil, "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("post-backup note survived restore: %d", got.Code)
	}
}

func TestConfigAPIActuallySetsWriteOnlyAccessKey(t *testing.T) {
	_, h, cfg := newTestServer(t)
	secret := "0123456789abcdef-secure"
	payload := `{"node_name":"secured-node","listen_address":"127.0.0.1","allow_lan":false,"max_upload_mb":128,"retention_days":30,"trash_retention_days":14,"auto_open_browser":false,"access_key":"` + secret + `","peers":[],"allowed_origins":[]}`
	w := request(t, h, http.MethodPut, "/api/v1/config", strings.NewReader(payload), "", "application/json")
	if w.Code != http.StatusOK {
		t.Fatalf("set key=%d %s", w.Code, w.Body.String())
	}
	if !cfg.HasAccessKey() || !cfg.VerifyAccessKey(secret) {
		t.Fatal("API accepted config but did not activate access key")
	}
	if got := request(t, h, http.MethodGet, "/api/v1/dashboard", nil, "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unkeyed request after key setup=%d", got.Code)
	}
	if got := request(t, h, http.MethodGet, "/api/v1/dashboard", nil, secret, ""); got.Code != http.StatusOK {
		t.Fatalf("keyed request=%d %s", got.Code, got.Body.String())
	}
	persisted, err := os.ReadFile(filepath.Join(cfg.Get().DataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(secret)) || bytes.Contains(persisted, []byte(`"access_key"`)) {
		t.Fatalf("plaintext key persisted: %s", persisted)
	}
}
