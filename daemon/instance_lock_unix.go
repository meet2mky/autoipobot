//go:build !windows

package main

import (
	"os"
	"syscall"
)

func acquireSingleInstanceLock(path string) (*os.File, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EAGAIN || err == syscall.EACCES {
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
