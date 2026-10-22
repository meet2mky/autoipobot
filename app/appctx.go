package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"

	"autoipo/app/internal/outputlog"
	"autoipo/internal/bridge"
	"autoipo/internal/config"
	"autoipo/internal/flow"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/users"
)

// eventLogCapacity bounds the in-memory "App events" log.
const eventLogCapacity = 500

// appContext holds the services shared by every tab. Fields set in
// newAppContext are immutable afterwards; mutable state is guarded by mu.
type appContext struct {
	app    fyne.App
	window fyne.Window

	secrets   secrets.Store
	users     *users.Store // nil when the users file is corrupt
	usersErr  error
	flows     *flow.Store
	schedules *schedule.Store
	bridge    *bridge.Client
	events    *outputlog.Log
	status    *statusHub

	mu         sync.Mutex
	settings   config.Settings
	bridgeProc *bridge.Process
	daemonBusy bool
	notices    []error // problems found during startup, shown once the window exists
}

func newAppContext(a fyne.App, w fyne.Window) *appContext {
	sec := secrets.Open()
	ctx := &appContext{
		app:       a,
		window:    w,
		secrets:   sec,
		flows:     flow.OpenStore(paths.FlowsFile()),
		schedules: schedule.Open(paths.SchedulesFile()),
		bridge:    bridge.NewClient(),
		events:    outputlog.New(eventLogCapacity),
		status:    newStatusHub(),
		settings:  config.Load(sec),
	}
	ctx.openUsers(sec)
	ctx.logEvent("secrets backend: %s · data directory: %s", sec.Backend(), paths.DataDir())
	return ctx
}

// openUsers loads the users registry. A corrupt file leaves ctx.users nil so
// the Users tab stays read-only instead of overwriting the file.
func (c *appContext) openUsers(sec secrets.Store) {
	store, err := users.Open(paths.UsersFile(), sec)
	c.users = store
	c.usersErr = err
	switch {
	case err == nil:
	case errors.Is(err, users.ErrCorrupt):
		c.users = nil
		c.addNotice(fmt.Errorf("the users file %s is corrupt and will not be modified; fix or remove it and restart the app (%v)", paths.UsersFile(), err))
	default:
		c.addNotice(fmt.Errorf("users registry opened with warnings: %w", err))
	}
}

// addNotice queues a startup problem for display after the window is shown.
func (c *appContext) addNotice(err error) {
	c.logEvent("%v", err)
	c.mu.Lock()
	c.notices = append(c.notices, err)
	c.mu.Unlock()
}

// takeNotices returns and clears the queued startup problems.
func (c *appContext) takeNotices() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	notices := c.notices
	c.notices = nil
	return notices
}

// logEvent appends a line to the App events pane. Safe from any goroutine.
func (c *appContext) logEvent(format string, args ...any) {
	c.events.Appendf(format, args...)
}

// reportError logs err and shows it in a dialog. Safe from any goroutine.
func (c *appContext) reportError(context string, err error) {
	c.logEvent("%s: %v", context, err)
	fyne.Do(func() { dialog.ShowError(fmt.Errorf("%s: %w", context, err), c.window) })
}

func (c *appContext) currentSettings() config.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

func (c *appContext) setSettings(settings config.Settings) {
	c.mu.Lock()
	c.settings = settings
	c.mu.Unlock()
	c.status.refreshSoon()
}

// saveSettings persists settings off the UI thread (the token goes to the
// secrets store, which may spawn a process) and reports the result to done on
// the UI thread. Errors are also shown in a dialog and logged.
func (c *appContext) saveSettings(settings config.Settings, done func(error)) {
	go func() {
		err := config.Save(settings, c.secrets)
		if err == nil {
			c.setSettings(config.Load(c.secrets))
			c.logEvent("settings saved (host label %q, chat ID %s)", settings.HostLabel, settings.ChatID)
		} else {
			c.logEvent("save settings failed: %v", err)
		}
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(fmt.Errorf("save settings: %w", err), c.window)
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// adoptBridgeProcess records a bridge child started by this app (directly or
// by a flow executor) so it can be stopped on exit.
func (c *appContext) adoptBridgeProcess(process *bridge.Process) {
	if process == nil {
		return
	}
	c.mu.Lock()
	previous := c.bridgeProc
	c.bridgeProc = process
	c.mu.Unlock()
	if previous != nil && previous != process && previous.Running() {
		go func() { _ = previous.Stop(3 * time.Second) }()
	}
	c.logEvent("extension bridge started by this app (pid %d)", process.PID())
	go func() {
		<-process.Done()
		if err := process.Err(); err != nil {
			c.logEvent("extension bridge exited: %v", err)
		} else {
			c.logEvent("extension bridge stopped")
		}
		c.status.refreshSoon()
	}()
	c.status.refreshSoon()
}

// ownedBridge returns the bridge process started by this app, if alive.
func (c *appContext) ownedBridge() *bridge.Process {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bridgeProc != nil && c.bridgeProc.Running() {
		return c.bridgeProc
	}
	return nil
}

// shutdown stops child processes started by this app. It blocks briefly.
func (c *appContext) shutdown() {
	if process := c.ownedBridge(); process != nil {
		_ = process.Stop(3 * time.Second)
	}
}

// listUsers loads user records off the UI thread (the secrets backend may
// spawn processes) and delivers them to done on the UI thread.
func (c *appContext) listUsers(done func([]users.Record)) {
	store := c.users
	if store == nil {
		done(nil)
		return
	}
	go func() {
		records := store.List()
		fyne.Do(func() { done(records) })
	}()
}

// eventWriter is an io.Writer that forwards complete lines to the app events
// log, used as stdout/stderr for child processes.
type eventWriter struct {
	log    *outputlog.Log
	prefix string
	mu     sync.Mutex
	buf    bytes.Buffer
}

func newEventWriter(log *outputlog.Log, prefix string) io.Writer {
	return &eventWriter{log: log, prefix: prefix}
}

// Write implements io.Writer.
func (w *eventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			w.buf.Reset()
			w.buf.WriteString(line)
			break
		}
		w.log.Append(w.prefix + strings.TrimRight(line, "\r\n"))
	}
	return len(p), nil
}
