package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"autoipo/internal/config"
	"autoipo/internal/paths"
	"autoipo/internal/telegram"
)

// telegramTestTimeout bounds the Test connection round trip.
const telegramTestTimeout = 20 * time.Second

const testMessage = "autoipo: test message from the desktop app"

// settingsPanel is the Settings tab: Telegram credentials, host label, a
// connection test and storage information.
type settingsPanel struct {
	ctx        *appContext
	label      *widget.Entry
	token      *widget.Entry
	chat       *widget.Entry
	testResult *widget.Label
	saveBtn    *widget.Button
	testBtn    *widget.Button
	root       fyne.CanvasObject
}

func newSettingsPanel(ctx *appContext) *settingsPanel {
	p := &settingsPanel{ctx: ctx}
	p.label = hintEntry(config.DefaultLabel)
	p.token = widget.NewPasswordEntry()
	p.token.SetPlaceHolder("Telegram bot token")
	p.chat = hintEntry("123456789")
	p.resetFields()
	p.saveBtn = widget.NewButton("Save", p.save)
	p.saveBtn.Importance = widget.HighImportance
	reset := widget.NewButton("Reset", p.resetFields)
	p.testBtn = widget.NewButton("Test connection", p.testConnection)
	p.testResult = wrappedLabel("")

	form := widget.NewForm(
		&widget.FormItem{Text: "Host label", Widget: p.label, HintText: "Shown in Telegram messages to tell machines apart"},
		&widget.FormItem{Text: "Bot token", Widget: p.token, HintText: "Stored in the " + ctx.secrets.Backend() + " secrets store"},
		&widget.FormItem{Text: "Chat ID", Widget: p.chat, HintText: "Only this chat may control the daemon"},
	)
	credentials := widget.NewCard("Telegram daemon", "Bot credentials and host identity",
		container.NewVBox(form, container.NewHBox(p.saveBtn, reset, p.testBtn), p.testResult))
	dataDir := widget.NewLabel(paths.DataDir())
	dataDir.Wrapping = fyne.TextWrapBreak
	storage := widget.NewCard("Storage", "", infoGrid(
		infoRow{"Secrets backend", widget.NewLabel(ctx.secrets.Backend())},
		infoRow{"Data directory", dataDir},
		infoRow{"Daemon binary", widget.NewLabel(paths.DaemonBinary())},
	))
	p.root = newPageScroll(container.NewVBox(credentials, storage))
	return p
}

func (p *settingsPanel) draft() config.Settings {
	return config.Settings{
		Token:     strings.TrimSpace(p.token.Text),
		ChatID:    strings.TrimSpace(p.chat.Text),
		HostLabel: strings.TrimSpace(p.label.Text),
	}
}

// resetFields reloads the entries from the saved settings.
func (p *settingsPanel) resetFields() {
	settings := p.ctx.currentSettings()
	p.label.SetText(settings.HostLabel)
	p.token.SetText(settings.Token)
	p.chat.SetText(settings.ChatID)
}

func (p *settingsPanel) save() {
	settings := p.draft()
	if err := settings.Validate(); err != nil {
		dialog.ShowError(err, p.ctx.window)
		return
	}
	p.saveBtn.Disable()
	p.ctx.saveSettings(settings, func(err error) {
		p.saveBtn.Enable()
		if err != nil {
			return
		}
		p.resetFields()
		p.offerRestart()
	})
}

// offerRestart proposes restarting the daemon so new settings take effect.
func (p *settingsPanel) offerRestart() {
	if !p.ctx.status.latest().DaemonRunning {
		p.offerStart()
		return
	}
	dialog.ShowConfirm("Restart daemon?", "Settings saved. Restart the daemon now so the new values take effect?", func(ok bool) {
		if ok {
			p.ctx.restartDaemon(nil)
		}
	}, p.ctx.window)
}

// offerStart proposes starting the daemon when it is not running.
func (p *settingsPanel) offerStart() {
	dialog.ShowConfirm("Start daemon?", "Settings saved. Start the daemon now?", func(ok bool) {
		if ok {
			p.ctx.startDaemon(nil)
		}
	}, p.ctx.window)
}

// testConnection verifies the token with getMe and sends a test message to
// the chat, off the UI thread.
func (p *settingsPanel) testConnection() {
	settings := p.draft()
	if err := settings.Validate(); err != nil {
		p.testResult.SetText("Cannot test: " + err.Error())
		return
	}
	chatID, _ := settings.ChatIDInt()
	p.testBtn.Disable()
	p.testResult.SetText("Testing…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), telegramTestTimeout)
		defer cancel()
		client := telegram.New(settings.Token)
		me, err := client.GetMe(ctx)
		if err == nil {
			err = client.SendMessage(ctx, chatID, testMessage)
		}
		fyne.Do(func() {
			p.testBtn.Enable()
			if err != nil {
				p.testResult.SetText("Test failed: " + err.Error())
				p.ctx.logEvent("telegram test failed: %v", err)
				return
			}
			p.testResult.SetText(fmt.Sprintf("Connected as @%s and sent a test message to chat %d.", me.Username, chatID))
			p.ctx.logEvent("telegram test succeeded as @%s", me.Username)
		})
	}()
}

// showFirstRunDialog asks for the required settings when they are missing.
// Cancel simply closes the dialog; the Status tab keeps showing "Settings
// incomplete" until the Settings tab is completed. onSaved runs after a
// successful save.
func showFirstRunDialog(ctx *appContext, onSaved func()) {
	settings := ctx.currentSettings()
	token := widget.NewPasswordEntry()
	token.SetPlaceHolder("Telegram bot token")
	token.SetText(settings.Token)
	chat := hintEntry("123456789")
	chat.SetText(settings.ChatID)
	label := hintEntry(config.DefaultLabel)
	label.SetText(settings.HostLabel)
	items := []*widget.FormItem{
		{Text: "", Widget: wrappedLabel("The daemon needs a Telegram bot token and the chat ID allowed to control it.")},
		{Text: "Bot token", Widget: token},
		{Text: "Chat ID", Widget: chat},
		{Text: "Host label", Widget: label},
	}
	form := dialog.NewForm("Welcome to autoipo", "Save", "Cancel", items, func(ok bool) {
		if !ok {
			ctx.logEvent("setup dialog dismissed; complete the Settings tab to start the daemon")
			return
		}
		draft := config.Settings{
			Token:     strings.TrimSpace(token.Text),
			ChatID:    strings.TrimSpace(chat.Text),
			HostLabel: strings.TrimSpace(label.Text),
		}
		if err := draft.Validate(); err != nil {
			dialog.ShowError(fmt.Errorf("%w — you can complete this later in the Settings tab", err), ctx.window)
			return
		}
		ctx.saveSettings(draft, func(err error) {
			if err == nil && onSaved != nil {
				onSaved()
			}
		})
	}, ctx.window)
	form.Resize(dialogSize(ctx.window, form.MinSize().Height))
	form.Show()
}
