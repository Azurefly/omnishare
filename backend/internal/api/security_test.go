package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// Any loopback process used to be able to claim an uninitialised instance by
// writing the first config. Only the token from the 0600 file may do that, and
// it must stop working once an access key exists.
func TestConfigWritesRequireOneTimeInitToken(t *testing.T) {
	s, h, cfg := newTestServer(t)
	if cfg.HasAccessKey() {
		t.Fatal("test server unexpectedly started initialised")
	}
	if s.initToken == "" || len(s.initToken) < 32 {
		t.Fatal("server did not generate an init token")
	}
	tokenPath := s.InitTokenPath()
	if tokenPath != filepath.Join(cfg.Get().DataDir, initTokenFile) {
		t.Fatalf("init token path=%q", tokenPath)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(tokenPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("init token mode=%o", info.Mode().Perm())
		}
	}

	const secure = "0123456789abcdef-secure"
	securePayload := `{"node_name":"init-node","access_key":"` + secure + `"}`

	if w := configWrite(t, h, securePayload, "", "", "127.0.0.1:12345"); w.Code != http.StatusForbidden {
		t.Fatalf("config write without token=%d %s", w.Code, w.Body.String())
	}
	if w := configWrite(t, h, securePayload, "", "wrong-token", "127.0.0.1:12345"); w.Code != http.StatusForbidden {
		t.Fatalf("config write with wrong token=%d %s", w.Code, w.Body.String())
	}
	// The token must not be usable from a non-loopback peer even when the
	// listener is somehow reachable there.
	if w := configWrite(t, h, securePayload, "", s.initToken, "192.168.1.5:12345"); w.Code != http.StatusForbidden {
		t.Fatalf("config write from LAN=%d %s", w.Code, w.Body.String())
	}
	if w := configWrite(t, h, securePayload, "", s.initToken, "127.0.0.1:12345"); w.Code != http.StatusOK {
		t.Fatalf("config write with token=%d %s", w.Code, w.Body.String())
	}
	if !cfg.HasAccessKey() || !cfg.VerifyAccessKey(secure) {
		t.Fatal("token-authorized write did not activate the access key")
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("init token survived initialisation: %v", err)
	}
	if s.InitTokenPath() != "" {
		t.Fatal("server still advertises an init token after the key was set")
	}
	// Once initialised, the token header is inert and the access key is required.
	if w := configWrite(t, h, `{"node_name":"token-replay"}`, "", s.initToken, "127.0.0.1:12345"); w.Code != http.StatusUnauthorized {
		t.Fatalf("token replay after initialisation=%d %s", w.Code, w.Body.String())
	}
	if w := configWrite(t, h, `{"node_name":"keyed-node","access_key":"__KEEP__"}`, secure, "", "127.0.0.1:12345"); w.Code != http.StatusOK {
		t.Fatalf("keyed config write=%d %s", w.Code, w.Body.String())
	}
}
