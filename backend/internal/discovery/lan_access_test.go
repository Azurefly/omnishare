package discovery

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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
