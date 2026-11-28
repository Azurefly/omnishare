package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var tailRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
var benchmarkRange = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}
var errBindingIdentity = errors.New("selected peer identity changed")

type returnAttempt struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

type bindingRouteError struct {
	cause    error
	attempts []returnAttempt
}

func (e *bindingRouteError) Error() string { return e.cause.Error() }
func (e *bindingRouteError) Unwrap() error { return e.cause }

func returnFailure(err error) string {
	var networkError net.Error
	var dnsError *net.DNSError
	var certificateError *tls.CertificateVerificationError
	switch {
	case errors.Is(err, errBindingIdentity), errors.Is(err, errInvalidPeerSignature):
		return "identity"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout():
		return "timeout"
	case errors.Is(err, syscall.ECONNREFUSED), runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10061)): // WSAECONNREFUSED
		return "refused"
	case errors.As(err, &dnsError):
		return "dns"
	case errors.As(err, &certificateError):
		return "certificate"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH),
		runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(10051)) || errors.Is(err, syscall.Errno(10065))):
		return "unreachable"
	case strings.Contains(err.Error(), "not a compatible"):
		return "incompatible"
	default:
		return "unavailable"
	}
}

func formatReturnAttempts(attempts []returnAttempt) string {
	reasons := map[string]string{
		"timeout": "连接超时", "refused": "端口拒绝连接", "identity": "设备身份校验失败",
		"unreachable": "网络或主机不可达", "dns": "地址解析失败", "certificate": "HTTPS 证书校验失败",
		"incompatible": "该端口未返回兼容的 OmniShare 身份",
	}
	var lines []string
	for _, a := range attempts[:min(len(attempts), 24)] {
		base, err := normalizePeerURL(a.URL)
		if err != nil {
			continue
		}
		reason := reasons[a.Reason]
		if reason == "" {
			reason = "未能完成 OmniShare 握手"
		}
		lines = append(lines, base+"　"+reason)
	}
	return strings.Join(lines, "\n")
}

func peerIPAllowed(ip net.IP, loop bool) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || benchmarkRange.Contains(ip) {
		return false
	}
	if ip.IsLoopback() {
		return loop
	}
	return ip.IsPrivate() || tailRange.Contains(ip)
}
func normalizePeerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("请输入设备的 http/https 地址和 OmniShare 端口，不要包含路径或密钥")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("端口无效")
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// Resolve on each connection, reject mixed public/private DNS answers, and pin
// the dial to the validated IP. Neither environment proxies nor redirects may
// turn a LAN peer request into a credential-bearing request to another host.
func peerHTTP(ctx context.Context, method, base, path string, body []byte, headers http.Header, loop bool) (*http.Response, error) {
	base, err := normalizePeerURL(base)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\r\n\\") {
		return nil, errors.New("invalid peer path")
	}
	u, _ := url.Parse(base)
	host := u.Hostname()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("no peer address")
	}
	for _, a := range ips {
		if !peerIPAllowed(a.IP, loop) {
			return nil, errors.New("device address is not a LAN or Tailscale address")
		}
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 8 * time.Second}
	transport.DialContext = func(c context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var last error
		for _, a := range ips {
			conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(c, network, net.JoinHostPort(a.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	return client.Do(req)
}
func peerError(err error) string {
	if errors.Is(err, errBindingIdentity) {
		return "此地址的设备身份已变化，请刷新设备列表后重试"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时，请检查服务端口、防火墙或 Tailscale 访问规则"
	}
	if errors.Is(err, context.Canceled) {
		return "连接已取消"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "连接超时"
	}
	text := err.Error()
	if strings.Contains(text, "refused") {
		return "连接被拒绝，目标端口没有服务或被防火墙拒绝"
	}
	if strings.Contains(text, "certificate") || strings.Contains(text, "tls:") {
		return "HTTPS 证书验证失败"
	}
	if strings.Contains(text, "not a LAN") {
		return "地址不是允许的内网或 Tailscale 地址"
	}
	if strings.Contains(text, "compatible") || strings.Contains(text, "identity") {
		return "对方不是兼容版本的 OmniShare，请两端同时升级"
	}
	return "无法连接 OmniShare 服务"
}
func uniqueRoutes(p peerBinding) []string {
	return mergePeerURLs([]string{p.URL}, p.VerifiedURLs, p.Identity.Addresses)
}

func mergePeerURLs(groups ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, routes := range groups {
		for _, raw := range routes {
			if base, err := normalizePeerURL(raw); err == nil && !seen[base] {
				seen[base] = true
				out = append(out, base)
			}
		}
	}
	return out
}

// Re-prove the selected identity immediately before binding. Only these
// read-only challenges race; exactly one endpoint receives the accept request.
func (s *Server) identifyBindingRoute(ctx context.Context, routes []string, expected peerIdentity, loop bool) (peerIdentity, string, error) {
	type result struct {
		identity peerIdentity
		base     string
		err      error
	}
	// Record only started probes. A deadline can leave queued routes untested.
	var mu sync.Mutex
	attempted := make(map[string]error)
	failure := func(cause error) error {
		mu.Lock()
		defer mu.Unlock()
		e := &bindingRouteError{cause: cause}
		for _, base := range routes {
			if err, started := attempted[base]; started {
				if err == nil {
					err = cause
				}
				e.attempts = append(e.attempts, returnAttempt{URL: base, Reason: returnFailure(err)})
			}
		}
		return e
	}
	probe := func(c context.Context, base string, allowLoop bool) result {
		mu.Lock()
		attempted[base] = nil
		mu.Unlock()
		id, err := s.readIdentity(c, base, allowLoop)
		if err == nil && expected.ID != "" && (id.ID != expected.ID || (expected.PublicKey != "" && id.PublicKey != expected.PublicKey)) {
			err = errBindingIdentity
		}
		mu.Lock()
		attempted[base] = err
		mu.Unlock()
		return result{id, base, err}
	}
	primary := routes[0]
	alternatives := []string{}
	if expected.ID != "" {
		for _, base := range routes[1:] {
			if !strings.HasPrefix(primary, "https://") || strings.HasPrefix(base, "https://") {
				alternatives = append(alternatives, base)
			}
		}
	}
	if len(alternatives) == 0 {
		r := probe(ctx, primary, loop)
		if r.err != nil {
			r.err = failure(r.err)
		}
		return r.identity, r.base, r.err
	}
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, 1+len(alternatives))
	go func() { results <- probe(c, primary, loop) }()
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	pending := 1 + len(alternatives)
	var firstErr error
	select {
	case r := <-results:
		if r.err == nil {
			return r.identity, r.base, nil
		}
		firstErr = r.err
		pending--
	case <-timer.C:
	case <-c.Done():
		return peerIdentity{}, "", failure(c.Err())
	}
	u, _ := url.Parse(primary)
	allowLoop := loop && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
	jobs := make(chan string, len(alternatives))
	for _, route := range alternatives {
		jobs <- route
	}
	close(jobs)
	for range min(3, len(alternatives)) {
		go func() {
			for route := range jobs {
				if c.Err() != nil {
					return
				}
				one, done := context.WithTimeout(c, 3*time.Second)
				r := probe(one, route, allowLoop)
				done()
				results <- r
			}
		}()
	}
	for range pending {
		select {
		case r := <-results:
			if r.err == nil {
				return r.identity, r.base, nil
			}
			if errors.Is(r.err, errBindingIdentity) || (r.base == primary && !errors.Is(firstErr, errBindingIdentity)) || firstErr == nil {
				firstErr = r.err
			}
		case <-c.Done():
			return peerIdentity{}, "", failure(c.Err())
		}
	}
	return peerIdentity{}, "", failure(firstErr)
}

func (s *Server) peerRequest(ctx context.Context, p peerBinding, method, path string, body []byte, extra http.Header) (*http.Response, error) {
	if method == http.MethodGet || method == http.MethodHead {
		if err := s.peerBackoff(p.Identity.ID); err != nil {
			return nil, err
		}
	}
	routes := s.peerRoutes(p)
	s.peers.mu.Lock()
	preferred := s.peers.routes[p.Identity.ID]
	s.peers.mu.Unlock()
	for i, route := range routes {
		if route == preferred {
			routes[0], routes[i] = routes[i], routes[0]
			break
		}
	}
	var last error = errors.New("no verified route to device")
	for _, base := range routes {
		id, err := s.routeProbe(ctx, base)
		if err != nil {
			last = err
			continue
		}
		if id.ID != p.Identity.ID || id.PublicKey != p.Identity.PublicKey {
			last = errors.New("peer identity changed; bind again explicitly")
			continue
		}
		h := s.signPeerRequest(method, path, p.Identity.ID, body)
		for _, key := range []string{"Content-Type", "Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
			if v := extra.Get(key); v != "" {
				h.Set(key, v)
			}
		}
		resp, err := peerHTTP(ctx, method, base, path, body, h, true)
		if err != nil {
			// Drop the remembered proof so the next request re-proves (or
			// backs off from) this route instead of trusting it blindly.
			s.recordRoute(ctx, base, peerIdentity{}, err)
			last = err
			if method == http.MethodGet || method == http.MethodHead {
				continue
			}
			return nil, err
		} // never replay a mutation
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
			resp.Body.Close()
			return nil, errors.New("peer redirected a protected request")
		}
		s.recordPeer(ctx, p.Identity.ID, nil)
		s.peers.mu.Lock()
		s.peers.routes[p.Identity.ID] = base
		s.peers.mu.Unlock()
		return resp, nil
	}
	if method == http.MethodGet || method == http.MethodHead {
		s.recordPeer(ctx, p.Identity.ID, last)
	}
	return nil, last
}
func relayPeerResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Content-Disposition", "Accept-Ranges", "ETag", "Last-Modified", "X-Total-Count"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
