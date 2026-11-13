package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/model"
)

func deviceNetwork(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "manual"
	}
	ip := net.ParseIP(u.Hostname())
	if tailRange.Contains(ip) || strings.HasPrefix(u.Hostname(), "fd7a:115c:a1e0:") {
		return "tailscale"
	}
	if ip != nil && ip.IsLoopback() {
		return "loopback"
	}
	return "lan"
}
func (s *Server) workspaceDevices(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	local := s.nodeIdentity()
	addresses := append([]string(nil), local.Addresses...)
	if len(addresses) == 0 {
		scheme := "http"
		if cfg.TLSCertFile != "" {
			scheme = "https"
		}
		addresses = []string{scheme + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))}
	}
	u, _ := url.Parse(addresses[0])
	devices := []model.DeviceNode{{ID: local.ID, Hostname: local.Name, URL: addresses[0], IP: u.Hostname(), Port: cfg.Port, NetworkType: deviceNetwork(addresses[0]), IsLocal: true, Online: true, Bound: true, Addresses: addresses, Status: "本机", LastSeen: time.Now()}}
	type candidate struct {
		p       peerBinding
		bound   bool
		network string
	}
	candidates := []candidate{}
	known := map[string]bool{local.ID: true}
	for _, p := range s.boundPeers() {
		candidates = append(candidates, candidate{p, true, deviceNetwork(p.URL)})
		known[p.Identity.ID] = true
	}
	if s.registry != nil {
		for _, d := range s.registry.List() {
			u, _ := url.Parse(d.URL)
			if u != nil && benchmarkRange.Contains(net.ParseIP(u.Hostname())) {
				continue
			}
			if !known[d.ID] {
				candidates = append(candidates, candidate{peerBinding{Identity: peerIdentity{ID: d.ID, Name: d.Hostname, Addresses: []string{d.URL}}, URL: d.URL}, false, "lan"})
				known[d.ID] = true
			}
		}
	}
	s.peers.tailMu.RLock()
	tailMessage := s.peers.tailStatus
	for _, p := range s.peers.tailNodes {
		if !known[p.ID] && len(p.Addresses) > 0 {
			candidates = append(candidates, candidate{peerBinding{Identity: p, URL: p.Addresses[0]}, false, "tailscale"})
			known[p.ID] = true
		}
	}
	s.peers.tailMu.RUnlock()
	for _, p := range cfg.Peers {
		if !known[p.ID] {
			candidates = append(candidates, candidate{peerBinding{Identity: peerIdentity{ID: p.ID, Name: p.Name, Addresses: []string{p.URL}}, URL: p.URL}, false, deviceNetwork(p.URL)})
			known[p.ID] = true
		}
	}
	if len(candidates) > 64 {
		candidates = candidates[:64]
	}
	results := make([]model.DeviceNode, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	ctx, cancel := context.WithTimeout(r.Context(), 7*time.Second)
	defer cancel()
	for i, c := range candidates {
		wg.Add(1)
		go func(i int, c candidate) {
			defer wg.Done()
			node := model.DeviceNode{ID: c.p.Identity.ID, Hostname: c.p.Identity.Name, URL: c.p.URL, Addresses: uniqueRoutes(c.p), Bound: c.bound, NetworkType: c.network, Status: "连接超时", ConnectionError: "暂时无法访问服务", LastSeen: time.Now()}
			results[i] = node
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			start := time.Now()
			for _, base := range s.peerRoutes(c.p) {
				one, stop := context.WithTimeout(ctx, 3*time.Second)
				id, err := s.readIdentity(one, base, true)
				stop()
				if err != nil {
					node.ConnectionError = peerError(err)
					continue
				}
				if c.bound && (id.ID != c.p.Identity.ID || id.PublicKey != c.p.Identity.PublicKey) {
					node.ConnectionError = "设备身份变化，请重新绑定"
					continue
				}
				node.ID = id.ID
				node.Hostname = id.Name
				node.Online = true
				node.Status = "在线"
				node.ConnectionError = ""
				node.URL = base
				node.NetworkType = deviceNetwork(base)
				node.Addresses = uniqueRoutes(peerBinding{URL: base, Identity: id})
				node.LatencyMS = time.Since(start).Milliseconds()
				break
			}
			results[i] = node
		}(i, c)
	}
	wg.Wait()
	// Binding wins over discoveries and old manually configured URL aliases.
	for _, d := range results {
		if d.ID == local.ID {
			continue
		}
		duplicate := -1
		for i, old := range devices {
			if old.ID == d.ID {
				duplicate = i
				break
			}
		}
		if duplicate < 0 {
			devices = append(devices, d)
		} else if d.Bound && !devices[duplicate].Bound {
			devices[duplicate] = d
		}
	}
	sort.SliceStable(devices, func(i, j int) bool {
		if devices[i].IsLocal != devices[j].IsLocal {
			return devices[i].IsLocal
		}
		if devices[i].Bound != devices[j].Bound {
			return devices[i].Bound
		}
		return devices[i].Hostname < devices[j].Hostname
	})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "success", "data": devices, "discovery": map[string]interface{}{"tailscale": tailMessage}})
}
