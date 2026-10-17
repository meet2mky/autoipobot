package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	Token               string
	ChatID              string
	HostLabel           string
	StartOnLogin        bool
	ChromeHelperEnabled bool
}

type userRecord struct {
	Name     string `json:"name"`
	PAN      string `json:"pan"`
	BankName string `json:"bank_name"`
	UserID   string `json:"user_id"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
}

type userStore map[string]userRecord

var supportedBankOptions = []string{"HDFC", "Axis"}

type appState struct {
	status          *widget.Label
	logs            *widget.Entry
	repoRoot        string
	settings        daemonSettings
	users           userStore
	usersList       *fyne.Container
	labelEntry      *widget.Entry
	tokenEntry      *widget.Entry
	chatEntry       *widget.Entry
	chromeHelperToggle *widget.Check
	startBtn        *widget.Button
	stopBtn         *widget.Button
	restartBtn      *widget.Button
	refreshLogsBtn  *widget.Button
	olderLogsBtn    *widget.Button
	newerLogsBtn    *widget.Button
	launchOnLoginBtn *widget.Button
	window          fyne.Window
	logViewStart    int
	logPageSize     int
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
		Token:               os.Getenv("TG_TOKEN"),
		ChatID:              os.Getenv("TG_CHAT_ID"),
		HostLabel:           os.Getenv("HOST_LABEL"),
		StartOnLogin:        true,
		ChromeHelperEnabled: false,
	}
	if v := strings.TrimSpace(os.Getenv("START_ON_LOGIN")); v != "" {
		cfg.StartOnLogin = strings.EqualFold(v, "true") || v == "1"
	}
	if v := strings.TrimSpace(os.Getenv("CHROME_HELPER_ENABLED")); v != "" {
		cfg.ChromeHelperEnabled = strings.EqualFold(v, "true") || v == "1"
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
				case "CHROME_HELPER_ENABLED":
					if value != "" { cfg.ChromeHelperEnabled = strings.EqualFold(value, "true") || value == "1" }
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
	content := fmt.Sprintf("TG_TOKEN=%s\nTG_CHAT_ID=%s\nHOST_LABEL=%s\nSTART_ON_LOGIN=%t\nCHROME_HELPER_ENABLED=%t\n", cfg.Token, cfg.ChatID, cfg.HostLabel, cfg.StartOnLogin, cfg.ChromeHelperEnabled)
	return os.WriteFile(path, []byte(content), 0o600)
}

func userStorePath() string {
	return filepath.Join(repoRoot(), ".users")
}

func decodeUserStore(data []byte) (userStore, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return userStore{}, nil
	}
	var raw map[string]userRecord
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	cleaned := userStore{}
	for pan, user := range raw {
		key := strings.ToUpper(strings.TrimSpace(pan))
		if key == "" {
			continue
		}
		user.PAN = strings.ToUpper(strings.TrimSpace(user.PAN))
		if user.PAN == "" {
			user.PAN = key
		}
		cleaned[key] = user
	}
	return cleaned, nil
}

func loadUserStore() userStore {
	path := userStorePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.WriteFile(path, []byte("{}"), 0o600)
			return userStore{}
		}
		return userStore{}
	}
	store, err := decodeUserStore(data)
	if err != nil {
		return userStore{}
	}
	return store
}

func saveUserStore(users userStore) error {
	if users == nil {
		users = userStore{}
	}
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(userStorePath(), append(data, '\n'), 0o600)
}

func missingRequiredSettings(cfg daemonSettings) []string {
	var missing []string
	if strings.TrimSpace(cfg.Token) == "" {
		missing = append(missing, "TG_TOKEN")
	}
	if strings.TrimSpace(cfg.ChatID) == "" {
		missing = append(missing, "TG_CHAT_ID")
	}
	if strings.TrimSpace(cfg.HostLabel) == "" {
		missing = append(missing, "HOST_LABEL")
	}
	return missing
}

func showMissingRequiredSettingsDialog(state *appState) {
	if state == nil || state.window == nil {
		return
	}
	missing := missingRequiredSettings(state.settings)
	if len(missing) == 0 {
		return
	}
	if state.settings.HostLabel == "" {
		state.settings.HostLabel = "MyMac"
	}

	tokenEntry := widget.NewPasswordEntry()
	tokenEntry.SetText(state.settings.Token)
	tokenEntry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("bot token is required")
		}
		return nil
	}
	chatEntry := widget.NewEntry()
	chatEntry.SetText(state.settings.ChatID)
	chatEntry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("chat ID is required")
		}
		return nil
	}
	hostEntry := widget.NewEntry()
	hostEntry.SetText(state.settings.HostLabel)
	hostEntry.SetPlaceHolder("MyMac")
	hostEntry.Validator = func(string) error { return nil }

	dialog.ShowForm("Required before startup",
		"Save & start",
		"Exit",
		[]*widget.FormItem{
			{Text: "Bot token", Widget: tokenEntry},
			{Text: "Chat ID", Widget: chatEntry},
			{Text: "Host label", Widget: hostEntry},
		},
		func(submit bool) {
			if !submit {
				state.window.Close()
				return
			}
			state.settings.Token = strings.TrimSpace(tokenEntry.Text)
			state.settings.ChatID = strings.TrimSpace(chatEntry.Text)
			state.settings.HostLabel = strings.TrimSpace(hostEntry.Text)
			if state.settings.HostLabel == "" {
				state.settings.HostLabel = "MyMac"
			}
			if state.labelEntry != nil {
				state.labelEntry.SetText(state.settings.HostLabel)
			}
			if state.tokenEntry != nil {
				state.tokenEntry.SetText(state.settings.Token)
			}
			if state.chatEntry != nil {
				state.chatEntry.SetText(state.settings.ChatID)
			}
			if len(missingRequiredSettings(state.settings)) > 0 {
				showMissingRequiredSettingsDialog(state)
				return
			}
			if err := saveSettingsFile(state.settings); err != nil {
				state.appendLog("save settings failed: " + err.Error())
				return
			}
			state.setStatus("Ready")
			refreshUiState(state)
		},
		state.window,
	)
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
		repoRoot:     repoRoot(),
		settings:     loadSettings(),
		users:        loadUserStore(),
		status:       widget.NewLabel("Status: Ready"),
		logs:         widget.NewMultiLineEntry(),
		logPageSize:  100,
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
		if len(missingRequiredSettings(state.settings)) > 0 {
			showMissingRequiredSettingsDialog(state)
			return
		}
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
	state.tokenEntry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("bot token is required")
		}
		return nil
	}

	state.chatEntry = widget.NewEntry()
	state.chatEntry.SetText(state.settings.ChatID)
	state.chatEntry.SetPlaceHolder("123456789")
	state.chatEntry.Validator = func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("chat ID is required")
		}
		return nil
	}

	state.chromeHelperToggle = widget.NewCheck("Enable Test Chrome Helper tab", func(checked bool) {
		state.settings.ChromeHelperEnabled = checked
		if err := saveSettingsFile(state.settings); err != nil {
			state.appendLog("save settings failed: " + err.Error())
			return
		}
		state.appendLog("chrome helper toggle saved")
	})
	state.chromeHelperToggle.Checked = state.settings.ChromeHelperEnabled

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Host label", Widget: state.labelEntry},
			{Text: "Bot token", Widget: state.tokenEntry},
			{Text: "Chat ID", Widget: state.chatEntry},
			{Text: "Chrome helper", Widget: state.chromeHelperToggle},
		},
		OnSubmit: func() {
			state.settings.HostLabel = strings.TrimSpace(state.labelEntry.Text)
			state.settings.Token = strings.TrimSpace(state.tokenEntry.Text)
			state.settings.ChatID = strings.TrimSpace(state.chatEntry.Text)
			state.settings.ChromeHelperEnabled = state.chromeHelperToggle.Checked
			if state.settings.HostLabel == "" {
				state.settings.HostLabel = "MyMac"
			}
			if err := saveSettingsFile(state.settings); err != nil {
				state.appendLog("save settings failed: " + err.Error())
				return
			}
			state.appendLog("settings saved")
			dialog.NewConfirm("Restart daemon?", "Your updated settings are saved. Do you want to restart the daemon now so the new values take effect immediately?", func(ok bool) {
				if !ok {
					return
				}
				if err := stopDaemon(); err != nil {
					state.appendLog("stop daemon after save failed: " + err.Error())
					state.setStatus("Error")
					return
				}
				time.Sleep(300 * time.Millisecond)
				if err := startDaemon(state.settings); err != nil {
					state.appendLog("start daemon after save failed: " + err.Error())
					state.setStatus("Error")
					return
				}
				if err := relaunchCurrentApp(); err != nil {
					state.appendLog("relaunch app after settings save failed: " + err.Error())
					state.setStatus("Error")
					return
				}
			}, state.window).Show()
		},
		OnCancel: func() {
			state.labelEntry.SetText("")
			state.tokenEntry.SetText("")
			state.chatEntry.SetText("")
			state.chromeHelperToggle.SetChecked(false)
		},
	}

	form.SubmitText = "Save"
	form.CancelText = "Reset"

	card := widget.NewCard("Daemon settings", "Configure the bot and host identity", form)
	card.Subtitle = "Secure settings"
	return container.NewPadded(card)
}

func refreshUserTiles(state *appState) {
	if state == nil || state.usersList == nil {
		return
	}
	state.usersList.Objects = nil
	keys := make([]string, 0, len(state.users))
	for pan := range state.users {
		keys = append(keys, pan)
	}
	sort.Strings(keys)
	for _, pan := range keys {
		user := state.users[pan]
		nameLabel := widget.NewLabel(user.Name)
		nameLabel.TextStyle = fyne.TextStyle{Bold: true}
		panLabel := widget.NewLabel("PAN: " + user.PAN)
		bankLabel := widget.NewLabel("Bank: " + user.BankName)
		userIDLabel := widget.NewLabel("User ID: " + user.UserID)
		passwordLabel := widget.NewLabel("Password: " + maskPassword(user.Password))
		emailLabel := widget.NewLabel(user.Email)
		phoneLabel := widget.NewLabel(user.Phone)
		editBtn := widget.NewButton("Edit", func(p string) func() {
			return func() { showEditUserDialog(state, p) }
		}(pan))
		deleteBtn := widget.NewButton("Delete", func(p string) func() {
			return func() {
				dialog.NewConfirm("Delete user", "Remove this user from the local registry?", func(ok bool) {
					if !ok {
						return
					}
					delete(state.users, p)
					if err := saveUserStore(state.users); err != nil {
						state.appendLog("save users failed: " + err.Error())
						return
					}
					refreshUserTiles(state)
				}, state.window).Show()
			}
		}(pan))
		card := widget.NewCard(user.Name, user.PAN, container.NewVBox(
			nameLabel,
			panLabel,
			bankLabel,
			userIDLabel,
			passwordLabel,
			emailLabel,
			phoneLabel,
			container.NewHBox(editBtn, deleteBtn),
		))
		state.usersList.Add(card)
	}
	state.usersList.Refresh()
}

func maskPassword(password string) string {
	if strings.TrimSpace(password) == "" {
		return "Not set"
	}
	return strings.Repeat("•", max(1, len(password)))
}

func normalizeBankName(value string) string {
	trimmed := strings.TrimSpace(value)
	for _, bank := range supportedBankOptions {
		if strings.EqualFold(trimmed, bank) {
			return bank
		}
	}
	return ""
}

func showUserDialog(state *appState, existing userRecord, existingPAN string, title string, saveLabel string) {
	nameEntry := widget.NewEntry()
	panEntry := widget.NewEntry()
	userIDEntry := widget.NewEntry()
	passwordEntry := widget.NewPasswordEntry()
	emailEntry := widget.NewEntry()
	phoneEntry := widget.NewEntry()
	bankSelect := widget.NewSelect(supportedBankOptions, func(string) {})
	nameEntry.SetText(existing.Name)
	panEntry.SetText(existing.PAN)
	userIDEntry.SetText(existing.UserID)
	passwordEntry.SetText(existing.Password)
	emailEntry.SetText(existing.Email)
	phoneEntry.SetText(existing.Phone)
	if existing.BankName != "" {
		bankSelect.SetSelected(existing.BankName)
	} else if len(supportedBankOptions) > 0 {
		bankSelect.SetSelected(supportedBankOptions[0])
	}
	panEntry.SetPlaceHolder("ABCDE1234F")
	dialog.ShowForm(title,
		saveLabel,
		"Cancel",
		[]*widget.FormItem{
			{Text: "Name", Widget: nameEntry},
			{Text: "PAN", Widget: panEntry},
			{Text: "Bank name", Widget: bankSelect},
			{Text: "User ID", Widget: userIDEntry},
			{Text: "Password", Widget: passwordEntry},
			{Text: "Email", Widget: emailEntry},
			{Text: "Phone", Widget: phoneEntry},
		},
		func(submit bool) {
			if !submit {
				return
			}
			user := userRecord{
				Name:     strings.TrimSpace(nameEntry.Text),
				PAN:      strings.ToUpper(strings.TrimSpace(panEntry.Text)),
				BankName: normalizeBankName(bankSelect.Selected),
				UserID:   strings.TrimSpace(userIDEntry.Text),
				Password: strings.TrimSpace(passwordEntry.Text),
				Email:    strings.TrimSpace(emailEntry.Text),
				Phone:    strings.TrimSpace(phoneEntry.Text),
			}
			if user.Name == "" || user.PAN == "" || user.BankName == "" || user.UserID == "" || user.Password == "" {
				dialog.ShowError(fmt.Errorf("name, PAN, bank name, user ID, and password are required"), state.window)
				return
			}
			if user.PAN != existingPAN {
				if _, exists := state.users[user.PAN]; exists {
					dialog.ShowError(fmt.Errorf("PAN %s already exists", user.PAN), state.window)
					return
				}
			}
			if existingPAN != "" {
				delete(state.users, existingPAN)
			}
			state.users[user.PAN] = user
			if err := saveUserStore(state.users); err != nil {
				state.appendLog("save users failed: " + err.Error())
				delete(state.users, user.PAN)
				if existingPAN != "" {
					state.users[existingPAN] = existing
				}
				return
			}
			refreshUserTiles(state)
		},
		state.window,
	)
}

func showAddUserDialog(state *appState) {
	showUserDialog(state, userRecord{}, "", "Add user", "Save user")
}

func showEditUserDialog(state *appState, pan string) {
	if user, ok := state.users[pan]; ok {
		showUserDialog(state, user, pan, "Edit user", "Update user")
	}
}

func newUsersPanel(state *appState) fyne.CanvasObject {
	state.usersList = container.NewGridWrap(fyne.NewSize(240, 180))
	refreshUserTiles(state)
	addUserBtn := widget.NewButton("Add user", func() {
		showAddUserDialog(state)
	})
	addUserBtn.Importance = widget.HighImportance
	header := container.NewBorder(nil, nil, nil, container.NewHBox(addUserBtn), widget.NewLabel("Users"))
	return container.NewBorder(header, nil, nil, nil, container.NewScroll(state.usersList))
}

func newTestChromeHelperPanel(state *appState) fyne.CanvasObject {
	urlEntry := widget.NewEntry()
	urlEntry.SetText("https://now.hdfc.bank.in/retail-app/")
	urlEntry.SetPlaceHolder("https://now.hdfc.bank.in/retail-app/")
	userIDEntry := widget.NewEntry()
	userIDEntry.SetText("demo-user")
	userIDEntry.SetPlaceHolder("demo-user")
	passwordEntry := widget.NewPasswordEntry()
	passwordEntry.SetText("DemoPass123!")
	passwordEntry.SetPlaceHolder("DemoPass123!")
	selectorEntry := widget.NewEntry()
	selectorEntry.SetText("#username")
	selectorEntry.SetPlaceHolder("#username")
	statusLabel := widget.NewLabel("Test chain status: idle")
	statusLabel.Wrapping = fyne.TextWrapWord
	statusError := widget.NewEntry()
	statusError.MultiLine = true
	statusError.Disable()
	statusError.SetPlaceHolder("No helper error yet")
	statusError.Wrapping = fyne.TextWrapWord

	type chainStep struct {
		name    string
		status  string
		require []string
	}
	steps := []chainStep{
		{name: "Open tab", status: "pending", require: []string{"url"}},
		{name: "Confirm loaded", status: "pending", require: []string{"url"}},
		{name: "Fill input", status: "pending", require: []string{"url", "user_id", "password"}},
		{name: "Right click", status: "pending", require: []string{"url", "selector"}},
	}

	var helper *ChromeHelper
	var currentStep = 0
	var stopAt = 3

	setStepStatus := func(index int, state string) {
		steps[index].status = state
	}

	setStatusText := func(msg string) {
		fyne.Do(func() {
			statusLabel.SetText(msg)
			if strings.HasPrefix(msg, "Step failed: ") {
				statusError.SetText(strings.TrimPrefix(msg, "Step failed: "))
				return
			}
			statusError.SetText("")
		})
	}

	getMissingRequirements := func(index int) []string {
		var missing []string
		for _, req := range steps[index].require {
			switch req {
			case "url":
				if strings.TrimSpace(urlEntry.Text) == "" {
					missing = append(missing, "Page URL")
				}
			case "user_id":
				if strings.TrimSpace(userIDEntry.Text) == "" {
					missing = append(missing, "User ID")
				}
			case "password":
				if strings.TrimSpace(passwordEntry.Text) == "" {
					missing = append(missing, "Password")
				}
			case "selector":
				if strings.TrimSpace(selectorEntry.Text) == "" {
					missing = append(missing, "Right-click selector")
				}
			}
		}
		return missing
	}

	buildStepCard := func(index int) fyne.CanvasObject {
		step := steps[index]
		bg := canvas.NewRectangle(color.NRGBA{R: 32, G: 38, B: 48, A: 255})
		switch step.status {
		case "pass":
			bg.FillColor = color.NRGBA{R: 22, G: 101, B: 52, A: 255}
		case "fail":
			bg.FillColor = color.NRGBA{R: 120, G: 28, B: 28, A: 255}
		case "waiting":
			bg.FillColor = color.NRGBA{R: 104, G: 80, B: 18, A: 255}
		}
		label := widget.NewLabel(step.name)
		label.TextStyle = fyne.TextStyle{Bold: true}
		status := widget.NewLabel(step.status)
		if step.status == "pass" {
			status.SetText("Passed")
		}
		if step.status == "fail" {
			status.SetText("Failed")
		}
		if step.status == "waiting" {
			status.SetText("Waiting for input")
		}
		if step.status == "pending" {
			status.SetText("Pending")
		}
		details := widget.NewLabel("")
		if len(getMissingRequirements(index)) > 0 && step.status != "pass" && step.status != "fail" {
			details.SetText("Missing: " + strings.Join(getMissingRequirements(index), ", "))
		}
		card := container.New(layout.NewGridLayoutWithColumns(2),
			container.NewPadded(container.NewVBox(label, details)),
			container.NewPadded(status),
		)
		return container.NewBorder(nil, nil, nil, nil, container.NewStack(bg, card))
	}

	statusPanel := container.NewVBox()

	renderChain := func() {
		fyne.Do(func() {
			items := make([]fyne.CanvasObject, 0, len(steps))
			for i := range steps {
				items = append(items, buildStepCard(i))
			}
			statusPanel.Objects = items
			statusPanel.Refresh()
		})
	}

	renderChain()

	runStep := func(index int) error {
		if helper == nil {
			newHelper, err := NewChromeHelper()
			if err != nil {
				return err
			}
			helper = newHelper
		}
		switch index {
		case 0:
			return helper.OpenTab(normalizeChromeURL(urlEntry.Text))
		case 1:
			return helper.ConfirmLoaded(20 * time.Second)
		case 2:
			return helper.FillKnownCredentials("hdfc", userIDEntry.Text, passwordEntry.Text)
		case 3:
			return helper.RightClick(selectorEntry.Text)
		default:
			return nil
		}
	}

	continueBtn := widget.NewButton("Continue", func() {
		for currentStep <= stopAt {
			if currentStep >= len(steps) {
				setStatusText("Test chain completed")
				return
			}
			missing := getMissingRequirements(currentStep)
			if len(missing) > 0 {
				setStepStatus(currentStep, "waiting")
				setStatusText("Step waiting for input: " + strings.Join(missing, ", "))
				renderChain()
				return
			}
			setStepStatus(currentStep, "running")
			renderChain()
			if err := runStep(currentStep); err != nil {
				setStepStatus(currentStep, "fail")
				setStatusText("Step failed: " + err.Error())
				renderChain()
				return
			}
			setStepStatus(currentStep, "pass")
			setStatusText("Step passed: " + steps[currentStep].name)
			currentStep++
			if currentStep > stopAt {
				setStatusText("Selected chain completed successfully")
				break
			}
			continue
		}
		renderChain()
	})
	continueBtn.Importance = widget.HighImportance

	resetBtn := widget.NewButton("Reset chain", func() {
		for i := range steps {
			steps[i].status = "pending"
		}
		currentStep = 0
		if helper != nil {
			helper.Close()
			helper = nil
		}
		setStatusText("Test chain reset")
		renderChain()
	})

	stopSelect := widget.NewSelect([]string{"Open tab", "Confirm loaded", "Fill input", "Right click"}, func(value string) {
		switch value {
		case "Open tab":
			stopAt = 0
		case "Confirm loaded":
			stopAt = 1
		case "Fill input":
			stopAt = 2
		case "Right click":
			stopAt = 3
		}
	})
	stopSelect.SetSelected("Right click")

	fields := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Page URL", Widget: urlEntry},
			{Text: "User ID", Widget: userIDEntry},
			{Text: "Password", Widget: passwordEntry},
			{Text: "Right-click selector", Widget: selectorEntry},
		},
	}

	controls := container.NewGridWithColumns(2, continueBtn, resetBtn)
	content := container.NewBorder(
		nil,
		nil,
		nil,
		nil,
		container.NewVBox(
			widget.NewLabel("Linear browser test chain"),
			container.NewHBox(widget.NewLabel("Stop after:"), stopSelect),
			fields,
			controls,
			statusLabel,
			statusError,
			statusPanel,
		),
	)
	return container.NewPadded(content)
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
	if len(missingRequiredSettings(state.settings)) > 0 {
		showMissingRequiredSettingsDialog(state)
	}
	refreshUiState(state)

	statusPage := newStatusPanel(state)
	settingsPage := newSettingsPanel(state)
	usersPage := newUsersPanel(state)
	logsPage := newLogsPanel(state)
	var testChromePage fyne.CanvasObject
	if state.settings.ChromeHelperEnabled {
		testChromePage = newTestChromeHelperPanel(state)
	}

	go func() {
		for range time.Tick(3 * time.Second) {
			refreshUiState(state)
		}
	}()

	head := brandHeader("autoipo")
	statusPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(statusPage))
	settingsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(settingsPage))
	usersPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(usersPage))
	logsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(logsPage))
	if testChromePage != nil {
		testChromePage = container.NewBorder(head, nil, nil, nil, container.NewPadded(testChromePage))
	}

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", statusPage),
		container.NewTabItem("Settings", settingsPage),
		container.NewTabItem("Users", usersPage),
	)
	if testChromePage != nil {
		tabs.Append(container.NewTabItem("Test Chrome Helper", testChromePage))
	}
	tabs.Append(container.NewTabItem("Logs", logsPage))
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.SetTabLocation(container.TabLocationLeading)
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(980, 660))
	w.SetMaster()
	w.ShowAndRun()
}
