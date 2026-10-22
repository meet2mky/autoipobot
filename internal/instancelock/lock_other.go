//go:build !unix

package instancelock

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("instancelock: advisory file locks are not supported on this platform")

func tryLock(*os.File) (bool, error) { return false, errUnsupported }

func unlock(*os.File) error { return nil }
