package api

// A binding trusts a device identity, not one IP address. Requests between bound
// nodes are signed with the existing machine key; user access keys are used only
// for the initial consent request and are never persisted or forwarded.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/durable"
)

const peerProtocol = "omnishare/workspace/1"
const maxBindings = 32

type peerIdentity struct {
	Protocol  string   `json:"protocol"`
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	PublicKey string   `json:"public_key"`
	Port      int      `json:"port"`
	Addresses []string `json:"addresses"`
	Protected bool     `json:"access_key_enabled"`
	Proof     string   `json:"proof,omitempty"`
}
type peerBinding struct {
	Identity peerIdentity `json:"identity"`
	URL      string       `json:"url"`
	BoundAt  time.Time    `json:"bound_at"`
}
type peerState struct {
	mu         sync.Mutex
	bindings   map[string]peerBinding
	revoked    map[string]bool
	owner      string
	nonces     map[string]time.Time
	cache      map[string]workspaceCache
	routes     map[string]string
	loadError  error
	tailMu     sync.RWMutex
	tailStatus string
	tailSelf   []string
	tailNames  map[string]bool
	tailNodes  []peerIdentity
	tailCancel context.CancelFunc
}
type bindingFile struct {
	Owner   string                 `json:"owner"`
	Peers   map[string]peerBinding `json:"peers"`
	Revoked map[string]bool        `json:"revoked,omitempty"`
}

func (s *Server) initPeers() {
	s.peers = &peerState{bindings: map[string]peerBinding{}, revoked: map[string]bool{}, owner: s.cfg.Get().NodeID, nonces: map[string]time.Time{}, cache: map[string]workspaceCache{}, routes: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(s.dataDir, "workspace-peers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.peers.loadError = err
		return
	}
	var file bindingFile
	if json.Unmarshal(data, &file) != nil || len(file.Peers) > maxBindings || len(file.Revoked) > 4096 {
		s.peers.loadError = errors.New("invalid workspace binding file; restore a valid backup before binding")
		return
	}
	if file.Owner != s.cfg.Get().NodeID {
		return
	} // a restored/cloned identity must rebind explicitly
	for id, p := range file.Peers {
		if id != p.Identity.ID || !validIdentity(p.Identity) {
			s.peers.loadError = errors.New("invalid bound identity")
			return
		}
	}
	s.peers.bindings = file.Peers
	if file.Revoked != nil {
		s.peers.revoked = file.Revoked
	}
}
func (s *Server) savePeer(p peerBinding) error {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if s.peers.owner != s.cfg.Get().NodeID {
		return errors.New("device identity changed; restart before binding")
	}
	if s.peers.loadError != nil {
		return s.peers.loadError
	}
	peers := make(map[string]peerBinding, len(s.peers.bindings)+1)
	for k, v := range s.peers.bindings {
		peers[k] = v
	}
	peers[p.Identity.ID] = p
	if len(peers) > maxBindings {
		return errors.New("too many bound devices")
	}
	revoked := cloneRevocations(s.peers.revoked)
	delete(revoked, p.Identity.ID)
	return s.writePeersLocked(peers, revoked)
}
func cloneRevocations(old map[string]bool) map[string]bool {
	out := make(map[string]bool, len(old))
	for k, v := range old {
		out[k] = v
	}
	return out
}
func (s *Server) writePeersLocked(peers map[string]peerBinding, revoked map[string]bool) error {
	data, err := json.MarshalIndent(bindingFile{Owner: s.cfg.Get().NodeID, Peers: peers, Revoked: revoked}, "", "  ")
	if err != nil {
		return err
	}
	if err = durable.WriteFile(filepath.Join(s.dataDir, "workspace-peers.json"), data, 0600); err != nil {
		return err
	}
	s.peers.bindings = peers
	s.peers.revoked = revoked
	s.peers.cache = map[string]workspaceCache{}
	return nil
}
func (s *Server) removePeer(id string) error {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	peers := make(map[string]peerBinding, len(s.peers.bindings))
	for k, v := range s.peers.bindings {
		if k != id {
			peers[k] = v
		}
	}
	revoked := cloneRevocations(s.peers.revoked)
	if _, exists := s.peers.bindings[id]; exists {
		revoked[id] = true
	}
	if len(revoked) > 4096 {
		return errors.New("revocation history full; remove obsolete legacy device entries")
	}
	return s.writePeersLocked(peers, revoked)
}
func (s *Server) boundPeers() []peerBinding {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if s.peers.owner != s.cfg.Get().NodeID {
		return nil
	}
	out := make([]peerBinding, 0, len(s.peers.bindings))
	for _, v := range s.peers.bindings {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity.ID < out[j].Identity.ID })
	return out
}
func (s *Server) boundPeer(id string) (peerBinding, bool) {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if s.peers.owner != s.cfg.Get().NodeID {
		return peerBinding{}, false
	}
	p, ok := s.peers.bindings[id]
	return p, ok
}
func validIdentity(p peerIdentity) bool {
	pub, err := base64.RawStdEncoding.DecodeString(p.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || p.Protocol != peerProtocol || p.Port < 1 || p.Port > 65535 || len(p.Addresses) > 16 || len([]rune(p.Name)) > 80 {
		return false
	}
	h := sha256.Sum256(pub)
	return p.ID == "node-"+hex.EncodeToString(h[:10])
}
func randomPeerNonce() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func identityProof(p peerIdentity, nonce string) []byte {
	p.Proof = ""
	data, _ := json.Marshal(p)
	return append([]byte("omnishare-identity\n"+nonce+"\n"), data...)
}
func (s *Server) nodeIdentity() peerIdentity {
	c := s.cfg.Get()
	_, key, _ := s.cfg.DiscoveryIdentity()
	p := peerIdentity{Protocol: peerProtocol, ID: c.NodeID, Name: c.NodeName, Port: c.Port, PublicKey: base64.RawStdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), Protected: s.cfg.AccessKeyEnabled(), Addresses: []string{}}
	scheme := "http"
	if c.TLSCertFile != "" {
		scheme = "https"
	}
	seen := map[string]bool{}
	if !c.AllowLAN {
		p.Addresses = []string{scheme + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))}
		return p
	}
	add := func(value string) {
		ip := net.ParseIP(value)
		if !peerIPAllowed(ip, false) || seen[value] {
			return
		}
		seen[value] = true
		p.Addresses = append(p.Addresses, scheme+"://"+net.JoinHostPort(value, strconv.Itoa(c.Port)))
	}
	if n := s.localNetwork(); n != nil {
		for _, v := range n.ips {
			add(v)
		}
	}
	s.peers.tailMu.RLock()
	for _, v := range s.peers.tailSelf {
		add(v)
	}
	s.peers.tailMu.RUnlock()
	sort.SliceStable(p.Addresses, func(i, j int) bool {
		left, _ := url.Parse(p.Addresses[i])
		right, _ := url.Parse(p.Addresses[j])
		return tailRange.Contains(net.ParseIP(left.Hostname())) && !tailRange.Contains(net.ParseIP(right.Hostname()))
	})
	if len(p.Addresses) > 16 {
		p.Addresses = p.Addresses[:16]
	}
	return p
}
func (s *Server) peerIdentityHandler(w http.ResponseWriter, r *http.Request) {
	p := s.nodeIdentity()
	nonce := r.URL.Query().Get("nonce")
	if len(nonce) > 128 {
		respond(w, 400, 400, "invalid nonce", nil)
		return
	}
	if nonce != "" {
		_, key, _ := s.cfg.DiscoveryIdentity()
		p.Proof = base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, identityProof(p, nonce)))
	}
	respond(w, 200, 0, "success", p)
}
func (s *Server) readIdentity(ctx context.Context, base string, allowLoopback bool) (peerIdentity, error) {
	var p peerIdentity
	nonce := randomPeerNonce()
	resp, err := peerHTTP(ctx, http.MethodGet, base, "/api/v1/peers/identity?nonce="+nonce, nil, nil, allowLoopback)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	var packet struct {
		Code int          `json:"code"`
		Data peerIdentity `json:"data"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&packet) != nil || packet.Code != 0 || !validIdentity(packet.Data) {
		return p, errors.New("not a compatible OmniShare workspace device")
	}
	p = packet.Data
	pub, _ := base64.RawStdEncoding.DecodeString(p.PublicKey)
	sig, _ := base64.RawStdEncoding.DecodeString(p.Proof)
	if !ed25519.Verify(pub, identityProof(p, nonce), sig) {
		return peerIdentity{}, errors.New("device identity verification failed")
	}
	p.Proof = ""
	return p, nil
}
func signedPeerText(method, path, target, stamp, nonce string, body []byte) []byte {
	h := sha256.Sum256(body)
	return []byte(strings.Join([]string{peerProtocol, method, path, target, stamp, nonce, hex.EncodeToString(h[:])}, "\n"))
}
func (s *Server) signPeerRequest(method, path, target string, body []byte) http.Header {
	id, key, _ := s.cfg.DiscoveryIdentity()
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randomPeerNonce()
	h := http.Header{}
	h.Set("X-OmniShare-Peer", id)
	h.Set("X-OmniShare-Time", stamp)
	h.Set("X-OmniShare-Nonce", nonce)
	h.Set("X-OmniShare-Signature", base64.RawStdEncoding.EncodeToString(ed25519.Sign(key, signedPeerText(method, path, target, stamp, nonce, body))))
	return h
}
func peerContentPath(path string) bool {
	if strings.HasPrefix(path, "/n/") || strings.HasPrefix(path, "/media/") {
		return true
	}
	for _, resource := range []string{"dashboard", "notes", "files", "pads", "shares", "trash", "audit"} {
		if path == "/api/v1/"+resource || strings.HasPrefix(path, "/api/v1/"+resource+"/") {
			return true
		}
	}
	return path == "/api/v1/peers/self"
}
func (s *Server) validPeerSignature(r *http.Request, p peerIdentity) bool {
	stamp := r.Header.Get("X-OmniShare-Time")
	when, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || time.Now().Unix()-when > 60 || when-time.Now().Unix() > 10 {
		return false
	}
	nonce := r.Header.Get("X-OmniShare-Nonce")
	if len(nonce) < 20 || len(nonce) > 64 || r.Header.Get("X-OmniShare-Peer") != p.ID {
		return false
	}
	body := []byte{}
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil || len(body) > maxJSONBody {
			return false
		}
	}
	pub, _ := base64.RawStdEncoding.DecodeString(p.PublicKey)
	sig, _ := base64.RawStdEncoding.DecodeString(r.Header.Get("X-OmniShare-Signature"))
	if !ed25519.Verify(pub, signedPeerText(r.Method, r.URL.RequestURI(), s.cfg.Get().NodeID, stamp, nonce, body), sig) {
		return false
	}
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	now := time.Now()
	for k, until := range s.peers.nonces {
		if now.After(until) {
			delete(s.peers.nonces, k)
		}
	}
	k := p.ID + ":" + nonce
	if _, found := s.peers.nonces[k]; found || len(s.peers.nonces) >= 4096 {
		return false
	}
	s.peers.nonces[k] = now.Add(75 * time.Second)
	return true
}
func (s *Server) authorizedPeer(r *http.Request) bool {
	if !peerContentPath(r.URL.Path) || r.URL.Query().Get("scope") == "all" {
		return false
	}
	p, ok := s.boundPeer(r.Header.Get("X-OmniShare-Peer"))
	return ok && s.validPeerSignature(r, p.Identity)
}
func (s *Server) bindPeer(w http.ResponseWriter, r *http.Request) { s.bindPeerMode(w, r, false) }
func (s *Server) bindPeerMode(w http.ResponseWriter, r *http.Request, automatic bool) {
	var input struct {
		URL string `json:"url"`
		Key string `json:"access_key"`
	}
	if err := decodeJSON(r, &input); err != nil {
		respond(w, 400, 400, err.Error(), nil)
		return
	}
	base, err := normalizePeerURL(input.URL)
	if err != nil {
		respond(w, 400, 400, err.Error(), nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
	defer cancel()
	loop := net.ParseIP(clientIP(r)).IsLoopback()
	remote, err := s.readIdentity(ctx, base, loop)
	if err != nil {
		respond(w, 502, 502, "无法连接设备："+peerError(err), nil)
		return
	}
	if automatic {
		s.peers.mu.Lock()
		revoked := s.peers.revoked[remote.ID]
		s.peers.mu.Unlock()
		if revoked {
			respond(w, 409, 409, "该设备已解绑；需要手动重新绑定", nil)
			return
		}
	}
	local := s.nodeIdentity()
	if remote.ID == local.ID {
		respond(w, 400, 400, "不能绑定本机或使用相同身份的克隆设备", nil)
		return
	}
	body, _ := json.Marshal(local)
	h := s.signPeerRequest(http.MethodPost, "/api/v1/peers/accept", remote.ID, body)
	h.Set("Content-Type", "application/json")
	if input.Key != "" {
		h.Set("X-OmniShare-Key-Base64", base64.StdEncoding.EncodeToString([]byte(input.Key)))
	}
	resp, err := peerHTTP(ctx, http.MethodPost, base, "/api/v1/peers/accept", body, h, loop)
	if err != nil {
		respond(w, 502, 502, "双向绑定失败："+peerError(err), nil)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		respond(w, 403, 403, "对方已开启密钥保护，请填写对方的访问密钥后重试", nil)
		return
	}
	var reply struct {
		Code    int          `json:"code"`
		Message string       `json:"message"`
		Data    peerIdentity `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply) != nil || resp.StatusCode != 200 || reply.Code != 0 || reply.Data.ID != remote.ID {
		respond(w, 502, 502, "对方未确认双向绑定；请检查反向访问地址、端口与版本", nil)
		return
	}
	p := peerBinding{Identity: remote, URL: base, BoundAt: time.Now().UTC()}
	if err = s.savePeer(p); err != nil {
		respond(w, 500, 500, "对方已登记，但本机保存绑定失败，请重试", nil)
		return
	}
	respond(w, 200, 0, "双向绑定成功，工作区已合并", p)
}
func (s *Server) acceptPeer(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		respond(w, 400, 400, "invalid identity", nil)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var p peerIdentity
	if json.Unmarshal(body, &p) != nil || !validIdentity(p) || p.ID == s.cfg.Get().NodeID || !s.validPeerSignature(r, p) {
		respond(w, 400, 400, "invalid signed device identity", nil)
		return
	}
	// The callback uses the actual TCP source, not an arbitrary URL from a peer.
	ip := net.ParseIP(clientIP(r))
	if !peerIPAllowed(ip, true) {
		respond(w, 400, 400, "binding requires a LAN or Tailscale source", nil)
		return
	}
	scheme := "http"
	if len(p.Addresses) > 0 {
		u, _ := url.Parse(p.Addresses[0])
		if u != nil && u.Scheme == "https" {
			scheme = "https"
		}
	}
	base := scheme + "://" + net.JoinHostPort(ip.String(), strconv.Itoa(p.Port))
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	verified, err := s.readIdentity(ctx, base, ip.IsLoopback())
	if err != nil || verified.ID != p.ID || verified.PublicKey != p.PublicKey {
		respond(w, 502, 502, "反向连接失败；确认发起设备的服务监听与防火墙", nil)
		return
	}
	if err = s.savePeer(peerBinding{Identity: verified, URL: base, BoundAt: time.Now().UTC()}); err != nil {
		respond(w, 500, 500, "cannot persist binding", nil)
		return
	}
	respond(w, 200, 0, "bound", s.nodeIdentity())
}
func (s *Server) unbindPeer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	remoteRemoved := false
	if p, ok := s.boundPeer(id); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		resp, err := s.peerRequest(ctx, p, http.MethodDelete, "/api/v1/peers/self", nil, nil)
		cancel()
		if err == nil {
			remoteRemoved = resp.StatusCode == 200
			resp.Body.Close()
		}
	}
	if err := s.removePeer(id); err != nil {
		respond(w, 500, 500, "cannot save revocation", nil)
		return
	}
	respond(w, 200, 0, "本机绑定已移除", map[string]bool{"reciprocal_removed": remoteRemoved})
}
func (s *Server) revokeSelf(w http.ResponseWriter, r *http.Request) {
	// Only a valid bound peer signature can remove that very peer, never others.
	if !s.authorizedPeer(r) {
		respond(w, 401, 401, "peer signature required", nil)
		return
	}
	if err := s.removePeer(r.Header.Get("X-OmniShare-Peer")); err != nil {
		respond(w, 500, 500, "cannot save revocation", nil)
		return
	}
	respond(w, 200, 0, "unbound", nil)
}

// Explicit addresses saved by earlier versions were user-added devices, not
// passive discoveries. Upgrade these to reciprocal bindings when both ends
// support it and the other end allows password-free binding. Protected peers
// still require the owner to provide the remote key in the binding dialog.
func (s *Server) reconcileConfiguredPeers(ctx context.Context) {
	for _, legacy := range s.cfg.Get().Peers {
		if ctx.Err() != nil {
			return
		}
		known := false
		for _, p := range s.boundPeers() {
			for _, route := range uniqueRoutes(p) {
				if strings.TrimRight(legacy.URL, "/") == route {
					known = true
				}
			}
		}
		if known {
			continue
		}
		one, cancel := context.WithTimeout(ctx, 5*time.Second)
		body, _ := json.Marshal(map[string]string{"url": legacy.URL})
		req, _ := http.NewRequestWithContext(one, http.MethodPost, "http://127.0.0.1/api/v1/peers/bind", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:0"
		req.Header.Set("Content-Type", "application/json")
		s.bindPeerMode(newCapture(), req, true)
		cancel()
	}
}

func (s *Server) peerRoutes(p peerBinding) []string {
	// Rediscovery can repair DHCP/VPN route changes, but cannot change trust.
	// Every route must still prove the saved public-key identity before use.
	if s.registry != nil {
		for _, d := range s.registry.List() {
			if d.ID == p.Identity.ID {
				p.Identity.Addresses = append(append([]string{}, p.Identity.Addresses...), d.URL)
			}
		}
	}
	s.peers.tailMu.RLock()
	for _, d := range s.peers.tailNodes {
		if d.ID == p.Identity.ID {
			p.Identity.Addresses = append(append([]string{}, p.Identity.Addresses...), d.Addresses...)
		}
	}
	s.peers.tailMu.RUnlock()
	routes := uniqueRoutes(p)
	if len(routes) > 24 {
		routes = routes[:24]
	}
	return routes
}
