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
	explicitRefresh := r.URL.Query().Get("refresh") == "1"
	if explicitRefresh {
		// An explicit user refresh re-probes every route immediately.
		s.peers.mu.Lock()
		s.resetRouteHealthLocked()
		s.peers.mu.Unlock()
		s.resetTailProbeBackoff(true)
	}
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
	devices := []model.DeviceNode{{ID: local.ID, Hostname: local.Name, Alias: cfg.NodeName, URL: addresses[0], IP: u.Hostname(), Port: cfg.Port, NetworkType: deviceNetwork(addresses[0]), IsLocal: true, Online: true, Bound: true, Addresses: addresses, Status: "本机", LastSeen: time.Now()}}
	type candidate struct {
		p       peerBinding
		bound   bool
		network string
	}
	candidates := []candidate{}
	known := map[string]int{local.ID: -1}
	addCandidate := func(c candidate) {
		if index, exists := known[c.p.Identity.ID]; exists {
			if index < 0 {
				return
			}
			previous := candidates[index]
			routes := append(uniqueRoutes(previous.p), uniqueRoutes(c.p)...)
			if c.bound && !previous.bound {
				previous = c
			}
			previous.p.Identity.Addresses = uniqueRoutes(peerBinding{URL: previous.p.URL, Identity: peerIdentity{Addresses: routes}})
			candidates[index] = previous
			return
		}
		known[c.p.Identity.ID] = len(candidates)
		candidates = append(candidates, c)
	}
	for _, p := range s.boundPeers() {
		addCandidate(candidate{p, true, deviceNetwork(p.URL)})
	}
	if s.registry != nil {
		for _, d := range s.registry.List() {
			u, _ := url.Parse(d.URL)
			if u != nil && benchmarkRange.Contains(net.ParseIP(u.Hostname())) {
				continue
			}
			addCandidate(candidate{peerBinding{Identity: peerIdentity{ID: d.ID, Name: d.Hostname, Addresses: append([]string{d.URL}, d.Addresses...)}, URL: d.URL}, false, "lan"})
		}
	}
	s.peers.tailMu.RLock()
	tailMessage := s.peers.tailStatus
	for _, p := range s.peers.tailNodes {
		if len(p.Addresses) > 0 {
			addCandidate(candidate{peerBinding{Identity: p, URL: p.Addresses[0]}, false, "tailscale"})
		}
	}
	s.peers.tailMu.RUnlock()
	for _, p := range cfg.Peers {
		addCandidate(candidate{peerBinding{Identity: peerIdentity{ID: p.ID, Name: p.Name, Addresses: []string{p.URL}}, URL: p.URL}, false, deviceNetwork(p.URL)})
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
			routes := s.peerRoutes(c.p)
			node := model.DeviceNode{ID: c.p.Identity.ID, Hostname: c.p.Identity.Name, Alias: c.p.Alias, URL: c.p.URL, Addresses: routes, Bound: c.bound, NetworkType: c.network, Status: "连接超时", ConnectionError: "暂时无法访问服务", LastSeen: time.Now()}
			results[i] = node
			if !explicitRefresh && c.p.Identity.ID != "" {
				if err := s.peerBackoff(c.p.Identity.ID); err != nil {
					node.ConnectionError = peerError(err)
					results[i] = node
					return
				}
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			start := time.Now()
			var lastErr error
			for _, base := range routes {
				id, err := s.routeProbe(ctx, base)
				if err != nil {
					lastErr = err
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
				node.Addresses = uniqueRoutes(peerBinding{URL: base, Identity: peerIdentity{Addresses: append(append([]string{}, routes...), id.Addresses...)}})
				node.LatencyMS = time.Since(start).Milliseconds()
				s.recordPeer(ctx, id.ID, nil)
				break
			}
			if !node.Online && c.p.Identity.ID != "" && lastErr != nil {
				s.recordPeer(ctx, c.p.Identity.ID, lastErr)
			}
			results[i] = node
		}(i, c)
	}
	wg.Wait()
	// Binding wins over discoveries and old manually configured URL aliases.
	for _, d := range results {
		if d.ID == local.ID || (!d.Bound && !d.Online) {
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
		} else {
			previous := devices[duplicate]
			preferred := previous
			if (d.Online && !previous.Online) || (d.Online == previous.Online && d.Bound && !previous.Bound) {
				preferred = d
			}
			preferred.Bound = previous.Bound || d.Bound
			routes := append(uniqueRoutes(peerBinding{URL: previous.URL, Identity: peerIdentity{Addresses: previous.Addresses}}), uniqueRoutes(peerBinding{URL: d.URL, Identity: peerIdentity{Addresses: d.Addresses}})...)
			preferred.Addresses = uniqueRoutes(peerBinding{URL: preferred.URL, Identity: peerIdentity{Addresses: routes}})
			devices[duplicate] = preferred
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
