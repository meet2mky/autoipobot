package main

import (
	"os"
	"path/filepath"
)

func instanceLockPath() string {
	return filepath.Join(os.TempDir(), "autoipo.lock")
}
