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
	"time"
)

const (
	pollTimeout   = 10 * time.Second
	staleTimeout  = 5 * time.Minute
	resumeGap     = 3 * time.Minute
	offsetFileTag = "autoipobot.offset"
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

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	offsetPath := filepath.Join(os.TempDir(), offsetFileTag)
	offset, err := loadOffset(offsetPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "failed to read offset:", err)
		os.Exit(1)
	}

	if err := sendText(cfg.Token, cfg.Chat, fmt.Sprintf("%s BOOT", cfg.Label)); err != nil {
		fmt.Fprintln(os.Stderr, "failed to send boot notification:", err)
	}

	lastPoll := time.Now()
	resumeSent := false
	for {
		if time.Since(lastPoll) > resumeGap && !resumeSent {
			if err := sendText(cfg.Token, cfg.Chat, fmt.Sprintf("%s RESUMED after %s", cfg.Label, formatDuration(time.Since(lastPoll)))); err != nil {
				fmt.Fprintln(os.Stderr, "failed to send resume notification:", err)
			}
			resumeSent = true
		}

		updates, err := getUpdates(cfg.Token, offset)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		lastPoll = time.Now()
		resumeSent = false
		for _, item := range updates {
			if item.UpdateID <= offset {
				continue
			}

			nextOffset := item.UpdateID + 1
			if err := persistOffset(offsetPath, nextOffset); err != nil {
				fmt.Fprintln(os.Stderr, "failed to persist offset:", err)
				continue
			}
			offset = nextOffset

			if item.Message == nil {
				continue
			}
			if err := handleMessage(cfg, item.Message); err != nil {
				fmt.Fprintln(os.Stderr, "failed to handle message:", err)
			}
		}
		if len(updates) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func loadConfig() (*config, error) {
	token := strings.TrimSpace(os.Getenv("TG_TOKEN"))
	if token == "" {
		return nil, errors.New("TG_TOKEN is required")
	}
	chatIDStr := strings.TrimSpace(os.Getenv("TG_CHAT_ID"))
	if chatIDStr == "" {
		return nil, errors.New("TG_CHAT_ID is required")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return nil, errors.New("TG_CHAT_ID must be an integer")
	}
	label := strings.TrimSpace(os.Getenv("HOST_LABEL"))
	if label == "" {
		label = "AUTOIPOBOT"
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
	req.Header.Set("User-Agent", "autoipobot/1.0")

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
	text = strings.ToLower(text)
	if at := strings.IndexByte(text, '@'); at >= 0 {
		text = text[:at]
	}
	if strings.HasPrefix(text, "/") {
		text = strings.TrimPrefix(text, "/")
	}
	return strings.TrimSpace(text)
}

func handleMessage(cfg *config, msg *message) error {
	command := parseCommand(msg.Text)
	if command == "" {
		return nil
	}
	if msg.Date > 0 {
		age := time.Since(time.Unix(msg.Date, 0))
		if age > staleTimeout && command != "ping" && command != "status" {
			return sendText(cfg.Token, msg.Chat.ID, fmt.Sprintf("STALE ignored (age %s)", formatDuration(age)))
		}
	}

	switch command {
	case "ping":
		return sendText(cfg.Token, msg.Chat.ID, "ACK /ping")
	case "status":
		return sendText(cfg.Token, msg.Chat.ID, "ACK /status")
	default:
		return nil
	}
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
	req.Header.Set("User-Agent", "autoipobot/1.0")

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
