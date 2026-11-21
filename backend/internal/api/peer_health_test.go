package api

import (
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// An offline bound device must not be re-dialed on every poll: the workspace UI
// polls every few seconds and each dial used to burn a fresh TCP SYN per route.
func TestWorkspacePollingBacksOffFromOfflinePeer(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	wsCall(t, b, "POST", "/api/v1/notes", map[string]string{"content": "snapshot"}, "")
	if _, p, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, ""); len(p["data"].([]interface{})) != 1 {
		t.Fatal("not aggregated")
	}
	// Replace B with a listener that counts connection attempts and drops them.
	addr := b.http.Listener.Addr().String()
	b.http.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skip("cannot rebind peer port:", err)
	}
	defer ln.Close()
	var dials atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			c.Close()
		}
	}()
	// The verified route is remembered briefly; expire it so the next poll
	// discovers the outage like a real one would after the TTL.
	a.s.peers.mu.Lock()
	for k, h := range a.s.peers.health {
		h.verified = time.Time{}
		a.s.peers.health[k] = h
	}
	a.s.peers.mu.Unlock()

	_, p, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	if p["workspace"].(map[string]interface{})["partial"] != true {
		t.Fatal("offline device hidden")
	}
	first := dials.Load()
	if first == 0 {
		t.Fatal("expected the outage to be probed once")
	}
	for i := 0; i < 5; i++ {
		_, p, _ = wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
		data := p["data"].([]interface{})
		if len(data) != 1 || data[0].(map[string]interface{})["source_online"] != false {
			t.Fatal("offline snapshot not served during backoff")
		}
		wsCall(t, a, "GET", "/api/v1/devices", nil, "")
	}
	if got := dials.Load(); got != first {
		t.Fatalf("offline peer re-dialed during backoff: %d -> %d", first, got)
	}
	// An explicit refresh re-probes immediately.
	wsCall(t, a, "GET", "/api/v1/devices?refresh=1", nil, "")
	if dials.Load() == first {
		t.Fatal("explicit refresh did not re-probe")
	}
}

func TestRouteBackoffGrowsAndResetsOnSuccess(t *testing.T) {
	s, _, _ := newTestServer(t)
	ctx := (&http.Request{}).Context()
	base := "http://127.0.0.1:9"
	var last time.Duration
	for i := 0; i < 6; i++ {
		s.recordRoute(ctx, base, peerIdentity{}, net.ErrClosed)
		h := s.peers.health[base]
		d := time.Until(h.until).Round(time.Second)
		if d < last || d > routeBackoffMax {
			t.Fatalf("backoff %v after %v", d, last)
		}
		last = d
	}
	if last != routeBackoffMax {
		t.Fatalf("backoff not capped: %v", last)
	}
	s.recordRoute(ctx, base, peerIdentity{ID: "x"}, nil)
	if h := s.peers.health[base]; h.failures != 0 || h.verified.IsZero() {
		t.Fatal("success did not reset backoff")
	}
}
