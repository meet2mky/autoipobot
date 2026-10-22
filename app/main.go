// Command autoipoapp is the Fyne desktop companion of the autoipo daemon: it
// controls the daemon, drives the Chrome extension bridge, edits and runs
// automation flows, manages users and shows logs.
package main

import (
	"context"
	"fmt"
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"

	"autoipo/app/internal/windowstate"
	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
)

const appID = "com.autoipo.app"

func main() {
	os.Exit(run())
}

// run wires the application together and returns the process exit code.
func run() int {
	lock, acquired, err := instancelock.Acquire(paths.AppLockFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "autoipoapp: cannot acquire the app lock: %v\n", err)
		return 1
	}
	if !acquired {
		fmt.Println("autoipo desktop app is already running; only one instance is allowed.")
		return 0
	}
	defer func() { _ = lock.Release() }()

	a := app.NewWithID(appID)
	a.Settings().SetTheme(newAppTheme())
	w := a.NewWindow("autoipo")
	ctx := newAppContext(a, w)
	background, cancel := context.WithCancel(context.Background())

	ui := buildUI(ctx)
	w.SetContent(ui.root)
	w.SetMaster()
	keeper := newWindowKeeper(ctx)
	keeper.restore()
	quit := func() {
		keeper.persist()
		a.Quit()
	}
	if tray := setupTray(ctx, quit); tray != nil {
		w.SetCloseIntercept(w.Hide)
	}
	showStartupDialogs(ctx, ui)
	ctx.status.subscribe(func(statusSnapshot) { keeper.persist() })
	// Background work starts once the event loop runs so that every fyne.Do
	// call is queued to the UI thread.
	a.Lifecycle().SetOnStarted(func() {
		go ctx.runStatusTicker(background)
		ui.logs.start(background)
		ui.users.reload()
		go installHelpers(ctx)
	})

	w.ShowAndRun()
	cancel()
	ctx.shutdown()
	return 0
}

// showStartupDialogs surfaces problems found while building the context and
// the first-run settings form. It must run after SetContent.
func showStartupDialogs(ctx *appContext, ui *mainUI) {
	for _, notice := range ctx.takeNotices() {
		dialog.ShowError(notice, ctx.window)
	}
	if !ctx.currentSettings().Complete() {
		showFirstRunDialog(ctx, func() {
			ui.settings.resetFields()
			ui.settings.offerStart()
		})
	}
}

// installHelpers copies the embedded daemon and bridge binaries into bin/
// (bundled builds only) and reports the outcome in the events log.
func installHelpers(ctx *appContext) {
	if !bundledHelpersAvailable {
		ctx.logEvent("no helper binaries embedded in this build; using %s", paths.BinDir())
		if err := checkDaemonBinary(); err != nil {
			ctx.logEvent("%v", err)
		}
		return
	}
	if err := installBundledHelpers(paths.Root()); err != nil {
		ctx.reportError("install bundled helpers", err)
		return
	}
	ctx.logEvent("installed bundled daemon and bridge binaries into %s", paths.BinDir())
}

// windowKeeper restores and persists the window size.
type windowKeeper struct {
	ctx  *appContext
	last windowstate.State
}

func newWindowKeeper(ctx *appContext) *windowKeeper { return &windowKeeper{ctx: ctx} }

func (k *windowKeeper) restore() {
	k.last = windowstate.Load(paths.WindowStateFile())
	k.ctx.window.Resize(fyne.NewSize(k.last.Width, k.last.Height))
}

// persist saves the current size when it changed (UI thread).
func (k *windowKeeper) persist() {
	size := k.ctx.window.Canvas().Size()
	state := windowstate.State{Width: size.Width, Height: size.Height}.Clamped()
	if state == k.last {
		return
	}
	k.last = state
	go func() {
		if err := windowstate.Save(paths.WindowStateFile(), state); err != nil {
			k.ctx.logEvent("save window state: %v", err)
		}
	}()
}
