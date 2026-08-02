package config

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"omnishare/internal/durable"
	"omnishare/internal/model"
)

const (
	defaultPort          = 8081
	defaultUploadMB      = 4096
	defaultTrashDays     = 30
	defaultKeyIterations = 210_000
	maxPeers             = 32
)

type Manager struct {
	mu          sync.RWMutex
	configPath  string
	cfg         model.AppConfig
	cacheMu     sync.Mutex
	cacheSecret [32]byte
	authCache   map[[32]byte]time.Time
}

func New(dataDir string, port int, nodeName string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		configPath: filepath.Join(abs, "config.json"),
		authCache:  map[[32]byte]time.Time{},
		cfg: model.AppConfig{
			SchemaVersion:       model.CurrentConfigSchema,
			DataDir:             abs,
			NodeName:            strings.TrimSpace(nodeName),
			Port:                defaultPort,
			ListenAddress:       "127.0.0.1",
			AutoOpenBrowser:     true,
			MaxUploadMB:         defaultUploadMB,
			TrashRetentionDays:  defaultTrashDays,
			AccessKeyIterations: defaultKeyIterations,
			AllowedOrigins:      []string{},
			Peers:               []model.PeerConfig{},
		},
	}
	if _, err := rand.Read(m.cacheSecret[:]); err != nil {
		return nil, fmt.Errorf("initialize authentication cache: %w", err)
	}
	loaded := false
	if err := m.load(); err == nil {
		loaded = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m.applyDefaults(abs)
	if port > 0 {
		if port > 65535 {
			return nil, errors.New("port must be between 1 and 65535")
		}
		m.cfg.Port = port
	} else if !loaded && m.cfg.Port <= 0 {
		m.cfg.Port = defaultPort
	}
	if strings.TrimSpace(nodeName) != "" {
		m.cfg.NodeName = strings.TrimSpace(nodeName)
	}
	if err := m.migrateLegacySecret(); err != nil {
		return nil, err
	}
	if err := m.ensureDiscoveryIdentity(); err != nil {
		return nil, err
	}
	if err := validateConfig(m.cfg); err != nil {
		return nil, err
	}
	if err := m.saveCandidate(m.cfg); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) applyDefaults(abs string) {
	m.cfg.SchemaVersion = model.CurrentConfigSchema
	m.cfg.DataDir = abs
	if strings.TrimSpace(m.cfg.NodeName) == "" {
		m.cfg.NodeName = "OmniShare"
	}
	if m.cfg.Port <= 0 || m.cfg.Port > 65535 {
		m.cfg.Port = defaultPort
	}
	if strings.TrimSpace(m.cfg.ListenAddress) == "" {
		m.cfg.ListenAddress = "127.0.0.1"
	}
	if m.cfg.MaxUploadMB <= 0 {
		m.cfg.MaxUploadMB = defaultUploadMB
	}
	if m.cfg.TrashRetentionDays <= 0 {
		m.cfg.TrashRetentionDays = defaultTrashDays
	}
	if m.cfg.AccessKeyIterations < 100_000 {
		m.cfg.AccessKeyIterations = defaultKeyIterations
	}
	if m.cfg.Peers == nil {
		m.cfg.Peers = []model.PeerConfig{}
	}
	if m.cfg.AllowedOrigins == nil {
		m.cfg.AllowedOrigins = []string{}
	}
	if !m.cfg.AllowLAN {
		m.cfg.ListenAddress = "127.0.0.1"
	}
}

func (m *Manager) load() error {
	decode := func(data []byte) error {
		if err := json.Unmarshal(data, &m.cfg); err != nil {
			return err
		}
		// v1.2 persisted a plaintext access_key. Keep migration support without
		// exposing a second field with the same JSON tag to normal API decoding.
		if m.cfg.AccessKeyHash == "" {
			var legacy struct {
				AccessKey string `json:"access_key"`
			}
			if err := json.Unmarshal(data, &legacy); err == nil {
				m.cfg.LegacyAccessKey = legacy.AccessKey
			}
		}
		return nil
	}
	data, err := os.ReadFile(m.configPath)
	if err == nil {
		if decode(data) == nil {
			return nil
		}
	}
	backup, backupErr := os.ReadFile(m.configPath + ".bak")
	if backupErr != nil {
		if err != nil {
			return err
		}
		return errors.New("config.json is corrupt and no valid backup exists")
	}
	if unmarshalErr := decode(backup); unmarshalErr != nil {
		return fmt.Errorf("config and backup are corrupt: %w", unmarshalErr)
	}
	return nil
}

func (m *Manager) migrateLegacySecret() error {
	if m.cfg.AccessKeyHash != "" || strings.TrimSpace(m.cfg.LegacyAccessKey) == "" {
		m.cfg.LegacyAccessKey = ""
		return nil
	}
	if err := setSecret(&m.cfg, strings.TrimSpace(m.cfg.LegacyAccessKey)); err != nil {
		return fmt.Errorf("migrate legacy access key: %w", err)
	}
	m.cfg.LegacyAccessKey = ""
	return nil
}

func (m *Manager) ensureDiscoveryIdentity() error {
	if m.cfg.DiscoveryKey != "" {
		keyBytes, err := base64.RawStdEncoding.DecodeString(m.cfg.DiscoveryKey)
		if err == nil && len(keyBytes) == ed25519.PrivateKeySize {
			pub := ed25519.PrivateKey(keyBytes).Public().(ed25519.PublicKey)
			m.cfg.NodeID = nodeID(pub)
			return nil
		}
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate discovery identity: %w", err)
	}
	m.cfg.DiscoveryKey = base64.RawStdEncoding.EncodeToString(privateKey)
	m.cfg.NodeID = nodeID(privateKey.Public().(ed25519.PublicKey))
	return nil
}

func nodeID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "node-" + hex.EncodeToString(h[:10])
}

func (m *Manager) saveCandidate(cfg model.AppConfig) error {
	cfg.AccessKey = ""
	cfg.LegacyAccessKey = ""
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return durable.WriteFile(m.configPath, data, 0o600)
}

func (m *Manager) Save() error {
	m.mu.RLock()
	candidate := cloneConfig(m.cfg)
	m.mu.RUnlock()
	return m.saveCandidate(candidate)
}

func (m *Manager) Get() model.AppConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneConfig(m.cfg)
}

func cloneConfig(cfg model.AppConfig) model.AppConfig {
	cfg.AccessKey = ""
	cfg.LegacyAccessKey = ""
	cfg.Peers = append([]model.PeerConfig(nil), cfg.Peers...)
	cfg.AllowedOrigins = append([]string(nil), cfg.AllowedOrigins...)
	return cfg
}

func (m *Manager) Public() model.PublicConfig {
	cfg := m.Get()
	return model.PublicConfig{
		DataDir:            cfg.DataDir,
		NodeID:             cfg.NodeID,
		NodeName:           cfg.NodeName,
		Port:               cfg.Port,
		ListenAddress:      cfg.ListenAddress,
		AllowLAN:           cfg.AllowLAN,
		PublicBaseURL:      cfg.PublicBaseURL,
		AutoOpenBrowser:    cfg.AutoOpenBrowser,
		MaxUploadMB:        cfg.MaxUploadMB,
		RetentionDays:      cfg.RetentionDays,
		TrashRetentionDays: cfg.TrashRetentionDays,
		HasAccessKey:       cfg.AccessKeyHash != "",
		TLSEnabled:         cfg.TLSCertFile != "" && cfg.TLSKeyFile != "",
		AllowedOrigins:     append([]string(nil), cfg.AllowedOrigins...),
		Peers:              append([]model.PeerConfig(nil), cfg.Peers...),
	}
}

func (m *Manager) HasAccessKey() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.AccessKeyHash != ""
}

func (m *Manager) VerifyAccessKey(got string) bool {
	m.mu.RLock()
	cfg := m.cfg
	m.mu.RUnlock()
	if cfg.AccessKeyHash == "" {
		return got == ""
	}
	cacheKey := m.accessCacheKey(got)
	now := time.Now()
	m.cacheMu.Lock()
	if expires, ok := m.authCache[cacheKey]; ok && now.Before(expires) {
		m.cacheMu.Unlock()
		return true
	}
	delete(m.authCache, cacheKey)
	m.cacheMu.Unlock()

	salt, err1 := base64.RawStdEncoding.DecodeString(cfg.AccessKeySalt)
	want, err2 := base64.RawStdEncoding.DecodeString(cfg.AccessKeyHash)
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	derived := pbkdf2SHA256([]byte(got), salt, cfg.AccessKeyIterations, len(want))
	if subtle.ConstantTimeCompare(derived, want) != 1 {
		return false
	}
	m.cacheMu.Lock()
	if len(m.authCache) > 128 {
		for key, expires := range m.authCache {
			if now.After(expires) {
				delete(m.authCache, key)
			}
		}
	}
	m.authCache[cacheKey] = now.Add(2 * time.Minute)
	m.cacheMu.Unlock()
	return true
}

func (m *Manager) accessCacheKey(secret string) [32]byte {
	mac := hmac.New(sha256.New, m.cacheSecret[:])
	_, _ = mac.Write([]byte(secret))
	var key [32]byte
	copy(key[:], mac.Sum(nil))
	return key
}

func (m *Manager) clearAuthCache() {
	m.cacheMu.Lock()
	m.authCache = map[[32]byte]time.Time{}
	m.cacheMu.Unlock()
}

// Update validates and durably saves a complete candidate before publishing it
// to readers. A failed write leaves the current in-memory configuration intact.
func (m *Manager) Update(req model.AppConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := cloneConfig(m.cfg)
	if strings.TrimSpace(req.NodeName) != "" {
		candidate.NodeName = strings.TrimSpace(req.NodeName)
	}
	if req.Port > 0 {
		candidate.Port = req.Port
	}
	if strings.TrimSpace(req.ListenAddress) != "" {
		candidate.ListenAddress = strings.TrimSpace(req.ListenAddress)
	}
	candidate.AllowLAN = req.AllowLAN
	candidate.PublicBaseURL = strings.TrimSpace(req.PublicBaseURL)
	if req.MaxUploadMB > 0 {
		candidate.MaxUploadMB = req.MaxUploadMB
	}
	if req.RetentionDays >= 0 {
		candidate.RetentionDays = req.RetentionDays
	}
	if req.TrashRetentionDays > 0 {
		candidate.TrashRetentionDays = req.TrashRetentionDays
	}
	candidate.AutoOpenBrowser = req.AutoOpenBrowser
	if req.AccessKey != "__KEEP__" {
		if req.AccessKey == "" {
			candidate.AccessKeyHash = ""
			candidate.AccessKeySalt = ""
			candidate.AccessKeyIterations = defaultKeyIterations
		} else if err := setSecret(&candidate, req.AccessKey); err != nil {
			return err
		}
	}
	if req.Peers != nil {
		peers, err := sanitizePeers(req.Peers)
		if err != nil {
			return err
		}
		candidate.Peers = peers
	}
	if req.AllowedOrigins != nil {
		origins, err := sanitizeOrigins(req.AllowedOrigins)
		if err != nil {
			return err
		}
		candidate.AllowedOrigins = origins
	}
	if strings.TrimSpace(req.TLSCertFile) != "" || strings.TrimSpace(req.TLSKeyFile) != "" {
		candidate.TLSCertFile = strings.TrimSpace(req.TLSCertFile)
		candidate.TLSKeyFile = strings.TrimSpace(req.TLSKeyFile)
	}
	if !candidate.AllowLAN {
		candidate.ListenAddress = "127.0.0.1"
	}
	if err := validateConfig(candidate); err != nil {
		return err
	}
	if err := m.saveCandidate(candidate); err != nil {
		return err
	}
	m.cfg = candidate
	m.clearAuthCache()
	return nil
}

func (m *Manager) ApplyRuntimeOverrides(listenAddress, certFile, keyFile string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate := cloneConfig(m.cfg)
	if strings.TrimSpace(listenAddress) != "" {
		candidate.ListenAddress = strings.TrimSpace(listenAddress)
		candidate.AllowLAN = !isLoopbackListen(candidate.ListenAddress)
	}
	if strings.TrimSpace(certFile) != "" || strings.TrimSpace(keyFile) != "" {
		candidate.TLSCertFile = strings.TrimSpace(certFile)
		candidate.TLSKeyFile = strings.TrimSpace(keyFile)
	}
	if err := validateConfig(candidate); err != nil {
		return err
	}
	if err := m.saveCandidate(candidate); err != nil {
		return err
	}
	m.cfg = candidate
	return nil
}

func (m *Manager) DiscoveryIdentity() (string, ed25519.PrivateKey, error) {
	m.mu.RLock()
	id, encoded := m.cfg.NodeID, m.cfg.DiscoveryKey
	m.mu.RUnlock()
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return "", nil, errors.New("invalid discovery identity")
	}
	return id, ed25519.PrivateKey(key), nil
}

// RestoreJSON validates and durably restores a backup configuration while
// keeping the current machine's data directory. Network changes take effect
// after the process is restarted.
func (m *Manager) RestoreJSON(data []byte) error {
	var candidate model.AppConfig
	if err := json.Unmarshal(data, &candidate); err != nil {
		return fmt.Errorf("decode backup config: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	candidate.DataDir = m.cfg.DataDir
	candidate.SchemaVersion = model.CurrentConfigSchema
	candidate.AccessKey = ""
	if candidate.AccessKeyHash == "" && strings.TrimSpace(candidate.LegacyAccessKey) != "" {
		if err := setSecret(&candidate, candidate.LegacyAccessKey); err != nil {
			return fmt.Errorf("migrate backup access key: %w", err)
		}
	}
	candidate.LegacyAccessKey = ""
	if strings.TrimSpace(candidate.NodeName) == "" {
		candidate.NodeName = "OmniShare"
	}
	if candidate.Port < 1 || candidate.Port > 65535 {
		candidate.Port = defaultPort
	}
	if strings.TrimSpace(candidate.ListenAddress) == "" {
		candidate.ListenAddress = "127.0.0.1"
	}
	if candidate.MaxUploadMB <= 0 {
		candidate.MaxUploadMB = defaultUploadMB
	}
	if candidate.TrashRetentionDays <= 0 {
		candidate.TrashRetentionDays = defaultTrashDays
	}
	if candidate.AccessKeyIterations < 100_000 {
		candidate.AccessKeyIterations = defaultKeyIterations
	}
	peers, err := sanitizePeers(candidate.Peers)
	if err != nil {
		return err
	}
	candidate.Peers = peers
	origins, err := sanitizeOrigins(candidate.AllowedOrigins)
	if err != nil {
		return err
	}
	candidate.AllowedOrigins = origins
	keyBytes, err := base64.RawStdEncoding.DecodeString(candidate.DiscoveryKey)
	if err != nil || len(keyBytes) != ed25519.PrivateKeySize {
		return errors.New("backup discovery identity is invalid")
	}
	candidate.NodeID = nodeID(ed25519.PrivateKey(keyBytes).Public().(ed25519.PublicKey))
	if !candidate.AllowLAN {
		candidate.ListenAddress = "127.0.0.1"
	}
	if err := validateConfig(candidate); err != nil {
		return err
	}
	if err := m.saveCandidate(candidate); err != nil {
		return err
	}
	m.cfg = candidate
	m.clearAuthCache()
	return nil
}

func (m *Manager) BackupJSON() ([]byte, error) {
	m.mu.RLock()
	candidate := cloneConfig(m.cfg)
	m.mu.RUnlock()
	return json.MarshalIndent(candidate, "", "  ")
}

func setSecret(cfg *model.AppConfig, secret string) error {
	secret = strings.TrimSpace(secret)
	if len([]byte(secret)) < 16 {
		return errors.New("access key must contain at least 16 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate access key salt: %w", err)
	}
	iterations := defaultKeyIterations
	dk := pbkdf2SHA256([]byte(secret), salt, iterations, 32)
	cfg.AccessKeySalt = base64.RawStdEncoding.EncodeToString(salt)
	cfg.AccessKeyHash = base64.RawStdEncoding.EncodeToString(dk)
	cfg.AccessKeyIterations = iterations
	return nil
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	if iterations < 1 {
		iterations = 1
	}
	result := make([]byte, 0, keyLen)
	for block := 1; len(result) < keyLen; block++ {
		mac := hmac.New(sha256.New, password)
		_, _ = mac.Write(salt)
		_, _ = mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			_, _ = mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLen]
}

func validateConfig(cfg model.AppConfig) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if cfg.MaxUploadMB < 1 || cfg.MaxUploadMB > 1024*1024 {
		return errors.New("max upload size is out of range")
	}
	if cfg.RetentionDays < 0 || cfg.RetentionDays > 36500 || cfg.TrashRetentionDays < 1 || cfg.TrashRetentionDays > 36500 {
		return errors.New("retention setting is out of range")
	}
	if cfg.AllowLAN && cfg.AccessKeyHash == "" {
		return errors.New("LAN access requires an access key")
	}
	if !cfg.AllowLAN && !isLoopbackListen(cfg.ListenAddress) {
		return errors.New("non-LAN mode must listen on loopback")
	}
	if cfg.PublicBaseURL != "" {
		if _, err := normalizeBaseURL(cfg.PublicBaseURL); err != nil {
			return fmt.Errorf("public base URL: %w", err)
		}
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	return nil
}

func isLoopbackListen(value string) bool {
	value = strings.TrimSpace(strings.Trim(value, "[]"))
	return value == "localhost" || value == "127.0.0.1" || value == "::1"
}

func sanitizePeers(peers []model.PeerConfig) ([]model.PeerConfig, error) {
	if len(peers) > maxPeers {
		return nil, fmt.Errorf("at most %d peers are allowed", maxPeers)
	}
	out := make([]model.PeerConfig, 0, len(peers))
	seen := map[string]bool{}
	for _, p := range peers {
		p.ID = strings.TrimSpace(p.ID)
		p.Name = cleanLabel(p.Name, 80)
		normalized, err := normalizeBaseURL(p.URL)
		if err != nil {
			return nil, fmt.Errorf("peer %q: %w", p.Name, err)
		}
		if normalized == "" || seen[normalized] {
			continue
		}
		p.URL = normalized
		if p.ID == "" {
			p.ID = "peer-" + shortHash(p.URL)
		}
		if p.Name == "" {
			p.Name = p.URL
		}
		seen[p.URL] = true
		out = append(out, p)
	}
	return out, nil
}

func sanitizeOrigins(origins []string) ([]string, error) {
	if len(origins) > 16 {
		return nil, errors.New("at most 16 CORS origins are allowed")
	}
	out := make([]string, 0, len(origins))
	seen := map[string]bool{}
	for _, raw := range origins {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("invalid origin %q", raw)
		}
		origin := u.Scheme + "://" + strings.ToLower(u.Host)
		if !seen[origin] {
			seen[origin] = true
			out = append(out, origin)
		}
	}
	return out, nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("only a plain http/https origin is allowed")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("URL path must be empty")
	}
	host := u.Hostname()
	if host == "" || len(host) > 253 {
		return "", errors.New("invalid host")
	}
	if port := u.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return "", errors.New("invalid port")
		}
	}
	if ip := net.ParseIP(host); ip == nil && strings.ContainsAny(host, " /\\") {
		return "", errors.New("invalid host")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func cleanLabel(value string, max int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u202a' || r == '\u202b' || r == '\u202d' || r == '\u202e' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	r := []rune(value)
	if len(r) > max {
		value = string(r[:max])
	}
	return value
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}
