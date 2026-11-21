package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceMutationInvalidatesOfflineSnapshot(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	_, p, _ := wsCall(t, b, "POST", "/api/v1/notes", map[string]string{"content": "delete this"}, "")
	id := p["data"].(map[string]interface{})["id"].(string)
	wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	status, _, _ := wsCall(t, a, "DELETE", "/api/v1/notes/"+qualifyID(b.cfg.Get().NodeID, id), nil, "")
	if status != 200 {
		t.Fatal("delete failed")
	}
	b.http.Close()
	_, p, _ = wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	if len(p["data"].([]interface{})) != 0 {
		t.Fatal("deleted remote note resurrected from old snapshot")
	}
	if p["workspace"].(map[string]interface{})["partial"] != true {
		t.Fatal("offline status hidden")
	}
}

func TestWorkspaceInFlightListCannotRecacheBeforeMutation(t *testing.T) {
	a := newWorkspaceFixture(t, "A")
	entered, release := make(chan struct{}), make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": []map[string]string{{"id": "obsolete", "content": "old"}}})
	})
	done := make(chan sourceResult, 1)
	go func() {
		done <- a.s.sourceData(context.Background(), httptest.NewRequest("GET", "/api/v1/notes?scope=all", nil), next, "notes", nil)
	}()
	<-entered
	a.s.invalidateWorkspaceCache(a.cfg.Get().NodeID)
	close(release)
	<-done
	a.s.peers.mu.Lock()
	defer a.s.peers.mu.Unlock()
	if len(a.s.peers.cache) != 0 {
		t.Fatal("in-flight stale query repopulated invalidated cache")
	}
}

func TestWorkspaceChronologicalSortingUnderFractionalAndOffsetTime(t *testing.T) {
	a := newWorkspaceFixture(t, "A")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": []map[string]string{
			{"id": "whole", "updated_at": "2026-11-13T01:00:00Z"},
			{"id": "fraction", "updated_at": "2026-11-13T01:00:00.5Z"},
			{"id": "offset", "updated_at": "2026-11-13T10:00:00+08:00"},
		}})
	})
	rec := httptest.NewRecorder()
	a.s.aggregateWorkspace(rec, httptest.NewRequest("GET", "/api/v1/notes?scope=all", nil), next, "notes")
	var packet struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"offset", "fraction", "whole"} {
		if packet.Data[i]["id"] != want {
			t.Fatalf("item %d: %v", i, packet.Data)
		}
	}
}

func TestWorkspaceTransferTimeoutIsNotJSONActionDeadline(t *testing.T) {
	for _, path := range []string{"/api/v1/files/f/download", "/api/v1/files/f/stream?x=y"} {
		if peerActionTimeout("GET", path) < time.Minute {
			t.Fatal("large file retains short JSON timeout")
		}
	}
	for _, path := range []string{"/api/v1/pads/p", "/api/v1/files/f/ticket"} {
		if peerActionTimeout("POST", path) != 18*time.Second {
			t.Fatal("mutation lost bound")
		}
	}
}

func TestWorkspaceCacheInvalidationDoesNotDiscardOtherDevices(t *testing.T) {
	a := newWorkspaceFixture(t, "A")
	a.s.peers.cache["node-a/notes?q=a"] = workspaceCache{Data: json.RawMessage(`[]`)}
	a.s.peers.cache["node-a/dashboard?"] = workspaceCache{Data: json.RawMessage(`{}`)}
	a.s.peers.cache["node-b/notes?"] = workspaceCache{Data: json.RawMessage(`[]`)}
	a.s.invalidateWorkspaceCache("node-a")
	if len(a.s.peers.cache) != 1 {
		t.Fatal("unrelated snapshots lost")
	}
	for key := range a.s.peers.cache {
		if !strings.HasPrefix(key, "node-b/") {
			t.Fatal("mutated device snapshot remained")
		}
	}
}
