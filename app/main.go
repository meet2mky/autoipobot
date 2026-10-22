package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	extensionCLI    *extensionCLIController
	cliOutput       *widget.Entry
	cliOutputSearch string
	cliBuildStatus  *widget.Label
	cliOutputMu     sync.Mutex
	cliOutputLines  []string
	startBtn        *widget.Button
	stopBtn         *widget.Button
	restartBtn      *widget.Button
	refreshLogsBtn  *widget.Button
	olderLogsBtn    *widget.Button
	newerLogsBtn    *widget.Button
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
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		dir := filepath.Dir(exePath)
		if _, err := os.Stat(filepath.Join(dir, "daemon")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if _, err := os.Stat(filepath.Join(parent, "daemon")); err == nil {
			return parent
		}
	}
	return filepath.Join(".")
}

func loadSettings() daemonSettings {
	cfg := daemonSettings{
		Token:               os.Getenv("TG_TOKEN"),
		ChatID:              os.Getenv("TG_CHAT_ID"),
		HostLabel:           os.Getenv("HOST_LABEL"),
	}
	if cfg.HostLabel == "" || cfg.Token == "" || cfg.ChatID == "" {
		path := appDataPath("env")
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
	if err := os.MkdirAll(appDataDir(), 0o700); err != nil {
		return fmt.Errorf("create app data directory: %w", err)
	}
	path := appDataPath("env")
	content := fmt.Sprintf("TG_TOKEN=%s\nTG_CHAT_ID=%s\nHOST_LABEL=%s\n", cfg.Token, cfg.ChatID, cfg.HostLabel)
	return os.WriteFile(path, []byte(content), 0o600)
}

func appDataDir() string {
	return filepath.Join(repoRoot(), ".data")
}

func appDataPath(name string) string {
	return filepath.Join(appDataDir(), name)
}

func userStorePath() string {
	return appDataPath("users")
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
	if err := os.MkdirAll(appDataDir(), 0o700); err != nil {
		return userStore{}
	}
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
	if err := os.MkdirAll(appDataDir(), 0o700); err != nil {
		return fmt.Errorf("create app data directory: %w", err)
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
			go func() {
				if err := startDaemon(state.settings); err != nil {
					fyne.Do(func() {
					state.appendLog("initial daemon start failed: " + err.Error())
					})
				}
				refreshUiState(state)
			}()
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
	executable := filepath.Join(repoRoot(), "bin", "autoipo")
	cmd := exec.Command("pgrep", "-f", executable+"$")
	if err := cmd.Run(); err == nil {
		return true, nil
	}
	return false, nil
}

func ensureDaemonBinary() error {
	root := repoRoot()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create binary directory: %w", err)
	}
	binPath := filepath.Join(binDir, "autoipo")
	if _, err := os.Stat(binPath); err == nil {
		return nil
	}
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = filepath.Join(root, "daemon")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build daemon: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func startDaemon(cfg daemonSettings) error {
	if err := ensureDaemonBinary(); err != nil {
		return err
	}
	root := repoRoot()
	cmd := exec.Command(filepath.Join(root, "bin", "autoipo"))
	cmd.Dir = filepath.Join(root, "daemon")
	cmd.Env = append(os.Environ(),
		"TG_TOKEN="+cfg.Token,
		"TG_CHAT_ID="+cfg.ChatID,
		"HOST_LABEL="+cfg.HostLabel,
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func stopDaemon() error {
	_ = exec.Command("pkill", "-f", filepath.Join(repoRoot(), "bin", "autoipo")+"$").Run()
	return nil
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
	if start == 0 {
		start = total - pageSize
	} else if start < 0 {
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
	if state.logViewStart != -1 || start != 0 {
		state.logViewStart = start
	}
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
	btn.SetText(label)
	btn.Resize(fyne.NewSize(120, 36))
	return btn
}

func newStatusPanel(state *appState) fyne.CanvasObject {
	state.startBtn = widget.NewButton("Start", func() {
		if len(missingRequiredSettings(state.settings)) > 0 {
			showMissingRequiredSettingsDialog(state)
			return
		}
		go func() {
			fyne.Do(func() {
				state.setStatus("Starting...")
				state.startBtn.Disable()
			})
			if err := startDaemon(state.settings); err != nil {
				fyne.Do(func() {
					state.appendLog(err.Error())
					state.setStatus("Error")
					state.syncButtonState()
				})
				return
			}
			fyne.Do(func() {
				state.appendLog("daemon started")
				refreshUiState(state)
			})
		}()
	})
	state.startBtn.Importance = widget.HighImportance
	state.startBtn.Resize(fyne.NewSize(120, 40))
	state.stopBtn = widget.NewButton("Stop", func() {
		dialog.NewConfirm("Stop daemon", "Are you sure you want to stop the autoipo daemon?", func(ok bool) {
			if !ok {
				return
			}
			go func() {
				fyne.Do(func() {
					state.setStatus("Stopping...")
					state.stopBtn.Disable()
				})
				if err := stopDaemon(); err != nil {
					fyne.Do(func() {
						state.appendLog(err.Error())
						state.setStatus("Error")
						state.syncButtonState()
					})
					return
				}
				fyne.Do(func() {
					state.appendLog("daemon stopped")
					refreshUiState(state)
				})
			}()
		}, state.window).Show()
	})
	state.stopBtn.Importance = widget.DangerImportance
	state.stopBtn.Resize(fyne.NewSize(120, 40))
	state.restartBtn = widget.NewButton("Restart", func() {
		dialog.NewConfirm("Restart daemon", "Are you sure you want to restart the autoipo daemon?", func(ok bool) {
			if !ok {
				return
			}
			go func() {
				fyne.Do(func() {
					state.setStatus("Restarting...")
					state.startBtn.Disable()
					state.stopBtn.Disable()
					state.restartBtn.Disable()
				})
				if err := stopDaemon(); err != nil {
					fyne.Do(func() {
						state.appendLog(err.Error())
						state.setStatus("Error")
						state.syncButtonState()
					})
					return
				}
				time.Sleep(300 * time.Millisecond)
				if err := startDaemon(state.settings); err != nil {
					fyne.Do(func() {
						state.appendLog(err.Error())
						state.setStatus("Error")
						state.syncButtonState()
					})
					return
				}
				fyne.Do(func() {
					state.appendLog("daemon restarted successfully")
					refreshUiState(state)
				})
			}()
		}, state.window).Show()
	})
	state.restartBtn.Importance = widget.WarningImportance
	state.restartBtn.Resize(fyne.NewSize(120, 40))
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
		),
	)

	return container.New(layout.NewVBoxLayout(),
		container.NewPadded(statusCard),
		container.NewPadded(summary),
	)
}

func newSettingsPanel(state *appState) fyne.CanvasObject {
	state.labelEntry = widget.NewEntry()
	state.labelEntry.SetText(state.settings.HostLabel)
	state.labelEntry.SetPlaceHolder("MyMac")
	state.labelEntry.OnSubmitted = func(string) {
		val := strings.TrimSpace(state.labelEntry.Text)
		if val != "" {
			state.settings.HostLabel = val
			_ = saveSettingsFile(state.settings)
		}
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

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Host label", Widget: state.labelEntry},
			{Text: "Bot token", Widget: state.tokenEntry},
			{Text: "Chat ID", Widget: state.chatEntry},
		},
		OnSubmit: func() {
			state.settings.HostLabel = strings.TrimSpace(state.labelEntry.Text)
			state.settings.Token = strings.TrimSpace(state.tokenEntry.Text)
			state.settings.ChatID = strings.TrimSpace(state.chatEntry.Text)
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
				go func() {
					fyne.Do(func() {
						state.setStatus("Restarting...")
					})
					if err := stopDaemon(); err != nil {
						fyne.Do(func() {
							state.appendLog("stop daemon after save failed: " + err.Error())
							state.setStatus("Error")
						})
						return
					}
					time.Sleep(300 * time.Millisecond)
					if err := startDaemon(state.settings); err != nil {
						fyne.Do(func() {
							state.appendLog("start daemon after save failed: " + err.Error())
							state.setStatus("Error")
						})
						return
					}
					fyne.Do(func() {
						state.appendLog("daemon restarted with updated settings")
						refreshUiState(state)
					})
				}()
			}, state.window).Show()
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
	state.usersList = container.NewGridWrap(fyne.NewSize(270, 270))
	refreshUserTiles(state)
	addUserBtn := widget.NewButton("Add user", func() {
		showAddUserDialog(state)
	})
	addUserBtn.Importance = widget.HighImportance
	header := container.NewBorder(nil, nil, nil, container.NewHBox(addUserBtn), widget.NewLabel("Users"))
	return container.NewBorder(header, nil, nil, nil, container.NewScroll(state.usersList))
}

func newLogsPanel(state *appState) fyne.CanvasObject {
	state.refreshLogsBtn = widget.NewButton("Refresh", func() {
		refreshLogView(state)
	})
	state.refreshLogsBtn.Importance = widget.HighImportance
	state.olderLogsBtn = widget.NewButton("Older", func() {
		text := readLogFile()
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		total := len(lines)
		if total <= state.logPageSize {
			return
		}
		newStart := state.logViewStart - state.logPageSize
		if newStart <= 0 {
			state.logViewStart = -1
		} else {
			state.logViewStart = newStart
		}
		refreshLogView(state)
	})
	state.newerLogsBtn = widget.NewButton("Newer", func() {
		text := readLogFile()
		lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
		total := len(lines)
		if total <= state.logPageSize {
			return
		}
		curr := state.logViewStart
		if curr < 0 {
			curr = 0
		}
		newStart := curr + state.logPageSize
		if newStart >= total-state.logPageSize {
			state.logViewStart = total - state.logPageSize
		} else {
			state.logViewStart = newStart
		}
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
	state.extensionCLI = newExtensionCLIController(repoRoot())
	state.window = w
	if len(missingRequiredSettings(state.settings)) > 0 {
		showMissingRequiredSettingsDialog(state)
	}
	refreshUiState(state)

	statusPage := newStatusPanel(state)
	settingsPage := newSettingsPanel(state)
	usersPage := newUsersPanel(state)
	logsPage := newLogsPanel(state)
	commandsPage := newCommandsPanel(state)
	flowsPage := newFlowsPanel(state)
	go func() {
		if err := state.extensionCLI.Build(); err != nil {
			fyne.Do(func() {
				if state.cliBuildStatus != nil {
					state.cliBuildStatus.SetText("Build failed: " + err.Error())
				}
			})
			return
		}
		fyne.Do(func() {
			if state.cliBuildStatus != nil {
				state.cliBuildStatus.SetText("Bundled CLI is ready")
			}
		})
	}()

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
	commandsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(commandsPage))
	flowsPage = container.NewBorder(head, nil, nil, nil, container.NewPadded(flowsPage))

	tabs := container.NewAppTabs(
		container.NewTabItem("Status", statusPage),
		container.NewTabItem("Commands", commandsPage),
		container.NewTabItem("Flows", flowsPage),
		container.NewTabItem("Settings", settingsPage),
		container.NewTabItem("Users", usersPage),
	)
	tabs.Append(container.NewTabItem("Logs", logsPage))
	tabs.SetTabLocation(container.TabLocationLeading)
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(980, 660))
	w.SetFullScreen(true)
	w.SetMaster()
	w.ShowAndRun()
	state.extensionCLI.Close()
}
