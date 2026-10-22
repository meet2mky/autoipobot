// Package telegram is a minimal Bot API client covering what autoipo needs:
// long-polling updates, sending messages and documents, and getMe.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// Update is one getUpdates item.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is an incoming chat message.
type Message struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	From      *User  `json:"from,omitempty"`
}

// Chat identifies a conversation.
type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

// User identifies a Telegram account (used by getMe and Message.From).
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// APIError is a non-OK Bot API response.
type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.Code, e.Description)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// Client calls the Bot API for one token.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// New returns a client with sane timeouts.
func New(token string) *Client {
	return &Client{Token: token, BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

func (c *Client) endpoint(method string) string {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return base + "/bot" + c.Token + "/" + method
}

func (c *Client) call(ctx context.Context, method string, body io.Reader, contentType string, out any) error {
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("telegram token is empty")
	}
	httpMethod := http.MethodGet
	if body != nil {
		httpMethod = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, httpMethod, c.endpoint(method), body)
	if err != nil {
		return err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("User-Agent", "autoipo/2.0")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return c.scrub(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	var envelope apiResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("telegram returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	if !envelope.OK {
		return &APIError{Code: envelope.ErrorCode, Description: envelope.Description}
	}
	if out != nil && len(envelope.Result) > 0 {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// scrub removes the bot token from transport errors, whose messages embed
// the full request URL.
func (c *Client) scrub(err error) error {
	if err == nil || c.Token == "" {
		return err
	}
	message := err.Error()
	if !strings.Contains(message, c.Token) {
		return err
	}
	return errors.New(strings.ReplaceAll(message, c.Token, "<token>"))
}

// GetMe verifies the token and returns the bot identity.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.call(ctx, "getMe", nil, "", &user)
	return user, err
}

// GetUpdates long-polls for updates after offset for up to timeout.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	query := url.Values{}
	query.Set("offset", strconv.FormatInt(offset, 10))
	query.Set("timeout", strconv.Itoa(int(timeout.Seconds())))
	query.Set("allowed_updates", `["message"]`)
	var updates []Update
	err := c.call(ctx, "getUpdates?"+query.Encode(), nil, "", &updates)
	return updates, err
}

// SendMessage posts a plain-text message. Long messages are split at
// Telegram's 4096-character limit.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	for _, chunk := range splitMessage(text, 4096) {
		payload, err := json.Marshal(map[string]any{"chat_id": chatID, "text": chunk, "disable_web_page_preview": true})
		if err != nil {
			return err
		}
		if err := c.call(ctx, "sendMessage", bytes.NewReader(payload), "application/json", nil); err != nil {
			return err
		}
	}
	return nil
}

// SendDocument uploads a file (e.g. a screenshot) with an optional caption.
func (c *Client) SendDocument(ctx context.Context, chatID int64, filename string, content []byte, caption string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = writer.WriteField("caption", truncate(caption, 1024))
	}
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return c.call(ctx, "sendDocument", &body, writer.FormDataContentType(), nil)
}

func splitMessage(text string, limit int) []string {
	if text == "" {
		return []string{""}
	}
	var chunks []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
		if len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	return append(chunks, string(runes))
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// ParseCommand extracts "/cmd@bot arg1 arg2" into ("cmd", ["arg1","arg2"]).
// Text without a leading slash is treated as a bare command word.
func ParseCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}
	command := strings.ToLower(fields[0])
	if at := strings.IndexByte(command, '@'); at >= 0 {
		command = command[:at]
	}
	command = strings.TrimPrefix(command, "/")
	return command, fields[1:]
}
