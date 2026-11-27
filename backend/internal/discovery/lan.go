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
const maxDiscoveredPeers = 1024
const maxDiscoveredRoutes = 16
const discoveredTTL = 12 * time.Second

// MaxClockSkew bounds signed discovery announcements and workspace requests.
const MaxClockSkew = time.Minute

var (
	_, lanCgnatNet, _     = net.ParseCIDR("100.64.0.0/10")
	_, lanBenchmarkNet, _ = net.ParseCIDR("198.18.0.0/15")
)

type announcement struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
	SentAt    int64  `json:"sent_at"`
	Protocol  string `json:"protocol"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
	Scheme    string `json:"scheme,omitempty"`
}

type Registry struct {
	startOnce  sync.Once
	mu         sync.RWMutex
	peers      map[string]model.DeviceNode
	routes     map[string]map[string]discoveredRoute
	localID    string
	privateKey ed25519.PrivateKey
}

type discoveredRoute struct {
	seen   time.Time
	sentAt int64
}

func NewRegistryWithIdentity(id string, privateKey ed25519.PrivateKey) *Registry {
	return &Registry{peers: map[string]model.DeviceNode{}, routes: map[string]map[string]discoveredRoute{}, localID: id, privateKey: append(ed25519.PrivateKey(nil), privateKey...)}
}

// NewRegistry remains useful for tests and creates a process-local signed identity.
func NewRegistry() *Registry {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return NewRegistryWithIdentity(identity(key.Public().(ed25519.PublicKey)), key)
}

// Start is compatible with older HTTP callers; discovery never requires a
// shared access key or pairing. Packet signatures are automatic node identity.
func (r *Registry) Start(ctx context.Context, nodeName string, port int) {
	r.StartWithScheme(ctx, nodeName, port, "http")
}

func (r *Registry) StartWithScheme(ctx context.Context, nodeName string, port int, scheme string) {
	if len(r.privateKey) != ed25519.PrivateKeySize || port < 1 || port > 65535 || (scheme != "http" && scheme != "https") {
		return
	}
	r.startOnce.Do(func() {
		go r.networkLoop(ctx, nodeName, port, scheme)
		go r.evict(ctx)
	})
}

func (r *Registry) List() []model.DeviceNode {
	r.mu.RLock()
	all := make([]model.DeviceNode, 0, len(r.peers))
	for _, p := range r.peers {
		p.Addresses = append([]string(nil), p.Addresses...)
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

type lanInterface struct {
	iface  net.Interface
	ip     net.IP
	subnet *net.IPNet
}

type lanEndpoint struct {
	receive, send *net.UDPConn
	done          chan struct{}
}

func eligibleMulticastInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
		return false
	}
	// Point-to-point TUN/VPN interfaces (e.g. Tailscale utun, Clash/Surge TUN) do
	// not carry LAN multicast discovery and add idle wakeups if joined.
	return iface.Flags&net.FlagPointToPoint == 0
}

func eligibleLANIPv4(ip net.IP) net.IP {
	ip4 := ip.To4()
	if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() || ip4.IsMulticast() {
		return nil
	}
	if (lanBenchmarkNet != nil && lanBenchmarkNet.Contains(ip4)) || (lanCgnatNet != nil && lanCgnatNet.Contains(ip4)) {
		return nil
	}
	return ip4
}

func activeInterfaces() map[string]lanInterface {
	out := map[string]lanInterface{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range interfaces {
		if !eligibleMulticastInterface(iface) {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, subnet, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			ip4 := eligibleLANIPv4(ip)
			if ip4 == nil {
				continue
			}
			out[fmt.Sprintf("%d/%s", iface.Index, ip4)] = lanInterface{iface: iface, ip: ip4, subnet: subnet}
		}
	}
	return out
}

// Each eligible interface joins and sends on the same LAN. Refreshing the set
// also recovers after sleep, a Wi-Fi switch, VPN changes or a transient bind error.
func (r *Registry) networkLoop(ctx context.Context, name string, port int, scheme string) {
	group, err := net.ResolveUDPAddr("udp4", multicastAddress)
	if err != nil {
		return
	}
	endpoints := map[string]*lanEndpoint{}
	closeEndpoint := func(e *lanEndpoint) { _ = e.receive.Close(); _ = e.send.Close(); <-e.done }
	defer func() {
		for _, e := range endpoints {
			closeEndpoint(e)
		}
	}()
	refresh := func() {
		interfaces := activeInterfaces()
		for key, e := range endpoints {
			_, exists := interfaces[key]
			select {
			case <-e.done:
				exists = false
			default:
			}
			if !exists {
				closeEndpoint(e)
				delete(endpoints, key)
			}
		}
		for key, entry := range interfaces {
			if _, exists := endpoints[key]; exists {
				continue
			}
			receiver, err := net.ListenMulticastUDP("udp4", &entry.iface, group)
			if err != nil {
				continue
			}
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: entry.ip})
			if err != nil {
				_ = receiver.Close()
				continue
			}
			if err = configureMulticast(sender, entry.ip); err != nil {
				_ = receiver.Close()
				_ = sender.Close()
				continue
			}
			_ = receiver.SetReadBuffer(64 * 1024)
			e := &lanEndpoint{receive: receiver, send: sender, done: make(chan struct{})}
			endpoints[key] = e
			go func(e *lanEndpoint, subnet *net.IPNet) {
				defer close(e.done)
				buf := make([]byte, 4096)
				for {
					n, source, err := e.receive.ReadFromUDP(buf)
					if err != nil {
						return
					}
					if subnet.Contains(source.IP) {
						r.observe(buf[:n], source.IP, time.Now())
					}
				}
			}(e, entry.subnet)
		}
	}
	send := func() {
		payload := r.announcementPayload(name, port, scheme, time.Now())
		for _, e := range endpoints {
			_ = e.send.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = e.send.WriteToUDP(payload, group)
		}
	}
	refresh()
	if len(endpoints) == 0 {
		log.Printf("[LAN discovery] no multicast interface available; retrying automatically")
	}
	send()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
			send()
		}
	}
}

func (r *Registry) observe(payload []byte, source net.IP, now time.Time) bool {
	if source == nil || source.To4() == nil || source.IsUnspecified() || source.IsMulticast() || len(payload) > 2048 {
		return false
	}
	var a announcement
	if json.Unmarshal(payload, &a) != nil || !validAnnouncement(a, r.localID, now) {
		return false
	}
	scheme := "http"
	if a.Protocol == "omnishare/3" {
		scheme = a.Scheme
	}
	node := model.DeviceNode{ID: a.ID, Hostname: a.Name, IP: source.String(), Port: a.Port,
		URL:         scheme + "://" + net.JoinHostPort(source.String(), strconv.Itoa(a.Port)),
		NetworkType: "lan", Online: true, LastSeen: now}
	r.mu.Lock()
	defer r.mu.Unlock()
	// A multi-homed node announces once per interface. Keep its live routes
	// together so the last packet cannot hide another reachable interface.
	routes := r.routes[a.ID]
	if routes == nil {
		if len(r.routes) >= maxDiscoveredPeers {
			return false
		}
		routes = map[string]discoveredRoute{}
		r.routes[a.ID] = routes
	}
	if previous, ok := routes[node.URL]; ok && a.SentAt <= previous.sentAt {
		return false
	}
	routes[node.URL] = discoveredRoute{seen: now, sentAt: a.SentAt}
	for address := range routes {
		node.Addresses = append(node.Addresses, address)
	}
	sort.Slice(node.Addresses, func(i, j int) bool {
		left, right := node.Addresses[i], node.Addresses[j]
		if !routes[left].seen.Equal(routes[right].seen) {
			return routes[left].seen.After(routes[right].seen)
		}
		return left < right
	})
	if len(node.Addresses) > maxDiscoveredRoutes {
		for _, address := range node.Addresses[maxDiscoveredRoutes:] {
			delete(routes, address)
		}
		node.Addresses = node.Addresses[:maxDiscoveredRoutes]
	}
	for i, address := range node.Addresses {
		if now.Sub(routes[address].seen) > discoveredTTL {
			node.Addresses = node.Addresses[:i]
			break
		}
	}
	r.peers[a.ID] = node
	return true
}

func validAnnouncement(a announcement, localID string, now time.Time) bool {
	if (a.Protocol != "omnishare/2" && a.Protocol != "omnishare/3") || a.ID == "" || a.ID == localID || a.Port < 1 || a.Port > 65535 || len([]rune(a.Name)) == 0 || len([]rune(a.Name)) > 80 {
		return false
	}
	if a.Protocol == "omnishare/2" && a.Scheme != "" {
		return false
	}
	if a.Protocol == "omnishare/3" && a.Scheme != "http" && a.Scheme != "https" {
		return false
	}
	if a.SentAt < now.Add(-MaxClockSkew).Unix() || a.SentAt > now.Add(MaxClockSkew).Unix() {
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

func (r *Registry) announcementPayload(nodeName string, port int, scheme string, now time.Time) []byte {
	name := strings.TrimSpace(nodeName)
	if name == "" {
		name = "OmniShare"
	}
	if runes := []rune(name); len(runes) > 80 {
		name = string(runes[:80])
	}
	// Continue emitting v2 for HTTP so already-deployed v2 nodes discover us.
	a := announcement{ID: r.localID, Name: name, Port: port, SentAt: now.Unix(), Protocol: "omnishare/2",
		PublicKey: base64.RawStdEncoding.EncodeToString(r.privateKey.Public().(ed25519.PublicKey))}
	if scheme == "https" {
		a.Protocol = "omnishare/3"
		a.Scheme = scheme
	}
	a.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(r.privateKey, canonical(a)))
	payload, _ := json.Marshal(a)
	return payload
}

func canonical(a announcement) []byte {
	value := fmt.Sprintf("%s\n%s\n%d\n%d\n%s\n%s", a.ID, a.Name, a.Port, a.SentAt, a.Protocol, a.PublicKey)
	if a.Protocol == "omnishare/3" {
		value += "\n" + a.Scheme
	}
	return []byte(value)
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
			r.expire(time.Now())
		}
	}
}

func (r *Registry) expire(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.peers {
		if now.Sub(p.LastSeen) > discoveredTTL {
			delete(r.peers, id)
		}
	}
	// Forget offline devices promptly, but remember each route's last signed
	// announcement until its timestamp can no longer be accepted.
	oldest := now.Add(-MaxClockSkew).Unix()
	for id, routes := range r.routes {
		for address, route := range routes {
			if route.sentAt < oldest && now.Sub(route.seen) > discoveredTTL {
				delete(routes, address)
			}
		}
		if len(routes) == 0 {
			delete(r.routes, id)
		}
	}
}
