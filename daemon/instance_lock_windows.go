//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func acquireSingleInstanceLock(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	overlapped := &windows.Overlapped{}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, false, nil
		}
		return nil, false, err
	}
	return file, true, nil
}

func releaseSingleInstanceLock(file *os.File) error {
	if file == nil {
		return nil
	}
	overlapped := &windows.Overlapped{}
	if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
