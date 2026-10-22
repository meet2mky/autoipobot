package daemonctl

import (
	"testing"

	"autoipo/internal/paths"
)

func resetPaths(t *testing.T) {
	t.Helper()
	paths.ResetForTesting()
	t.Cleanup(paths.ResetForTesting)
}
