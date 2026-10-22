//go:build bundled

package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// bundledHelpers holds the daemon and bridge binaries that deploy.sh copies
// into app/bundled immediately before building with -tags bundled.
//
//go:embed bundled/autoipo bundled/chrome-extension-cli
var bundledHelpers embed.FS

// bundledHelpersAvailable reports whether this binary embeds the helpers.
const bundledHelpersAvailable = true

// installBundledHelpers writes the embedded helper binaries into <root>/bin.
// Each file is written to a temporary name and renamed into place so a
// running daemon or bridge never observes a partially written executable.
func installBundledHelpers(root string) error {
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create helper binary directory: %w", err)
	}
	for _, name := range []string{"autoipo", "chrome-extension-cli"} {
		data, err := fs.ReadFile(bundledHelpers, "bundled/"+name)
		if err != nil {
			return fmt.Errorf("read bundled %s: %w", name, err)
		}
		if err := writeExecutableAtomic(filepath.Join(binDir, name), data); err != nil {
			return fmt.Errorf("install bundled %s: %w", name, err)
		}
	}
	return nil
}

func writeExecutableAtomic(destination string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		cleanup()
		return err
	}
	return nil
}
