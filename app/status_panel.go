package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
)

// statusPanel is the Status tab: daemon, bridge and automation cards fed by
// the shared status snapshot, plus the daemon controls.
type statusPanel struct {
	ctx *appContext

	daemonState, daemonUptime, daemonPoll, daemonCounts, daemonCommand *widget.Label
	bridgeState, bridgeExtension, bridgeProtocol                       *widget.Label
	autoRun, autoLast, autoSchedules                                   *widget.Label
	startBtn, stopBtn, restartBtn                                      *widget.Button
	openSettings                                                       func()

	root fyne.CanvasObject
}

func newStatusPanel(ctx *appContext, openSettings func()) *statusPanel {
	p := &statusPanel{ctx: ctx, openSettings: openSettings}
	p.daemonState, p.daemonUptime = widget.NewLabel("…"), widget.NewLabel("—")
	p.daemonPoll, p.daemonCounts, p.daemonCommand = widget.NewLabel("—"), widget.NewLabel("—"), widget.NewLabel("—")
	p.bridgeState, p.bridgeExtension, p.bridgeProtocol = widget.NewLabel("…"), widget.NewLabel("—"), widget.NewLabel("—")
	p.autoRun, p.autoLast, p.autoSchedules = widget.NewLabel("…"), wrappedLabel("—"), widget.NewLabel("—")

	p.startBtn = widget.NewButton("Start", p.onStart)
	p.startBtn.Importance = widget.HighImportance
	p.stopBtn = widget.NewButton("Stop", p.onStop)
	p.stopBtn.Importance = widget.DangerImportance
	p.restartBtn = widget.NewButton("Restart", p.onRestart)
	p.restartBtn.Importance = widget.WarningImportance
	for _, button := range []*widget.Button{p.startBtn, p.stopBtn, p.restartBtn} {
		button.Disable()
	}

	daemon := widget.NewCard("Daemon", "Telegram polling process", container.NewVBox(
		infoGrid(
			infoRow{"State", p.daemonState},
			infoRow{"Uptime", p.daemonUptime},
			infoRow{"Last poll", p.daemonPoll},
			infoRow{"Activity", p.daemonCounts},
			infoRow{"Last command", p.daemonCommand},
		),
		container.NewHBox(p.startBtn, p.stopBtn, p.restartBtn),
	))
	bridgeCard := widget.NewCard("Bridge", "Local extension bridge", infoGrid(
		infoRow{"Bridge", p.bridgeState},
		infoRow{"Extension", p.bridgeExtension},
		infoRow{"Protocol", p.bridgeProtocol},
	))
	automation := widget.NewCard("Automation", "Flows and schedules", infoGrid(
		infoRow{"Run", p.autoRun},
		infoRow{"Last run", p.autoLast},
		infoRow{"Schedules", p.autoSchedules},
	))
	p.root = container.NewVScroll(container.NewVBox(daemon, bridgeCard, automation))
	ctx.status.subscribe(p.apply)
	return p
}

// infoRow is one caption/value pair of an infoGrid.
type infoRow struct {
	caption string
	value   fyne.CanvasObject
}

// infoGrid lays out caption/value pairs in two columns.
func infoGrid(rows ...infoRow) fyne.CanvasObject {
	grid := container.NewGridWithColumns(2)
	for _, row := range rows {
		grid.Add(boldLabel(row.caption))
		grid.Add(row.value)
	}
	return grid
}

// apply renders a snapshot; always called on the UI thread.
func (p *statusPanel) apply(s statusSnapshot) {
	p.applyDaemon(s)
	p.applyBridge(s)
	p.applyAutomation(s)
	setEnabled(p.startBtn, s.SettingsOK && !s.DaemonRunning && !s.DaemonBusy)
	setEnabled(p.stopBtn, s.DaemonRunning && !s.DaemonBusy)
	setEnabled(p.restartBtn, s.SettingsOK && !s.DaemonBusy)
}

func (p *statusPanel) applyDaemon(s statusSnapshot) {
	switch {
	case s.DaemonBusy:
		p.daemonState.SetText("Working…")
	case !s.SettingsOK && !s.DaemonRunning:
		p.daemonState.SetText("Settings incomplete — open the Settings tab")
	case s.DaemonRunning && s.DaemonPID > 0:
		p.daemonState.SetText(fmt.Sprintf("Running (PID %d)", s.DaemonPID))
	case s.DaemonRunning:
		p.daemonState.SetText("Running")
	default:
		p.daemonState.SetText("Stopped")
	}
	if !s.DaemonRunning || !s.DaemonStatusOK {
		p.daemonUptime.SetText("—")
		p.daemonPoll.SetText("—")
		p.daemonCounts.SetText("—")
		p.daemonCommand.SetText("—")
		return
	}
	p.daemonUptime.SetText(format.Duration(s.Daemon.Uptime(s.Taken)))
	poll := format.Age(s.Daemon.LastPollAt, s.Taken)
	if s.Daemon.LastPollError != "" {
		poll += " · error: " + s.Daemon.LastPollError
	}
	p.daemonPoll.SetText(poll)
	p.daemonCounts.SetText(fmt.Sprintf("%d polls · %d commands", s.Daemon.Polls, s.Daemon.Commands))
	if s.Daemon.LastCommand == "" {
		p.daemonCommand.SetText("none yet")
	} else {
		p.daemonCommand.SetText(fmt.Sprintf("%s (%s)", s.Daemon.LastCommand, format.Age(s.Daemon.LastCommandAt, s.Taken)))
	}
}

func (p *statusPanel) applyBridge(s statusSnapshot) {
	if !s.BridgeReachable {
		p.bridgeState.SetText("Not running")
		p.bridgeExtension.SetText("—")
		p.bridgeProtocol.SetText("—")
		return
	}
	state := "Reachable"
	if s.BridgeOwned {
		state += " (started by this app)"
	}
	p.bridgeState.SetText(state)
	if s.Bridge.ExtensionConnected {
		p.bridgeExtension.SetText("Connected")
	} else {
		p.bridgeExtension.SetText("Not connected — open Chrome with the extension enabled")
	}
	if s.Bridge.ProtocolVersion > 0 {
		p.bridgeProtocol.SetText(fmt.Sprintf("v%d · %d pending command(s)", s.Bridge.ProtocolVersion, s.Bridge.PendingCommands))
	} else {
		p.bridgeProtocol.SetText("unknown")
	}
}

func (p *statusPanel) applyAutomation(s statusSnapshot) {
	if s.RunActive {
		p.autoRun.SetText("A flow is running")
	} else {
		p.autoRun.SetText("Idle")
	}
	p.autoLast.SetText(s.LastRun.Summary())
	pending := s.PendingSchedules()
	running := len(s.Schedules) - pending
	parts := []string{fmt.Sprintf("%d pending", pending)}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	p.autoSchedules.SetText(strings.Join(parts, " · "))
}

func (p *statusPanel) onStart() {
	if !p.ctx.currentSettings().Complete() {
		p.openSettings()
		return
	}
	p.ctx.startDaemon(nil)
}

func (p *statusPanel) onStop() {
	dialog.ShowConfirm("Stop daemon", "Stop the autoipo daemon? Telegram commands will not be handled until it is started again.", func(ok bool) {
		if ok {
			p.ctx.stopDaemon(nil)
		}
	}, p.ctx.window)
}

func (p *statusPanel) onRestart() {
	dialog.ShowConfirm("Restart daemon", "Restart the autoipo daemon now?", func(ok bool) {
		if ok {
			p.ctx.restartDaemon(nil)
		}
	}, p.ctx.window)
}
