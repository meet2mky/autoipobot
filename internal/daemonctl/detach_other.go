//go:build !unix

package daemonctl

import "os/exec"

func detach(*exec.Cmd) {}
