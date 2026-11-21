package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configWrite(t *testing.T, h http.Handler, payload, key, token, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(payload))
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("X-OmniShare-Key", key)
	}
	if token != "" {
		r.Header.Set("X-OmniShare-Init-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// LAN-first mode does not require a bootstrap secret; only the optional switch
// enables authentication, and an enabled key is required to switch it off.
func TestOptionalKeySwitchWithoutInitializationToken(t *testing.T) {
	s, h, cfg := newTestServer(t)
	if _, err := os.Stat(filepath.Join(cfg.Get().DataDir, "init-token")); !os.IsNotExist(err) {
		t.Fatalf("legacy token created: %v", err)
	}
	if s.InitToken() != "" {
		t.Fatal("token exposed")
	}
	payload := `{"node_name":"LAN node","allow_lan":true,"access_key_enabled":true,"access_key":"1"}`
	if w := configWrite(t, h, payload, "", "", "192.168.1.5:12345"); w.Code != 200 {
		t.Fatalf("enable=%d %s", w.Code, w.Body.String())
	}
	if !cfg.AccessKeyEnabled() || !cfg.VerifyAccessKey("1") {
		t.Fatal("short key not enabled")
	}
	disable := `{"allow_lan":true,"access_key_enabled":false,"keep_access_key":true}`
	if w := configWrite(t, h, disable, "", "obsolete", "127.0.0.1:12345"); w.Code != 401 {
		t.Fatalf("unauthorized disable=%d", w.Code)
	}
	if w := configWrite(t, h, disable, "1", "", "192.168.1.5:12345"); w.Code != 200 {
		t.Fatalf("disable=%d %s", w.Code, w.Body.String())
	}
	if cfg.AccessKeyEnabled() {
		t.Fatal("switch did not disable protection")
	}
	if w := rawGet(t, h, "/api/v1/notes", ""); w.Code != 200 {
		t.Fatalf("disabled read=%d", w.Code)
	}
	enable := `{"allow_lan":true,"access_key_enabled":true,"keep_access_key":true}`
	if w := configWrite(t, h, enable, "", "", "192.168.1.5:12345"); w.Code != 200 {
		t.Fatalf("reenable=%d", w.Code)
	}
	if !cfg.VerifyAccessKey("1") || cfg.VerifyAccessKey("wrong") {
		t.Fatal("saved secret not preserved")
	}
}
