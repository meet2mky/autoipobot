package main

import (
	"os"
	"path/filepath"
)

func daemonLockPath() string {
	return filepath.Join(os.TempDir(), "autoipo-daemon.lock")
}
