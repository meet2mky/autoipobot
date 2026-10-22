package main

import (
	"context"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/tail"
	"autoipo/internal/paths"
)

// logsRefreshInterval is how often the daemon log file is checked for changes.
const logsRefreshInterval = 2 * time.Second

// logsPanel is the Logs tab: the daemon log tail on top and the app events
// pane below.
type logsPanel struct {
	ctx *appContext

	daemonPane   *textPane
	daemonFilter *widget.Entry
	follow       *widget.Check
	daemonLines  []string // last tail read; UI thread only
	daemonStatus *widget.Label

	eventsPane   *textPane
	eventsFilter *widget.Entry

	root fyne.CanvasObject
}

func newLogsPanel(ctx *appContext) *logsPanel {
	p := &logsPanel{ctx: ctx}
	p.daemonPane = newTextPane("log file not created yet")
	p.daemonFilter = hintEntry("Filter daemon log…")
	p.daemonFilter.OnChanged = func(string) { p.renderDaemon() }
	p.follow = widget.NewCheck("Follow", func(bool) { p.renderDaemon() })
	p.follow.SetChecked(true)
	p.daemonStatus = widget.NewLabel(paths.DaemonLogFile())
	p.daemonStatus.Truncation = fyne.TextTruncateEllipsis
	copyDaemon := widget.NewButton("Copy", func() { ctx.app.Clipboard().SetContent(p.daemonPane.label.Text) })
	daemonBar := container.NewBorder(nil, nil, nil, container.NewHBox(p.follow, copyDaemon), p.daemonFilter)
	daemonCard := widget.NewCard("Daemon log", "", container.NewBorder(
		container.NewVBox(daemonBar, p.daemonStatus), nil, nil, nil, p.daemonPane.scroll))

	p.eventsPane = newTextPane("No app events yet.")
	p.eventsFilter = hintEntry("Filter app events…")
	p.eventsFilter.OnChanged = func(string) { p.renderEvents() }
	copyEvents := widget.NewButton("Copy", func() { ctx.app.Clipboard().SetContent(p.eventsPane.label.Text) })
	clearEvents := widget.NewButton("Clear", ctx.events.Clear)
	eventsBar := container.NewBorder(nil, nil, nil, container.NewHBox(copyEvents, clearEvents), p.eventsFilter)
	eventsCard := widget.NewCard("App events", "Messages from this desktop app (bridge, daemon control, flows)",
		container.NewBorder(eventsBar, nil, nil, nil, p.eventsPane.scroll))

	split := container.NewVSplit(daemonCard, eventsCard)
	split.SetOffset(0.62)
	p.root = split

	ctx.events.SetOnChange(func() { fyne.Do(p.renderEvents) })
	p.renderEvents()
	return p
}

// start launches the daemon log watcher; call it once the Fyne event loop is
// running so widget updates are queued to the UI thread.
func (p *logsPanel) start(ctx context.Context) {
	go p.watchDaemonLog(ctx)
}

// watchDaemonLog re-reads the daemon log tail only when its size or mtime
// changed, then applies the new lines on the UI thread.
func (p *logsPanel) watchDaemonLog(ctx context.Context) {
	var last tail.Fingerprint
	known := false
	ticker := time.NewTicker(logsRefreshInterval)
	defer ticker.Stop()
	for {
		current, err := tail.Stat(paths.DaemonLogFile())
		switch {
		case err != nil && known:
			known = false
			fyne.Do(func() { p.setDaemonLines(nil, "log file not created yet") })
		case err == nil && (!known || !current.Equal(last)):
			known, last = true, current
			lines, readErr := tail.LastLines(paths.DaemonLogFile(), tail.DefaultMaxBytes, tail.DefaultMaxLines)
			status := paths.DaemonLogFile()
			if readErr != nil {
				status = "cannot read log: " + readErr.Error()
			}
			fyne.Do(func() { p.setDaemonLines(lines, status) })
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *logsPanel) setDaemonLines(lines []string, status string) {
	p.daemonLines = lines
	p.daemonStatus.SetText(status)
	p.renderDaemon()
}

func (p *logsPanel) renderDaemon() {
	query := strings.ToLower(strings.TrimSpace(p.daemonFilter.Text))
	lines := p.daemonLines
	if query != "" {
		lines = nil
		for _, line := range p.daemonLines {
			if strings.Contains(strings.ToLower(line), query) {
				lines = append(lines, line)
			}
		}
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		if len(p.daemonLines) == 0 {
			text = "log file not created yet"
		} else {
			text = "No matching lines."
		}
	}
	p.daemonPane.setText(text, p.follow.Checked)
}

func (p *logsPanel) renderEvents() {
	text := p.ctx.events.Filter(p.eventsFilter.Text)
	if text == "" {
		if p.ctx.events.Len() == 0 {
			text = "No app events yet."
		} else {
			text = "No matching events."
		}
	}
	p.eventsPane.setText(text, true)
}
