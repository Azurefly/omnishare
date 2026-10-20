package api

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The desktop watchdog does not parse JSON: it opens a raw socket and greps the
// response body for the literal `"status":"ok"`. That turns the health payload
// into a contract spanning two languages, so a body-shape change must fail a Go
// test instead of silently breaking desktop startup detection on the next build.
func TestHealthPayloadKeepsWatchdogContract(t *testing.T) {
	_, handler, _ := newTestServer(t)
	w := request(t, handler, http.MethodGet, "/api/v1/health", nil, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("health returned %d", w.Code)
	}
	body := w.Body.String()
	// Must stay byte-identical to the marker in
	// desktop/src-tauri/src/runtime/health.rs.
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("watchdog marker missing from health payload: %s", body)
	}
	// The probe downloads the whole response every few seconds for the life of
	// the app, so the payload must stay small.
	if len(body) > 512 {
		t.Fatalf("health payload grew to %d bytes; the watchdog polls it every 5s", len(body))
	}
}

// Host validation runs on every request. Snapshot reads must therefore be safe
// without a lock and must not mutate the snapshot other readers observe.
func TestLocalNetworkSnapshotIsSharedAndStable(t *testing.T) {
	s, _, _ := newTestServer(t)

	first := s.localNetwork()
	if first == nil {
		t.Skip("this host exposed no usable interfaces")
	}
	second := s.localNetwork()
	if first != second {
		t.Fatal("localNetwork refreshed inside the TTL; every request would pay the syscalls")
	}
	if len(first.addrs) == 0 {
		t.Fatal("snapshot published an empty address set")
	}
	if len(first.ips) == 0 {
		t.Fatal("snapshot published no address list for the device endpoint")
	}

	// A refresh must replace the snapshot atomically rather than mutate it, so
	// expire the current one and confirm the published snapshot really changed.
	s.localCache.Store(&localNetworkSnapshot{addrs: map[string]struct{}{"10.0.0.1": {}}, ips: []string{"10.0.0.1"}, until: time.Now().Add(-time.Second)})
	refreshed := s.localNetwork()
	if refreshed == nil || !refreshed.until.After(time.Now()) {
		t.Fatal("expired snapshot was not refreshed")
	}
	if len(refreshed.ips) == 0 {
		t.Fatal("refreshed snapshot is unusable for the device endpoint")
	}
	if _, ok := refreshed.addrs[refreshed.ips[0]]; !ok {
		t.Fatal("refreshed snapshot lost its own address list")
	}
	if !s.isLocalAddress(net.ParseIP(refreshed.ips[0])) {
		t.Fatal("isLocalAddress lost track of a local address after refresh")
	}
	if s.isLocalAddress(net.ParseIP("192.0.2.1")) {
		t.Fatal("isLocalAddress accepted an address that is not local")
	}
}

// Concurrent requests hit validHost and the device list at the same time. The
// snapshot is designed to be read without holding any lock.
func TestLocalNetworkConcurrentAccessIsRaceFree(t *testing.T) {
	s, handler, _ := newTestServer(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				s.localNetwork()
				s.isLocalAddress(net.ParseIP("127.0.0.2"))
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
				r.Host = "127.0.0.1:8081"
				r.RemoteAddr = "127.0.0.1:12345"
				handler.ServeHTTP(httptest.NewRecorder(), r)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// A blocked client must stay blocked while an idle table is not swept, and a
// crowded table must be trimmed without locking a currently blocked client out.
func TestCleanupAuthKeepsBlockedAndBoundsTable(t *testing.T) {
	s, handler, cfg := newTestServer(t)
	current := cfg.Get()
	current.AccessKey = "0123456789abcdef"
	current.AllowLAN = true
	current.ListenAddress = "0.0.0.0"
	if err := cfg.Update(current); err != nil {
		t.Fatal(err)
	}
	ip := "203.0.113.7"

	for i := 0; i < 5; i++ {
		requestAuth(t, handler, "bad-bad-bad-bad", "203.0.113.7")
	}
	s.mu.Lock()
	entry, ok := s.auth[ip]
	if !ok {
		s.mu.Unlock()
		t.Fatal("failed login attempt was not recorded")
	}
	if entry.BlockedTill.IsZero() {
		s.mu.Unlock()
		t.Fatal("five failures should have blocked the client")
	}
	// Force the sweep a long time in the future: it must be throttled, and the
	// blocked entry must survive because it is still inside its window.
	s.authSweep = time.Now().Add(time.Hour)
	s.mu.Unlock()

	if retry := s.authRetryAfter(ip); retry <= 0 {
		t.Fatal("blocked client was released while still blocked")
	}

	// Two kinds of table growth exist: stale entries that the timestamp check
	// should reclaim, and fresh entries that only the cap can trim. Check both,
	// and confirm a single call cannot do unbounded eviction work.
	// The blocked entry was created moments ago, so it must outrank the fresh
	// filler entries below.
	sweepAt := time.Now().Add(11 * time.Minute)
	s.mu.Lock()
	s.cleanupAuth(sweepAt)
	stillBlocked := s.auth[ip]
	s.mu.Unlock()
	if stillBlocked == nil || stillBlocked.BlockedTill.IsZero() {
		t.Fatal("sweep dropped a blocked entry it should have kept")
	}

	crowd := time.Now()
	s.mu.Lock()
	for i := 0; i < 200; i++ {
		s.auth[fmt.Sprintf("198.51.100.%d", i)] = &authState{WindowStart: crowd.Add(-time.Hour), BlockedTill: crowd.Add(-time.Hour)}
	}
	for i := 0; i < 150; i++ {
		// Inside the 10 minute window, so only the cap can reclaim these.
		s.auth[fmt.Sprintf("192.0.2.%d", i)] = &authState{WindowStart: crowd.Add(-9 * time.Minute)}
	}
	s.authSweep = time.Time{}
	before := len(s.auth)
	s.cleanupAuth(crowd)
	afterFirst := len(s.auth)
	blocked := s.auth[ip]
	s.mu.Unlock()

	if afterFirst >= before {
		t.Fatal("cleanup did not trim a table above its cap")
	}
	if blocked == nil {
		t.Fatal("eviction removed the currently blocked entry")
	}
	if afterFirst > maxAuthEntries+1 {
		t.Fatalf("cleanup left %d entries, above the %d cap", afterFirst, maxAuthEntries)
	}
	// Entries older than the window are reclaimed by the timestamp check; the
	// per-call ceiling only bounds the cap-driven evictions.
	if removed := before - afterFirst; removed < 200 {
		t.Fatalf("cleanup reclaimed %d stale entries, want at least 200", removed)
	}
}

// requestAuth sends an authentication attempt from a chosen source address.
func requestAuth(t *testing.T, h http.Handler, key, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/verify", nil)
	r.Host = "127.0.0.1:8081"
	r.RemoteAddr = remoteAddr + ":54321"
	if key != "" {
		r.Header.Set("X-OmniShare-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The global counter must track the per-IP table exactly, including the
// peripheral paths where a key disappears or was never entered.
func TestRequestConcurrencyAccountingIsExact(t *testing.T) {
	s, _, _ := newTestServer(t)

	for i := 0; i < 8; i++ {
		if !s.enterRequest("192.0.2.9", 8, 128) {
			t.Fatalf("request %d rejected below the per-IP limit", i)
		}
	}
	if s.enterRequest("192.0.2.9", 8, 128) {
		t.Fatal("per-IP concurrency limit was not enforced")
	}
	if !s.enterRequest("192.0.2.10", 8, 128) {
		t.Fatal("a different client was blocked by the per-IP limiter")
	}
	if s.requestTotal != 9 {
		t.Fatalf("global counter is %d, want 9", s.requestTotal)
	}
	for i := 0; i < 100; i++ {
		s.leaveRequest("192.0.2.99") // never entered: must not corrupt the total
	}
	for i := 0; i < 8; i++ {
		s.leaveRequest("192.0.2.9")
	}
	s.leaveRequest("192.0.2.10")
	if s.requestTotal != 0 {
		t.Fatalf("global counter leaked to %d", s.requestTotal)
	}
	if s.enterRequest("192.0.2.9", 8, 128) != true {
		t.Fatal("limiter stayed closed after releases")
	}
}
