package discovery

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in because some isolated CI networks intentionally prohibit multicast.
// This test exercises real sockets, not injected registry entries.
func TestLiveMulticastTwoNodes(t *testing.T) {
	if os.Getenv("OMNISHARE_TEST_MULTICAST") != "1" {
		t.Skip("set OMNISHARE_TEST_MULTICAST=1 on a multicast-capable test host")
	}
	if len(activeInterfaces()) == 0 {
		t.Fatal("no active multicast interface")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := NewRegistry(), NewRegistry()
	a.Start(ctx, "LAN test A", 49081)
	b.Start(ctx, "LAN test B", 49082)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		hasA, hasB := false, false
		for _, p := range a.List() {
			if p.ID == b.localID && p.Port == 49082 {
				hasB = true
			}
		}
		for _, p := range b.List() {
			if p.ID == a.localID && p.Port == 49081 {
				hasA = true
			}
		}
		if hasA && hasB {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nodes did not discover each other over actual multicast sockets")
}
