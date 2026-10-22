package main

import (
	"context"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
)

// drawerMaxLines bounds the live transcript kept in the drawer.
const drawerMaxLines = 500

// runDrawer is the panel docked under the Flows pages that shows the live
// transcript of the current run, the elapsed time, a Stop button and the
// final result.
type runDrawer struct {
	title   *widget.Label
	elapsed *widget.Label
	result  *widget.Label
	pane    *textPane
	stopBtn *widget.Button
	hideBtn *widget.Button

	lines      []string
	cancel     context.CancelFunc
	started    time.Time
	stopTicker context.CancelFunc
	root       *fyne.Container
	// onToggle is invoked after the drawer is shown or hidden so the owning
	// container can re-run its layout (Border layouts skip hidden children).
	onToggle func()
}

func newRunDrawer() *runDrawer {
	d := &runDrawer{}
	d.title = boldLabel("")
	d.elapsed = widget.NewLabel("")
	d.result = wrappedLabel("")
	d.pane = newTextPane("")
	d.pane.scroll.SetMinSize(fyne.NewSize(0, 150))
	d.stopBtn = widget.NewButton("Stop", d.stop)
	d.stopBtn.Importance = widget.DangerImportance
	d.hideBtn = widget.NewButton("Hide", d.hide)
	header := container.NewBorder(nil, nil, container.NewHBox(d.title, d.elapsed), container.NewHBox(d.stopBtn, d.hideBtn))
	d.root = container.NewVBox(widget.NewSeparator(), header, d.pane.scroll, d.result)
	d.root.Hide()
	return d
}

// show/hide toggle visibility and let the parent container re-layout.
func (d *runDrawer) show() {
	d.root.Show()
	if d.onToggle != nil {
		d.onToggle()
	}
}

func (d *runDrawer) hide() {
	d.root.Hide()
	if d.onToggle != nil {
		d.onToggle()
	}
}

// begin shows the drawer for a new run; cancel stops the run.
func (d *runDrawer) begin(flowName string, cancel context.CancelFunc) {
	d.cancel = cancel
	d.lines = nil
	d.started = time.Now()
	d.title.SetText("Running " + flowName)
	d.elapsed.SetText("0s")
	d.result.SetText("")
	d.pane.setText("", false)
	d.stopBtn.Enable()
	d.hideBtn.Disable()
	d.show()
	ctx, stop := context.WithCancel(context.Background())
	d.stopTicker = stop
	go d.tick(ctx, d.started)
}

func (d *runDrawer) tick(ctx context.Context, started time.Time) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			text := format.Duration(time.Since(started))
			fyne.Do(func() { d.elapsed.SetText(text) })
		}
	}
}

// appendLine adds a transcript line (UI thread).
func (d *runDrawer) appendLine(line string) {
	d.lines = append(d.lines, line)
	if len(d.lines) > drawerMaxLines {
		d.lines = d.lines[len(d.lines)-drawerMaxLines:]
	}
	d.pane.setText(strings.Join(d.lines, "\n"), true)
}

// finish records the outcome and stops the elapsed timer (UI thread).
func (d *runDrawer) finish(summary string) {
	if d.stopTicker != nil {
		d.stopTicker()
		d.stopTicker = nil
	}
	d.cancel = nil
	d.elapsed.SetText(format.Duration(time.Since(d.started)))
	d.result.SetText(summary)
	d.stopBtn.Disable()
	d.hideBtn.Enable()
}

func (d *runDrawer) stop() {
	if d.cancel == nil {
		return
	}
	d.cancel()
	d.stopBtn.Disable()
	d.result.SetText("Stopping…")
}
