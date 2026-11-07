package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rawGet(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:12345"
	if token != "" {
		r.Header.Set("X-OmniShare-Init-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// An uninitialised instance denies every /api/v1 read by default. Health stays
// public for the desktop watchdog, and the loopback init token keeps the
// first-run browser flow usable.
func TestUninitialisedReadsAreDeniedUnlessToken(t *testing.T) {
	s, h, cfg := newTestServer(t)
	if cfg.HasAccessKey() {
		t.Fatal("test server unexpectedly initialised")
	}
	if w := rawGet(t, h, "/api/v1/notes", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated read=%d %s", w.Code, w.Body.String())
	}
	if body := rawGet(t, h, "/api/v1/notes", "").Body.String(); !strings.Contains(body, "not initialised") {
		t.Fatalf("denial should explain the first-run flow: %s", body)
	}
	if w := rawGet(t, h, "/api/v1/notes", s.initToken); w.Code != http.StatusOK {
		t.Fatalf("token-authenticated read=%d %s", w.Code, w.Body.String())
	}
	if w := rawGet(t, h, "/api/v1/health", ""); w.Code != http.StatusOK {
		t.Fatalf("health must stay public: %d", w.Code)
	}

	// A token from a non-loopback peer is not enough.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notes", nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "192.168.1.5:12345"
	r.Header.Set("X-OmniShare-Init-Token", s.initToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("LAN token read=%d", w.Code)
	}

	// Once a key exists the init token is inert and only the key works.
	current := cfg.Get()
	current.AccessKey = "0123456789abcdef-nodekey"
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	s.finishInitialization()
	if w := rawGet(t, h, "/api/v1/notes", s.initToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("stale init token accepted after initialisation: %d", w.Code)
	}
	if w := rawGet(t, h, "/api/v1/notes", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("keyless read accepted after initialisation: %d", w.Code)
	}
}
