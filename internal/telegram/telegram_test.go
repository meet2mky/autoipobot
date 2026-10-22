package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		raw  string
		cmd  string
		args []string
	}{
		{" /PING@autoipo ", "ping", nil},
		{"Status", "status", nil},
		{"/run  My Flow  alice", "run", []string{"My", "Flow", "alice"}},
		{"   ", "", nil},
		{"@autoipo", "", nil},
	}
	for _, tc := range tests {
		cmd, args := ParseCommand(tc.raw)
		if cmd != tc.cmd || len(args) != len(tc.args) {
			t.Errorf("ParseCommand(%q) = (%q, %v), want (%q, %v)", tc.raw, cmd, args, tc.cmd, tc.args)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	text := strings.Repeat("line\n", 2000)
	chunks := splitMessage(text, 4096)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 4096 {
			t.Fatalf("chunk exceeds limit: %d", len(chunk))
		}
	}
	if got := splitMessage("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatalf("splitMessage short = %v", got)
	}
}

func TestClientCalls(t *testing.T) {
	var sent []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bottok/") {
			http.Error(w, "bad token path", http.StatusNotFound)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/bottok/") {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"autoipo","username":"autoipo_bot"}}`))
		case "getUpdates":
			if r.URL.Query().Get("offset") != "7" || r.URL.Query().Get("timeout") != "30" {
				t.Errorf("unexpected query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":7,"message":{"message_id":1,"date":5,"chat":{"id":42},"text":"/ping"}}]}`))
		case "sendMessage":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			sent = append(sent, payload)
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		case "sendDocument":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("sendDocument content type = %s", r.Header.Get("Content-Type"))
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
		}
	}))
	defer server.Close()

	client := &Client{Token: "tok", BaseURL: server.URL, HTTP: server.Client()}
	ctx := context.Background()
	me, err := client.GetMe(ctx)
	if err != nil || me.Username != "autoipo_bot" {
		t.Fatalf("GetMe = %+v, %v", me, err)
	}
	updates, err := client.GetUpdates(ctx, 7, 30*time.Second)
	if err != nil || len(updates) != 1 || updates[0].Message.Chat.ID != 42 {
		t.Fatalf("GetUpdates = %+v, %v", updates, err)
	}
	if err := client.SendMessage(ctx, 42, "hello"); err != nil || len(sent) != 1 || sent[0]["text"] != "hello" {
		t.Fatalf("SendMessage = %v, sent=%v", err, sent)
	}
	if err := client.SendDocument(ctx, 42, "shot.png", []byte{1, 2, 3}, "cap"); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	err = client.call(ctx, "unknown", nil, "", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 404 {
		t.Fatalf("expected APIError 404, got %v", err)
	}
	if err := (&Client{}).SendMessage(ctx, 1, "x"); err == nil {
		t.Fatal("empty token should error")
	}
}

func TestTransportErrorsNeverContainToken(t *testing.T) {
	client := &Client{Token: "123:SECRET", BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked into error: %v", err)
	}
	if !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("expected scrubbed placeholder in error: %v", err)
	}
}
