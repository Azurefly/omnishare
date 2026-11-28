package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"omnishare/internal/model"
)

func TestBindDiscoveredDeviceAfterItsPrimaryRouteTimesOut(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	var unavailable atomic.Bool
	original := b.s.Handler()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			<-r.Context().Done()
			return
		}
		original.ServeHTTP(w, r)
	}))
	t.Cleanup(first.Close)
	identity := b.s.nodeIdentity()
	identity.Addresses = []string{first.URL, b.http.URL}
	a.s.peers.tailNodes = []peerIdentity{identity}
	_, listing, _ := wsCall(t, a, http.MethodGet, "/api/v1/devices", nil, "")
	card := listing["data"].([]interface{})[1].(map[string]interface{})
	if card["url"] != first.URL || card["online"] != true || card["bound"] == true {
		t.Fatalf("missing discovered device: %v", card)
	}
	unavailable.Store(true)
	status, packet, _ := wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", map[string]string{
		"url": card["url"].(string), "node_id": card["id"].(string),
	}, "")
	if status != http.StatusOK {
		t.Fatalf("binding the visible device failed: %d %v", status, packet)
	}
	bound, ok := a.s.boundPeer(identity.ID)
	if !ok || bound.URL != b.http.URL || len(b.s.boundPeers()) != 1 {
		t.Fatalf("alternate route did not create a reciprocal binding: %v", bound)
	}
}

func TestBindDiscoveredDeviceRequiresSelectedIdentityBeforeAccept(t *testing.T) {
	a, b, other := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B"), newWorkspaceFixture(t, "B")
	identity := b.s.nodeIdentity()
	var accepts atomic.Int32
	handler := other.s.Handler()
	reassigned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/peers/accept" || r.Header.Get("X-OmniShare-Key-Base64") != "" {
			accepts.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(reassigned.Close)
	request := map[string]string{"url": reassigned.URL, "node_id": identity.ID, "access_key": "fixture-key"}
	status, _, _ := wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", request, "")
	if status != http.StatusBadGateway || accepts.Load() != 0 || len(a.s.boundPeers()) != 0 {
		t.Fatal("a reassigned address was accepted")
	}
	identity.Addresses = []string{b.http.URL}
	a.s.peers.tailNodes = []peerIdentity{identity}
	status, _, _ = wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", request, "")
	if status != http.StatusOK || accepts.Load() != 0 || len(b.s.boundPeers()) != 1 || len(other.s.boundPeers()) != 0 {
		t.Fatal("selected device did not receive the only binding")
	}
}

func TestBindUsesIndependentlyDiscoveredReturnRoute(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	// A's public service port differs from the port accepting the callback.
	// Only B's own successful discovery supplies the correct return address.
	closed := httptest.NewServer(http.NotFoundHandler())
	port := closed.Listener.Addr().(*net.TCPAddr).Port
	closed.Close()
	cfg := a.cfg.Get()
	cfg.Port, cfg.KeepAccessKey = port, true
	if err := a.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	aID := a.s.nodeIdentity()
	request := map[string]string{"url": b.http.URL}
	status, result, _ := wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", request, "")
	if status != http.StatusBadGateway || !strings.Contains(fmt.Sprint(result["message"]), fmt.Sprintf("TCP %d", port)) || len(a.s.boundPeers())+len(b.s.boundPeers()) != 0 {
		t.Fatalf("missing callback diagnosis or unexpected binding: %v", result)
	}
	if message := fmt.Sprint(result["message"]); !strings.Contains(message, closed.URL) || !strings.Contains(message, "端口拒绝连接") {
		t.Fatalf("callback address or its own refusal was lost: %v", result)
	}
	aID.Addresses = []string{a.http.URL}
	b.s.peers.tailNodes = []peerIdentity{aID}
	status, result, _ = wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", request, "")
	bound, ok := b.s.boundPeer(aID.ID)
	if status != http.StatusOK || !ok || bound.URL != a.http.URL {
		t.Fatalf("independently proven callback was not used: %v", result)
	}
	b.s.peers.tailNodes = nil
	if err := a.s.removePeer(b.cfg.Get().NodeID); err != nil {
		t.Fatal(err)
	}
	status, result, _ = wsCall(t, a, http.MethodPost, "/api/v1/peers/bind", request, "")
	if status != http.StatusOK || result["data"].(map[string]any)["return_url"] != a.http.URL {
		t.Fatalf("the receiver forgot its previously verified return path: %v", result)
	}
}

func TestWorkspaceRoutesPrioritizeRediscoveryOverOldAdapters(t *testing.T) {
	a := newWorkspaceFixture(t, "A")
	p := peerBinding{URL: "http://192.168.31.99:8081", Identity: peerIdentity{ID: "remote"}}
	discovered := peerIdentity{ID: p.Identity.ID}
	for i := 1; i <= 16; i++ {
		p.Identity.Addresses = append(p.Identity.Addresses, fmt.Sprintf("http://192.168.1.%d:8081", i))
		discovered.Addresses = append(discovered.Addresses, fmt.Sprintf("http://100.64.0.%d:8081", i))
	}
	a.s.peers.tailMu.Lock()
	a.s.peers.tailNodes = []peerIdentity{discovered}
	a.s.peers.tailMu.Unlock()
	routes := a.s.peerRoutes(p)
	if len(routes) != 24 || routes[0] != p.URL {
		t.Fatalf("primary route or route bound changed: %v", routes)
	}
	for i, address := range discovered.Addresses {
		if routes[i+1] != address {
			t.Fatalf("fresh route was crowded out by obsolete adapters: %v", routes)
		}
	}
}

func TestWorkspaceDevicesMergeRoutesBeforeAndAfterIdentityResolution(t *testing.T) {
	a, b, other := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "same name"), newWorkspaceFixture(t, "same name")
	wsBind(t, a, b, "")
	alias := httptest.NewServer(b.s.Handler())
	t.Cleanup(alias.Close)
	bound, ok := a.s.boundPeer(b.cfg.Get().NodeID)
	if !ok {
		t.Fatal("binding missing")
	}
	oldRoute, tailRoute := "http://192.168.31.99:8081", "http://100.64.162.25:8081"
	bound.Identity.Addresses = []string{oldRoute, b.http.URL + "/"}
	if err := a.s.savePeer(bound); err != nil {
		t.Fatal(err)
	}
	discovered := b.s.nodeIdentity()
	discovered.Addresses = []string{tailRoute, b.http.URL}
	a.s.peers.tailMu.Lock()
	a.s.peers.tailNodes = []peerIdentity{discovered}
	a.s.peers.tailMu.Unlock()
	cfg := a.cfg.Get()
	cfg.Peers = []model.PeerConfig{
		{Name: "old URL entry", URL: alias.URL},
		{Name: "local URL entry", URL: a.http.URL},
		{Name: "same name", URL: other.http.URL},
	}
	cfg.KeepAccessKey = true
	if err := a.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	status, packet, _ := wsCall(t, a, http.MethodGet, "/api/v1/devices", nil, "")
	if status != http.StatusOK {
		t.Fatal(packet)
	}
	devices := packet["data"].([]interface{})
	if len(devices) != 3 {
		t.Fatalf("expected local plus two distinct identities, got %v", devices)
	}
	var remote map[string]interface{}
	for _, entry := range devices {
		device := entry.(map[string]interface{})
		if device["id"] == b.cfg.Get().NodeID {
			remote = device
		}
	}
	if remote == nil || remote["bound"] != true || remote["online"] != true {
		t.Fatalf("bound online identity was replaced: %v", remote)
	}
	addresses := map[string]bool{}
	for _, address := range remote["addresses"].([]interface{}) {
		addresses[address.(string)] = true
	}
	for _, expected := range []string{b.http.URL, oldRoute, tailRoute, alias.URL} {
		if !addresses[expected] {
			t.Errorf("device lost address %s: %v", expected, remote)
		}
	}
	if addresses[b.http.URL+"/"] {
		t.Fatal("equivalent routes were not canonicalized")
	}
}

func TestKnownDeviceAddsAnotherRouteWithoutRebindingOrLosingAlias(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "PC")
	wsBind(t, a, b, "")
	first, _ := a.s.boundPeer(b.cfg.Get().NodeID)
	if err := a.s.setPeerAlias(first.Identity.ID, "工作电脑"); err != nil {
		t.Fatal(err)
	}
	cfg := b.cfg.Get()
	enabled := true
	cfg.AccessKeyEnabled, cfg.AccessKey = &enabled, "fixture-key"
	if err := b.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	var accepts atomic.Int32
	handler := b.s.Handler()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/peers/accept" {
			accepts.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer second.Close()
	status, reply, _ := wsCall(t, a, "POST", "/api/v1/peers/bind", map[string]string{"url": second.URL}, "")
	if status != 200 || reply["data"].(map[string]any)["already_bound"] != true || accepts.Load() != 0 {
		t.Fatalf("new endpoint required another pairing: %d %v", status, reply)
	}
	restarted := New(a.s.store, a.cfg)
	peers := restarted.boundPeers()
	if len(peers) != 1 || peers[0].Alias != "工作电脑" || !peers[0].BoundAt.Equal(first.BoundAt) || peers[0].URL != second.URL ||
		!slices.Contains(verifiedPeerURLs(peers[0]), b.http.URL) {
		t.Fatalf("original binding, alias or routes were lost after restart: %+v", peers)
	}
}

func TestKnownAddressOnLegacyWorkbenchFallsBackToReciprocalPairing(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "PC")
	wsBind(t, a, b, "")
	id := b.cfg.Get().NodeID
	if err := a.s.setPeerAlias(id, "旧版设备"); err != nil {
		t.Fatal(err)
	}
	var accepts atomic.Int32
	next := b.s.Handler()
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/peers/self" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><title>OmniShare</title>")
			return
		}
		if r.URL.Path == "/api/v1/peers/accept" {
			accepts.Add(1)
		}
		next.ServeHTTP(w, r)
	}))
	defer legacy.Close()
	status, reply, _ := wsCall(t, a, "POST", "/api/v1/peers/bind", map[string]string{"url": legacy.URL}, "")
	saved, _ := a.s.boundPeer(id)
	if status != 200 || accepts.Load() != 1 || saved.URL != legacy.URL || saved.Alias != "旧版设备" {
		t.Fatalf("legacy alternate address failed: %d %v", status, reply)
	}
}

func TestAddingKnownAddressRequiresFreshReturnConnection(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "PC")
	wsBind(t, a, b, "")
	localUI := httptest.NewServer(a.s.Handler())
	defer localUI.Close()
	newRoute := httptest.NewServer(b.s.Handler())
	defer newRoute.Close()
	a.http.Close()
	caller := &workspaceFixture{s: a.s, cfg: a.cfg, http: localUI}
	status, reply, _ := wsCall(t, caller, "POST", "/api/v1/peers/bind", map[string]string{"url": newRoute.URL}, "")
	saved, _ := a.s.boundPeer(b.cfg.Get().NodeID)
	if status != 502 || !strings.Contains(fmt.Sprint(reply["message"]), a.http.URL) || slices.Contains(verifiedPeerURLs(saved), newRoute.URL) {
		t.Fatalf("stale mutual registration was accepted without a fresh callback: %d %v", status, reply)
	}
}

func TestManualDiscoveryRestoresOfflineBoundDeviceWithItsAlias(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "PC")
	wsBind(t, a, b, "")
	id := b.cfg.Get().NodeID
	if err := a.s.setPeerAlias(id, "家中电脑"); err != nil {
		t.Fatal(err)
	}
	second := httptest.NewServer(b.s.Handler())
	defer second.Close()
	b.http.Close()
	cfg := a.cfg.Get()
	cfg.Peers, cfg.KeepAccessKey = []model.PeerConfig{{Name: "新地址", URL: second.URL}}, true
	if err := a.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	_, reply, _ := wsCall(t, a, "GET", "/api/v1/devices?refresh=1", nil, "")
	devices := reply["data"].([]any)
	if len(devices) != 2 {
		t.Fatalf("unexpected device count: %d", len(devices))
	}
	card := devices[1].(map[string]any)
	if card["id"] != id || card["online"] != true || card["bound"] != true || card["alias"] != "家中电脑" || card["url"] != second.URL {
		t.Fatalf("reachable route lost its existing identity or alias: %v", card)
	}
	saved, _ := New(a.s.store, a.cfg).boundPeer(id)
	if !slices.Contains(verifiedPeerURLs(saved), second.URL) || a.s.peerBackoff(id) != nil {
		t.Fatal("alternate route was shown but could not be used")
	}
}

func TestRebindKeepsAliasWithoutRestoringRevokedAccess(t *testing.T) {
	a, b := newWorkspaceFixture(t, "Mac"), newWorkspaceFixture(t, "PC")
	wsBind(t, a, b, "")
	id := b.cfg.Get().NodeID
	if err := a.s.setPeerAlias(id, "工作电脑"); err != nil {
		t.Fatal(err)
	}
	wsCall(t, a, "DELETE", "/api/v1/peers/"+id, nil, "")
	restarted := New(a.s.store, a.cfg)
	if _, ok := restarted.boundPeer(id); ok || !restarted.peers.revoked[id] || restarted.peers.revokedAliases[id] != "工作电脑" {
		t.Fatal("revoking trust did not preserve the local name independently")
	}
	second := httptest.NewServer(b.s.Handler())
	defer second.Close()
	status, _, _ := wsCall(t, a, "POST", "/api/v1/peers/bind", map[string]string{"url": second.URL}, "")
	saved, ok := a.s.boundPeer(id)
	if status != 200 || !ok || saved.Alias != "工作电脑" {
		t.Fatal("explicit pairing at a new address did not restore the local alias")
	}
}

func TestTailCallbackUsesOnlyCorroboratedPrivateAddresses(t *testing.T) {
	a := newWorkspaceFixture(t, "PC")
	_, subnet, _ := net.ParseCIDR("192.168.31.0/24")
	a.s.localCache.Store(&localNetworkSnapshot{addrs: map[string]struct{}{"192.168.31.200": {}}, lanSubnets: []*net.IPNet{subnet}, until: time.Now().Add(time.Minute)})
	const tail = "100.64.162.25"
	const lan = "http://192.168.31.99:8081"
	const v6 = "http://[fd7a:115c:a1e0::25]:8081"
	caller := peerIdentity{Port: 8081, Addresses: []string{lan, v6, "http://192.168.31.200:8081", "http://10.0.0.1:8081", "http://192.168.31.99:41641"}}
	status := tailStatus{Peer: map[string]*tailPeer{"mac": {Online: true, CurAddr: "192.168.31.99:41641", TailscaleIPs: []string{tail, "fd7a:115c:a1e0::25"}}}}
	a.s.cacheTailReturnIPs(status, time.Now())
	got := a.s.tailReturnRoutes(context.Background(), net.ParseIP(tail), caller, "http")
	if !slices.Equal(got, []string{v6, lan}) {
		t.Fatalf("unexpected return paths: %v", got)
	}
	for _, endpoint := range []string{"203.0.113.2:41641", "10.0.0.1:41641", "192.168.31.200:41641", ""} {
		status.Peer["mac"].CurAddr = endpoint
		a.s.cacheTailReturnIPs(status, time.Now())
		if got := a.s.tailReturnRoutes(context.Background(), net.ParseIP(tail), caller, "http"); !slices.Equal(got, []string{v6}) {
			t.Fatalf("unexpected LAN path for %q: %v", endpoint, got)
		}
	}
}

func TestWorkspaceOfflineDevicesRetainRediscoveredRoutes(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	alias := httptest.NewServer(b.s.Handler())
	alias.Close()
	bound, _ := a.s.boundPeer(b.cfg.Get().NodeID)
	bound.Identity.Addresses = nil
	if err := a.s.savePeer(bound); err != nil {
		t.Fatal(err)
	}
	discovered := b.s.nodeIdentity()
	discovered.Addresses = []string{alias.URL}
	a.s.peers.tailMu.Lock()
	a.s.peers.tailNodes = []peerIdentity{discovered}
	a.s.peers.tailMu.Unlock()
	b.http.Close()
	status, packet, _ := wsCall(t, a, http.MethodGet, "/api/v1/devices", nil, "")
	if status != http.StatusOK {
		t.Fatal(packet)
	}
	devices := packet["data"].([]interface{})
	if len(devices) != 2 {
		t.Fatal(devices)
	}
	remote := devices[1].(map[string]interface{})
	if remote["bound"] != true || remote["online"] != false {
		t.Fatalf("offline binding lost: %v", remote)
	}
	addresses := remote["addresses"].([]interface{})
	if len(addresses) != 2 || addresses[0] != b.http.URL || addresses[1] != alias.URL {
		t.Fatalf("offline route union lost: %v", addresses)
	}
}

func TestWorkspaceRediscoveryCannotChangeBoundIdentity(t *testing.T) {
	a, b, stranger := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B"), newWorkspaceFixture(t, "same name as B")
	wsBind(t, a, b, "")
	var contentRequests atomic.Int32
	original := stranger.http.Config.Handler
	stranger.http.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/notes") {
			contentRequests.Add(1)
		}
		original.ServeHTTP(w, r)
	})
	wsCall(t, stranger, http.MethodPost, "/api/v1/notes", map[string]string{"content": "untrusted content"}, "")
	contentRequests.Store(0)
	bound, _ := a.s.boundPeer(b.cfg.Get().NodeID)
	bound.Identity.Addresses = nil
	if err := a.s.savePeer(bound); err != nil {
		t.Fatal(err)
	}
	discovered := b.s.nodeIdentity()
	discovered.Addresses = []string{stranger.http.URL}
	a.s.peers.tailMu.Lock()
	a.s.peers.tailNodes = []peerIdentity{discovered}
	a.s.peers.tailMu.Unlock()
	b.http.Close()
	status, packet, _ := wsCall(t, a, http.MethodGet, "/api/v1/notes?scope=all", nil, "")
	if status != http.StatusOK || packet["workspace"].(map[string]interface{})["partial"] != true {
		t.Fatalf("changed peer identity was not rejected: %v", packet)
	}
	if contentRequests.Load() != 0 || len(packet["data"].([]interface{})) != 0 {
		t.Fatalf("discovered route changed workspace trust: %v", packet)
	}
}

func TestTailProbeBackoffPreferredURLAndRefresh(t *testing.T) {
	a := newWorkspaceFixture(t, "A")
	var mu sync.Mutex
	calls := []string{}
	a.s.peers.tailProbeFn = func(_ context.Context, base string) (peerIdentity, error) {
		mu.Lock()
		calls = append(calls, base)
		mu.Unlock()
		if base == "http://100.64.162.25:8084" {
			return peerIdentity{ID: "node-mac", Name: "macOS", PublicKey: "pk"}, nil
		}
		return peerIdentity{}, errors.New("connection timeout")
	}

	status := tailStatus{
		BackendState: "Running",
		Peer: map[string]*tailPeer{
			"dead-peer": {Online: true, TailscaleIPs: []string{"100.115.127.40"}},
			"live-peer": {Online: true, TailscaleIPs: []string{"100.64.162.25"}},
		},
	}
	baseTime := time.Unix(1_700_000_000, 0)
	a.s.refreshTailDevicesFromStatus(context.Background(), status, baseTime, false)

	mu.Lock()
	firstCalls := append([]string(nil), calls...)
	calls = nil
	mu.Unlock()
	if len(firstCalls) < 5 {
		t.Fatalf("expected initial scan to probe candidate ports, got %v", firstCalls)
	}

	// 30s later: dead peer is backed off (0 probes), live peer hits its remembered :8084 URL on the first try.
	a.s.refreshTailDevicesFromStatus(context.Background(), status, baseTime.Add(30*time.Second), false)
	mu.Lock()
	secondCalls := append([]string(nil), calls...)
	calls = nil
	mu.Unlock()
	if len(secondCalls) != 1 || secondCalls[0] != "http://100.64.162.25:8084" {
		t.Fatalf("expected only preferred URL probe during backoff window, got %v", secondCalls)
	}

	// Explicit refresh clears backoff and immediately re-probes the previously failed peer.
	a.s.resetTailProbeBackoff(false)
	a.s.refreshTailDevicesFromStatus(context.Background(), status, baseTime.Add(40*time.Second), false)
	mu.Lock()
	refreshedCalls := append([]string(nil), calls...)
	calls = nil
	mu.Unlock()
	probedDead := false
	for _, url := range refreshedCalls {
		if strings.HasPrefix(url, "http://100.115.127.40:") {
			probedDead = true
			break
		}
	}
	if !probedDead {
		t.Fatalf("expected explicit reset to re-probe backed-off peer, got %v", refreshedCalls)
	}

	// Going offline clears backoff so returning online probes immediately too.
	offline := tailStatus{
		BackendState: "Running",
		Peer:         map[string]*tailPeer{"dead-peer": {Online: false, TailscaleIPs: []string{"100.115.127.40"}}},
	}
	a.s.refreshTailDevicesFromStatus(context.Background(), offline, baseTime.Add(50*time.Second), false)
	a.s.refreshTailDevicesFromStatus(context.Background(), status, baseTime.Add(60*time.Second), false)
	mu.Lock()
	reconnectedCalls := append([]string(nil), calls...)
	mu.Unlock()
	probedDead = false
	for _, url := range reconnectedCalls {
		if strings.HasPrefix(url, "http://100.115.127.40:") {
			probedDead = true
			break
		}
	}
	if !probedDead {
		t.Fatalf("expected reconnected peer to probe immediately after coming back online, got %v", reconnectedCalls)
	}
}

func TestWorkspaceDeviceAliasAndHideOfflineDiscovered(t *testing.T) {
	a, b, unreachable := newWorkspaceFixture(t, "OmniShare"), newWorkspaceFixture(t, "OmniShare"), newWorkspaceFixture(t, "OfflineNode")
	wsBind(t, a, b, "")

	// Seed a note on b so federated scope=all carries b's source_node_name.
	st, _, _ := wsCall(t, b, http.MethodPost, "/api/v1/notes", map[string]interface{}{"content": "remote note"}, "")
	if st != http.StatusCreated {
		t.Fatalf("create note on b failed: %d", st)
	}

	// Set alias on bound device b.
	st, _, _ = wsCall(t, a, http.MethodPut, "/api/v1/peers/"+b.cfg.Get().NodeID+"/alias", map[string]string{"alias": "公司台式机"}, "")
	if st != http.StatusOK {
		t.Fatalf("set alias failed: %d", st)
	}

	// Add an unreachable unbound discovered device and verify it is hidden from /api/v1/devices.
	deadID := unreachable.s.nodeIdentity()
	deadURL := unreachable.http.URL
	unreachable.http.Close()
	a.s.peers.tailMu.Lock()
	a.s.peers.tailNodes = []peerIdentity{{ID: deadID.ID, Name: deadID.Name, PublicKey: deadID.PublicKey, Port: deadID.Port, Addresses: []string{deadURL}}}
	a.s.peers.tailMu.Unlock()

	st, devPacket, _ := wsCall(t, a, http.MethodGet, "/api/v1/devices", nil, "")
	if st != http.StatusOK {
		t.Fatal(devPacket)
	}
	devices := devPacket["data"].([]interface{})
	if len(devices) != 2 {
		t.Fatalf("expected only local + bound device (offline discovered hidden), got %v", devices)
	}
	var foundB map[string]interface{}
	for _, entry := range devices {
		d := entry.(map[string]interface{})
		if d["id"] == b.cfg.Get().NodeID {
			foundB = d
		}
		if d["id"] == deadID.ID {
			t.Fatalf("offline discovered device should be hidden: %v", d)
		}
	}
	if foundB == nil || foundB["alias"] != "公司台式机" {
		t.Fatalf("expected bound device alias '公司台式机', got %v", foundB)
	}

	st, notesPacket, _ := wsCall(t, a, http.MethodGet, "/api/v1/notes?scope=all", nil, "")
	if st != http.StatusOK {
		t.Fatal(notesPacket)
	}
	items := notesPacket["data"].([]interface{})
	if len(items) != 1 || items[0].(map[string]interface{})["source_node_name"] != "公司台式机" {
		t.Fatalf("expected federated note source_node_name to use alias '公司台式机', got %v", items)
	}
}
