package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

type daemonSettings struct {
	Token     string
	ChatID    string
	HostLabel string
}

type appState struct {
	status         *widget.Label
	logs           *widget.Entry
	repoRoot       string
	settings       daemonSettings
	settingsForm   *widget.Form
	labelEntry     *widget.Entry
	tokenEntry     *widget.Entry
	chatEntry      *widget.Entry
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err == nil {
		if _, err := os.Stat(filepath.Join(wd, "daemon")); err == nil {
			return wd
		}
		if parent := filepath.Dir(wd); parent != wd {
			if _, err := os.Stat(filepath.Join(parent, "daemon")); err == nil {
				return parent
			}
		}
	}
	return filepath.Join(".")
}

func loadSettings() daemonSettings {
	cfg := daemonSettings{
		Token:     os.Getenv("TG_TOKEN"),
		ChatID:    os.Getenv("TG_CHAT_ID"),
		HostLabel: os.Getenv("HOST_LABEL"),
	}
	if cfg.HostLabel == "" {
		cfg.HostLabel = "MyMac"
	}
	return cfg
}

func saveSettingsFile(cfg daemonSettings) error {
	path := filepath.Join(repoRoot(), ".env")
	content := fmt.Sprintf("TG_TOKEN=%s\nTG_CHAT_ID=%s\nHOST_LABEL=%s\n", cfg.Token, cfg.ChatID, cfg.HostLabel)
	return os.WriteFile(path, []byte(content), 0o600)
}

func logFilePath() string {
	root := repoRoot()
	return filepath.Join(root, "daemon", "logs", "autoipo.log")
}

func readLogFile() string {
	path := logFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "log file not created yet\n"
	}
	return string(data)
}

func daemonRunning() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("bash", "-lc", "launchctl print gui/$(id -u)/com.autoipo >/dev/null 2>&1 && echo running || echo stopped")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(string(out)) == "running", nil
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_Process -Filter \"Name='autoipo.exe'\").Count")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, err
		}
		count, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return false, nil
		}
		return count > 0, nil
	default:
		cmd := exec.Command("bash", "-lc", "pgrep -af autoipo >/dev/null 2>&1 && echo running || echo stopped")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(string(out)) == "running", nil
	}
}

func startDaemon(cfg daemonSettings) error {
	root := repoRoot()
	cmd := exec.Command("bash", "-lc", fmt.Sprintf("cd %q && TG_TOKEN=%q TG_CHAT_ID=%q HOST_LABEL=%q ./deploy.sh", root, cfg.Token, cfg.ChatID, cfg.HostLabel))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("start failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func stopDaemon() error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("bash", "-lc", "launchctl bootout gui/$(id -u) /Users/$(whoami)/Library/LaunchAgents/com.autoipo.plist 2>/dev/null || true; launchctl unload -w /Users/$(whoami)/Library/LaunchAgents/com.autoipo.plist 2>/dev/null || true; pkill -f \"/Users/$(whoami)/Desktop/autoipobot/daemon/autoipo\" 2>/dev/null || true")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("stop failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "Stop-Process -Name autoipo -Force -ErrorAction SilentlyContinue")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("stop failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		cmd := exec.Command("bash", "-lc", "pkill -f autoipo 2>/dev/null || true")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("stop failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

func refreshUiState(state *appState) {
	running, err := daemonRunning()
	if err != nil {
		state.setStatus("Unknown")
		state.appendLog("status check failed: " + err.Error())
		return
	}
	if running {
		state.setStatus("Running")
	} else {
		state.setStatus("Stopped")
	}
	state.logs.SetText(readLogFile())
}

func newAppState() *appState {
	state := &appState{
		repoRoot: repoRoot(),
		settings: loadSettings(),
		status:   widget.NewLabel("Status: Ready"),
		logs:     widget.NewMultiLineEntry(),
	}
	state.logs.SetText(readLogFile())
	state.logs.Disable()
	return state
}

func (s *appState) setStatus(text string) {
	s.status.SetText("Status: " + text)
}

func (s *appState) appendLog(msg string) {
	text := s.logs.Text
	stamp := time.Now().Format(time.RFC3339)
	if text == "" {
		text = stamp + " " + msg
	} else {
		text = text + "\n" + stamp + " " + msg
	}
	s.logs.SetText(text)
}

func newStatusPanel(state *appState) fyne.CanvasObject {
	card := widget.NewCard("Overview", "",
		container.New(layout.NewVBoxLayout(),
			widget.NewLabel("Host: "+state.settings.HostLabel),
			widget.NewLabel("Daemon: autoipo"),
			widget.NewLabel("Mode: Telegram polling"),
			widget.NewLabel("Auto-start: enabled"),
		),
	)

	controls := container.NewHBox(
		widget.NewButton("Start", func() {
			if err := startDaemon(state.settings); err != nil {
				state.appendLog(err.Error())
				state.setStatus("Error")
				return
			}
			refreshUiState(state)
		}),
		widget.NewButton("Stop", func() {
			if err := stopDaemon(); err != nil {
				state.appendLog(err.Error())
				state.setStatus("Error")
				return
			}
			refreshUiState(state)
		}),
		widget.NewButton("Restart", func() {
			if err := stopDaemon(); err != nil {
				state.appendLog(err.Error())
				state.setStatus("Error")
				return
			}
			time.Sleep(300 * time.Millisecond)
			if err := startDaemon(state.settings); err != nil {
				state.appendLog(err.Error())
				state.setStatus("Error")
				return
			}
			refreshUiState(state)
		}),
	)

	statusTop := container.NewPadded(container.NewBorder(
		nil,
		nil,
		nil,
		nil,
		container.New(layout.NewVBoxLayout(),
			widget.NewLabel("autoipo"),
			state.status,
			controls,
		),
	))

	return container.New(layout.NewVBoxLayout(),
		statusTop,
		card,
	)
}

func newSettingsPanel(state *appState) fyne.CanvasObject {
	state.labelEntry = widget.NewEntry()
	state.labelEntry.SetText(state.settings.HostLabel)
	state.labelEntry.SetPlaceHolder("MyMac")

	state.tokenEntry = widget.NewPasswordEntry()
	state.tokenEntry.SetText(state.settings.Token)
	state.tokenEntry.SetPlaceHolder("Telegram bot token")

	state.chatEntry = widget.NewEntry()
	state.chatEntry.SetText(state.settings.ChatID)
	state.chatEntry.SetPlaceHolder("123456789")

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Host label", Widget: state.labelEntry},
			{Text: "Bot token", Widget: state.tokenEntry},
			{Text: "Chat ID", Widget: state.chatEntry},
		},
		OnSubmit: func() {
			state.settings.HostLabel = state.labelEntry.Text
			state.settings.Token = state.tokenEntry.Text
			state.settings.ChatID = state.chatEntry.Text
			if err := saveSettingsFile(state.settings); err != nil {
				state.appendLog("save settings failed: " + err.Error())
				return
			}
			state.appendLog("settings saved")
		},
		OnCancel: func() {
			state.labelEntry.SetText(state.settings.HostLabel)
			state.tokenEntry.SetText(state.settings.Token)
			state.chatEntry.SetText(state.settings.ChatID)
		},
	}

	form.SubmitText = "Save"
	form.CancelText = "Reset"
	return container.NewPadded(form)
}

func newLogsPanel(state *appState) fyne.CanvasObject {
	return container.NewPadded(state.logs)
}

func main() {
	a := app.New()
	w := a.NewWindow("autoipo")
	state := newAppState()
	refreshUiState(state)

	statusPage := newStatusPanel(state)
	settingsPage := newSettingsPanel(state)
	logsPage := newLogsPanel(state)

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", statusPage),
		container.NewTabItem("Settings", settingsPage),
		container.NewTabItem("Logs", logsPage),
	)
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(900, 620))
	w.SetMaster()
	w.ShowAndRun()
}
