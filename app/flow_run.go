package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/format"
	"autoipo/internal/flow"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/users"
)

// dialogWidth is the preferred width of the Play/Schedule forms.
const dialogWidth float32 = 520

// flowRunner starts flows from the app (Play dialog) and creates schedule
// entries (Schedule… dialog). Only one app-initiated run exists at a time.
type flowRunner struct {
	panel  *flowsPanel
	active bool
}

func newFlowRunner(p *flowsPanel) *flowRunner { return &flowRunner{panel: p} }

// play opens the Play dialog for f after loading the user records.
func (r *flowRunner) play(f flow.Flow) {
	if r.active {
		dialog.ShowInformation("Run in progress", "Wait for the current run to finish, or stop it from the run panel below.", r.panel.ctx.window)
		return
	}
	r.panel.ctx.listUsers(func(records []users.Record) { r.showPlayDialog(f, records) })
}

func (r *flowRunner) showPlayDialog(f flow.Flow, records []users.Record) {
	picker := newUserPicker(records)
	names := f.ReferencedPlaceholders()
	defaults := f.DefaultValues()
	entries := make(map[string]*widget.Entry, len(names))
	items := []*widget.FormItem{{Text: "Run as user", Widget: picker.widget}}
	for _, name := range names {
		entry := widget.NewEntry()
		if isSecretPlaceholder(name) {
			entry = widget.NewPasswordEntry()
		}
		entry.SetPlaceHolder("Value for @" + name)
		entry.SetText(defaults[name])
		entries[name] = entry
		items = append(items, &widget.FormItem{Text: "@" + name, Widget: entry})
	}
	if len(names) == 0 {
		items = append(items, &widget.FormItem{Text: "", Widget: wrappedLabel("This flow has no placeholders to fill in.")})
	}
	picker.onChange = func(record *users.Record) { prefillPlaceholders(entries, defaults, record) }
	form := dialog.NewForm("Play "+f.Name, "Start", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		values := make(map[string]string, len(entries))
		for name, entry := range entries {
			values[name] = entry.Text
		}
		r.start(f, values, picker.selected())
	}, r.panel.ctx.window)
	form.Resize(fyne.NewSize(dialogWidth, form.MinSize().Height))
	form.Show()
}

// prefillPlaceholders overlays a user's values on the flow defaults.
func prefillPlaceholders(entries map[string]*widget.Entry, defaults map[string]string, record *users.Record) {
	var userValues map[string]string
	if record != nil {
		userValues = record.Placeholders()
	}
	for name, entry := range entries {
		value := defaults[name]
		if userValue, ok := userValues[name]; ok && userValue != "" {
			value = userValue
		}
		entry.SetText(value)
	}
}

// isSecretPlaceholder reports whether a placeholder must be entered masked.
func isSecretPlaceholder(name string) bool {
	lower := strings.ToLower(name)
	return flow.SecretPlaceholders[lower] || strings.Contains(lower, "password")
}

// start runs f in a goroutine, streaming the transcript into the drawer.
func (r *flowRunner) start(f flow.Flow, values map[string]string, user *users.Record) {
	ctx := r.panel.ctx
	runCtx, cancel := context.WithCancel(context.Background())
	r.active = true
	r.panel.drawer.begin(f.Name, cancel)
	executor := flow.NewBridgeExecutor(paths.CLIBinary())
	executor.Started = ctx.adoptBridgeProcess
	request := flow.RunRequest{
		Flow:   f.Clone(),
		Values: values,
		Source: "app",
		OnLine: func(line string) { fyne.Do(func() { r.panel.drawer.appendLine(line) }) },
		OnState: func(nodeID string) {
			fyne.Do(func() { r.panel.editor.setRunningNode(f.ID, nodeID) })
		},
	}
	if user != nil {
		request.UserValues = user.Placeholders()
		request.UserLabel = user.Name
	}
	ctx.logEvent("flow %q started from the app", f.Name)
	ctx.status.refreshSoon()
	go func() {
		result, err := flow.Run(runCtx, request, executor, flow.DefaultOptions())
		cancel()
		fyne.Do(func() { r.finish(f, result, err) })
	}()
}

// finish reports the outcome (UI thread).
func (r *flowRunner) finish(f flow.Flow, result flow.RunResult, err error) {
	ctx := r.panel.ctx
	r.active = false
	r.panel.editor.setRunningNode(f.ID, "")
	ctx.status.refreshSoon()
	switch {
	case errors.Is(err, flow.ErrAnotherRunActive):
		r.panel.drawer.finish("Not started: another flow run is already in progress.")
		ctx.logEvent("flow %q not started: %v", f.Name, err)
		dialog.ShowInformation("Another run is active",
			"A flow is already running (started from Telegram, a schedule or this app). Wait for it to finish and try again.",
			ctx.window)
	case err != nil:
		r.panel.drawer.finish("Run failed to start: " + err.Error())
		ctx.reportError("run flow "+f.Name, err)
	default:
		summary := runSummary(result)
		r.panel.drawer.finish(summary)
		ctx.logEvent("flow %q finished: %s", f.Name, summary)
	}
}

func runSummary(result flow.RunResult) string {
	state := "FAILED — " + result.Reason
	switch {
	case result.Success:
		state = "SUCCESS"
	case result.Cancelled:
		state = "CANCELLED"
	}
	text := fmt.Sprintf("%s in %s (%d steps) · log: %s", state, format.Duration(result.FinishedAt.Sub(result.StartedAt)), result.Steps, result.LogPath)
	switch {
	case result.ScreenshotPath != "":
		text += " · screenshot: " + result.ScreenshotPath
	case result.ScreenshotErr != "":
		text += " · no screenshot: " + result.ScreenshotErr
	}
	return text
}

// showScheduleDialog creates a one-shot schedule entry executed by the daemon.
func (r *flowRunner) showScheduleDialog(f flow.Flow) {
	r.panel.ctx.listUsers(func(records []users.Record) {
		picker := newUserPicker(records)
		at := hintEntry("YYYY-MM-DD HH:MM, HH:MM, +15m")
		items := []*widget.FormItem{
			{Text: "Run as user", Widget: picker.widget},
			{Text: "When", Widget: at, HintText: "YYYY-MM-DD HH:MM · HH:MM (today, or tomorrow if past) · +15m · +2h"},
			{Text: "", Widget: wrappedLabel("Scheduled runs are executed by the daemon; keep it running.")},
		}
		form := dialog.NewForm("Schedule "+f.Name, "Schedule", "Cancel", items, func(ok bool) {
			if !ok {
				return
			}
			when, err := schedule.ParseAt(at.Text, time.Now())
			if err != nil {
				dialog.ShowError(err, r.panel.ctx.window)
				return
			}
			r.addSchedule(f, picker.selected(), when)
		}, r.panel.ctx.window)
		form.Resize(fyne.NewSize(dialogWidth, form.MinSize().Height))
		form.Show()
	})
}

func (r *flowRunner) addSchedule(f flow.Flow, user *users.Record, at time.Time) {
	entry := schedule.Entry{FlowID: f.ID, FlowName: f.Name, At: at, Source: "app"}
	if user != nil {
		entry.UserPAN, entry.UserName = user.PAN, user.Name
	}
	ctx := r.panel.ctx
	go func() {
		added, err := ctx.schedules.Add(entry)
		if err != nil {
			ctx.reportError("schedule flow", err)
			return
		}
		ctx.logEvent("scheduled %q for %s (id %s)", f.Name, at.Format("2006-01-02 15:04"), added.ID)
		ctx.status.refreshSoon()
	}()
}

// userPicker is a Select over the stored users with a "none" option.
type userPicker struct {
	widget   *widget.Select
	records  map[string]users.Record
	onChange func(*users.Record)
}

const noUserOption = "— none —"

func newUserPicker(records []users.Record) *userPicker {
	p := &userPicker{records: make(map[string]users.Record, len(records))}
	options := []string{noUserOption}
	for _, record := range records {
		label := fmt.Sprintf("%s (%s · %s)", record.Name, record.BankName, format.MaskPAN(record.PAN))
		options = append(options, label)
		p.records[label] = record
	}
	p.widget = widget.NewSelect(options, func(string) {
		if p.onChange != nil {
			p.onChange(p.selected())
		}
	})
	p.widget.SetSelected(noUserOption)
	return p
}

// selected returns the chosen record, or nil for "none".
func (p *userPicker) selected() *users.Record {
	if record, ok := p.records[p.widget.Selected]; ok {
		return &record
	}
	return nil
}
