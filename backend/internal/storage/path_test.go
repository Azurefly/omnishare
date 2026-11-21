package storage

import (
	"errors"
	"testing"
)

func TestValidateStoragePath(t *testing.T) {
	valid := []string{
		"uploads/blobs/aa/bb",
		`uploads\blobs\aa\bb`, // native Windows state written before this check
		"uploads/.staging/x",
	}
	for _, p := range valid {
		if err := ValidateStoragePath(p); err != nil {
			t.Fatalf("rejected valid path %q: %v", p, err)
		}
	}
	invalid := []string{
		"../secret.txt",
		"..\\secret.txt",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		"C:/Windows/win.ini",
		"uploads/../../secret.txt",
		"uploads/../secret.txt",
		"uploads//blobs",
		"uploads/./blobs",
		"uploads",
		"uploads/",
		"data/uploads/x",
		"uploads/blobs/../../../x",
	}
	for _, p := range invalid {
		if err := ValidateStoragePath(p); !errors.Is(err, ErrUnsafeStoragePath) {
			t.Fatalf("accepted unsafe path %q: %v", p, err)
		}
	}
	if err := ValidateStoragePath(""); err != nil {
		t.Fatalf("empty path should mean no blob: %v", err)
	}
}
