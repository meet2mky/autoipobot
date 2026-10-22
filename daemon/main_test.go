package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/config"
	"autoipo/internal/daemonctl"
	"autoipo/internal/envfile"
	"autoipo/internal/paths"
	"autoipo/internal/secrets"
	"autoipo/internal/telegram"
)

const testToken = "123456:SECRET-TOKEN"

// newTestCLI returns a cli writing to buffers, using the file secret store and
// a Bot API client aimed at server (nil leaves the real constructor).
func newTestCLI(t *testing.T, server *httptest.Server) (*cli, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	setupPaths(t)
	var stdout, stderr bytes.Buffer
	c := &cli{
		stdout:      &stdout,
		stderr:      &stderr,
		secrets:     func() secrets.Store { return secrets.NewFileStore(paths.SecretsFile()) },
		newTelegram: telegram.New,
	}
	if server != nil {
		c.newTelegram = func(token string) *telegram.Client {
			return &telegram.Client{Token: token, BaseURL: server.URL, HTTP: server.Client()}
		}
	}
	return c, &stdout, &stderr
}

func TestCLIVersionHelpAndUnknownCommand(t *testing.T) {
	c, stdout, stderr := newTestCLI(t, nil)
	if code := c.run([]string{"version"}); code != 0 || !strings.HasPrefix(stdout.String(), "autoipo dev (go") {
		t.Fatalf("version: code %d, out %q", code, stdout.String())
	}
	for _, flag := range []string{"help", "-h", "--help"} {
		stdout.Reset()
		if code := c.run([]string{flag}); code != 0 || !strings.Contains(stdout.String(), "configure   save TG_TOKEN") {
			t.Fatalf("%s: code %d, out %q", flag, code, stdout.String())
		}
	}
	if code := c.run([]string{"bogus"}); code != 2 || !strings.Contains(stderr.String(), `unknown command "bogus"`) {
		t.Fatalf("bogus: code %d, err %q", code, stderr.String())
	}
}

func TestCLIConfigureThenCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/getMe" {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"autoipo","username":"autoipo_bot"}}`)
	}))
	defer server.Close()
	c, stdout, stderr := newTestCLI(t, server)

	// Nothing configured yet.
	if code := c.run([]string{"check"}); code != 1 || !strings.Contains(stdout.String(), "configuration: incomplete") {
		t.Fatalf("check unconfigured: code %d, out %q", code, stdout.String())
	}
	stdout.Reset()
	if code := c.run([]string{"configure"}); code != 1 || !strings.Contains(stderr.String(), "required") {
		t.Fatalf("configure without env: code %d, err %q", code, stderr.String())
	}
	stderr.Reset()

	t.Setenv(config.EnvToken, testToken)
	t.Setenv(config.EnvChatID, "4242")
	t.Setenv(config.EnvLabel, "TestHost")
	if code := c.run([]string{"configure"}); code != 0 {
		t.Fatalf("configure: code %d, err %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "TG_CHAT_ID=4242") || !strings.Contains(out, "HOST_LABEL=TestHost") || !strings.Contains(out, "file secret store") {
		t.Fatalf("configure output = %q", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatalf("configure must not print the token: %q", out)
	}
	values, err := envfile.Load(paths.EnvFile())
	if err != nil || values[config.EnvChatID] != "4242" || values[config.EnvLabel] != "TestHost" {
		t.Fatalf("env file = %v, %v", values, err)
	}
	if _, found := values[config.EnvToken]; found {
		t.Fatal("token must not be written to the env file")
	}
	if token, err := c.secrets().Get(secrets.KeyTelegramToken); err != nil || token != testToken {
		t.Fatalf("stored token = %q, %v", token, err)
	}

	// check must work from the saved configuration alone.
	t.Setenv(config.EnvToken, "")
	t.Setenv(config.EnvChatID, "")
	t.Setenv(config.EnvLabel, "")
	stdout.Reset()
	if code := c.run([]string{"check"}); code != 0 {
		t.Fatalf("check: code %d, out %q, err %q", code, stdout.String(), stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "configuration: complete (label TestHost") || !strings.Contains(out, "bot: @autoipo_bot (autoipo)") {
		t.Fatalf("check output = %q", out)
	}

	// A rejected token fails without echoing it.
	if err := c.secrets().Set(secrets.KeyTelegramToken, "999:WRONG-SECRET"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := c.run([]string{"check"}); code != 1 || !strings.Contains(stderr.String(), "getMe failed") {
		t.Fatalf("check with bad token: code %d, err %q", code, stderr.String())
	}
	if strings.Contains(stderr.String()+stdout.String(), "WRONG-SECRET") {
		t.Fatalf("check leaked the token: %q", stderr.String())
	}
}

func TestCLIStatus(t *testing.T) {
	c, stdout, _ := newTestCLI(t, nil)
	t.Setenv(bridge.EnvPort, "1") // nothing listens on port 1
	if code := c.run([]string{"status"}); code != 0 {
		t.Fatalf("status: code %d", code)
	}
	out := stdout.String()
	for _, want := range []string{"daemon: not running", "status: not published yet", "bridge: not reachable", "last run: no flow has run yet", "schedules: 0 pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}

	started := time.Now().Add(-90 * time.Minute)
	if err := daemonctl.WriteStatus(daemonctl.Status{PID: 4321, Version: "1.2.3", Label: "Mac", ChatID: 4242, StartedAt: started, LastPollAt: time.Now().Add(-time.Minute), Polls: 180, Commands: 2, LastCommand: "ping", LastCommandAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := c.run([]string{"status"}); code != 0 {
		t.Fatalf("status: code %d", code)
	}
	out = stdout.String()
	for _, want := range []string{"status: published ", "Mac · autoipo 1.2.3 · pid 4321", "up 1h30m0s", "last poll 1m0s ago · polls 180 · commands 2", "last command /ping at"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "4242") {
		t.Fatalf("status must not print the chat ID:\n%s", out)
	}
}

func TestRunDaemonRefusesToStartWithoutConfiguration(t *testing.T) {
	setupPaths(t)
	var stderr bytes.Buffer
	if code := runDaemon(&stderr, secrets.NewFileStore(paths.SecretsFile())); code != 1 || !strings.Contains(stderr.String(), "cannot start") {
		t.Fatalf("runDaemon unconfigured: code %d, err %q", code, stderr.String())
	}
}
