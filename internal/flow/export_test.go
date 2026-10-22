package flow

import (
	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
)

func acquireRunLock() (*instancelock.Lock, bool, error) {
	return instancelock.Acquire(paths.FlowRunLockFile())
}
