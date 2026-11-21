package discovery

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestNodesDiscoverWithoutAccessKeysAndKeepLegacyHTTP(t *testing.T) {
	receiver, sender := NewRegistry(), NewRegistry()
	now := time.Now()
	httpPayload := sender.announcementPayload("客厅电脑", 8081, "http", now)
	var a announcement
	_ = json.Unmarshal(httpPayload, &a)
	if a.Protocol != "omnishare/2" || a.Scheme != "" {
		t.Fatal("existing HTTP nodes would lose interoperability")
	}
	if !receiver.observe(httpPayload, net.ParseIP("192.168.1.9"), now) {
		t.Fatal("automatic announcement rejected")
	}
	nodes := receiver.List()
	if len(nodes) != 1 || nodes[0].URL != "http://192.168.1.9:8081" || nodes[0].Hostname != "客厅电脑" {
		t.Fatalf("bad discovered node: %+v", nodes)
	}
	if sender.observe(httpPayload, net.ParseIP("192.168.1.9"), now) {
		t.Fatal("self appeared as a remote device")
	}
}

func TestHTTPSDiscoveryBindsProtocolIntoSignature(t *testing.T) {
	receiver, sender := NewRegistry(), NewRegistry()
	now := time.Now()
	payload := sender.announcementPayload("HTTPS node", 8443, "https", now)
	if !receiver.observe(payload, net.ParseIP("10.0.0.8"), now) {
		t.Fatal("TLS announcement rejected")
	}
	if receiver.List()[0].URL != "https://10.0.0.8:8443" {
		t.Fatal("HTTPS node incorrectly advertised HTTP")
	}
	var a announcement
	_ = json.Unmarshal(payload, &a)
	a.Scheme = "http"
	tampered, _ := json.Marshal(a)
	if receiver.observe(tampered, net.ParseIP("10.0.0.8"), now) {
		t.Fatal("unsigned scheme change accepted")
	}
	a.Protocol = "omnishare/2"
	a.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(sender.privateKey, canonical(a)))
	tampered, _ = json.Marshal(a)
	if receiver.observe(tampered, net.ParseIP("10.0.0.8"), now) {
		t.Fatal("v2 unsigned scheme accepted")
	}
}

func TestMulticastInterfaceCanBeConfiguredWithoutPrivileges(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := configureMulticast(conn, net.IPv4zero); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryKeepsLiveRoutesForOneIdentity(t *testing.T) {
	receiver, sender := NewRegistry(), NewRegistry()
	now := time.Now()
	for i, address := range []string{"192.168.1.9", "10.0.0.9"} {
		seen := now.Add(time.Duration(i) * time.Second)
		if !receiver.observe(sender.announcementPayload("dual interface", 8081, "http", seen), net.ParseIP(address), seen) {
			t.Fatal("valid announcement rejected")
		}
	}
	nodes := receiver.List()
	if len(nodes) != 1 || len(nodes[0].Addresses) != 2 || nodes[0].URL != "http://10.0.0.9:8081" {
		t.Fatalf("multi-interface device lost a route: %+v", nodes)
	}
	// Caller changes to a snapshot cannot mutate the registry's next response.
	nodes[0].Addresses[0] = "http://changed.invalid"
	if receiver.List()[0].Addresses[0] == "http://changed.invalid" {
		t.Fatal("mutable address slice escaped registry")
	}
	seen := now.Add(14 * time.Second)
	if !receiver.observe(sender.announcementPayload("dual interface", 8081, "http", seen), net.ParseIP("10.0.0.9"), seen) {
		t.Fatal("fresh announcement rejected")
	}
	if got := receiver.List()[0].Addresses; len(got) != 1 || got[0] != "http://10.0.0.9:8081" {
		t.Fatalf("expired interface remained: %v", got)
	}
}

func TestDiscoveryRouteSetRemainsBounded(t *testing.T) {
	receiver, sender := NewRegistry(), NewRegistry()
	now := time.Now()
	for i := 1; i <= maxDiscoveredRoutes+4; i++ {
		seen := now.Add(time.Duration(i) * time.Millisecond)
		if !receiver.observe(sender.announcementPayload("many interfaces", 8081, "http", seen), net.ParseIP(fmt.Sprintf("10.0.0.%d", i)), seen) {
			t.Fatal("valid announcement rejected")
		}
	}
	nodes := receiver.List()
	if len(nodes) != 1 || len(nodes[0].Addresses) != maxDiscoveredRoutes || len(receiver.routes[sender.localID]) != maxDiscoveredRoutes {
		t.Fatalf("unbounded route state: %+v", nodes)
	}
	if nodes[0].Addresses[0] != fmt.Sprintf("http://10.0.0.%d:8081", maxDiscoveredRoutes+4) {
		t.Fatal("newest route was evicted")
	}
}
