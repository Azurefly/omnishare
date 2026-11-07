package api

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Uploads must keep working while the archive is being extracted. Only the
// final swap takes the exclusive lock.
func TestUploadWorksWhileRestoreExtracts(t *testing.T) {
	s, h, _ := newTestServer(t)
	if w := upload(t, h, "before.txt", []byte("before"), ""); w.Code != http.StatusCreated {
		t.Fatalf("seed upload: %d %s", w.Code, w.Body.String())
	}
	backup := downloadBackup(t, h, "")
	if backup.Code != http.StatusOK {
		t.Fatalf("backup: %d %s", backup.Code, backup.Body.String())
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("backup", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(backup.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/restore", &body)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = "127.0.0.1:77"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-OmniShare-Confirm", "RESTORE-BACKUP")
	applyTestAuth(r)

	reached := make(chan struct{})
	release := make(chan struct{})
	s.restorePreSwapHook = func() {
		close(reached)
		<-release
	}
	dst := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		dst <- w.Code
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("restore never reached the pre-swap point")
	}

	var upBody bytes.Buffer
	uw := multipart.NewWriter(&upBody)
	upPart, err := uw.CreateFormFile("file", "during-restore.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = upPart.Write([]byte("concurrent upload"))
	_ = uw.Close()
	uploadDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/files/upload", &upBody)
		r.Host = "127.0.0.1:8081"
		r.RemoteAddr = "127.0.0.1:78"
		r.Header.Set("Content-Type", uw.FormDataContentType())
		applyTestAuth(r)
		h.ServeHTTP(w, r)
		uploadDone <- w.Code
	}()
	select {
	case code := <-uploadDone:
		if code != http.StatusCreated {
			t.Fatalf("upload during restore=%d, want 201", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upload was blocked by the restore")
	}

	close(release)
	select {
	case code := <-dst:
		if code != http.StatusOK {
			t.Fatalf("restore=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish after the swap was released")
	}
}

// A stalled restore connection must be reclaimed by the idle timer instead of
// hanging forever with a zero total timeout.
func TestIdleTimeoutBodyReclaimsStalledUpload(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	old := restoreIdleTimeout
	restoreIdleTimeout = 60 * time.Millisecond
	defer func() { restoreIdleTimeout = old }()

	body := newIdleTimeoutBody(pr, restoreIdleTimeout, nil)
	defer body.Stop()
	done := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 32))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled read returned no error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle timeout did not close the stalled body")
	}
}

// A real net/http request body holds its internal lock during a socket read.
// Closing it from a timer cannot interrupt that read, unlike an io.Pipe.
func TestRestoreIdleTimeoutInterruptsHTTPConnection(t *testing.T) {
	s, h, _ := newTestServer(t)
	old := restoreIdleTimeout
	restoreIdleTimeout = 60 * time.Millisecond
	defer func() { restoreIdleTimeout = old }()
	server := httptest.NewServer(h)
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /api/v1/restore HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=idle-test\r\nContent-Length: 1048576\r\nX-OmniShare-Confirm: RESTORE-BACKUP\r\nX-OmniShare-Init-Token: %s\r\n\r\n--idle-test\r\nContent-Disposition: form-data; name=\"backup\"; filename=\"backup.zip\"\r\nContent-Type: application/zip\r\n\r\n", addr, s.InitToken())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled HTTP upload was not reclaimed by the idle deadline: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("stalled upload status = %d", response.StatusCode)
	}
}

func TestRestoreEarlyRejectKeepsBodyDrainDeadline(t *testing.T) {
	s, h, _ := newTestServer(t)
	old := restoreIdleTimeout
	restoreIdleTimeout = 60 * time.Millisecond
	defer func() { restoreIdleTimeout = old }()
	server := httptest.NewServer(h)
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /api/v1/restore HTTP/1.1\r\nHost: %s\r\nContent-Type: text/plain\r\nContent-Length: 1024\r\nX-OmniShare-Confirm: RESTORE-BACKUP\r\nX-OmniShare-Init-Token: %s\r\n\r\n", addr, s.InitToken())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("early-rejected body drain was not bounded: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("early-rejected upload status = %d", response.StatusCode)
	}
}

func TestRestoreCompletedPartKeepsTailDrainDeadline(t *testing.T) {
	s, h, _ := newTestServer(t)
	old := restoreIdleTimeout
	restoreIdleTimeout = 60 * time.Millisecond
	defer func() { restoreIdleTimeout = old }()
	server := httptest.NewServer(h)
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Complete the selected file part, but promise additional HTTP bytes and
	// withhold them. An invalid archive is enough to exercise the early return.
	prefix := "--tail-test\r\nContent-Disposition: form-data; name=\"backup\"; filename=\"bad.zip\"\r\n\r\nnot a zip\r\n--tail-test--\r\n"
	_, err = fmt.Fprintf(conn, "POST /api/v1/restore HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=tail-test\r\nContent-Length: %d\r\nX-OmniShare-Confirm: RESTORE-BACKUP\r\nX-OmniShare-Init-Token: %s\r\n\r\n%s", addr, len(prefix)+1024, s.InitToken(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("completed-part tail drain was not bounded: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("invalid archive status = %d", response.StatusCode)
	}
}
