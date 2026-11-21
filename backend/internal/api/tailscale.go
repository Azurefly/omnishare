package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type tailStatus struct {
	BackendState string
	Self         *tailPeer
	Peer         map[string]*tailPeer
}
type tailPeer struct {
	HostName     string
	DNSName      string
	Online       bool
	TailscaleIPs []string
}
type boundedTailOutput struct{ bytes.Buffer }

func (w *boundedTailOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 2<<20 {
		return 0, io.ErrShortBuffer
	}
	return w.Buffer.Write(p)
}
func tailscaleExecutable() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	}
	if runtime.GOOS == "windows" {
		candidates = []string{filepath.Join(os.Getenv("ProgramFiles"), "Tailscale", "tailscale.exe")}
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && filepath.IsAbs(p) && !info.IsDir() {
			return p
		}
	}
	return ""
}
func readTailStatus(ctx context.Context) (tailStatus, error) {
	var data tailStatus
	path := tailscaleExecutable()
	if path == "" {
		return data, errors.New("未检测到 Tailscale 客户端；仍可手动绑定其 IP 地址")
	}
	cmd := exec.CommandContext(ctx, path, "status", "--json")
	configureTailCommand(cmd)
	cmd.Env = append(os.Environ(), "TERM=xterm", "SHLVL=1")
	var out boundedTailOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return data, errors.New("无法读取 Tailscale 状态，请确认已登录且当前用户有读取权限")
	}
	if json.Unmarshal(out.Bytes(), &data) != nil || data.BackendState != "Running" {
		return data, errors.New("tailscale 未连接或状态无效")
	}
	return data, nil
}
func tailIPs(values []string) []string {
	out := []string{}
	for _, v := range values {
		ip := net.ParseIP(v)
		if ip != nil && (tailRange.Contains(ip) || strings.HasPrefix(ip.String(), "fd7a:115c:a1e0:")) {
			out = append(out, ip.String())
		}
	}
	return out
}

const (
	tailPollInterval     = 60 * time.Second
	tailProbeBackoffBase = 2 * time.Minute
	tailProbeBackoffMax  = 15 * time.Minute
	tailProbeLimit       = 256
)

// Tailscale is routed, not a broadcast LAN. Read the local CLI, without an admin
// API key, then verify OmniShare on a bounded set of service ports. Discovered
// devices are candidates only; they do not acquire workspace trust automatically.
func (s *Server) StartNetworkServices(ctx context.Context) {
	s.peers.tailMu.Lock()
	s.peers.tailStarted = true
	refreshCh := s.peers.tailRefresh
	s.peers.tailMu.Unlock()
	go func() {
		s.refreshTailDevices(ctx, false)
		s.reconcileConfiguredPeers(ctx)
		t := time.NewTicker(tailPollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshCh:
				s.refreshTailDevices(ctx, true)
				s.reconcileConfiguredPeers(ctx)
			case <-t.C:
				s.refreshTailDevices(ctx, false)
				s.reconcileConfiguredPeers(ctx)
			}
		}
	}()
}

func (s *Server) resetTailProbeBackoff(trigger bool) {
	s.peers.tailMu.Lock()
	for k, st := range s.peers.tailProbes {
		st.failures = 0
		st.nextProbe = time.Time{}
		s.peers.tailProbes[k] = st
	}
	started := s.peers.tailStarted
	refreshCh := s.peers.tailRefresh
	s.peers.tailMu.Unlock()
	if trigger && started && refreshCh != nil {
		select {
		case refreshCh <- struct{}{}:
		default:
		}
	}
}

func tailProbeCandidateURLs(ips []string, ports []int, preferredURL string) []string {
	urls := make([]string, 0, len(ips)*len(ports)+1)
	seen := map[string]bool{}
	if preferredURL != "" {
		urls = append(urls, preferredURL)
		seen[preferredURL] = true
	}
	for _, ip := range ips {
		for _, port := range ports {
			base := "http://" + net.JoinHostPort(ip, strconv.Itoa(port))
			if !seen[base] {
				seen[base] = true
				urls = append(urls, base)
			}
		}
	}
	return urls
}

func (s *Server) refreshTailDevices(parent context.Context, force bool) {
	if !s.cfg.Get().AllowLAN {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	probe, done := context.WithTimeout(ctx, 4*time.Second)
	status, err := readTailStatus(probe)
	done()
	if err != nil {
		s.peers.tailMu.Lock()
		s.peers.tailStatus = err.Error()
		s.peers.tailNodes = nil
		s.peers.tailSelf = nil
		s.peers.tailNames = nil
		s.peers.tailMu.Unlock()
		return
	}
	s.refreshTailDevicesFromStatus(ctx, status, time.Now(), force)
}

func (s *Server) refreshTailDevicesFromStatus(ctx context.Context, status tailStatus, now time.Time, force bool) {
	cfg := s.cfg.Get()
	s.peers.tailMu.Lock()
	if status.Self != nil {
		s.peers.tailSelf = tailIPs(status.Self.TailscaleIPs)
		s.peers.tailNames = map[string]bool{}
		if name := strings.ToLower(strings.TrimSuffix(status.Self.DNSName, ".")); name != "" {
			s.peers.tailNames[name] = true
		}
	}
	s.peers.tailStatus = "Tailscale 已连接，正在检测 OmniShare 服务"
	if s.peers.tailProbes == nil {
		s.peers.tailProbes = map[string]tailProbeState{}
	}
	// Clear state for peers that went offline or disappeared so reconnecting wakes probing immediately.
	for k := range s.peers.tailProbes {
		p := status.Peer[k]
		if p == nil || !p.Online {
			delete(s.peers.tailProbes, k)
		}
	}
	s.peers.tailMu.Unlock()

	keys := []string{}
	for k, p := range status.Peer {
		if p != nil && p.Online {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 32 {
		keys = keys[:32]
	}
	ports := []int{cfg.Port, 8081, 8082, 8083, 8084, 8085}
	seenPorts := map[int]bool{}
	unique := []int{}
	for _, p := range ports {
		if !seenPorts[p] {
			seenPorts[p] = true
			unique = append(unique, p)
		}
	}

	type probeOutcome struct {
		key       string
		ipSig     string
		attempted bool
		aborted   bool
		ok        bool
		lastURL   string
		node      peerIdentity
	}
	outcomes := make([]probeOutcome, 0, len(keys))
	var wg sync.WaitGroup
	var mu sync.Mutex
	found := []peerIdentity{}
	sem := make(chan struct{}, 4)

	s.peers.tailMu.RLock()
	probeFn := s.peers.tailProbeFn
	if probeFn == nil {
		probeFn = func(c context.Context, b string) (peerIdentity, error) {
			return s.readIdentity(c, b, false)
		}
	}
	snapshot := make(map[string]tailProbeState, len(keys))
	for _, k := range keys {
		if st, ok := s.peers.tailProbes[k]; ok {
			snapshot[k] = st
		}
	}
	s.peers.tailMu.RUnlock()

scanPeers:
	for _, k := range keys {
		p := status.Peer[k]
		ips := tailIPs(p.TailscaleIPs)
		if len(ips) == 0 {
			continue
		}
		ipSig := strings.Join(ips, ",")
		prev, hasPrev := snapshot[k]
		if hasPrev && !force && prev.ipSig == ipSig && !prev.nextProbe.IsZero() && now.Before(prev.nextProbe) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break scanPeers
		}
		preferred := ""
		if hasPrev && prev.ipSig == ipSig {
			preferred = prev.lastURL
		}
		urls := tailProbeCandidateURLs(ips, unique, preferred)
		wg.Add(1)
		go func(key, ipSig string, urls []string) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, base := range urls {
				if ctx.Err() != nil {
					mu.Lock()
					outcomes = append(outcomes, probeOutcome{key: key, ipSig: ipSig, attempted: true, aborted: true})
					mu.Unlock()
					return
				}
				one, c := context.WithTimeout(ctx, 1100*time.Millisecond)
				id, e := probeFn(one, base)
				c()
				if e == nil && id.ID != cfg.NodeID {
					id.Addresses = append([]string{base}, id.Addresses...)
					mu.Lock()
					found = append(found, id)
					outcomes = append(outcomes, probeOutcome{key: key, ipSig: ipSig, attempted: true, ok: true, lastURL: base, node: id})
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			outcomes = append(outcomes, probeOutcome{key: key, ipSig: ipSig, attempted: true, aborted: ctx.Err() != nil})
			mu.Unlock()
		}(k, ipSig, urls)
	}
	wg.Wait()

	s.peers.tailMu.Lock()
	if len(s.peers.tailProbes) >= tailProbeLimit {
		s.peers.tailProbes = map[string]tailProbeState{}
	}
	for _, out := range outcomes {
		if !out.attempted || out.aborted {
			continue
		}
		if out.ok {
			s.peers.tailProbes[out.key] = tailProbeState{
				ipSig:   out.ipSig,
				lastURL: out.lastURL,
				node:    out.node,
				hasNode: true,
			}
			continue
		}
		prev := s.peers.tailProbes[out.key]
		failures := 1
		if prev.ipSig == out.ipSig {
			failures = prev.failures + 1
		}
		backoff := tailProbeBackoffBase << min(failures-1, 3)
		if backoff > tailProbeBackoffMax {
			backoff = tailProbeBackoffMax
		}
		s.peers.tailProbes[out.key] = tailProbeState{
			ipSig:     out.ipSig,
			failures:  failures,
			nextProbe: now.Add(backoff),
		}
	}
	s.peers.tailNodes = found
	s.peers.tailStatus = "Tailscale 已连接 · 已发现 " + strconv.Itoa(len(found)) + " 个 OmniShare 设备（其他端口或 HTTPS 请手动添加）"
	s.peers.tailMu.Unlock()
}
