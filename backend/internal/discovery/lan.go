package discovery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/model"
)

const multicastAddress = "239.255.42.99:48281"

type announcement struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	SentAt    int64  `json:"sent_at"`
	Protocol  string `json:"protocol"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

type Registry struct {
	mu         sync.RWMutex
	peers      map[string]model.DeviceNode
	localID    string
	privateKey ed25519.PrivateKey
}

func NewRegistryWithIdentity(id string, privateKey ed25519.PrivateKey) *Registry {
	return &Registry{peers: map[string]model.DeviceNode{}, localID: id, privateKey: append(ed25519.PrivateKey(nil), privateKey...)}
}

// NewRegistry remains useful for tests and creates a process-local signed identity.
func NewRegistry() *Registry {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return NewRegistryWithIdentity(identity(key.Public().(ed25519.PublicKey)), key)
}

func (r *Registry) Start(ctx context.Context, nodeName string, port int) {
	go r.listen(ctx)
	go r.announce(ctx, nodeName, port)
	go r.evict(ctx)
}

func (r *Registry) List() []model.DeviceNode {
	r.mu.RLock()
	all := make([]model.DeviceNode, 0, len(r.peers))
	for _, p := range r.peers {
		all = append(all, p)
	}
	r.mu.RUnlock()

	// Map iteration is deliberately randomized by Go. Sort before URL
	// deduplication so the selected representative is deterministic: prefer the
	// freshest announcement, then the lowest stable node ID as a tie breaker.
	sort.Slice(all, func(i, j int) bool {
		if all[i].URL != all[j].URL {
			return all[i].URL < all[j].URL
		}
		if !all[i].LastSeen.Equal(all[j].LastSeen) {
			return all[i].LastSeen.After(all[j].LastSeen)
		}
		return all[i].ID < all[j].ID
	})
	out := make([]model.DeviceNode, 0, len(all))
	for _, p := range all {
		if len(out) > 0 && out[len(out)-1].URL == p.URL {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hostname != out[j].Hostname {
			return out[i].Hostname < out[j].Hostname
		}
		if out[i].URL != out[j].URL {
			return out[i].URL < out[j].URL
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (r *Registry) listen(ctx context.Context) {
	group, err := net.ResolveUDPAddr("udp4", multicastAddress)
	if err != nil {
		log.Printf("[LAN discovery] resolve: %v", err)
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, group)
	if err != nil {
		log.Printf("[LAN discovery] listen unavailable: %v", err)
		return
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(64 * 1024)
	go func() { <-ctx.Done(); _ = conn.Close() }()
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[LAN discovery] read: %v", err)
			}
			return
		}
		var a announcement
		if n > 2048 || json.Unmarshal(buf[:n], &a) != nil || !validAnnouncement(a, r.localID, time.Now()) {
			continue
		}
		node := model.DeviceNode{ID: a.ID, Hostname: a.Name, IP: addr.IP.String(), Port: a.Port, URL: "http://" + net.JoinHostPort(addr.IP.String(), strconv.Itoa(a.Port)), NetworkType: "lan", Online: true, LastSeen: time.Now()}
		r.mu.Lock()
		r.peers[a.ID] = node
		r.mu.Unlock()
	}
}

func validAnnouncement(a announcement, localID string, now time.Time) bool {
	if a.Protocol != "omnishare/2" || a.ID == "" || a.ID == localID || a.Port < 1 || a.Port > 65535 || len([]rune(a.Name)) == 0 || len([]rune(a.Name)) > 80 {
		return false
	}
	if delta := now.Unix() - a.SentAt; delta < -10 || delta > 10 {
		return false
	}
	pub, err := base64.RawStdEncoding.DecodeString(a.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || identity(ed25519.PublicKey(pub)) != a.ID {
		return false
	}
	sig, err := base64.RawStdEncoding.DecodeString(a.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), canonical(a), sig)
}

func (r *Registry) announce(ctx context.Context, nodeName string, port int) {
	if len(r.privateKey) != ed25519.PrivateKeySize || port < 1 || port > 65535 {
		return
	}
	addr, err := net.ResolveUDPAddr("udp4", multicastAddress)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("[LAN discovery] announce unavailable: %v", err)
		return
	}
	defer conn.Close()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	send := func() {
		name := strings.TrimSpace(nodeName)
		if runes := []rune(name); len(runes) > 80 {
			name = string(runes[:80])
		}
		a := announcement{ID: r.localID, Name: name, Port: port, SentAt: time.Now().Unix(), Protocol: "omnishare/2", PublicKey: base64.RawStdEncoding.EncodeToString(r.privateKey.Public().(ed25519.PublicKey))}
		a.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(r.privateKey, canonical(a)))
		payload, err := json.Marshal(a)
		if err == nil && len(payload) <= 2048 {
			_, _ = conn.Write(payload)
		}
	}
	send()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func canonical(a announcement) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%d\n%d\n%s\n%s", a.ID, a.Name, a.Port, a.SentAt, a.Protocol, a.PublicKey))
}

func identity(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "node-" + hex.EncodeToString(h[:10])
}

func (r *Registry) evict(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			now := time.Now()
			for id, p := range r.peers {
				if now.Sub(p.LastSeen) > 12*time.Second {
					delete(r.peers, id)
				}
			}
			r.mu.Unlock()
		}
	}
}
