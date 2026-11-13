package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func rawGet(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "192.168.1.5:12345"
	if token != "" {
		r.Header.Set("X-OmniShare-Init-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFreshLANReadsNeedNeitherKeyNorInitToken(t *testing.T) {
	s, h, cfg := newTestServer(t)
	if cfg.AccessKeyEnabled() || !cfg.Get().AllowLAN {
		t.Fatal("incorrect LAN defaults")
	}
	if s.InitToken() != "" || s.InitTokenPath() != "" {
		t.Fatal("obsolete token exposed")
	}
	for _, path := range []string{"/api/v1/notes", "/api/v1/config", "/api/v1/devices", "/api/v1/health"} {
		for _, token := range []string{"", "obsolete-token"} {
			if w := rawGet(t, h, path, token); w.Code != http.StatusOK {
				t.Fatalf("open %s = %d: %s", path, w.Code, w.Body.String())
			}
		}
	}
	current := cfg.Get()
	current.AccessKey = "1"
	enabled := true
	current.AccessKeyEnabled = &enabled
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "obsolete-token"} {
		if w := rawGet(t, h, "/api/v1/notes", token); w.Code != http.StatusUnauthorized {
			t.Fatalf("enabled protection bypassed: %d", w.Code)
		}
	}
}
