package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	pollTimeout   = 1 * time.Second
	staleTimeout  = 5 * time.Minute
	resumeGap     = 3 * time.Minute
	offsetFileTag = "autoipo.offset"
)

type config struct {
	Token string
	Chat  int64
	Label string
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type message struct {
	MessageID int64 `json:"message_id"`
	Date      int64 `json:"date"`
	Chat      chat  `json:"chat"`
	Text      string `json:"text"`
}

type chat struct {
	ID int64 `json:"id"`
}

type tgGetUpdatesResponse struct {
	Ok          bool     `json:"ok"`
	Result      []update `json:"result"`
	Description string   `json:"description"`
}

type tgSendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

var (
	logMu   sync.Mutex
	logFile *os.File
)

func resolveLogPathFor(exePath string) (string, error) {
	exeDir := filepath.Dir(exePath)
	if filepath.Base(exeDir) == "bin" {
		exeDir = filepath.Join(filepath.Dir(exeDir), "daemon")
	}
	logDir := filepath.Join(exeDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(logDir, "autoipo.log"), nil
}

func resolveLogPath() string {
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		return filepath.Join(os.TempDir(), "autoipo.log")
	}
	logPath, err := resolveLogPathFor(exePath)
	if err != nil {
		return filepath.Join(os.TempDir(), "autoipo.log")
	}
	return logPath
}

func initLogger() {
	path := resolveLogPath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	logFile = file
}

func closeLogger() {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		_ = logFile.Close()
		logFile = nil
	}
}

func main() {
	initLogger()
	defer closeLogger()

	lockFile, acquired, err := acquireSingleInstanceLock(daemonLockPath())
	if err != nil {
		logLine("failed to acquire daemon lock: " + err.Error())
		os.Exit(1)
	}
	if !acquired {
		logLine("another autoipo daemon instance is already running; exiting")
		return
	}
	defer func() {
		if err := releaseSingleInstanceLock(lockFile); err != nil {
			logLine("failed to release daemon lock: " + err.Error())
		}
	}()

	cfg, err := loadConfig()
	if err != nil {
		logLine("loadConfig failed: " + err.Error())
		os.Exit(1)
	}
	logLine(fmt.Sprintf("starting autoipo for host=%s chat=%d", cfg.Label, cfg.Chat))

	offsetPath := filepath.Join(os.TempDir(), offsetFileTag)
	offset, err := loadOffset(offsetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logLine("failed to read offset: " + err.Error())
		os.Exit(1)
	}
	logLine(fmt.Sprintf("loaded offset=%d from %s", offset, offsetPath))

	asyncSendText(cfg.Token, cfg.Chat, fmt.Sprintf("%s BOOT", cfg.Label))

	lastPoll := time.Now()
	lastIdlePollLog := time.Time{}
	for {
		gap := time.Since(lastPoll)

		updates, err := getUpdates(cfg.Token, offset)
		if err != nil {
			logLine("getUpdates failed: " + err.Error())
			time.Sleep(5 * time.Second)
			continue
		}

		// Only send RESUMED after we have confirmed network connectivity (getUpdates succeeded)
		if gap > resumeGap {
			asyncSendText(cfg.Token, cfg.Chat, fmt.Sprintf("%s RESUMED after %s", cfg.Label, formatDuration(gap)))
		}
		lastPoll = time.Now()

		if len(updates) == 0 {
			if lastIdlePollLog.IsZero() || time.Since(lastIdlePollLog) >= 30*time.Second {
				logLine(fmt.Sprintf("telegram idle offset=%d", offset))
				lastIdlePollLog = time.Now()
			}
			continue
		}
		logLine(fmt.Sprintf("telegram poll offset=%d -> %d update(s)", offset, len(updates)))
		lastIdlePollLog = time.Time{}

		for _, item := range updates {
			logLine(fmt.Sprintf("received update_id=%d current_offset=%d", item.UpdateID, offset))
			if item.UpdateID < offset {
				continue
			}

			nextOffset := item.UpdateID + 1
			if err := persistOffset(offsetPath, nextOffset); err != nil {
				logLine("failed to persist offset: " + err.Error())
			} else {
				offset = nextOffset
			}

			if item.Message == nil {
				continue
			}
			if err := handleMessage(cfg, item.Message); err != nil {
				logLine("failed to handle message: " + err.Error())
			}
		}
	}
}

func findEnvPath() string {
	candidates := []string{
		filepath.Join(".data", "env"),
		filepath.Join("..", ".data", "env"),
	}
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		dir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(dir, ".data", "env"),
			filepath.Join(dir, "..", ".data", "env"),
		)
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func loadEnvFile(path string) map[string]string {
	res := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return res
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			res[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return res
}

func loadConfig() (*config, error) {
	token := strings.TrimSpace(os.Getenv("TG_TOKEN"))
	chatIDStr := strings.TrimSpace(os.Getenv("TG_CHAT_ID"))
	label := strings.TrimSpace(os.Getenv("HOST_LABEL"))

	if token == "" || chatIDStr == "" || label == "" {
		if envPath := findEnvPath(); envPath != "" {
			envMap := loadEnvFile(envPath)
			if token == "" {
				token = envMap["TG_TOKEN"]
			}
			if chatIDStr == "" {
				chatIDStr = envMap["TG_CHAT_ID"]
			}
			if label == "" {
				label = envMap["HOST_LABEL"]
			}
		}
	}

	if token == "" {
		return nil, errors.New("TG_TOKEN is required")
	}
	if chatIDStr == "" {
		return nil, errors.New("TG_CHAT_ID is required")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return nil, errors.New("TG_CHAT_ID must be an integer")
	}
	if label == "" {
		label = "AUTOIPO"
	}
	return &config{Token: token, Chat: chatID, Label: label}, nil
}

func getUpdates(token string, offset int64) ([]update, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", token)
	query := url.Values{}
	query.Set("offset", strconv.FormatInt(offset, 10))
	query.Set("timeout", strconv.Itoa(int(pollTimeout.Seconds())))

	urlString := endpoint + "?" + query.Encode()
	req, err := http.NewRequest(http.MethodGet, urlString, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "autoipo/1.0")

	resp, err := (&http.Client{Timeout: pollTimeout + 10*time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("telegram request failed: %s", strings.TrimSpace(string(body)))
	}

	var result tgGetUpdatesResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	if !result.Ok {
		return nil, fmt.Errorf("telegram api error: %s", strings.TrimSpace(result.Description))
	}
	return result.Result, nil
}

func parseCommand(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}

	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	text = strings.ToLower(parts[0])
	if at := strings.IndexByte(text, '@'); at >= 0 {
		text = text[:at]
	}
	text = strings.TrimPrefix(text, "/")
	return strings.TrimSpace(text)
}

func logLine(msg string) {
	line := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), msg)
	if _, err := fmt.Fprint(os.Stderr, line); err != nil {
		_ = err
	}

	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		if _, err := logFile.WriteString(line); err != nil {
			_ = logFile.Close()
			logFile = nil
		}
	}
}

func handleMessage(cfg *config, msg *message) error {
	if msg.Chat.ID != cfg.Chat {
		logLine(fmt.Sprintf("ignoring message from unauthorized chat=%d (expected %d)", msg.Chat.ID, cfg.Chat))
		return nil
	}
	command := parseCommand(msg.Text)
	if command == "" {
		return nil
	}
	logLine(fmt.Sprintf("handling command=%q chat=%d message_id=%d", command, msg.Chat.ID, msg.MessageID))
	if msg.Date > 0 {
		age := time.Since(time.Unix(msg.Date, 0))
		if age > staleTimeout && command != "ping" && command != "status" {
			logLine(fmt.Sprintf("STALE ignored: command=%q age=%s chat=%d", command, formatDuration(age), msg.Chat.ID))
			asyncSendText(cfg.Token, msg.Chat.ID, fmt.Sprintf("STALE ignored (age %s)", formatDuration(age)))
			return nil
		}
	}

	switch command {
	case "ping":
		asyncSendText(cfg.Token, msg.Chat.ID, "ACK /ping")
		return nil
	case "status":
		asyncSendText(cfg.Token, msg.Chat.ID, "ACK /status")
		return nil
	case "help":
		asyncSendText(cfg.Token, msg.Chat.ID, "Commands: /ping, /status, /help")
		return nil
	default:
		return nil
	}
}

func asyncSendText(token string, chatID int64, text string) {
	go func() {
		if err := sendText(token, chatID, text); err != nil {
			logLine(fmt.Sprintf("failed to send message to chat=%d: %s", chatID, err))
		}
	}()
}

func sendText(token string, chatID int64, text string) error {
	payload, err := json.Marshal(tgSendMessageRequest{ChatID: chatID, Text: text})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "autoipo/1.0")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("sendMessage failed: %s", strings.TrimSpace(string(body)))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	if ok, exists := result["ok"]; exists && ok == false {
		return fmt.Errorf("telegram send error")
	}
	return nil
}

func loadOffset(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func persistOffset(path string, offset int64) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strconv.FormatInt(offset, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm%ds", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}
