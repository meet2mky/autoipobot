package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// trayController owns the system tray menu. The menu is rebuilt only when
// the visible state changes, because every refresh resets the native menu.
type trayController struct {
	ctx        *appContext
	desk       desktop.App
	menu       *fyne.Menu
	statusItem *fyne.MenuItem
	startItem  *fyne.MenuItem
	stopItem   *fyne.MenuItem
	restart    *fyne.MenuItem
	lastKey    string
	onQuit     func()
}

// setupTray installs the tray menu when the platform supports one. It returns
// nil otherwise so callers can keep the normal close behaviour.
func setupTray(ctx *appContext, onQuit func()) *trayController {
	desk, ok := ctx.app.(desktop.App)
	if !ok {
		return nil
	}
	t := &trayController{ctx: ctx, desk: desk, onQuit: onQuit}
	t.statusItem = fyne.NewMenuItem("Starting…", nil)
	t.statusItem.Disabled = true
	t.startItem = fyne.NewMenuItem("Start daemon", func() { ctx.startDaemon(nil) })
	t.stopItem = fyne.NewMenuItem("Stop daemon", func() { ctx.stopDaemon(nil) })
	t.restart = fyne.NewMenuItem("Restart daemon", func() { ctx.restartDaemon(nil) })
	show := fyne.NewMenuItem("Show window", func() {
		ctx.window.Show()
		ctx.window.RequestFocus()
		// A window that was hidden while the status ticker kept updating
		// widgets can come back with a stale frame; force a repaint.
		ctx.window.Content().Refresh()
	})
	quit := fyne.NewMenuItem("Quit", onQuit)
	quit.IsQuit = true
	t.menu = fyne.NewMenu("autoipo",
		t.statusItem,
		fyne.NewMenuItemSeparator(),
		t.startItem, t.stopItem, t.restart,
		fyne.NewMenuItemSeparator(),
		show, quit,
	)
	desk.SetSystemTrayMenu(t.menu)
	ctx.status.subscribe(t.apply)
	return t
}

// apply updates the tray from a snapshot (UI thread).
func (t *trayController) apply(s statusSnapshot) {
	label := s.daemonLabel() + " · " + s.bridgeLabel()
	if s.RunActive {
		label += " · flow running"
	}
	startEnabled := s.SettingsOK && !s.DaemonRunning && !s.DaemonBusy
	stopEnabled := s.DaemonRunning && !s.DaemonBusy
	restartEnabled := s.SettingsOK && !s.DaemonBusy
	key := label + boolKey(startEnabled) + boolKey(stopEnabled) + boolKey(restartEnabled)
	if key == t.lastKey {
		return
	}
	t.lastKey = key
	t.statusItem.Label = label
	t.startItem.Disabled = !startEnabled
	t.stopItem.Disabled = !stopEnabled
	t.restart.Disabled = !restartEnabled
	t.desk.SetSystemTrayMenu(t.menu)
}

func boolKey(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
