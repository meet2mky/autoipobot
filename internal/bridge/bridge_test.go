package bridge

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

func TestBuildRequest(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    Request
		wantErr bool
	}{
		{"set-url", []string{"example.com/login"}, Request{Type: TypeSetURL, URL: "https://example.com/login"}, false},
		{"set-url", []string{"ftp://x"}, Request{}, true},
		{"open", nil, Request{Type: TypeOpen}, false},
		{"open", []string{"extra"}, Request{}, true},
		{"click", []string{"#go"}, Request{Type: TypeClick, Selector: "#go"}, false},
		{"click", []string{"   "}, Request{}, true},
		{"fill", []string{"#u", "name"}, Request{Type: TypeFill, Selector: "#u", Value: "name"}, false},
		{"choose", []string{"#bank", "HDFC"}, Request{Type: TypeChoose, Selector: "#bank", Value: "HDFC"}, false},
		{"press-key", []string{"Escape"}, Request{Type: TypePressKey, Key: "Escape"}, false},
		{"press-key", []string{""}, Request{}, true},
		{"read-text", []string{".total"}, Request{Type: TypeReadText, Selector: ".total"}, false},
		{"screenshot", nil, Request{Type: TypeScreenshot}, false},
		{"nope", nil, Request{}, true},
	}
	for _, tc := range tests {
		got, err := BuildRequest(tc.name, tc.args)
		if (err != nil) != tc.wantErr {
			t.Errorf("BuildRequest(%s %v) error = %v, wantErr %v", tc.name, tc.args, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("BuildRequest(%s %v) = %+v, want %+v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestChoiceRoundTrip(t *testing.T) {
	for _, choice := range []Choice{{Label: "HDFC Bank", Value: "hdfc"}, {Label: "Plain", Value: "Plain"}, {Label: "Only label"}} {
		parsed := ParseChoice(choice.String())
		if parsed.Label != choice.Label {
			t.Errorf("ParseChoice(%q).Label = %q", choice.String(), parsed.Label)
		}
		wantValue := choice.Value
		if wantValue == "" {
			wantValue = choice.Label
		}
		if parsed.Value != wantValue {
			t.Errorf("ParseChoice(%q).Value = %q, want %q", choice.String(), parsed.Value, wantValue)
		}
	}
}

func TestAckNormalizeLegacyOptions(t *testing.T) {
	var ack Ack
	if err := json.Unmarshal([]byte(`{"type":"ack","id":"1","ok":true,"options":["A [a]","B"]}`), &ack); err != nil {
		t.Fatal(err)
	}
	ack.Normalize()
	if len(ack.Choices) != 2 || ack.Choices[0] != (Choice{Label: "A", Value: "a"}) || ack.Choices[1] != (Choice{Label: "B", Value: "B"}) {
		t.Fatalf("Normalize produced %+v", ack.Choices)
	}
}

func TestFormatCommandRedactsFillValue(t *testing.T) {
	got := FormatCommand("fill", []string{"#pw", "hunter2"})
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "[value redacted]") {
		t.Fatalf("FormatCommand leaked value: %s", got)
	}
	if got := FormatCommand("click", []string{"#a"}); got != `chrome-extension-cli click "#a"` {
		t.Fatalf("FormatCommand = %s", got)
	}
}

func TestFormatResult(t *testing.T) {
	dropdown := FormatResult(Request{Type: TypeDropdown}, Result{OK: true, Choices: []Choice{{Label: "One", Value: "1"}, {Label: "Two"}}})
	if dropdown != "1. One [1]\n2. Two" {
		t.Fatalf("dropdown format = %q", dropdown)
	}
	if got := FormatResult(Request{Type: TypePageContains}, Result{OK: true, Output: "true"}); got != "true" {
		t.Fatalf("page-contains format = %q", got)
	}
	if got := FormatResult(Request{Type: TypeClick}, Result{OK: true}); got != "Command completed" {
		t.Fatalf("click format = %q", got)
	}
	list := FormatResult(Request{Type: TypeListSelectors}, Result{OK: true, Elements: []SelectorMetadata{{Kind: "Fillable", Label: "User", Selector: "#u", Context: "Login"}}})
	if !strings.Contains(list, "Fillable · User") || !strings.Contains(list, "#u") || !strings.Contains(list, "Login") {
		t.Fatalf("list format = %q", list)
	}
}

func newTestServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, Token: "tok", HTTP: server.Client()}
}

func TestClientSendSuccessAndCommandError(t *testing.T) {
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		var request Request
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Type {
		case TypePageContains:
			_ = json.NewEncoder(w).Encode(Result{OK: true, Output: "true"})
		case TypeClick:
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(Result{OK: false, Error: "no such element"})
		default:
			http.Error(w, "unknown command type", http.StatusBadRequest)
		}
	})
	ctx := context.Background()
	found, err := client.PageContains(ctx, "#x")
	if err != nil || !found {
		t.Fatalf("PageContains = (%v, %v)", found, err)
	}
	_, err = client.Exec(ctx, "click", []string{"#missing"})
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.Message != "no such element" {
		t.Fatalf("Exec error = %v, want CommandError", err)
	}
	_, err = client.Send(ctx, Request{Type: TypeOpen})
	if !errors.As(err, &commandErr) || !strings.Contains(err.Error(), "unknown command type") {
		t.Fatalf("Send plain-text error = %v", err)
	}
}

func TestClientUnreachable(t *testing.T) {
	client := &Client{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	if err := client.Health(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Health = %v, want ErrUnreachable", err)
	}
	if client.Reachable(context.Background()) {
		t.Fatal("Reachable should be false")
	}
}

func TestClientStatusAndWaitForExtension(t *testing.T) {
	connected := false
	client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(Status{Serving: true, ExtensionConnected: connected})
		connected = true
	})
	status, err := client.Status(context.Background())
	if err != nil || !status.Serving || status.ExtensionConnected {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	if err := client.WaitForExtension(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("WaitForExtension: %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	if got, err := ValidateURL(" example.com "); err != nil || got != "https://example.com" {
		t.Fatalf("ValidateURL = %q, %v", got, err)
	}
	for _, bad := range []string{"", "javascript:alert(1)", "http://", "file:///etc/passwd"} {
		if _, err := ValidateURL(bad); err == nil {
			t.Errorf("ValidateURL(%q) should fail", bad)
		}
	}
}

func TestImageExtension(t *testing.T) {
	cases := map[string][]byte{
		".png":  []byte("\x89PNG\r\n\x1a\n...."),
		".jpg":  {0xFF, 0xD8, 0xFF, 0xDB, 0},
		".webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
	}
	for want, data := range cases {
		if got := ImageExtension(data); got != want {
			t.Errorf("ImageExtension(%q) = %q, want %q", data[:4], got, want)
		}
	}
	if got := ImageExtension([]byte("nonsense")); got != ".png" {
		t.Errorf("unknown data should default to .png, got %q", got)
	}
}
