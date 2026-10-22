package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
	"autoipo/internal/flow"
	"autoipo/internal/schedule"
)

// flowListPage lists the saved flows as cards and shows the pending
// schedules card.
type flowListPage struct {
	panel        *flowsPanel
	cards        *fyne.Container
	scheduleRows *fyne.Container
	scheduleKey  string // last rendered schedule state ("unset" before the first snapshot)
	root         fyne.CanvasObject
}

func newFlowListPage(p *flowsPanel) *flowListPage {
	l := &flowListPage{panel: p, cards: container.NewVBox(), scheduleRows: container.NewVBox(), scheduleKey: "unset"}
	newButton := widget.NewButton("New flow", p.newFlow)
	newButton.Importance = widget.HighImportance
	header := container.NewBorder(nil, nil, boldLabel("Flows"), newButton)
	schedules := widget.NewCard("Schedules", "Pending and running one-shot runs executed by the daemon", l.scheduleRows)
	l.root = container.NewBorder(header, nil, nil, nil, container.NewVScroll(container.NewVBox(l.cards, schedules)))
	l.refresh()
	p.ctx.status.subscribe(l.applyStatus)
	return l
}

// refresh rebuilds the flow cards from the panel's flows.
func (l *flowListPage) refresh() {
	l.cards.Objects = nil
	for _, f := range l.panel.flows {
		l.cards.Add(l.card(f))
	}
	if len(l.panel.flows) == 0 {
		l.cards.Add(wrappedLabel("No saved flows yet. Create one to automate a bank portal step by step."))
	}
	l.cards.Refresh()
}

func (l *flowListPage) card(f flow.Flow) fyne.CanvasObject {
	transitions := make([]string, 0, len(f.Nodes))
	for _, node := range f.Nodes {
		transitions = append(transitions, f.TransitionSummary(node))
	}
	summary := wrappedLabel(strings.Join(transitions, "\n"))
	play := widget.NewButton("Play", func() { l.panel.runner.play(f) })
	play.Importance = widget.HighImportance
	buttons := container.NewHBox(
		play,
		widget.NewButton("Edit", func() { l.panel.openEditor(f) }),
		widget.NewButton("Run logs", func() { l.panel.showPage(newRunLogsPage(l.panel, f)) }),
		widget.NewButton("Schedule…", func() { l.panel.runner.showScheduleDialog(f) }),
		widget.NewButton("Duplicate", func() { l.panel.duplicateFlow(f) }),
		widget.NewButton("Delete", func() { l.panel.deleteFlow(f) }),
	)
	subtitle := fmt.Sprintf("%d states · keep %d run logs", len(f.Nodes), f.RunsToKeep)
	if issues := f.Lint(); len(issues) > 0 {
		subtitle += fmt.Sprintf(" · %d lint issue(s)", len(issues))
	}
	return widget.NewCard(f.Name, subtitle, container.NewVBox(summary, buttons))
}

// applyStatus re-renders the schedules card when the entries changed.
func (l *flowListPage) applyStatus(s statusSnapshot) {
	key := scheduleKey(s.Schedules)
	if key == l.scheduleKey {
		return
	}
	l.scheduleKey = key
	l.scheduleRows.Objects = nil
	if len(s.Schedules) == 0 {
		l.scheduleRows.Add(widget.NewLabel("Nothing scheduled. Use Schedule… on a flow to run it later."))
	}
	for _, entry := range s.Schedules {
		l.scheduleRows.Add(l.scheduleRow(entry))
	}
	l.scheduleRows.Refresh()
}

func scheduleKey(entries []schedule.Entry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.ID+":"+entry.Status+":"+entry.At.Format("200601021504"))
	}
	return strings.Join(parts, ",")
}

func (l *flowListPage) scheduleRow(entry schedule.Entry) fyne.CanvasObject {
	who := entry.UserName
	if who == "" && entry.UserPAN != "" {
		who = format.MaskPAN(entry.UserPAN)
	}
	if who == "" {
		who = "no user"
	}
	text := fmt.Sprintf("%s · as %s · %s · %s", entry.FlowName, who, entry.At.Local().Format("2006-01-02 15:04"), entry.Status)
	label := wrappedLabel(text)
	if entry.Status != schedule.StatusPending {
		return label
	}
	cancel := widget.NewButton("Cancel", func() { l.cancelSchedule(entry) })
	return container.NewBorder(nil, nil, nil, container.NewHBox(layout.NewSpacer(), cancel), label)
}

func (l *flowListPage) cancelSchedule(entry schedule.Entry) {
	ctx := l.panel.ctx
	go func() {
		err := ctx.schedules.Cancel(entry.ID)
		if err != nil {
			ctx.reportError("cancel schedule", err)
		} else {
			ctx.logEvent("cancelled schedule %s (%s)", entry.ID, entry.FlowName)
		}
		ctx.status.refreshSoon()
	}()
}
