package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnishare/internal/model"
)

type unexpectedParentRead struct{}

func (unexpectedParentRead) Read([]byte) (int, error) {
	panic("standalone service must not read stdin")
}

func TestParentInputIsIgnoredUnlessExplicitlyEnabled(t *testing.T) {
	if got := parentInputClosed(false, unexpectedParentRead{}); got != nil {
		t.Fatal("standalone service enabled stdin shutdown")
	}
}

func TestParentInputRemainsOpenUntilEOF(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	closed := parentInputClosed(true, reader)
	if _, err := io.WriteString(writer, "input is not a shutdown command"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		t.Fatal("stdin content triggered shutdown before EOF")
	default:
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("stdin EOF did not request shutdown")
	}
}

// Re-enter the real command in an isolated test process rather than replacing
// its HTTP server or shutdown path with a mock.
func TestParentPipeChildProcess(t *testing.T) {
	if os.Getenv("OMNISHARE_TEST_PARENT_PIPE") != "1" {
		t.Skip("subprocess entry point")
	}
	flag.CommandLine = flag.NewFlagSet("omnishare", flag.ExitOnError)
	os.Args = []string{os.Args[0], "--no-browser", "--shutdown-on-stdin-close", "--listen", "127.0.0.1",
		"--port", os.Getenv("OMNISHARE_TEST_PARENT_PORT"), "--data-dir", os.Getenv("OMNISHARE_TEST_PARENT_DATA")}
	main()
}

func TestDesktopParentPipeShutdownPreservesBufferedCounters(t *testing.T) {
	dir := t.TempDir()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	_ = reservation.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestParentPipeChildProcess$")
	command.Env = append(os.Environ(), "OMNISHARE_TEST_PARENT_PIPE=1", "OMNISHARE_TEST_PARENT_DATA="+dir,
		fmt.Sprintf("OMNISHARE_TEST_PARENT_PORT=%d", port))
	// The backend logs a first-run URL. Keep that credential out of test output.
	command.Stdout, command.Stderr = io.Discard, io.Discard
	parent, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	t.Cleanup(func() {
		_ = parent.Close()
		if !waited {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
			}
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			waited = true
			t.Fatalf("desktop child exited before readiness: %v", err)
		default:
		}
		response, err := client.Get(base + "/api/v1/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("desktop child did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "init-token")); !os.IsNotExist(err) {
		t.Fatal("desktop first run unexpectedly created an init token")
	}
	request := func(method, path, body string, status int) model.QuickNote {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("%s %s returned %d, want %d", method, path, response.StatusCode, status)
		}
		var result struct {
			Data model.QuickNote `json:"data"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	note := request(http.MethodPost, "/api/v1/notes", `{"content":"desktop shutdown persistence"}`, http.StatusCreated)
	read := request(http.MethodGet, "/api/v1/notes/"+note.ID, "", http.StatusOK)
	if read.ReadCount != 1 {
		t.Fatalf("initial read count = %d", read.ReadCount)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Fatalf("desktop child did not exit cleanly after EOF: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("desktop child ignored parent pipe closure")
	}
	store, _, err := openStorage(dir, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetNote(note.ID)
	if err != nil || got.Content != note.Content || got.ReadCount != 1 {
		t.Fatalf("normal desktop shutdown lost buffered state: note=%+v, err=%v", got, err)
	}
}
