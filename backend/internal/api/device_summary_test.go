package api

import (
	"net"
	"testing"

	"omnishare/internal/model"
)

func TestUsableLocalIPRejectsLinkLocalAddresses(t *testing.T) {
	for _, value := range []string{"169.254.10.20", "fe80::1", "0.0.0.0", "::"} {
		if usableLocalIP(net.ParseIP(value)) {
			t.Fatalf("expected %s to be rejected", value)
		}
	}
	for _, value := range []string{"127.0.0.1", "10.0.0.8", "100.92.10.2", "192.168.1.20"} {
		if !usableLocalIP(net.ParseIP(value)) {
			t.Fatalf("expected %s to be accepted", value)
		}
	}
}

func TestLocalAddressScorePrefersConfiguredAndOutboundAddress(t *testing.T) {
	configured := localAddressScore("127.0.0.1", "127.0.0.1", "192.168.1.10")
	outbound := localAddressScore("192.168.1.10", "0.0.0.0", "192.168.1.10")
	private := localAddressScore("10.10.0.2", "0.0.0.0", "")
	tailscale := localAddressScore("100.92.10.2", "0.0.0.0", "")
	loopback := localAddressScore("127.0.0.1", "0.0.0.0", "")

	if configured <= outbound || outbound <= private || private <= tailscale || tailscale <= loopback {
		t.Fatalf("unexpected score order: configured=%d outbound=%d private=%d tailscale=%d loopback=%d", configured, outbound, private, tailscale, loopback)
	}
}

func TestDedupeDeviceNodesKeepsOneLocalAndOneURL(t *testing.T) {
	devices := []model.DeviceNode{
		{ID: "local-primary", URL: "http://127.0.0.1:8084/", IsLocal: true},
		{ID: "local-other", URL: "http://10.0.0.1:8084/", IsLocal: true},
		{ID: "remote-a", URL: "http://192.168.1.20:8084/"},
		{ID: "remote-b", URL: "http://192.168.1.20:8084"},
		{ID: "remote-c", URL: "http://100.90.0.2:8084/"},
	}

	result := dedupeDeviceNodes(devices)
	if len(result) != 3 {
		t.Fatalf("expected 3 summarized devices, got %d: %#v", len(result), result)
	}
	localCount := 0
	for _, device := range result {
		if device.IsLocal {
			localCount++
		}
	}
	if localCount != 1 {
		t.Fatalf("expected one local device, got %d", localCount)
	}
}

func TestCanonicalDeviceURLNormalizesTrailingSlashAndCase(t *testing.T) {
	left := canonicalDeviceURL("HTTP://Example.COM:8084/")
	right := canonicalDeviceURL("http://example.com:8084")
	if left != right {
		t.Fatalf("expected canonical URLs to match: %q != %q", left, right)
	}
}
