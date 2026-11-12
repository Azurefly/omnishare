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
		return data, errors.New("Tailscale 未连接或状态无效")
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

// Tailscale is routed, not a broadcast LAN. Read the local CLI, without an admin
// API key, then verify OmniShare on a bounded set of service ports. Discovered
// devices are candidates only; they do not acquire workspace trust automatically.
func (s *Server) StartNetworkServices(ctx context.Context) {
	go func() {
		s.refreshTailDevices(ctx)
		s.reconcileConfiguredPeers(ctx)
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.refreshTailDevices(ctx)
				s.reconcileConfiguredPeers(ctx)
			}
		}
	}()
}
func (s *Server) refreshTailDevices(parent context.Context) {
	if !s.cfg.Get().AllowLAN {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	probe, done := context.WithTimeout(ctx, 4*time.Second)
	status, err := readTailStatus(probe)
	done()
	s.peers.tailMu.Lock()
	if err != nil {
		s.peers.tailStatus = err.Error()
		s.peers.tailNodes = nil
		s.peers.tailSelf = nil
		s.peers.tailNames = nil
		s.peers.tailMu.Unlock()
		return
	}
	if status.Self != nil {
		s.peers.tailSelf = tailIPs(status.Self.TailscaleIPs)
		s.peers.tailNames = map[string]bool{}
		if name := strings.ToLower(strings.TrimSuffix(status.Self.DNSName, ".")); name != "" {
			s.peers.tailNames[name] = true
		}
	}
	s.peers.tailStatus = "Tailscale 已连接，正在检测 OmniShare 服务"
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
	ports := []int{s.cfg.Get().Port, 8081, 8082, 8083, 8084, 8085}
	seenPorts := map[int]bool{}
	unique := []int{}
	for _, p := range ports {
		if !seenPorts[p] {
			seenPorts[p] = true
			unique = append(unique, p)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	found := []peerIdentity{}
	sem := make(chan struct{}, 4)
	for _, k := range keys {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(p *tailPeer) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, ip := range tailIPs(p.TailscaleIPs) {
				for _, port := range unique {
					if ctx.Err() != nil {
						return
					}
					base := "http://" + net.JoinHostPort(ip, strconv.Itoa(port))
					one, c := context.WithTimeout(ctx, 1100*time.Millisecond)
					id, e := s.readIdentity(one, base, false)
					c()
					if e == nil && id.ID != s.cfg.Get().NodeID {
						id.Addresses = append([]string{base}, id.Addresses...)
						mu.Lock()
						found = append(found, id)
						mu.Unlock()
						return
					}
				}
			}
		}(status.Peer[k])
	}
	wg.Wait()
	s.peers.tailMu.Lock()
	s.peers.tailNodes = found
	s.peers.tailStatus = "Tailscale 已连接 · 已发现 " + strconv.Itoa(len(found)) + " 个 OmniShare 设备（其他端口或 HTTPS 请手动添加）"
	s.peers.tailMu.Unlock()
}
