//go:build unix

package daemonctl

import (
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own session so that closing the desktop app
// (or its terminal) does not deliver SIGHUP to it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
