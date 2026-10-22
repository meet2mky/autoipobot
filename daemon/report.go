package main

import (
	"context"
	"fmt"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/daemonctl"
	"autoipo/internal/flow"
	"autoipo/internal/schedule"
	"autoipo/internal/users"
)

const (
	timeLayout    = "2006-01-02 15:04:05"
	bridgeTimeout = 2 * time.Second
)

// formatDuration renders a duration with second precision ("1h2m3s").
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// statusLines renders a published daemon status for humans. The chat ID is
// deliberately omitted.
func statusLines(status daemonctl.Status, now time.Time) []string {
	lines := []string{fmt.Sprintf("%s · autoipo %s · pid %d", status.Label, status.Version, status.PID)}
	if !status.StartedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("up %s (since %s)", formatDuration(now.Sub(status.StartedAt)), status.StartedAt.Local().Format(timeLayout)))
	}
	poll := "last poll: never"
	if !status.LastPollAt.IsZero() {
		poll = fmt.Sprintf("last poll %s ago", formatDuration(now.Sub(status.LastPollAt)))
	}
	lines = append(lines, fmt.Sprintf("%s · polls %d · commands %d", poll, status.Polls, status.Commands))
	if status.LastPollError != "" {
		lines = append(lines, "last poll error: "+status.LastPollError)
	}
	if status.LastCommand != "" {
		lines = append(lines, fmt.Sprintf("last command /%s at %s", status.LastCommand, status.LastCommandAt.Local().Format(timeLayout)))
	}
	return lines
}

// bridgeLine describes the extension bridge, probing it for at most
// bridgeTimeout.
func bridgeLine(ctx context.Context, client *bridge.Client) string {
	ctx, cancel := context.WithTimeout(ctx, bridgeTimeout)
	defer cancel()
	status, err := client.Status(ctx)
	switch {
	case err != nil:
		return "bridge: not reachable"
	case status.ExtensionConnected:
		return "bridge: reachable · extension connected"
	default:
		return "bridge: reachable · extension not connected"
	}
}

// lastRunLine summarises the most recent flow run on this machine.
func lastRunLine() string {
	last, err := flow.ReadLastRun()
	if err != nil {
		return "last run: unavailable (" + err.Error() + ")"
	}
	return "last run: " + last.Summary()
}

// schedulesLine reports how many scheduled runs are waiting.
func schedulesLine(store *schedule.Store) string {
	pending, err := pendingSchedules(store)
	if err != nil {
		return "schedules: unavailable (" + err.Error() + ")"
	}
	return fmt.Sprintf("schedules: %d pending", pending)
}

// pendingSchedules counts entries that have not run yet.
func pendingSchedules(store *schedule.Store) (int, error) {
	entries, err := store.List()
	if err != nil {
		return 0, err
	}
	pending := 0
	for _, entry := range entries {
		if entry.Status == schedule.StatusPending {
			pending++
		}
	}
	return pending, nil
}

// maskPAN hides the digits of a PAN ("ABCDE••••F"); anything that is not a
// well-formed PAN is masked completely.
func maskPAN(pan string) string {
	pan = users.NormalizePAN(pan)
	if !users.ValidPAN(pan) {
		return "••••••••••"
	}
	return pan[:5] + "••••" + pan[9:]
}
