package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireSingleInstanceLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "autoipo.lock")

	first, acquired, err := acquireSingleInstanceLock(lockPath)
	if err != nil {
		t.Fatalf("first lock acquire failed: %v", err)
	}
	if !acquired {
		t.Fatal("expected first lock acquisition to succeed")
	}
	if first == nil {
		t.Fatal("expected first lock handle to be non-nil")
	}
	defer func() {
		_ = releaseSingleInstanceLock(first)
	}()

	second, acquired, err := acquireSingleInstanceLock(lockPath)
	if err != nil {
		t.Fatalf("second lock acquire returned unexpected error: %v", err)
	}
	if acquired {
		t.Fatal("expected second instance to fail acquiring the same lock")
	}
	if second != nil {
		_ = releaseSingleInstanceLock(second)
	}

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("expected lock file to exist at %s: %v", lockPath, err)
	}
}
