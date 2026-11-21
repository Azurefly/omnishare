package api

import (
	"os"
	"path/filepath"
)

// Init tokens are no longer part of startup, navigation or authorization.
// Keep these accessors empty for older embedders and test helpers.
func (s *Server) InitToken() string     { return "" }
func (s *Server) InitTokenPath() string { return "" }

func (s *Server) finishInitialization() {
	path := filepath.Join(s.dataDir, "init-token")
	if info, err := os.Lstat(path); err == nil && !info.IsDir() {
		_ = os.Remove(path) // Remove the legacy entry, never follow a symlink.
	}
}
