package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// initTokenFile is created once in the data directory before any access key is
// configured. A local process that cannot read the user's data directory must
// not be able to claim the instance by writing the first config, so config
// writes stay behind this one-time secret until a key exists.
const initTokenFile = "init-token"

func (s *Server) ensureInitToken() error {
	path := filepath.Join(s.dataDir, initTokenFile)
	if s.cfg.HasAccessKey() {
		// Initialisation is over; a leftover token must not stay usable.
		_ = os.Remove(path)
		return nil
	}
	data, err := os.ReadFile(path)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if len(token) >= 32 {
			s.initToken = token
			s.initTokenPath = path
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	s.initToken = token
	s.initTokenPath = path
	return nil
}

// InitTokenPath returns the on-disk token location while the instance is still
// uninitialised (no access key). It is empty once a key is configured.
func (s *Server) InitTokenPath() string {
	if s.cfg.HasAccessKey() {
		return ""
	}
	return s.initTokenPath
}

// initTokenValid accepts only a loopback request carrying the exact token.
func (s *Server) initTokenValid(r *http.Request) bool {
	if s.initToken == "" {
		return false
	}
	ip := net.ParseIP(clientIP(r))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	got := r.Header.Get("X-OmniShare-Init-Token")
	if len(got) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.initToken)) == 1
}

// finishInitialization removes the token after a key has been configured so it
// cannot be replayed and so no secret lingers on disk.
func (s *Server) finishInitialization() {
	if s.initTokenPath == "" || !s.cfg.HasAccessKey() {
		return
	}
	_ = os.Remove(s.initTokenPath)
	s.initToken = ""
}
