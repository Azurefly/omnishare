package delivery

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const publisherToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const guestToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func fixture(t *testing.T) (*Service, Options) {
	t.Helper()
	opts := Options{Root: t.TempDir(), PublicOrigin: "https://example.test", MaxBytes: 1 << 20, CapacityBytes: 2 << 20,
		Publishers: map[string]string{"alice": digest(publisherToken), "bob": digest(guestToken)}}
	server, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return server, opts
}

func submit(t *testing.T, handler http.Handler, manifest Manifest, contents string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	multi := multipart.NewWriter(&body)
	part, err := multi.CreateFormField("manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(part).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) > 0 {
		part, err = multi.CreateFormFile("file", "ignored")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err = multi.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/deliveries", &body)
	r.Header.Set("Content-Type", multi.FormDataContentType())
	if authorized {
		r.Header.Set("X-OmniShare-Publisher", "alice")
		r.Header.Set("Authorization", "Bearer "+publisherToken)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, r)
	return out
}

func TestPublishClaimRevokeAndRestart(t *testing.T) {
	s, opts := fixture(t)
	manifest := Manifest{RequestID: strings.Repeat("1", 64), Token: guestToken, Password: "valid&code", Title: "验收", Article: "<script>alert(1)</script>",
		TTLSeconds: 3600, Files: []File{{Name: "原件.txt", Size: 5, SHA256: digest("proof")}}}
	if out := submit(t, s.Admin(), manifest, "proof", false); out.Code != 401 {
		t.Fatalf("publication bypass: %d", out.Code)
	}
	if out := submit(t, s.Admin(), manifest, "wrong", true); out.Code != 422 {
		t.Fatalf("corrupt upload: %d", out.Code)
	}
	if out := submit(t, s.Public(), manifest, "proof", true); out.Code != 404 {
		t.Fatalf("public endpoint exposes admin: %d", out.Code)
	}
	out := submit(t, s.Admin(), manifest, "proof", true)
	var body packet
	if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &body) != nil || body.Entry == nil {
		t.Fatalf("publish: %d %s", out.Code, out.Body)
	}
	id := body.Entry.ID
	root := sharePath(id)
	if strings.Contains(body.Entry.URL, "?") || !strings.HasSuffix(body.Entry.URL, "#"+guestToken) {
		t.Fatal("token must be sent in the fragment")
	}
	if out := submit(t, s.Admin(), manifest, "proof", true); out.Code != 201 || len(s.entries) != 1 {
		t.Fatal("same request must be idempotent")
	}

	run := func(h http.Handler, method, path, content, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(content))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		o := httptest.NewRecorder()
		h.ServeHTTP(o, r)
		return o
	}
	if o := run(s.Public(), "GET", root+"files/0", "", "", nil); o.Code != 404 {
		t.Fatal("direct file bypass")
	}
	claim := func(password, origin string) *httptest.ResponseRecorder {
		return run(s.Public(), "POST", root+"claim", url.Values{"token": {guestToken}, "password": {password}}.Encode(), origin, nil)
	}
	if o := claim("wrong", "https://example.test"); o.Code != 403 {
		t.Fatal("wrong pickup code was accepted")
	}
	if o := claim("valid&code", "https://other.test"); o.Code != 403 {
		t.Fatal("foreign origin was accepted")
	}
	if o := claim("valid&code", "null"); o.Code != 403 {
		t.Fatal("opaque origin was accepted")
	}
	form := run(s.Public(), "GET", root, "", "", nil)
	if form.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("claim form must send its same-origin browser Origin")
	}
	valid := claim("valid&code", "https://example.test")
	if valid.Code != 303 {
		t.Fatalf("valid claim: %d", valid.Code)
	}
	cookie := valid.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure cookie")
	}

	page := run(s.Public(), "GET", root, "", "", cookie)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "&lt;script&gt;") || strings.Contains(page.Body.String(), "<script>alert") {
		t.Fatal("document escaping failed")
	}
	data := run(s.Public(), "GET", root+"files/0", "", "", cookie)
	if data.Code != 200 || data.Body.String() != "proof" || !strings.HasPrefix(data.Header().Get("Content-Disposition"), "attachment") || data.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("download response failed")
	}
	other := manifest
	other.RequestID = strings.Repeat("2", 64)
	if o := submit(t, s.Admin(), other, "proof", true); o.Code != 201 {
		t.Fatal("second share setup failed")
	}
	otherID := digest("alice:" + other.RequestID)
	if o := run(s.Public(), "GET", sharePath(otherID)+"files/0", "", "", cookie); o.Code != 404 {
		t.Fatal("session leaked between shares")
	}

	revoke := func(owner, token string) int {
		r := httptest.NewRequest("DELETE", "/v1/deliveries/"+id, nil)
		r.Header.Set("X-OmniShare-Publisher", owner)
		r.Header.Set("Authorization", "Bearer "+token)
		o := httptest.NewRecorder()
		s.Admin().ServeHTTP(o, r)
		return o.Code
	}
	if revoke("bob", guestToken) != 404 {
		t.Fatal("cross-publisher revoke allowed")
	}
	if revoke("alice", publisherToken) != 204 {
		t.Fatal("revoke failed")
	}
	restarted, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if o := run(restarted.Public(), "GET", root+"files/0", "", "", cookie); o.Code != 404 {
		t.Fatal("old cookie retained access")
	}
	if o := submit(t, restarted.Admin(), manifest, "proof", true); o.Code != 410 {
		t.Fatal("retry resurrected a revoked share")
	}
}
