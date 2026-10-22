package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"autoipo/internal/bridge"
)

// fakeBridge is an HTTP stand-in for a running bridge that records commands
// and serves scripted results.
type fakeBridge struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []bridge.Request
	results  map[string]bridge.Result // keyed by request type
	status   bridge.Status
}

// installFakeBridge starts a fake bridge and points newClient at it.
func installFakeBridge(t *testing.T) *fakeBridge {
	t.Helper()
	fake := &fakeBridge{results: map[string]bridge.Result{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	previous := newClient
	newClient = func() *bridge.Client {
		return &bridge.Client{BaseURL: fake.server.URL, Token: "tok", HTTP: fake.server.Client()}
	}
	t.Cleanup(func() { newClient = previous })
	return fake
}

func (f *fakeBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "missing or invalid bridge session token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/command":
		var request bridge.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, request)
		result, scripted := f.results[request.Type]
		f.mu.Unlock()
		if !scripted {
			result = bridge.Result{OK: true}
		}
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, result)
	case r.Method == http.MethodGet && r.URL.Path == "/status":
		f.mu.Lock()
		status := f.status
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, status)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeBridge) setResult(kind string, result bridge.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[kind] = result
}

func (f *fakeBridge) lastRequest(t *testing.T) bridge.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the fake bridge received no command")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeBridge) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// runCLI executes run with captured output.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// useScreenshotDir points the default screenshot directory at a temp dir.
func useScreenshotDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "screenshots")
	previous := screenshotDir
	screenshotDir = func() string { return dir }
	t.Cleanup(func() { screenshotDir = previous })
	return dir
}

func decodeResult(t *testing.T, stdout string) bridge.Result {
	t.Helper()
	var result bridge.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not a JSON result: %v\n%s", err, stdout)
	}
	if strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
		t.Fatalf("JSON output should be a single line:\n%s", stdout)
	}
	return result
}

// Tests ----------------------------------------------------------------------

func TestUsageAndHelp(t *testing.T) {
	code, stdout, stderr := runCLI()
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("no args: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	for _, flag := range []string{"help", "-h", "--help"} {
		code, stdout, stderr = runCLI(flag)
		if code != exitOK || stderr != "" || !strings.Contains(stdout, "Usage:") {
			t.Fatalf("%s: code %d stdout %q stderr %q", flag, code, stdout, stderr)
		}
	}
	code, _, stderr = runCLI("explode")
	if code != exitUsage || !strings.Contains(stderr, `unknown command "explode"`) {
		t.Fatalf("unknown command: code %d stderr %q", code, stderr)
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	commands := append([]string{"serve", "status", "random-sleep", "help"}, bridge.CLICommands...)
	for _, name := range commands {
		if !regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(name) + `\b`).MatchString(usageText) {
			t.Errorf("usage does not list %q", name)
		}
	}
	if !strings.Contains(usageText, jsonFlag) {
		t.Error("usage does not describe --json")
	}
}

func TestArgumentValidation(t *testing.T) {
	fake := installFakeBridge(t)
	cases := [][]string{
		{"click"},
		{"click", "#a", "extra"},
		{"fill", "#a"},
		{"set-url", "ftp://x"},
		{"press-key", "   "},
		{"open", "extra"},
		{"serve", "extra"},
		{"status", "extra"},
		{"screenshot", "--bogus"},
		{"screenshot", "--out"},
		{"screenshot", "--out="},
		{"random-sleep"},
	}
	for _, args := range cases {
		code, stdout, stderr := runCLI(args...)
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, "chrome-extension-cli help") {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	if fake.requestCount() != 0 {
		t.Fatalf("invalid invocations reached the bridge: %d request(s)", fake.requestCount())
	}
}

func TestRandomSleep(t *testing.T) {
	for _, bad := range []string{"0", "-1", "abc", "86401", "NaN", "Inf", ""} {
		started := time.Now()
		code, _, stderr := runCLI("random-sleep", bad)
		if code != exitUsage || !strings.Contains(stderr, "max-seconds") {
			t.Errorf("random-sleep %q: code %d stderr %q", bad, code, stderr)
		}
		if time.Since(started) > time.Second {
			t.Errorf("random-sleep %q slept despite being invalid", bad)
		}
	}
	code, stdout, stderr := runCLI("random-sleep", "0.01")
	if code != exitOK || stderr != "" || !strings.HasPrefix(stdout, "Slept for ") {
		t.Fatalf("random-sleep 0.01: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI("--json", "random-sleep", "0.01")
	if result := decodeResult(t, stdout); code != exitOK || !result.OK || !strings.HasPrefix(result.Output, "Slept for ") {
		t.Fatalf("random-sleep --json: code %d stdout %q", code, stdout)
	}
}

func TestBridgeCommandsHumanOutput(t *testing.T) {
	fake := installFakeBridge(t)
	fake.setResult(bridge.TypePageContains, bridge.Result{OK: true, Output: "true"})
	fake.setResult(bridge.TypeDropdown, bridge.Result{OK: true, Choices: []bridge.Choice{{Label: "HDFC", Value: "hdfc"}, {Label: "Plain"}}})

	code, stdout, stderr := runCLI("page-contains", "#login")
	if code != exitOK || stdout != "true\n" || stderr != "" {
		t.Fatalf("page-contains: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if got := fake.lastRequest(t); got != (bridge.Request{Type: bridge.TypePageContains, Selector: "#login"}) {
		t.Fatalf("bridge received %+v", got)
	}

	code, stdout, _ = runCLI("dropdown", "#bank")
	if code != exitOK || stdout != "1. HDFC [hdfc]\n2. Plain\n" {
		t.Fatalf("dropdown: code %d stdout %q", code, stdout)
	}

	code, stdout, _ = runCLI("set-url", "example.com/login")
	if code != exitOK || stdout != "Command completed\n" {
		t.Fatalf("set-url: code %d stdout %q", code, stdout)
	}
	if got := fake.lastRequest(t); got.Type != bridge.TypeSetURL || got.URL != "https://example.com/login" {
		t.Fatalf("set-url sent %+v", got)
	}

	code, _, _ = runCLI("fill", "#user", "secret-value")
	if got := fake.lastRequest(t); code != exitOK || got != (bridge.Request{Type: bridge.TypeFill, Selector: "#user", Value: "secret-value"}) {
		t.Fatalf("fill sent %+v (code %d)", got, code)
	}

	code, _, _ = runCLI("press-key", "Escape")
	if got := fake.lastRequest(t); code != exitOK || got != (bridge.Request{Type: bridge.TypePressKey, Key: "Escape"}) {
		t.Fatalf("press-key sent %+v (code %d)", got, code)
	}
}

func TestJSONFlagAnywhere(t *testing.T) {
	fake := installFakeBridge(t)
	fake.setResult(bridge.TypePageContains, bridge.Result{OK: true, Output: "false"})
	for _, args := range [][]string{
		{"--json", "page-contains", "#x"},
		{"page-contains", "--json", "#x"},
		{"page-contains", "#x", "--json"},
	} {
		code, stdout, stderr := runCLI(args...)
		if code != exitOK || stderr != "" || stdout != `{"ok":true,"output":"false"}`+"\n" {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestCommandFailure(t *testing.T) {
	fake := installFakeBridge(t)
	fake.setResult(bridge.TypeClick, bridge.Result{OK: false, Error: "no such element"})

	code, stdout, stderr := runCLI("click", "#missing")
	if code != exitFailure || stdout != "" || stderr != "extension command failed: no such element\n" {
		t.Fatalf("click: code %d stdout %q stderr %q", code, stdout, stderr)
	}

	code, stdout, stderr = runCLI("--json", "click", "#missing")
	result := decodeResult(t, stdout)
	if code != exitFailure || result.OK || result.Error != "no such element" || !strings.Contains(stderr, "no such element") {
		t.Fatalf("click --json: code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestUnreachableBridgeHint(t *testing.T) {
	previous := newClient
	newClient = func() *bridge.Client {
		return &bridge.Client{BaseURL: "http://127.0.0.1:1", Token: "tok", HTTP: &http.Client{Timeout: 2 * time.Second}}
	}
	t.Cleanup(func() { newClient = previous })

	for _, args := range [][]string{{"open"}, {"status"}} {
		code, stdout, stderr := runCLI(args...)
		if code != exitFailure || stdout != "" || !strings.Contains(stderr, "chrome-extension-cli serve") || !strings.Contains(stderr, bridge.ErrUnreachable.Error()) {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	code, stdout, _ := runCLI("--json", "open")
	if result := decodeResult(t, stdout); code != exitFailure || result.OK || !strings.Contains(result.Error, bridge.ErrUnreachable.Error()) {
		t.Fatalf("open --json unreachable: code %d stdout %q", code, stdout)
	}
}

func TestScreenshotExplicitPath(t *testing.T) {
	fake := installFakeBridge(t)
	png := []byte("\x89PNG\r\n\x1a\nfake image bytes")
	fake.setResult(bridge.TypeScreenshot, bridge.Result{OK: true, Screenshot: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})

	path := filepath.Join(t.TempDir(), "nested", "shot.png")
	code, stdout, stderr := runCLI("screenshot", "--out", path)
	if code != exitOK || stderr != "" || stdout != path+"\n" {
		t.Fatalf("screenshot --out: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatalf("saved file = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved file mode = %v, %v; want 0600", info.Mode(), err)
	}
	if got := fake.lastRequest(t); got != (bridge.Request{Type: bridge.TypeScreenshot}) {
		t.Fatalf("bridge received %+v", got)
	}

	other := filepath.Join(t.TempDir(), "other.png")
	if code, _, _ := runCLI("screenshot", "--out="+other); code != exitOK {
		t.Fatalf("screenshot --out=: code %d", code)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("screenshot --out= did not write the file: %v", err)
	}
}

func TestScreenshotDefaultDirectory(t *testing.T) {
	fake := installFakeBridge(t)
	dir := useScreenshotDir(t)
	png := []byte("\x89PNG\r\n\x1a\nmore fake bytes")
	fake.setResult(bridge.TypeScreenshot, bridge.Result{OK: true, Screenshot: base64.StdEncoding.EncodeToString(png)})

	code, stdout, stderr := runCLI("screenshot")
	if code != exitOK || stderr != "" {
		t.Fatalf("screenshot: code %d stderr %q", code, stderr)
	}
	path := strings.TrimSuffix(stdout, "\n")
	if filepath.Dir(path) != dir || !regexp.MustCompile(`^\d{8}-\d{6}\.png$`).MatchString(filepath.Base(path)) {
		t.Fatalf("screenshot path = %q, want <dir>/<YYYYMMDD-HHMMSS>.png in %s", path, dir)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, png) {
		t.Fatalf("saved file = %q, %v", data, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("screenshot dir mode = %v, %v; want 0700", info.Mode(), err)
	}

	// A second capture in the same second must not overwrite the first.
	code, stdout, _ = runCLI("--json", "screenshot")
	result := decodeResult(t, stdout)
	if code != exitOK || !result.OK || result.Output == "" || result.Output == path || result.Screenshot == "" {
		t.Fatalf("screenshot --json: code %d result %+v", code, result)
	}
	if _, err := os.Stat(result.Output); err != nil {
		t.Fatalf("second screenshot missing: %v", err)
	}
}

func TestScreenshotWithoutData(t *testing.T) {
	fake := installFakeBridge(t)
	dir := useScreenshotDir(t)
	fake.setResult(bridge.TypeScreenshot, bridge.Result{OK: true})
	code, stdout, stderr := runCLI("screenshot")
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, "no screenshot data") {
		t.Fatalf("screenshot without data: code %d stdout %q stderr %q", code, stdout, stderr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("no file should be written without data, found %d", len(entries))
	}
}

func TestStatusCommand(t *testing.T) {
	fake := installFakeBridge(t)
	fake.status = bridge.Status{
		Serving:            true,
		ExtensionConnected: true,
		ExtensionOrigin:    "chrome-extension://abc",
		PendingCommands:    2,
		UptimeSeconds:      3725,
		LastCommandAt:      "2026-10-22T12:00:00Z",
		ProtocolVersion:    bridge.ProtocolVersion,
	}
	code, stdout, stderr := runCLI("status")
	if code != exitOK || stderr != "" {
		t.Fatalf("status: code %d stderr %q", code, stderr)
	}
	for _, want := range []string{fake.server.URL, "protocol v2", "1h2m5s", "connected (chrome-extension://abc)", "pending commands: 2", "last command: 2026-10-22T12:00:00Z"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, stdout)
		}
	}

	code, stdout, _ = runCLI("status", "--json")
	var status bridge.Status
	if err := json.Unmarshal([]byte(stdout), &status); err != nil || code != exitOK || status != fake.status {
		t.Fatalf("status --json: code %d err %v status %+v", code, err, status)
	}

	fake.status = bridge.Status{Serving: true, ProtocolVersion: bridge.ProtocolVersion}
	_, stdout, _ = runCLI("status")
	if !strings.Contains(stdout, "extension: not connected") || !strings.Contains(stdout, "last command: never") {
		t.Fatalf("idle status output:\n%s", stdout)
	}
}

func TestParseScreenshotArgs(t *testing.T) {
	cases := []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{nil, "", false},
		{[]string{"--out", "a.png"}, "a.png", false},
		{[]string{"--out=b.png"}, "b.png", false},
		{[]string{"--out"}, "", true},
		{[]string{"--out="}, "", true},
		{[]string{"extra"}, "", true},
		{[]string{"--out", "a.png", "extra"}, "", true},
	}
	for _, tc := range cases {
		got, err := parseScreenshotArgs(tc.args)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("parseScreenshotArgs(%v) = (%q, %v), want (%q, wantErr %v)", tc.args, got, err, tc.want, tc.wantErr)
		}
	}
}
