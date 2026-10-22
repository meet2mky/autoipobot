package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"autoipo/internal/daemonctl"
	"autoipo/internal/paths"
)

// daemonStopTimeout bounds how long Stop waits before escalating to SIGKILL.
const daemonStopTimeout = 10 * time.Second

var errDaemonBusy = errors.New("another daemon operation is still in progress")

// checkDaemonBinary verifies the deployed daemon exists; the app never builds
// it on the fly.
func checkDaemonBinary() error {
	binary := paths.DaemonBinary()
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("daemon binary not found at %s; run deploy.sh to build and install it", binary)
	}
	return nil
}

func (c *appContext) isDaemonBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.daemonBusy
}

func (c *appContext) setDaemonBusy(busy bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if busy && c.daemonBusy {
		return false
	}
	c.daemonBusy = busy
	return true
}

// runDaemonAction performs op in a goroutine. Only one operation runs at a
// time; while it runs the status snapshot reports DaemonBusy so every
// Start/Stop/Restart control is disabled. Errors are shown in a dialog and in
// the events log, then passed to done (on the UI thread).
func (c *appContext) runDaemonAction(label string, op func() error, done func(error)) {
	if !c.setDaemonBusy(true) {
		if done != nil {
			done(errDaemonBusy)
		}
		return
	}
	c.logEvent("%s…", label)
	c.status.refreshSoon()
	go func() {
		err := op()
		c.setDaemonBusy(false)
		c.status.refreshSoon()
		fyne.Do(func() {
			if err != nil {
				c.logEvent("%s failed: %v", label, err)
				dialog.ShowError(fmt.Errorf("%s: %w", label, err), c.window)
			} else {
				c.logEvent("%s done", label)
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// startDaemon launches the deployed daemon binary.
func (c *appContext) startDaemon(done func(error)) {
	c.runDaemonAction("start daemon", func() error {
		if err := checkDaemonBinary(); err != nil {
			return err
		}
		return daemonctl.Start(paths.DaemonBinary(), nil, nil)
	}, done)
}

// stopDaemon terminates the running daemon.
func (c *appContext) stopDaemon(done func(error)) {
	c.runDaemonAction("stop daemon", func() error {
		return daemonctl.Stop(daemonStopTimeout)
	}, done)
}

// restartDaemon is the single restart path used by the Status tab, the tray
// and the Settings tab after a successful save.
func (c *appContext) restartDaemon(done func(error)) {
	c.runDaemonAction("restart daemon", func() error {
		if err := checkDaemonBinary(); err != nil {
			return err
		}
		return daemonctl.Restart(paths.DaemonBinary(), nil, nil)
	}, done)
}
