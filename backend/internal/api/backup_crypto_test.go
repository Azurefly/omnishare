package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"omnishare/internal/model"
)

func downloadBackup(t *testing.T, h http.Handler, password string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/backup", nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:12345"
	if password != "" {
		r.Header.Set("X-OmniShare-Backup-Password", password)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func postRestorePassword(t *testing.T, h http.Handler, archive []byte, password string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("backup", "backup.osb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/restore", &body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:55"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-OmniShare-Confirm", "RESTORE-BACKUP")
	if password != "" {
		r.Header.Set("X-OmniShare-Backup-Password", password)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Encrypted export -> encrypted restore round trip, including header/manifest
// validation and wrong-password rejection.
func TestEncryptedBackupRoundTrip(t *testing.T) {
	_, h, _ := newTestServer(t)
	if w := upload(t, h, "secret.txt", []byte("payload"), ""); w.Code != 201 {
		t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
	}

	w := downloadBackup(t, h, "correct horse battery staple")
	if w.Code != 200 {
		t.Fatalf("backup failed: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), ".osb") {
		t.Fatalf("encrypted backup should use the .osb extension, got %q", w.Header().Get("Content-Disposition"))
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, backupMagic) {
		t.Fatal("encrypted backup does not start with the container magic")
	}
	// The header must authenticate and decrypt into a valid ZIP with the same
	// manifest the header advertised.
	var decrypted bytes.Buffer
	header, err := DecryptBackup(bytes.NewReader(body), &decrypted, "correct horse battery staple")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if header.FormatVersion != backupFormatVersion || header.Cipher != "AES-256-GCM" || header.KDF != "PBKDF2-HMAC-SHA256" {
		t.Fatalf("unexpected header: %+v", header)
	}
	if header.Manifest.FormatVersion != 1 || header.Manifest.StateSHA256 == "" {
		t.Fatal("header manifest is incomplete")
	}
	if got := zipEntry(t, decrypted.Bytes(), "backup-manifest.json"); !bytes.Contains(got, []byte(header.Manifest.StateSHA256)) {
		t.Fatal("header manifest does not match the archived manifest")
	}

	// Same server restore: the package must be accepted with the right password.
	if resp := postRestorePassword(t, h, body, "correct horse battery staple"); resp.Code != 200 {
		t.Fatalf("encrypted restore failed: %d %s", resp.Code, resp.Body.String())
	}
	// Missing password is a clear 400, not a corrupt archive.
	if resp := postRestorePassword(t, h, body, ""); resp.Code != 400 || !strings.Contains(resp.Body.String(), "password") {
		t.Fatalf("missing password should be rejected with 400, got %d %s", resp.Code, resp.Body.String())
	}
	// Wrong password must fail authentication.
	if resp := postRestorePassword(t, h, body, "wrong password"); resp.Code != 400 {
		t.Fatalf("wrong password should be rejected, got %d %s", resp.Code, resp.Body.String())
	}
	// A flipped ciphertext byte must fail too.
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-5] ^= 0xff
	if resp := postRestorePassword(t, h, tampered, "correct horse battery staple"); resp.Code != 400 {
		t.Fatalf("tampered package should be rejected, got %d %s", resp.Code, resp.Body.String())
	}
	// Plain ZIP restores are still accepted for older backups (no password header).
	if resp := postRestore(t, h, decrypted.Bytes()); resp.Code != 200 {
		t.Fatalf("plain zip restore failed: %d %s", resp.Code, resp.Body.String())
	}
}

// The encrypted container is a real stream: a multi-megabyte payload is
// encrypted and decrypted without buffering the whole file in memory.
func TestEncryptedBackupStreamsLargePayload(t *testing.T) {
	password := "streaming password"
	manifest := modelManifest()
	var out bytes.Buffer
	enc, err := NewBackupEncryptWriter(&out, password, manifest)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), (3<<20)/16) // 3 MiB
	if _, err := enc.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	cipherLen := out.Len()
	ciphertext := append([]byte(nil), out.Bytes()...)
	var decoded bytes.Buffer
	if _, err := DecryptBackup(bytes.NewReader(ciphertext), &decoded, password); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(decoded.Bytes(), payload) {
		t.Fatal("streaming round trip corrupted the payload")
	}
	if cipherLen < len(payload) {
		t.Fatal("ciphertext should not shrink")
	}
}

func modelManifest() (manifest model.BackupManifest) {
	manifest.FormatVersion = 1
	return manifest
}
