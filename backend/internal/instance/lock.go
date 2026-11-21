package instance

import (
	"fmt"
	"os"
	"path/filepath"
)

type Lock struct {
	file *os.File
	path string
}

func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, ".omnishare.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another OmniShare instance is already using %s: %w", dataDir, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
		_ = f.Sync()
	}
	return &Lock{file: f, path: path}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err1 := unlockFile(l.file)
	err2 := l.file.Close()
	l.file = nil
	if err1 != nil {
		return err1
	}
	return err2
}
