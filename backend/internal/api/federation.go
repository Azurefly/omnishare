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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/discovery"
	"omnishare/internal/durable"
)

const peerProtocol = "omnishare/workspace/1"
const maxBindings = 32

var (
	errInvalidPeerSignature   = errors.New("invalid signed device identity")
	errPeerRefreshUnsupported = errors.New("peer has no address refresh endpoint")
	errPeerClockSkew          = errors.New("两台设备的系统时间相差超过 1 分钟，请在两台设备上启用自动设置日期与时间后重新绑定")
)

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
	Identity     peerIdentity `json:"identity"`
	URL          string       `json:"url"`
	Alias        string       `json:"alias,omitempty"`
	BoundAt      time.Time    `json:"bound_at"`
	VerifiedURLs []string     `json:"verified_urls,omitempty"`
}
type peerState struct {
	mu             sync.Mutex
	bindings       map[string]peerBinding
	revoked        map[string]bool
	revokedAliases map[string]string
	owner          string
	nonces         map[string]time.Time
	cacheEpoch     uint64
	cache          map[string]workspaceCache
	routes         map[string]string
	health         map[string]routeHealth
	loadError      error
	tailMu         sync.RWMutex
	tailReturnMu   sync.Mutex
	tailReturnIPs  map[string][]string
	tailReturnAt   time.Time
	tailStatus     string
	tailSelf       []string
	tailNames      map[string]bool
	tailNodes      []peerIdentity
	tailProbes     map[string]tailProbeState
	tailStarted    bool
	tailRefresh    chan struct{}
	tailProbeFn    func(context.Context, string) (peerIdentity, error)
}
type tailProbeState struct {
	ipSig     string
	lastURL   string
	node      peerIdentity
	hasNode   bool
	failures  int
	nextProbe time.Time
}
type bindingFile struct {
	Owner          string                 `json:"owner"`
	Peers          map[string]peerBinding `json:"peers"`
	Revoked        map[string]bool        `json:"revoked,omitempty"`
	RevokedAliases map[string]string      `json:"revoked_aliases,omitempty"`
}

func (s *Server) initPeers() {
	s.peers = &peerState{bindings: map[string]peerBinding{}, revoked: map[string]bool{}, owner: s.cfg.Get().NodeID, nonces: map[string]time.Time{}, cache: map[string]workspaceCache{}, routes: map[string]string{}, health: map[string]routeHealth{}, tailProbes: map[string]tailProbeState{}, tailRefresh: make(chan struct{}, 1)}
	data, err := os.ReadFile(filepath.Join(s.dataDir, "workspace-peers.json"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		s.peers.loadError = err
		return
	}
	var file bindingFile
	if json.Unmarshal(data, &file) != nil || len(file.Peers) > maxBindings || len(file.Revoked) > 4096 || len(file.RevokedAliases) > 4096 {
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
	s.peers.revokedAliases = file.RevokedAliases
}
func (s *Server) savePeer(p peerBinding) error {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	return s.savePeerLocked(p)
}

// A saved primary URL in older workspaces was also challenged successfully.
// Only these local observations may be used to initiate a binding callback.
func verifiedPeerURLs(p peerBinding) []string {
	return mergePeerURLs([]string{p.URL}, p.VerifiedURLs)
}

func (s *Server) savePeerLocked(p peerBinding) error {
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
	p.VerifiedURLs = verifiedPeerURLs(p)
	if existing, ok := s.peers.bindings[p.Identity.ID]; ok {
		if existing.Identity.PublicKey != p.Identity.PublicKey {
			return errBindingIdentity
		}
		p.Alias = existing.Alias
		p.BoundAt = existing.BoundAt
		p.VerifiedURLs = mergePeerURLs(p.VerifiedURLs, verifiedPeerURLs(existing))
	} else if p.Alias == "" {
		p.Alias = s.peers.revokedAliases[p.Identity.ID]
	}
	if len(p.VerifiedURLs) > 16 {
		p.VerifiedURLs = p.VerifiedURLs[:16]
	}
	peers[p.Identity.ID] = p
	if len(peers) > maxBindings {
		return errors.New("too many bound devices")
	}
	revoked := cloneRevocations(s.peers.revoked)
	delete(revoked, p.Identity.ID)
	return s.writePeersLocked(peers, revoked)
}

// Record a newly proven route without recreating a binding removed during the probe.
func (s *Server) rememberBoundRoute(id peerIdentity, base string, prefer bool) (peerBinding, bool, error) {
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	p, ok := s.peers.bindings[id.ID]
	if !ok || s.peers.owner != s.cfg.Get().NodeID || p.Identity.PublicKey != id.PublicKey {
		return peerBinding{}, false, nil
	}
	routes := verifiedPeerURLs(p)
	if !slices.Contains(routes, base) || (prefer && p.URL != base) {
		p.VerifiedURLs = mergePeerURLs([]string{base}, routes)
		if prefer {
			p.URL, p.Identity = base, id
		}
		if err := s.savePeerLocked(p); err != nil {
			return p, true, err
		}
	}
	return s.peers.bindings[id.ID], true, nil
}
func cloneRevocations(old map[string]bool) map[string]bool {
	out := make(map[string]bool, len(old))
	for k, v := range old {
		out[k] = v
	}
	return out
}
func (s *Server) writePeersLocked(peers map[string]peerBinding, revoked map[string]bool) error {
	aliases := make(map[string]string)
	for id := range revoked {
		alias := s.peers.revokedAliases[id]
		if bound, ok := s.peers.bindings[id]; ok {
			alias = bound.Alias
		}
		if alias != "" {
			aliases[id] = alias
		}
	}
	data, err := json.MarshalIndent(bindingFile{Owner: s.cfg.Get().NodeID, Peers: peers, Revoked: revoked, RevokedAliases: aliases}, "", "  ")
	if err != nil {
		return err
	}
	if err = durable.WriteFile(filepath.Join(s.dataDir, "workspace-peers.json"), data, 0600); err != nil {
		return err
	}
	s.peers.bindings = peers
	s.peers.revoked = revoked
	s.peers.revokedAliases = aliases
	s.peers.cache = map[string]workspaceCache{}
	s.peers.cacheEpoch++
	s.resetRouteHealthLocked()
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
		return peerIdentity{}, errInvalidPeerSignature
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
	for _, resource := range []string{"dashboard", "notes", "files", "pads", "folders", "shares", "trash", "audit"} {
		if path == "/api/v1/"+resource || strings.HasPrefix(path, "/api/v1/"+resource+"/") {
			return true
		}
	}
	return path == "/api/v1/peers/self" || path == "/api/v1/ai-quota/shared"
}
func (s *Server) verifyPeerSignature(r *http.Request, p peerIdentity, clock func() time.Time) error {
	stamp := r.Header.Get("X-OmniShare-Time")
	when, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return errInvalidPeerSignature
	}
	nonce := r.Header.Get("X-OmniShare-Nonce")
	if len(nonce) < 20 || len(nonce) > 64 || r.Header.Get("X-OmniShare-Peer") != p.ID {
		return errInvalidPeerSignature
	}
	body := []byte{}
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err != nil || len(body) > maxJSONBody {
			return errInvalidPeerSignature
		}
	}
	pub, _ := base64.RawStdEncoding.DecodeString(p.PublicKey)
	sig, _ := base64.RawStdEncoding.DecodeString(r.Header.Get("X-OmniShare-Signature"))
	if !ed25519.Verify(pub, signedPeerText(r.Method, r.URL.RequestURI(), s.cfg.Get().NodeID, stamp, nonce, body), sig) {
		return errInvalidPeerSignature
	}
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	now := clock()
	if when < now.Add(-discovery.MaxClockSkew).Unix() || when > now.Add(discovery.MaxClockSkew).Unix() {
		return errPeerClockSkew
	}
	for k, until := range s.peers.nonces {
		if !now.Before(until) {
			delete(s.peers.nonces, k)
		}
	}
	k := p.ID + ":" + nonce
	if _, found := s.peers.nonces[k]; found || len(s.peers.nonces) >= 4096 {
		return errInvalidPeerSignature
	}
	// A peer whose clock is ahead signs a request that expires later on this receiver.
	// Timestamps have whole-second precision and the last valid second is inclusive.
	s.peers.nonces[k] = time.Unix(when, 0).Add(discovery.MaxClockSkew + time.Second)
	return nil
}
func (s *Server) authorizedPeer(r *http.Request) bool {
	if !peerContentPath(r.URL.Path) || r.URL.Query().Get("scope") == "all" {
		return false
	}
	p, ok := s.boundPeer(r.Header.Get("X-OmniShare-Peer"))
	return ok && s.verifyPeerSignature(r, p.Identity, time.Now) == nil
}

type peerBindResult struct {
	peerBinding
	AlreadyBound bool   `json:"already_bound,omitempty"`
	ReturnURL    string `json:"return_url,omitempty"`
}

type peerAcceptData struct {
	peerIdentity
	ReturnURL string          `json:"return_url,omitempty"`
	Attempts  []returnAttempt `json:"return_attempts,omitempty"`
}

type peerAcceptReply struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    peerAcceptData `json:"data"`
}

func (s *Server) postPeerAcceptance(ctx context.Context, base, path, target, key string, loop bool, local peerIdentity) (int, peerAcceptReply, error) {
	body, err := json.Marshal(local)
	if err != nil {
		return 0, peerAcceptReply{}, err
	}
	h := s.signPeerRequest(http.MethodPost, path, target, body)
	h.Set("Content-Type", "application/json")
	if key != "" {
		h.Set("X-OmniShare-Key-Base64", base64.StdEncoding.EncodeToString([]byte(key)))
	}
	resp, err := peerHTTP(ctx, http.MethodPost, base, path, body, h, loop)
	if err != nil {
		return 0, peerAcceptReply{}, err
	}
	defer resp.Body.Close()
	var reply peerAcceptReply
	if path == "/api/v1/peers/self" {
		// Older releases serve the workbench HTML for an unknown API route.
		// Retry their normal pairing protocol, which still verifies the return path.
		mediaType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed ||
			(resp.StatusCode == http.StatusOK && strings.EqualFold(strings.TrimSpace(mediaType), "text/html")) {
			return resp.StatusCode, reply, errPeerRefreshUnsupported
		}
	}
	if resp.StatusCode != http.StatusUnauthorized {
		err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply)
	}
	return resp.StatusCode, reply, err
}

func (s *Server) bindPeer(w http.ResponseWriter, r *http.Request) { s.bindPeerMode(w, r, false) }
func (s *Server) bindPeerMode(w http.ResponseWriter, r *http.Request, automatic bool) {
	var input struct {
		URL    string `json:"url"`
		Key    string `json:"access_key"`
		NodeID string `json:"node_id"`
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
	routes := []string{base}
	expected := peerIdentity{ID: input.NodeID}
	if expected.ID != "" {
		s.peers.mu.Lock()
		previous := s.peers.health[base].identity
		s.peers.mu.Unlock()
		if previous.ID == expected.ID {
			expected = previous
		}
		routes = s.peerRoutes(peerBinding{URL: base, Identity: expected})
	}
	remote, selected, err := s.identifyBindingRoute(ctx, routes, expected, loop)
	if err != nil {
		message := "未能读取 " + base + " 的设备身份：" + peerError(err)
		if len(routes) > 1 {
			message = "未找到所选设备的可用连接（首选地址 " + base + "）：" + peerError(err)
		}
		respond(w, 502, 502, message, nil)
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
	bound, known := s.boundPeer(remote.ID)
	if known && bound.Identity.PublicKey != remote.PublicKey {
		respond(w, 409, 409, peerError(errBindingIdentity), nil)
		return
	}
	var reply peerAcceptReply
	var status int
	if known {
		status, reply, err = s.postPeerAcceptance(ctx, selected, "/api/v1/peers/self", remote.ID, "", loop, local)
	}
	if !known || errors.Is(err, errPeerRefreshUnsupported) {
		status, reply, err = s.postPeerAcceptance(ctx, selected, "/api/v1/peers/accept", remote.ID, input.Key, loop, local)
	}
	if err != nil {
		respond(w, 502, 502, "绑定失败："+peerError(err), nil)
		return
	}
	if status == 401 {
		respond(w, 403, 403, "对方已开启密钥保护，请填写对方的访问密钥后重试", nil)
		return
	}
	if status == http.StatusPreconditionFailed && reply.Code == http.StatusPreconditionFailed {
		respond(w, http.StatusPreconditionFailed, http.StatusPreconditionFailed, errPeerClockSkew.Error(), nil)
		return
	}
	if status == http.StatusBadGateway && strings.HasPrefix(reply.Message, "反向连接失败") {
		message := fmt.Sprintf("已联系 %s，对方无法回连本机的 OmniShare（TCP %d）。", selected, local.Port)
		details := formatReturnAttempts(reply.Data.Attempts)
		if details == "" {
			details = "对方版本未提供逐地址诊断，两端升级后重试。"
		}
		respond(w, 502, 502, message+"\n"+details, nil)
		return
	}
	if status != 200 || reply.Code != 0 || reply.Data.ID != remote.ID || reply.Data.PublicKey != remote.PublicKey {
		respond(w, 502, 502, "对方未确认双向绑定；请检查反向访问地址、端口与版本", nil)
		return
	}
	var saved peerBinding
	if known {
		var stillBound bool
		saved, stillBound, err = s.rememberBoundRoute(remote, selected, true)
		if err != nil || !stillBound {
			respond(w, 409, 409, "本机未能保存连接地址，请刷新设备状态后重试", nil)
			return
		}
	} else {
		if err = s.savePeer(peerBinding{Identity: remote, URL: selected, BoundAt: time.Now().UTC()}); err != nil {
			respond(w, 500, 500, "对方已登记，但本机保存绑定失败，请重试", nil)
			return
		}
		saved, _ = s.boundPeer(remote.ID)
	}
	returnURL, _ := normalizePeerURL(reply.Data.ReturnURL)
	respond(w, 200, 0, "双向绑定成功，工作区已合并", peerBindResult{peerBinding: saved, ReturnURL: returnURL, AlreadyBound: known})
}
func (s *Server) acceptPeer(w http.ResponseWriter, r *http.Request) {
	s.acceptPeerMode(w, r, false)
}

func (s *Server) refreshBoundPeer(w http.ResponseWriter, r *http.Request) {
	s.acceptPeerMode(w, r, true)
}

func (s *Server) acceptPeerMode(w http.ResponseWriter, r *http.Request, refresh bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		respond(w, 400, 400, "invalid identity", nil)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var p peerIdentity
	if json.Unmarshal(body, &p) != nil || !validIdentity(p) || p.ID == s.cfg.Get().NodeID {
		respond(w, 400, 400, errInvalidPeerSignature.Error(), nil)
		return
	}
	existing, known := s.boundPeer(p.ID)
	if refresh && (!known || existing.Identity.PublicKey != p.PublicKey) {
		respond(w, 404, 404, "此设备尚未绑定", nil)
		return
	}
	if err := s.verifyPeerSignature(r, p, time.Now); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errPeerClockSkew) {
			status = http.StatusPreconditionFailed
		}
		respond(w, status, status, err.Error(), nil)
		return
	}
	// Start with the TCP source. Alternatives must have been observed independently
	// on this node. Advertised addresses alone cannot authorize a callback.
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
	observed, _ := s.discoveredRoutes(p.ID)
	if known && existing.Identity.PublicKey == p.PublicKey {
		observed = mergePeerURLs(verifiedPeerURLs(existing), observed)
	}
	observed = append(observed, s.tailReturnRoutes(ctx, ip, p, scheme)...)
	routes := limitedPeerRoutes(peerBinding{URL: base, Identity: peerIdentity{Addresses: observed}})
	verified, selected, err := s.identifyBindingRoute(ctx, routes, p, ip.IsLoopback())
	if err != nil {
		var failed *bindingRouteError
		data := peerAcceptData{}
		if errors.As(err, &failed) {
			data.Attempts = failed.attempts
		}
		respond(w, 502, 502, "反向连接失败", data)
		return
	}
	if refresh {
		if _, ok, err := s.rememberBoundRoute(verified, selected, true); !ok || err != nil {
			respond(w, 409, 409, "绑定状态已变化，请重试", nil)
			return
		}
	} else if err = s.savePeer(peerBinding{Identity: verified, URL: selected, BoundAt: time.Now().UTC()}); err != nil {
		respond(w, 500, 500, "cannot persist binding", nil)
		return
	}
	respond(w, 200, 0, "bound", peerAcceptData{peerIdentity: s.nodeIdentity(), ReturnURL: selected})
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
func peerDisplayName(p peerBinding) string {
	if alias := strings.TrimSpace(p.Alias); alias != "" {
		return alias
	}
	return p.Identity.Name
}
func (s *Server) setPeerAlias(id, alias string) error {
	alias = strings.TrimSpace(alias)
	if len([]rune(alias)) > 40 {
		return errors.New("设备别名不能超过 40 个字符")
	}
	cfg := s.cfg.Get()
	if id == cfg.NodeID {
		if alias == "" {
			alias = "OmniShare"
		}
		cfg.NodeName = alias
		cfg.KeepAccessKey = true
		if err := s.cfg.Update(cfg); err != nil {
			return err
		}
		s.peers.mu.Lock()
		s.peers.cache = map[string]workspaceCache{}
		s.peers.cacheEpoch++
		s.peers.mu.Unlock()
		return nil
	}
	s.peers.mu.Lock()
	defer s.peers.mu.Unlock()
	if s.peers.owner != cfg.NodeID {
		return errors.New("设备未绑定")
	}
	existing, ok := s.peers.bindings[id]
	if !ok {
		return errors.New("设备未绑定，请先绑定后再设置别名")
	}
	existing.Alias = alias
	peers := make(map[string]peerBinding, len(s.peers.bindings))
	for k, v := range s.peers.bindings {
		peers[k] = v
	}
	peers[id] = existing
	return s.writePeersLocked(peers, cloneRevocations(s.peers.revoked))
}
func (s *Server) updatePeerAlias(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input struct {
		Alias string `json:"alias"`
	}
	if err := decodeJSON(r, &input); err != nil {
		respond(w, 400, 400, err.Error(), nil)
		return
	}
	if err := s.setPeerAlias(id, input.Alias); err != nil {
		code := 400
		if strings.Contains(err.Error(), "未绑定") {
			code = 404
		}
		respond(w, code, code, err.Error(), nil)
		return
	}
	respond(w, 200, 0, "设备别名已更新", map[string]string{"id": id, "alias": strings.TrimSpace(input.Alias)})
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

func (s *Server) discoveredRoutes(id string) (observed, advertised []string) {
	if s.registry != nil {
		for _, d := range s.registry.List() {
			if d.ID == id {
				observed = append(observed, d.URL)
				observed = append(observed, d.Addresses...)
			}
		}
	}
	s.peers.tailMu.RLock()
	for _, d := range s.peers.tailNodes {
		if d.ID == id && len(d.Addresses) > 0 {
			// Tailscale discovery prepends the address that answered its signed probe.
			observed = append(observed, d.Addresses[0])
			advertised = append(advertised, d.Addresses[1:]...)
		}
	}
	s.peers.tailMu.RUnlock()
	return observed, advertised
}

func (s *Server) peerRoutes(p peerBinding) []string {
	observed, advertised := s.discoveredRoutes(p.Identity.ID)
	// Prefer recent discovery over an old adapter list, and apply the route
	// limit afterwards so stale saved addresses cannot crowd out a new VPN.
	p.Identity.Addresses = mergePeerURLs(observed, p.VerifiedURLs, advertised, p.Identity.Addresses)
	p.VerifiedURLs = nil
	return limitedPeerRoutes(p)
}

func limitedPeerRoutes(p peerBinding) []string {
	routes := uniqueRoutes(p)
	if len(routes) > 24 {
		routes = routes[:24]
	}
	return routes
}
