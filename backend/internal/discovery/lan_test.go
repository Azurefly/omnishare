package discovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"omnishare/internal/model"
)

func signedAnnouncement(t *testing.T, when time.Time) announcement {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := announcement{ID: identity(pub), Name: "test node", Port: 8081, SentAt: when.Unix(), Protocol: "omnishare/2", PublicKey: base64.RawStdEncoding.EncodeToString(pub)}
	a.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, canonical(a)))
	return a
}

func TestValidAnnouncementSignatureIdentityAndFreshness(t *testing.T) {
	now := time.Now()
	a := signedAnnouncement(t, now)
	if !validAnnouncement(a, "other-node", now) {
		t.Fatal("valid signed announcement rejected")
	}
	for name, mutate := range map[string]func(*announcement){
		"self":         func(v *announcement) { v.ID = "local" },
		"tamper":       func(v *announcement) { v.Name = "tampered" },
		"expired":      func(v *announcement) { v.SentAt = now.Add(-11 * time.Second).Unix() },
		"future":       func(v *announcement) { v.SentAt = now.Add(11 * time.Second).Unix() },
		"bad port":     func(v *announcement) { v.Port = 0 },
		"bad protocol": func(v *announcement) { v.Protocol = "omnishare/1" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := a
			mutate(&candidate)
			local := "local"
			if name != "self" {
				local = "other-node"
			}
			if validAnnouncement(candidate, local, now) {
				t.Fatalf("invalid announcement accepted: %s", name)
			}
		})
	}
}

func TestRegistryListDeduplicatesAndSorts(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.peers["b"] = device("b", "Zulu", "http://10.0.0.2:8081", now)
	r.peers["a"] = device("a", "Alpha", "http://10.0.0.1:8081", now)
	r.peers["dup"] = device("dup", "Duplicate", "http://10.0.0.1:8081", now)
	items := r.List()
	if len(items) != 2 {
		t.Fatalf("expected two unique URLs, got %d", len(items))
	}
	if items[0].Hostname != "Alpha" {
		t.Fatalf("unexpected sort order: %+v", items)
	}
}

func device(id, name, url string, seen time.Time) model.DeviceNode {
	return model.DeviceNode{ID: id, Hostname: name, URL: url, LastSeen: seen, Online: true}
}
