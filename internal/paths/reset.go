package paths

import "sync"

// ResetForTesting clears the cached root so tests can change AUTOIPO_HOME
// between cases. It lives in a non-test file because tests in other packages
// (daemonctl, flow, config, …) need to call it; it must not be used by
// production code.
func ResetForTesting() {
	rootOnce = sync.Once{}
	rootDir = ""
}
