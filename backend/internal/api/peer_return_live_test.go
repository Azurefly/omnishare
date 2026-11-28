package api

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

// Optional read-only check against an operator's two known addresses for the
// same device. It reads signed identities and never submits a binding.
func TestLiveTailReturnIdentity(t *testing.T) {
	tailIP, wifi := os.Getenv("OMNISHARE_TEST_RETURN_TAIL_IP"), os.Getenv("OMNISHARE_TEST_RETURN_WIFI_URL")
	if tailIP == "" || wifi == "" {
		t.Skip("set both known addresses to check a real device")
	}
	src := net.ParseIP(tailIP)
	u, err := url.Parse(wifi)
	if err != nil || !tailRange.Contains(src) || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsPrivate() {
		t.Fatal("expected the nominated device's Tailscale IPv4 and private Wi-Fi URL")
	}
	a := newWorkspaceFixture(t, "read-only return route check")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	remote, err := a.s.readIdentity(ctx, wifi, false)
	if err != nil {
		t.Fatal("the Wi-Fi address did not return a signed OmniShare identity")
	}
	base := "http://" + net.JoinHostPort(src.String(), strconv.Itoa(remote.Port))
	routes := append([]string{base}, a.s.tailReturnRoutes(ctx, src, remote, "http")...)
	id, selected, err := a.s.identifyBindingRoute(ctx, routes, remote, false)
	if err != nil || id.ID != remote.ID || id.PublicKey != remote.PublicKey {
		t.Fatalf("no independently corroborated return route to this device (candidates %d): %v", len(routes), err)
	}
	t.Logf("same signed device, return network %s, candidates %d", deviceNetwork(selected), len(routes))
}
