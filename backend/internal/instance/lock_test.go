package instance

import "testing"

func TestAcquireRejectsSecondInstanceAndAllowsAfterRelease(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := Acquire(dir); err == nil {
		t.Fatal("second acquire unexpectedly succeeded")
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}
