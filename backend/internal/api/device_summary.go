package api

import (
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"omnishare/internal/model"
)

func summarizedLocalDevices(name string, port int, tls bool, listenAddress string) []model.DeviceNode {
	scheme := "http"
	if tls {
		scheme = "https"
	}

	outbound := preferredOutboundIP()
	candidates := make([]model.DeviceNode, 0, 4)
	seen := map[string]struct{}{}
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		ip := addressIP(addr)
		if !usableLocalIP(ip) {
			continue
		}
		value := ip.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		candidates = append(candidates, model.DeviceNode{
			ID:          "local-primary",
			Hostname:    name,
			IP:          value,
			Port:        port,
			URL:         scheme + "://" + net.JoinHostPort(value, strconv.Itoa(port)),
			NetworkType: networkType(value),
			IsLocal:     true,
			Online:      true,
			LastSeen:    time.Now(),
		})
	}

	if len(candidates) == 0 {
		value := "127.0.0.1"
		return []model.DeviceNode{{
			ID: "local-primary", Hostname: name, IP: value, Port: port,
			URL:         scheme + "://" + net.JoinHostPort(value, strconv.Itoa(port)),
			NetworkType: "loopback", IsLocal: true, Online: true, LastSeen: time.Now(),
		}}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := localAddressScore(candidates[i].IP, listenAddress, outbound)
		right := localAddressScore(candidates[j].IP, listenAddress, outbound)
		if left != right {
			return left > right
		}
		return candidates[i].IP < candidates[j].IP
	})
	return candidates[:1]
}

func dedupeDeviceNodes(devices []model.DeviceNode) []model.DeviceNode {
	out := make([]model.DeviceNode, 0, len(devices))
	seenIDs := map[string]struct{}{}
	seenURLs := map[string]struct{}{}
	localAdded := false

	for _, device := range devices {
		if device.IsLocal {
			if localAdded {
				continue
			}
			localAdded = true
		}

		id := strings.TrimSpace(device.ID)
		canonicalURL := canonicalDeviceURL(device.URL)
		if id != "" {
			if _, exists := seenIDs[id]; exists {
				continue
			}
		}
		if canonicalURL != "" {
			if _, exists := seenURLs[canonicalURL]; exists {
				continue
			}
		}

		if id != "" {
			seenIDs[id] = struct{}{}
		}
		if canonicalURL != "" {
			seenURLs[canonicalURL] = struct{}{}
		}
		out = append(out, device)
	}
	return out
}

func addressIP(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func usableLocalIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func localAddressScore(value, listenAddress, outbound string) int {
	ip := net.ParseIP(value)
	if ip == nil {
		return -1
	}
	if configured := net.ParseIP(strings.Trim(strings.TrimSpace(listenAddress), "[]")); configured != nil && configured.Equal(ip) {
		return 10000
	}
	if preferred := net.ParseIP(outbound); preferred != nil && preferred.Equal(ip) {
		return 9000
	}
	if ip.IsLoopback() {
		return 100
	}
	if networkType(value) == "tailscale" {
		return 650
	}
	if ip.IsPrivate() {
		return 700
	}
	if ip.To4() != nil {
		return 600
	}
	return 500
}

func preferredOutboundIP() string {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 53})
	if err != nil {
		return ""
	}
	defer conn.Close()
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok && local.IP != nil {
		return local.IP.String()
	}
	return ""
}

func canonicalDeviceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
