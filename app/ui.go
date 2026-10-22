package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// mainUI is the window content: one header above the tabs.
type mainUI struct {
	root         fyne.CanvasObject
	tabs         *container.AppTabs
	settingsTab  *container.TabItem
	settings     *settingsPanel
	users        *usersPanel
	logs         *logsPanel
	headerStatus *widget.Label
}

func buildUI(ctx *appContext) *mainUI {
	ui := &mainUI{}
	ui.settings = newSettingsPanel(ctx)
	ui.settingsTab = container.NewTabItem("Settings", ui.settings.root)
	status := newStatusPanel(ctx, ui.openSettings)
	commands := newCommandsPanel(ctx)
	flows := newFlowsPanel(ctx)
	ui.users = newUsersPanel(ctx)
	ui.logs = newLogsPanel(ctx)
	ui.tabs = container.NewAppTabs(
		container.NewTabItem("Status", status.root),
		container.NewTabItem("Commands", commands.root),
		container.NewTabItem("Flows", flows.root),
		container.NewTabItem("Users", ui.users.root),
		ui.settingsTab,
		container.NewTabItem("Logs", ui.logs.root),
	)
	ui.tabs.SetTabLocation(container.TabLocationLeading)
	ui.root = container.NewBorder(ui.header(ctx), nil, nil, nil, ui.tabs)
	return ui
}

func (ui *mainUI) header(ctx *appContext) fyne.CanvasObject {
	title := canvas.NewText("autoipo", accentPrimary)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 22
	subtitle := widget.NewLabel("Desktop control for the Telegram IPO daemon")
	ui.headerStatus = widget.NewLabel("Checking status…")
	ui.headerStatus.Alignment = fyne.TextAlignTrailing
	ctx.status.subscribe(func(s statusSnapshot) {
		ui.headerStatus.SetText(s.daemonLabel() + "  ·  " + s.bridgeLabel())
	})
	row := container.NewBorder(nil, nil, container.NewHBox(title, subtitle), ui.headerStatus)
	return container.NewVBox(container.NewPadded(row), widget.NewSeparator())
}

func (ui *mainUI) openSettings() { ui.tabs.Select(ui.settingsTab) }
