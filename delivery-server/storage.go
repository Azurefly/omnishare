package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDir(parent)
}

func syncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) loadRevocations() error {
	path := filepath.Join(s.options.Root, "revoked")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	items, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, item := range items {
		if !hexID.MatchString(item.Name()) || !item.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, item.Name()))
		if err != nil {
			return err
		}
		expires, err := time.Parse(time.RFC3339Nano, string(data))
		if err != nil {
			return errors.New("invalid revoked delivery status")
		}
		s.revoked[item.Name()] = expires
	}
	return nil
}

func (s *Service) tombstone(item entryRecord) error {
	if _, exists := s.revoked[item.ID]; exists {
		return nil
	}
	expires := time.Now().UTC().Add(8 * 24 * time.Hour)
	if err := writeAtomic(filepath.Join(s.options.Root, "revoked", item.ID), []byte(expires.Format(time.RFC3339Nano)), 0o600); err != nil {
		return err
	}
	s.revoked[item.ID] = expires
	return nil
}
