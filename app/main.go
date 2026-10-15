package main

import (
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type daemonSettings struct {
	Token        string
	ChatID       string
	HostLabel    string
	StartOnLogin bool
}

type appState struct {
	status         *widget.Label
	logs           *widget.Entry
	repoRoot       string
	settings       daemonSettings
	labelEntry     *widget.Entry
	tokenEntry     *widget.Entry
	chatEntry      *widget.Entry
	startBtn       *widget.Button
	stopBtn        *widget.Button
	restartBtn     *widget.Button
	refreshLogsBtn *widget.Button
	olderLogsBtn   *widget.Button
	newerLogsBtn   *widget.Button
	launchOnLoginBtn *widget.Button
	window         fyne.Window
	logViewStart   int
	logPageSize    int
}

type modernAppTheme struct{}

func (m *modernAppTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 10, G: 14, B: 24, A: 255}
	case theme.ColorNameButton:
		return color.NRGBA{R: 24, G: 35, B: 52, A: 255}
	case theme.ColorNameDisabled:
		return color.NRGBA{R: 92, G: 102, B: 118, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 228, G: 234, B: 244, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 99, G: 102, B: 241, A: 255}
	case theme.ColorNameHover:
		return color.NRGBA{R: 31, G: 41, B: 55, A: 255}
	case theme.ColorNameSelection:
		return color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	case theme.ColorNameOverlayBackground:
		return color.NRGBA{R: 15, G: 23, B: 42, A: 220}
	case theme.ColorNameError:
		return color.NRGBA{R: 239, G: 68, B: 68, A: 255}
	case theme.ColorNameFocus:
		return color.NRGBA{R: 45, G: 212, B: 191, A: 255}
	default:
		return theme.DefaultTheme().Color(name, variant)
	}
}

func (m *modernAppTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (m *modernAppTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (m *modernAppTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}

var appLockFile *os.File

func relaunchCurrentApp() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find app binary: %w", err)
	}
	if appLockFile != nil {
		if err := releaseSingleInstanceLock(appLockFile); err != nil {
			return fmt.Errorf("release app lock: %w", err)
		}
		appLockFile = nil
	}
	cmd := exec.Command(execPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start replacement app: %w", err)
	}
	os.Exit(0)
	return nil
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

func appLaunchOnLoginLabel() string {
	return "com.autoipo.app"
}

func appLaunchOnLoginPlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(repoRoot(), "deploy", appLaunchOnLoginLabel()+".plist")
	}
	return filepath.Join(home, "Library", "LaunchAgents", appLaunchOnLoginLabel()+".plist")
}

func appLaunchAgentPlistContent(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Interactive</string>
</dict>
</plist>
`, appLaunchOnLoginLabel(), execPath)
}

func installAppAutoStart() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find app binary: %w", err)
	}
	plistPath := appLaunchOnLoginPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create launch agents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, []byte(appLaunchAgentPlistContent(execPath)), 0o600); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}
	label := appLaunchOnLoginLabel()
	cmd := exec.Command("bash", "-lc", fmt.Sprintf("launchctl bootout gui/$(id -u) %q 2>/dev/null || true; launchctl unload -w %q 2>/dev/null || true; launchctl remove gui/$(id -u)/%s 2>/dev/null || true; launchctl bootstrap gui/$(id -u) %q; launchctl kickstart -k gui/$(id -u)/%s", plistPath, plistPath, label, plistPath, label))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("load launch agent: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallAppAutoStart() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	plistPath := appLaunchOnLoginPlistPath()
	label := appLaunchOnLoginLabel()
	cmd := exec.Command("bash", "-lc", fmt.Sprintf("launchctl bootout gui/$(id -u) %q 2>/dev/null || true; launchctl unload -w %q 2>/dev/null || true; launchctl remove gui/$(id -u)/%s 2>/dev/null || true; rm -f %q", plistPath, plistPath, label, plistPath))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("unload launch agent: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ensureAppAutoStartState(cfg daemonSettings) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("bash", "-lc", fmt.Sprintf("launchctl print gui/$(id -u)/%s >/dev/null 2>&1 && echo enabled || echo disabled", appLaunchOnLoginLabel()))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check launch agent: %w: %s", err, strings.TrimSpace(string(out)))
	}
	enabled := strings.TrimSpace(string(out)) == "enabled"
	if cfg.StartOnLogin && !enabled {
		return installAppAutoStart()
	}
	if !cfg.StartOnLogin && enabled {
		return uninstallAppAutoStart()
	}
	return nil
}

func loadSettings() daemonSettings {
	cfg := daemonSettings{
		Token:        os.Getenv("TG_TOKEN"),
		ChatID:       os.Getenv("TG_CHAT_ID"),
		HostLabel:    os.Getenv("HOST_LABEL"),
		StartOnLogin: true,
	}
	if v := strings.TrimSpace(os.Getenv("START_ON_LOGIN")); v != "" {
		cfg.StartOnLogin = strings.EqualFold(v, "true") || v == "1"
	}
	if cfg.HostLabel == "" || cfg.Token == "" || cfg.ChatID == "" {
		path := filepath.Join(repoRoot(), ".env")
		data, err := os.ReadFile(path)
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				switch key {
				case "TG_TOKEN":
					if cfg.Token == "" { cfg.Token = value }
				case "TG_CHAT_ID":
					if cfg.ChatID == "" { cfg.ChatID = value }
				case "HOST_LABEL":
					if cfg.HostLabel == "" { cfg.HostLabel = value }
				case "START_ON_LOGIN":
					if value != "" { cfg.StartOnLogin = strings.EqualFold(value, "true") || value == "1" }
				}
			}
		}
	}
	if cfg.HostLabel == "" {
		cfg.HostLabel = "MyMac"
	}
	return cfg
}

func saveSettingsFile(cfg daemonSettings) error {
	path := filepath.Join(repoRoot(), ".env")
	content := fmt.Sprintf("TG_TOKEN=%s\nTG_CHAT_ID=%s\nHOST_LABEL=%s\nSTART_ON_LOGIN=%t\n", cfg.Token, cfg.ChatID, cfg.HostLabel, cfg.StartOnLogin)
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
		cmd := exec.Command("bash", "-lc", "for label in com.autoipo com.autoipobot; do if launchctl print gui/$(id -u)/$label >/dev/null 2>&1; then echo running; exit 0; fi; done; echo stopped")
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
		cmd := exec.Command("bash", "-lc", `for label in com.autoipo com.autoipobot; do
  plist="$HOME/Library/LaunchAgents/${label}.plist"
  launchctl bootout gui/$(id -u) "$plist" 2>/dev/null || true
  launchctl unload -w "$plist" 2>/dev/null || true
  launchctl remove gui/$(id -u)/$label 2>/dev/null || true
  rm -f "$plist"
done
while read -r pid; do
  kill "$pid" 2>/dev/null || true
done < <(ps -axo pid,command | awk '($2 ~ /\/daemon\/autoipo$/ || $2 ~ /\/daemon\/autoipobot$/ || $2 == "autoipo" || $2 == "autoipobot") && $2 !~ /autoipoapp/ {print $1}')`)
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

func (s *appState) syncButtonState() {
	if s.startBtn == nil || s.stopBtn == nil || s.restartBtn == nil {
		return
	}
	running, err := daemonRunning()
	if err != nil {
		s.startBtn.Disable()
		s.stopBtn.Disable()
		s.restartBtn.Disable()
		return
	}
	s.startBtn.Enable()
	s.stopBtn.Enable()
	s.restartBtn.Enable()
	if running {
		s.startBtn.Disable()
		s.stopBtn.Enable()
		s.restartBtn.Enable()
	} else {
		s.startBtn.Enable()
		s.stopBtn.Disable()
		s.restartBtn.Enable()
	}
}

func logTailWindow(rawText string, pageSize int, start int) (string, int, bool, bool) {
	if pageSize <= 0 {
		pageSize = 100
	}
	text := strings.TrimRight(rawText, "\n")
	if text == "" {
		return "", 0, false, false
	}
	lines := strings.Split(text, "\n")
	total := len(lines)
	if total <= pageSize {
		return strings.Join(lines, "\n"), 0, false, false
	}
	if start <= 0 {
		start = total - pageSize
	}
	if start < 0 {
		start = 0
	}
	if start > total-pageSize {
		start = total - pageSize
	}
	end := min(start+pageSize, total)
	window := strings.Join(lines[start:end], "\n")
	canOlder := start > 0
	canNewer := end < total
	return window, start, canOlder, canNewer
}

func renderLogWindow(state *appState) {
	text := readLogFile()
	window, start, canOlder, canNewer := logTailWindow(text, state.logPageSize, state.logViewStart)
	state.logViewStart = start
	state.logs.SetText(window)
	if state.olderLogsBtn != nil {
		if canOlder {
			state.olderLogsBtn.Enable()
		} else {
			state.olderLogsBtn.Disable()
		}
	}
	if state.newerLogsBtn != nil {
		if canNewer {
			state.newerLogsBtn.Enable()
		} else {
			state.newerLogsBtn.Disable()
		}
	}
}

func refreshLogView(state *appState) {
	fyne.Do(func() {
		renderLogWindow(state)
	})
}

func refreshUiState(state *appState) {
	running, err := daemonRunning()
	fyne.Do(func() {
		if err != nil {
			state.setStatus("Unknown")
			state.appendLog("status check failed: " + err.Error())
			state.syncButtonState()
			return
		}
		if running {
			state.setStatus("Running")
		} else {
			state.setStatus("Stopped")
		}
		renderLogWindow(state)
		state.syncButtonState()
	})
}

func newAppState() *appState {
	state := &appState{
		repoRoot:    repoRoot(),
		settings:    loadSettings(),
		status:      widget.NewLabel("Status: Ready"),
		logs:        widget.NewMultiLineEntry(),
		logPageSize: 100,
		logViewStart: 0,
	}
	state.logs.SetText("")
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

func (s *appState) updateLaunchOnLoginButton() {
	if s.launchOnLoginBtn == nil {
		return
	}
	if s.settings.StartOnLogin {
		s.launchOnLoginBtn.SetText("Launch on login: ON")
		s.launchOnLoginBtn.Importance = widget.SuccessImportance
	} else {
		s.launchOnLoginBtn.SetText("Launch on login: OFF")
		s.launchOnLoginBtn.Importance = widget.DangerImportance
	}
	s.launchOnLoginBtn.Refresh()
}

func (s *appState) toggleAutoStart() {
	desired := !s.settings.StartOnLogin
	var err error
	if desired {
		err = installAppAutoStart()
	} else {
		err = uninstallAppAutoStart()
	}
	if err != nil {
		s.appendLog("launch setting update failed: " + err.Error())
		return
	}
	s.settings.StartOnLogin = desired
	if err := saveSettingsFile(s.settings); err != nil {
		s.appendLog("save settings failed: " + err.Error())
		return
	}
	s.updateLaunchOnLoginButton()
	if s.settings.StartOnLogin {
		s.appendLog("launch on login enabled")
	} else {
		s.appendLog("launch on login disabled")
	}
}

func badgeLabel(text string, accent color.Color, textColor color.Color) *fyne.Container {
	label := canvas.NewText(text, textColor)
	label.TextStyle = fyne.TextStyle{Bold: true}
	dot := canvas.NewRectangle(accent)
	dot.SetMinSize(fyne.NewSize(10, 10))
	row := container.NewHBox(dot, label)
	row.Objects[0].(*canvas.Rectangle).Refresh()
	return container.NewPadded(row)
}

func brandHeader(title string) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 15, G: 23, B: 42, A: 255})
	label := canvas.NewText(title, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	label.TextStyle = fyne.TextStyle{Bold: true}
	label.TextSize = 24
	content := container.NewVBox(label)
	content.Move(fyne.NewPos(18, 18))
	return container.NewStack(bg, container.NewPadded(content))
}

func statusPill(text string, bg color.Color, fg color.Color) *widget.Button {
	pill := widget.NewButton(text, nil)
	pill.Disable()
	pill.Importance = widget.LowImportance
	pill.SetText(text)
	pill.Alignment = widget.ButtonAlignCenter
	pill.Resize(fyne.NewSize(120, 28))
	if bg != nil {
		pill.CreateRenderer()
	}
	return pill
}

func styledActionButton(label string, accent color.Color, importance widget.Importance) *widget.Button {
	btn := widget.NewButton(label, nil)
	btn.Importance = importance
	btn.Importance = importance
	btn.SetText(label)
	btn.Resize(fyne.NewSize(120, 36))
	if accent != nil {
		btn.Importance = importance
	}
	return btn
}

func newStatusPanel(state *appState) fyne.CanvasObject {
	state.startBtn = widget.NewButton("Start", func() {
		if err := startDaemon(state.settings); err != nil {
			state.appendLog(err.Error())
			state.setStatus("Error")
			return
		}
		refreshUiState(state)
	})
	state.startBtn.Importance = widget.HighImportance
	state.startBtn.Resize(fyne.NewSize(120, 40))
	state.stopBtn = widget.NewButton("Stop", func() {
		dialog.NewConfirm("Stop daemon", "Are you sure you want to stop the autoipo daemon?", func(ok bool) {
			if !ok {
				return
			}
			if err := stopDaemon(); err != nil {
				state.appendLog(err.Error())
				state.setStatus("Error")
				return
			}
			refreshUiState(state)
		}, state.window).Show()
	})
	state.stopBtn.Importance = widget.DangerImportance
	state.stopBtn.Resize(fyne.NewSize(120, 40))
	state.restartBtn = widget.NewButton("Restart", func() {
		dialog.NewConfirm("Restart daemon", "Are you sure you want to restart the autoipo daemon?", func(ok bool) {
			if !ok {
				return
			}
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
			if err := relaunchCurrentApp(); err != nil {
				state.appendLog("relaunch failed: " + err.Error())
				state.setStatus("Error")
				return
			}
		}, state.window).Show()
	})
	state.restartBtn.Importance = widget.WarningImportance
	state.restartBtn.Resize(fyne.NewSize(120, 40))
	state.launchOnLoginBtn = widget.NewButton("Launch on login: ON", func() {
		state.toggleAutoStart()
	})
	state.updateLaunchOnLoginButton()
	state.syncButtonState()

	summary := widget.NewCard("Daemon overview", "",
		container.New(layout.NewVBoxLayout(),
			widget.NewLabel("Host: "+state.settings.HostLabel),
			widget.NewLabel("Daemon: autoipo"),
			widget.NewLabel("Mode: Telegram polling"),
		),
	)
	statusCard := widget.NewCard("System status", "",
		container.New(layout.NewVBoxLayout(),
			container.NewHBox(
				canvas.NewText("autoipo", color.NRGBA{R: 168, G: 85, B: 247, A: 255}),
				layout.NewSpacer(),
				widget.NewLabel("Live"),
			),
			state.status,
			container.NewHBox(state.startBtn, state.stopBtn, state.restartBtn),
			autoStartRow(state.launchOnLoginBtn),
		),
	)

	return container.New(layout.NewVBoxLayout(),
		container.NewPadded(statusCard),
		container.NewPadded(summary),
	)
}

func autoStartRow(btn *widget.Button) fyne.CanvasObject {
	label := widget.NewLabel("Startup mode")
	label.TextStyle = fyne.TextStyle{Bold: true}
	return container.NewBorder(nil, nil, nil, nil, container.NewHBox(
		label,
		layout.NewSpacer(),
		btn,
	))
}

func newSettingsPanel(state *appState) fyne.CanvasObject {
	state.labelEntry = widget.NewEntry()
	state.labelEntry.SetText(state.settings.HostLabel)
	state.labelEntry.SetPlaceHolder("MyMac")
	state.labelEntry.OnSubmitted = func(string) {
		state.settings.HostLabel = state.labelEntry.Text
		state.settings.Token = state.tokenEntry.Text
		state.settings.ChatID = state.chatEntry.Text
		_ = saveSettingsFile(state.settings)
	}

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

	card := widget.NewCard("Daemon settings", "Configure the bot and host identity", form)
	card.Subtitle = "Secure settings"
	return container.NewPadded(card)
}

func newLogsPanel(state *appState) fyne.CanvasObject {
	state.refreshLogsBtn = widget.NewButton("Refresh", func() {
		refreshLogView(state)
	})
	state.refreshLogsBtn.Importance = widget.HighImportance
	state.olderLogsBtn = widget.NewButton("Older", func() {
		if state.logViewStart <= 0 {
			return
		}
		state.logViewStart = max(0, state.logViewStart-state.logPageSize)
		refreshLogView(state)
	})
	state.newerLogsBtn = widget.NewButton("Newer", func() {
		text := readLogFile()
		if text == "" || strings.TrimRight(text, "\n") == "" {
			return
		}
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		total := len(lines)
		if total <= state.logPageSize {
			state.logViewStart = 0
			refreshLogView(state)
			return
		}
		state.logViewStart = min(total-state.logPageSize, state.logViewStart+state.logPageSize)
		refreshLogView(state)
	})
	state.olderLogsBtn.Disable()
	state.newerLogsBtn.Disable()

	header := container.NewBorder(
		nil,
		nil,
		nil,
		container.NewHBox(state.newerLogsBtn, state.olderLogsBtn, state.refreshLogsBtn),
		widget.NewLabel("Live daemon log"),
	)
	state.logs.Resize(fyne.NewSize(820, 420))
	refreshLogView(state)
	return container.NewBorder(
		header,
		nil,
		nil,
		nil,
		container.NewPadded(state.logs),
	)
}

func main() {
	lockFile, acquired, err := acquireSingleInstanceLock(instanceLockPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create app lock: %v\n", err)
		os.Exit(1)
	}
	if !acquired {
		fmt.Fprintln(os.Stderr, "autoipo is already running. Only one instance is allowed.")
		return
	}
	appLockFile = lockFile
	defer func() {
		if appLockFile != nil {
			if err := releaseSingleInstanceLock(appLockFile); err != nil {
				fmt.Fprintf(os.Stderr, "failed to release app lock: %v\n", err)
			}
			appLockFile = nil
		}
	}()

	a := app.New()
	a.Settings().SetTheme(&modernAppTheme{})
	w := a.NewWindow("autoipo")
	w.SetTitle("autoipo")
	state := newAppState()
	if err := ensureAppAutoStartState(state.settings); err != nil {
		state.appendLog("launch-on-login sync failed: " + err.Error())
	}
	state.window = w
	refreshUiState(state)

	statusPage := newStatusPanel(state)
	settingsPage := newSettingsPanel(state)
	logsPage := newLogsPanel(state)

	go func() {
		for range time.Tick(3 * time.Second) {
			refreshUiState(state)
		}
	}()

	head := brandHeader("autoipo")
	statusPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(statusPage))
	settingsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(settingsPage))
	logsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(logsPage))

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", statusPage),
		container.NewTabItem("Settings", settingsPage),
		container.NewTabItem("Logs", logsPage),
	)
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.SetTabLocation(container.TabLocationLeading)
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(980, 660))
	w.SetMaster()
	w.ShowAndRun()
}
