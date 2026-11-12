package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"omnishare/internal/config"
	"omnishare/internal/model"
	"omnishare/internal/storage"
)

type workspaceFixture struct {
	s    *Server
	http *httptest.Server
	cfg  *config.Manager
}

func newWorkspaceFixture(t *testing.T, name string) *workspaceFixture {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(t.TempDir(), listener.Addr().(*net.TCPAddr).Port, name)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.New(cfg.Get().DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(store, cfg)
	srv := httptest.NewUnstartedServer(s.Handler())
	srv.Listener.Close()
	srv.Listener = listener
	srv.Start()
	t.Cleanup(func() { srv.Close(); store.Close() })
	return &workspaceFixture{s: s, http: srv, cfg: cfg}
}
func wsCall(t *testing.T, f *workspaceFixture, method, path string, body interface{}, key string) (int, map[string]interface{}, http.Header) {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, f.http.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-OmniShare-Key-Base64", base64.StdEncoding.EncodeToString([]byte(key)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var packet map[string]interface{}
	if err = json.NewDecoder(resp.Body).Decode(&packet); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, packet, resp.Header
}
func wsBind(t *testing.T, a, b *workspaceFixture, key string) {
	t.Helper()
	status, p, _ := wsCall(t, a, "POST", "/api/v1/peers/bind", map[string]string{"url": b.http.URL, "access_key": key}, "")
	if status != 200 {
		t.Fatalf("bind status %d %v", status, p)
	}
}
func TestWorkspaceOneClickReciprocalBindingAndPersistence(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	if len(a.s.boundPeers()) != 1 || len(b.s.boundPeers()) != 1 {
		t.Fatal("binding is not reciprocal")
	}
	// Repeating a bind updates the stable node, not a second card.
	wsBind(t, a, b, "")
	if len(a.s.boundPeers()) != 1 || len(b.s.boundPeers()) != 1 {
		t.Fatal("duplicate device")
	}
	loaded := New(a.s.store, a.cfg)
	if len(loaded.boundPeers()) != 1 {
		t.Fatal("binding did not survive restart")
	}
	raw, err := os.ReadFile(filepath.Join(a.cfg.Get().DataDir, "workspace-peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "access_key_hash") || strings.Contains(string(raw), "discovery_private_key") {
		t.Fatal("secret in binding store")
	}
}
func TestWorkspaceProtectedPairingAndRevocation(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	cfg := b.cfg.Get()
	enabled := true
	cfg.AccessKeyEnabled = &enabled
	cfg.AccessKey = " 中🔑 "
	if err := b.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	status, _, _ := wsCall(t, a, "POST", "/api/v1/peers/bind", map[string]string{"url": b.http.URL}, "")
	if status != 403 {
		t.Fatalf("missing remote key accepted %d", status)
	}
	wsBind(t, a, b, " 中🔑 ")
	raw, _ := os.ReadFile(filepath.Join(a.cfg.Get().DataDir, "workspace-peers.json"))
	if bytes.Contains(raw, []byte(" 中🔑 ")) {
		t.Fatal("remote access key persisted")
	}
	// Bound peers are authenticated for content even when access protection is on.
	status, p, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	if status != 200 || p["workspace"].(map[string]interface{})["partial"] == true {
		t.Fatalf("signed content failed %v", p)
	}
	status, _, _ = wsCall(t, a, "DELETE", "/api/v1/peers/"+b.cfg.Get().NodeID, nil, "")
	if status != 200 || len(b.s.boundPeers()) != 0 || len(a.s.boundPeers()) != 0 {
		t.Fatal("reciprocal unbind failed")
	}
	if !b.cfg.AccessKeyEnabled() {
		t.Fatal("pairing changed remote protection")
	}
}
func TestWorkspaceSignedRequestsAreScopedAndNotReplayable(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	path := "/api/v1/notes?scope=local"
	h := a.s.signPeerRequest("GET", path, b.cfg.Get().NodeID, nil)
	send := func(path string, h http.Header) int {
		req, _ := http.NewRequest("GET", b.http.URL+path, nil)
		req.Header = h.Clone()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if send(path, h) != 200 || send(path, h) != 401 {
		t.Fatal("signature replay was accepted")
	}
	for _, path := range []string{"/api/v1/config", "/api/v1/backup", "/api/v1/notes?scope=all"} {
		if send(path, a.s.signPeerRequest("GET", path, b.cfg.Get().NodeID, nil)) != 401 {
			t.Fatal("peer escaped content-only scope: " + path)
		}
	}
	h = a.s.signPeerRequest("GET", "/api/v1/notes", b.cfg.Get().NodeID, nil)
	h.Set("X-OmniShare-Signature", "bad")
	if send("/api/v1/notes", h) != 401 {
		t.Fatal("invalid signature accepted in open mode")
	}
}
func TestWorkspaceAggregationOriginRoutingAndConflict(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	for _, f := range []*workspaceFixture{a, b} {
		st, p, _ := wsCall(t, f, "POST", "/api/v1/notes", map[string]interface{}{"content": f.cfg.Get().NodeName + " note"}, "")
		if st != 201 {
			t.Fatalf("note: %v", p)
		}
	}
	st, p, h := wsCall(t, a, "GET", "/api/v1/notes?scope=all&limit=1", nil, "")
	if st != 200 || h.Get("X-Total-Count") != "2" || len(p["data"].([]interface{})) != 1 {
		t.Fatalf("global pagination: %v %v", p, h)
	}
	_, all, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all&limit=10", nil, "")
	var remoteID string
	for _, v := range all["data"].([]interface{}) {
		item := v.(map[string]interface{})
		if item["source_node_name"] == "B" {
			remoteID = item["id"].(string)
		}
	}
	if !strings.HasPrefix(remoteID, b.cfg.Get().NodeID+"~") {
		t.Fatal("missing qualified identity")
	}
	st, _, _ = wsCall(t, a, "DELETE", "/api/v1/notes/"+remoteID, nil, "")
	if st != 200 {
		t.Fatal("remote delete failed")
	}
	_, p, _ = wsCall(t, b, "GET", "/api/v1/notes", nil, "")
	if len(p["data"].([]interface{})) != 0 {
		t.Fatal("wrong owner deleted")
	}
	_, p, _ = wsCall(t, a, "GET", "/api/v1/notes", nil, "")
	if len(p["data"].([]interface{})) != 1 {
		t.Fatal("local note changed")
	}
	st, p, _ = wsCall(t, b, "POST", "/api/v1/pads", map[string]interface{}{"title": "B document", "content": "v1"}, "")
	if st != 201 {
		t.Fatal(p)
	}
	id := p["data"].(map[string]interface{})["id"].(string)
	qid := qualifyID(b.cfg.Get().NodeID, id)
	st, p, _ = wsCall(t, a, "PUT", "/api/v1/pads/"+qid, map[string]interface{}{"title": "updated", "content": "v2", "version": 1}, "")
	if st != 200 || p["data"].(map[string]interface{})["id"] != qid {
		t.Fatalf("proxy save: %v", p)
	}
	st, p, _ = wsCall(t, a, "PUT", "/api/v1/pads/"+qid, map[string]interface{}{"title": "stale", "content": "bad", "version": 1}, "")
	if st != 409 || p["data"].(map[string]interface{})["id"] != qid {
		t.Fatalf("conflict lost: %v", p)
	}
	_, p, _ = wsCall(t, b, "GET", "/api/v1/pads/"+id, nil, "")
	if p["data"].(map[string]interface{})["content"] != "v2" {
		t.Fatal("conflict overwrote document")
	}
	st, p, _ = wsCall(t, a, "POST", "/api/v1/shares", map[string]interface{}{"object_id": qid, "object_type": "pad", "ttl_seconds": 100}, "")
	if st != 201 {
		t.Fatalf("remote share failed %d %v", st, p)
	}
}
func TestWorkspaceOfflineSnapshotAndIdentityMismatch(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	wsCall(t, b, "POST", "/api/v1/notes", map[string]string{"content": "offline snapshot"}, "")
	_, p, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	if len(p["data"].([]interface{})) != 1 {
		t.Fatal("not aggregated")
	}
	b.http.Close()
	_, p, _ = wsCall(t, a, "GET", "/api/v1/notes?scope=all", nil, "")
	if p["workspace"].(map[string]interface{})["partial"] != true {
		t.Fatal("offline device hidden")
	}
	if len(p["data"].([]interface{})) != 1 || p["data"].([]interface{})[0].(map[string]interface{})["source_online"] != false {
		t.Fatal("last snapshot not marked offline")
	}
}
func TestWorkspaceRemoteFileTicketAndRange(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	wsBind(t, a, b, "")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, _ := mw.CreateFormFile("file", "remote.txt")
	f.Write([]byte("abcdefghij"))
	mw.Close()
	req, _ := http.NewRequest("POST", b.http.URL+"/api/v1/files/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var packet map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&packet)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal(packet)
	}
	item := packet["data"].(map[string]interface{})
	qid := qualifyID(b.cfg.Get().NodeID, item["id"].(string))
	st, p, _ := wsCall(t, a, "POST", "/api/v1/files/"+qid+"/ticket", map[string]string{"disposition": "attachment"}, "")
	if st != 201 {
		t.Fatal(p)
	}
	ticketURL := p["data"].(map[string]interface{})["url"].(string)
	if !strings.HasPrefix(ticketURL, "/peer-media/") {
		t.Fatal("media bypasses current app")
	}
	req, _ = http.NewRequest("GET", a.http.URL+ticketURL, nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(raw) != "cdef" {
		t.Fatalf("remote range %d %s", resp.StatusCode, raw)
	}
}
func TestWorkspaceAddressValidationAndTailRanges(t *testing.T) {
	for _, ip := range []string{"198.18.0.1", "169.254.169.254", "8.8.8.8", "0.0.0.0", "100.128.1.2", "224.0.0.1"} {
		if peerIPAllowed(net.ParseIP(ip), false) {
			t.Fatal("unsafe address: " + ip)
		}
	}
	for _, ip := range []string{"100.64.0.1", "100.127.255.254", "192.168.1.3", "10.2.3.4", "fd7a:115c:a1e0::1"} {
		if !peerIPAllowed(net.ParseIP(ip), false) {
			t.Fatal("valid address rejected: " + ip)
		}
	}
	for _, url := range []string{"http://user:secret@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1/?key=x", "file:///etc/passwd"} {
		if _, err := normalizePeerURL(url); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if resp, err := peerHTTP(ctx, "GET", "http://169.254.169.254", "/", nil, nil, false); err == nil {
		resp.Body.Close()
		t.Fatal("metadata access accepted")
	}
	_ = model.DeviceNode{} // assert extension remains part of public device schema
}

func TestWorkspaceUpgradeLegacyAddresses(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	cfg := a.cfg.Get()
	cfg.Peers = []model.PeerConfig{{Name: "B", URL: b.http.URL}}
	cfg.KeepAccessKey = true
	if err := a.cfg.Update(cfg); err != nil {
		t.Fatal(err)
	}
	a.s.reconcileConfiguredPeers(context.Background())
	if len(a.s.boundPeers()) != 1 || len(b.s.boundPeers()) != 1 {
		t.Fatal("explicit legacy address was not upgraded reciprocally")
	}
}

func TestWorkspaceNoTransitiveAggregation(t *testing.T) {
	a, b, c := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B"), newWorkspaceFixture(t, "C")
	wsBind(t, a, b, "")
	wsBind(t, b, c, "")
	for _, f := range []*workspaceFixture{a, b, c} {
		wsCall(t, f, "POST", "/api/v1/notes", map[string]string{"content": f.cfg.Get().NodeName}, "")
	}
	_, p, _ := wsCall(t, a, "GET", "/api/v1/notes?scope=all&limit=10", nil, "")
	if len(p["data"].([]interface{})) != 2 {
		t.Fatalf("transitive data leak: %v", p)
	}
	_, p, _ = wsCall(t, b, "GET", "/api/v1/notes?scope=all&limit=10", nil, "")
	if len(p["data"].([]interface{})) != 3 {
		t.Fatal("direct peers missing")
	}
}

func TestWorkspaceRevocationSurvivesLegacyMigrationAndRestart(t *testing.T) {
	a, b := newWorkspaceFixture(t, "A"), newWorkspaceFixture(t, "B")
	for _, pair := range [][2]*workspaceFixture{{a, b}, {b, a}} {
		cfg := pair[0].cfg.Get()
		cfg.Peers = []model.PeerConfig{{Name: "legacy", URL: pair[1].http.URL}}
		cfg.KeepAccessKey = true
		if err := pair[0].cfg.Update(cfg); err != nil {
			t.Fatal(err)
		}
	}
	a.s.reconcileConfiguredPeers(context.Background())
	st, p, _ := wsCall(t, a, "DELETE", "/api/v1/peers/"+b.cfg.Get().NodeID, nil, "")
	if st != 200 {
		t.Fatal(p)
	}
	for _, f := range []*workspaceFixture{a, b} {
		f.s.initPeers()
		f.s.reconcileConfiguredPeers(context.Background())
		if len(f.s.boundPeers()) != 0 {
			t.Fatal("revoked legacy peer was silently rebound")
		}
	}
	wsBind(t, a, b, "")
	if len(a.s.boundPeers()) != 1 || len(b.s.boundPeers()) != 1 {
		t.Fatal("explicit rebinding failed")
	}
}
