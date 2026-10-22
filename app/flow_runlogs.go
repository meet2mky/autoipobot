package main

import (
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
	"autoipo/internal/flow"
)

// newRunLogsPage shows the retained run transcripts of f, newest first,
// with the failure screenshot path when one was captured.
func newRunLogsPage(p *flowsPanel, f flow.Flow) fyne.CanvasObject {
	header := container.NewBorder(nil, nil, widget.NewButton("Back to flows", p.showList), nil, boldLabel("Run logs — "+f.Name))
	runs := flow.ListRuns(f.ID)
	if len(runs) == 0 {
		return container.NewBorder(header, nil, nil, nil,
			widget.NewCard("No run logs yet", "", wrappedLabel("Play the flow to create its first run log.")))
	}
	pane := newTextPane("")
	screenshot := wrappedLabel("")
	options := make([]string, len(runs))
	byOption := make(map[string]flow.RunInfo, len(runs))
	for i, run := range runs {
		label := run.RunID
		if !run.Time.IsZero() {
			label = format.Timestamp(run.Time) + " · " + run.RunID
		}
		switch i {
		case 0:
			label = "Latest — " + label
		case 1:
			label = "Previous — " + label
		}
		options[i] = label
		byOption[label] = run
	}
	picker := widget.NewSelect(options, func(option string) {
		showRunLog(byOption[option], f.ID, pane, screenshot)
	})
	picker.SetSelected(options[0])
	card := widget.NewCard("Retained runs", "Choose a run to read its transcript",
		container.NewBorder(container.NewVBox(picker, screenshot), nil, nil, nil, pane.scroll))
	return container.NewBorder(header, nil, nil, nil, card)
}

func showRunLog(run flow.RunInfo, flowID string, pane *textPane, screenshot *widget.Label) {
	data, err := os.ReadFile(run.Path)
	if err != nil {
		pane.setText("Could not read this run log: "+err.Error(), false)
	} else {
		pane.setText(string(data), false)
	}
	png := filepath.Join(flow.RunsDir(flowID), run.RunID+".png")
	if _, err := os.Stat(png); err == nil {
		screenshot.SetText("Screenshot: " + png)
	} else {
		screenshot.SetText("")
	}
}
