package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnicodeKeyTransportAndExplicitDisable(t *testing.T) {
	_, h, cfg := newTestServer(t)
	key := " 🔑中文\t1 "
	req := cfg.Get()
	on := true
	req.AccessKeyEnabled = &on
	req.AccessKey = key
	if err := cfg.Update(req); err != nil {
		t.Fatal(err)
	}
	get := func(encoded string) int {
		r := httptest.NewRequest("GET", "/api/v1/notes", nil)
		r.Host = "127.0.0.1:8081"
		r.RemoteAddr = "192.168.1.5:50000"
		r.Header.Set("X-OmniShare-Key-Base64", encoded)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if get(base64.StdEncoding.EncodeToString([]byte(key))) != 200 {
		t.Fatal("UTF-8 key rejected")
	}
	if get("invalid-base64") != 401 || get(base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(key)))) != 401 {
		t.Fatal("key normalized or invalid encoding accepted")
	}
	off := false
	req = cfg.Get()
	req.AccessKeyEnabled = &off
	req.KeepAccessKey = true
	if err := cfg.Update(req); err != nil {
		t.Fatal(err)
	}
	if get("invalid-base64") != 200 {
		t.Fatal("disabled switch still verifies keys")
	}
}

func TestLiteralReservedWordsCanBeUsedAsKeys(t *testing.T) {
	for _, key := range []string{"CLEAR", "__KEEP__"} {
		_, h, _ := newTestServer(t)
		payload, _ := json.Marshal(map[string]any{"access_key_enabled": true, "access_key": key, "allow_lan": true})
		if w := configWrite(t, h, string(payload), "", "", "192.168.1.2:50000"); w.Code != 200 {
			t.Fatalf("set %q: %d %s", key, w.Code, w.Body.String())
		}
		if w := request(t, h, http.MethodGet, "/api/v1/notes", nil, key, ""); w.Code != 200 {
			t.Fatalf("literal %q did not authenticate", key)
		}
	}
}
