package instancelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "test.lock")

	first, acquired, err := Acquire(path)
	if err != nil || !acquired || first == nil {
		t.Fatalf("first Acquire = (%v, %v, %v), want lock", first, acquired, err)
	}
	t.Cleanup(func() { _ = first.Release() })

	second, acquired, err := Acquire(path)
	if err != nil {
		t.Fatalf("second Acquire error: %v", err)
	}
	if acquired || second != nil {
		t.Fatalf("second Acquire should fail while first is held")
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	third, acquired, err := Acquire(path)
	if err != nil || !acquired {
		t.Fatalf("Acquire after release = (%v, %v), want success", acquired, err)
	}
	_ = third.Release()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should remain: %v", err)
	}
}

func TestReleaseNil(t *testing.T) {
	var lock *Lock
	if err := lock.Release(); err != nil {
		t.Fatalf("Release on nil: %v", err)
	}
	if lock.Path() != "" {
		t.Fatal("Path on nil should be empty")
	}
}

func TestWaitTimesOutWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wait.lock")
	held, _, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := Wait(path, 60*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Wait = %v, want ErrTimeout", err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("Wait returned too early")
	}
	_ = held.Release()
	lock, err := Wait(path, time.Second)
	if err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
	_ = lock.Release()
}
