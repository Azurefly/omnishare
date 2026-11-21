package durable

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFile atomically replaces path and keeps the previous valid generation at
// path+.bak. File and directory syncs make a successful return substantially
// stronger than a plain os.Rename acknowledgement.
func WriteFile(path string, data []byte, mode fs.FileMode) error {
	return WriteFileFunc(path, mode, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// WriteFileFunc behaves like WriteFile but streams the new generation through
// write instead of requiring a fully materialized buffer. Large states can be
// encoded directly into the temporary file, which avoids a second multi-megabyte
// allocation on every mutation.
func WriteFileFunc(path string, mode fs.FileMode, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	defer cleanup()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	buffered := bufio.NewWriterSize(f, 64*1024)
	if err := write(buffered); err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	backup := path + ".bak"
	_ = os.Remove(backup)
	hadCurrent := false
	if _, err := os.Stat(path); err == nil {
		hadCurrent = true
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("preserve previous generation: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		if hadCurrent {
			_ = os.Rename(backup, path)
		}
		return fmt.Errorf("activate new generation: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

func syncDir(dir string) error {
	// Windows does not provide a portable directory-handle equivalent of the
	// Unix directory fsync used to persist rename metadata. os.File.Sync on a
	// directory returns ERROR_ACCESS_DENIED even after the file itself was
	// successfully flushed and atomically renamed. Treat this platform limit as
	// best effort instead of reporting a false write failure.
	if runtime.GOOS == "windows" {
		return nil
	}

	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
