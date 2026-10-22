package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"autoipo/internal/bridge"
	"autoipo/internal/daemonctl"
	"autoipo/internal/flow"
	"autoipo/internal/schedule"
)

// Refresh cadence of the status ticker and the per-probe bridge timeout.
const (
	statusInterval = 3 * time.Second
	bridgeProbe    = 1500 * time.Millisecond
)

// statusSnapshot is everything the Status tab, the tray, the header and the
// Commands bridge line need. It is gathered off the UI thread and applied on
// it, so subscribers may read it freely.
type statusSnapshot struct {
	Taken           time.Time
	SettingsOK      bool
	DaemonRunning   bool
	DaemonPID       int
	DaemonBusy      bool
	Daemon          daemonctl.Status // zero value when the daemon never published
	DaemonStatusOK  bool
	BridgeReachable bool
	Bridge          bridge.Status
	BridgeOwned     bool // started by this app and still alive
	RunActive       bool
	LastRun         flow.LastRun
	Schedules       []schedule.Entry // pending and running entries only
}

// PendingSchedules counts entries that have not started yet.
func (s statusSnapshot) PendingSchedules() int {
	count := 0
	for _, entry := range s.Schedules {
		if entry.Status == schedule.StatusPending {
			count++
		}
	}
	return count
}

// daemonLabel is the short daemon state used by the tray and header.
func (s statusSnapshot) daemonLabel() string {
	switch {
	case !s.SettingsOK:
		return "Daemon: settings incomplete"
	case s.DaemonRunning:
		return "Daemon: running"
	default:
		return "Daemon: stopped"
	}
}

// bridgeLabel is the short bridge state used by the tray and header.
func (s statusSnapshot) bridgeLabel() string {
	switch {
	case !s.BridgeReachable:
		return "Bridge: not running"
	case s.Bridge.ExtensionConnected:
		return "Bridge: extension connected"
	default:
		return "Bridge: waiting for extension"
	}
}

// statusHub fans a snapshot out to UI subscribers on the UI thread.
type statusHub struct {
	mu   sync.Mutex
	last statusSnapshot
	have bool
	subs []func(statusSnapshot)
	kick chan struct{}
}

func newStatusHub() *statusHub {
	return &statusHub{kick: make(chan struct{}, 1)}
}

// subscribe registers fn, which is always invoked on the UI thread. When a
// snapshot already exists fn receives it immediately.
func (h *statusHub) subscribe(fn func(statusSnapshot)) {
	h.mu.Lock()
	h.subs = append(h.subs, fn)
	last, have := h.last, h.have
	h.mu.Unlock()
	if have {
		fn(last)
	}
}

// publish stores the snapshot and delivers it to subscribers via fyne.Do.
func (h *statusHub) publish(snapshot statusSnapshot) {
	h.mu.Lock()
	h.last, h.have = snapshot, true
	subs := make([]func(statusSnapshot), len(h.subs))
	copy(subs, h.subs)
	h.mu.Unlock()
	fyne.Do(func() {
		for _, sub := range subs {
			sub(snapshot)
		}
	})
}

// latest returns the most recent snapshot (zero value before the first tick).
func (h *statusHub) latest() statusSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// refreshSoon asks the ticker to gather a fresh snapshot without waiting for
// the next interval. It never blocks.
func (h *statusHub) refreshSoon() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// runStatusTicker is the single background loop that gathers status until
// ctx is cancelled.
func (c *appContext) runStatusTicker(ctx context.Context) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	for {
		c.status.publish(c.gatherStatus(ctx))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.status.kick:
		}
	}
}

// gatherStatus performs every status read (files, locks, bridge HTTP) in one
// place. It must never run on the UI thread.
func (c *appContext) gatherStatus(ctx context.Context) statusSnapshot {
	snapshot := statusSnapshot{Taken: time.Now(), SettingsOK: c.currentSettings().Complete()}
	snapshot.DaemonPID, snapshot.DaemonRunning = daemonctl.Running()
	if status, err := daemonctl.ReadStatus(); err == nil {
		snapshot.Daemon, snapshot.DaemonStatusOK = status, true
		if snapshot.DaemonPID == 0 && snapshot.DaemonRunning {
			snapshot.DaemonPID = status.PID
		}
	}
	snapshot.DaemonBusy = c.isDaemonBusy()

	probe, cancel := context.WithTimeout(ctx, bridgeProbe)
	status, err := c.bridge.Status(probe)
	cancel()
	switch {
	case err == nil:
		snapshot.BridgeReachable, snapshot.Bridge = true, status
	case errors.Is(err, bridge.ErrUnreachable), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
	default:
		snapshot.BridgeReachable = true // listening but /status failed (older bridge)
	}
	snapshot.BridgeOwned = c.ownedBridge() != nil

	snapshot.RunActive = flow.RunActive()
	snapshot.LastRun, _ = flow.ReadLastRun()
	if entries, err := c.schedules.List(); err == nil {
		for _, entry := range entries {
			if entry.Status == schedule.StatusPending || entry.Status == schedule.StatusRunning {
				snapshot.Schedules = append(snapshot.Schedules, entry)
			}
		}
	}
	return snapshot
}
