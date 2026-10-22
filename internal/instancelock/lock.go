// Package instancelock provides a cross-process single-instance lock backed
// by an advisory file lock.
package instancelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Lock is a held single-instance lock. Release it when the process exits.
type Lock struct {
	file *os.File
	path string
}

// Path returns the lock file location.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Acquire tries to take an exclusive, non-blocking lock on path. It returns
// (lock, true, nil) on success, (nil, false, nil) when another process holds
// the lock, and a non-nil error for any other failure.
func Acquire(path string) (*Lock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	acquired, err := tryLock(file)
	if err != nil {
		_ = file.Close()
		return nil, false, err
	}
	if !acquired {
		_ = file.Close()
		return nil, false, nil
	}
	return &Lock{file: file, path: path}, true, nil
}

// Release unlocks and closes the lock file. It is safe to call on nil.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlock(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// ErrTimeout is returned by Wait when the lock stays held for the whole
// timeout, i.e. another process genuinely owns it.
var ErrTimeout = errors.New("instancelock: timed out waiting for lock")

// Wait blocks (polling) until the lock can be acquired or timeout elapses.
// Real acquirers (the daemon, the flow runner) should prefer Wait with a short
// timeout over Acquire, because liveness probes elsewhere take and release
// the lock momentarily and must not be mistaken for a running owner.
func Wait(path string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lock, acquired, err := Acquire(path)
		if err != nil {
			return nil, err
		}
		if acquired {
			return lock, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: %s", ErrTimeout, path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// WritePID records pid inside the lock file. Only the lock holder can write
// it, so readers can trust the content as the identity of the live owner
// (unlike a separate PID file, which can be stale or edited).
func (l *Lock) WritePID(pid int) error {
	if l == nil || l.file == nil {
		return errors.New("instancelock: lock is not held")
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0); err != nil {
		return err
	}
	return l.file.Sync()
}

// ReadPID returns the PID written into the lock file by its holder, or 0
// when the file is missing or empty. It does not check whether the lock is
// currently held; combine it with a failed Acquire for that.
func ReadPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
