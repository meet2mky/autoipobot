package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/daemonctl"
	"autoipo/internal/flow"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/telegram"
	"autoipo/internal/users"
)

const (
	testChatID = int64(4242)
	testPAN    = "ABCDE1234F"
)

// setupPaths isolates all runtime state under a temporary AUTOIPO_HOME.
func setupPaths(t *testing.T) {
	t.Helper()
	t.Setenv(paths.EnvRoot, t.TempDir())
	t.Setenv(secrets.EnvBackend, "file")
	paths.ResetForTesting()
	t.Cleanup(paths.ResetForTesting)
}

// logBuffer is a concurrency-safe bytes.Buffer for logger output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// recorder captures outbound traffic. It satisfies both outbox (used by the
// bot and scheduler) and transport (used by the sender).
type recorder struct {
	mu    sync.Mutex
	texts []string
	docs  []recordedDoc
}

type recordedDoc struct {
	name    string
	content []byte
	caption string
}

func (r *recorder) text(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
}

func (r *recorder) document(name string, content []byte, caption string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = append(r.docs, recordedDoc{name: name, content: append([]byte(nil), content...), caption: caption})
}

func (r *recorder) SendMessage(_ context.Context, _ int64, text string) error {
	r.text(text)
	return nil
}

func (r *recorder) SendDocument(_ context.Context, _ int64, name string, content []byte, caption string) error {
	r.document(name, content, caption)
	return nil
}

func (r *recorder) allTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

func (r *recorder) lastText() string {
	texts := r.allTexts()
	if len(texts) == 0 {
		return ""
	}
	return texts[len(texts)-1]
}

func (r *recorder) allDocs() []recordedDoc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedDoc(nil), r.docs...)
}

// fakeExecutor is a flow.Executor whose commands succeed, fail or block
// until the context ends.
type fakeExecutor struct {
	fail  bool
	block bool
	shot  []byte

	mu       sync.Mutex
	commands []string
}

func (f *fakeExecutor) PageContains(context.Context, string) (bool, error) { return true, nil }

func (f *fakeExecutor) Command(ctx context.Context, name string, args []string) (string, error) {
	f.mu.Lock()
	f.commands = append(f.commands, name+" "+strings.Join(args, " "))
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if f.fail {
		return "", errors.New("element not found")
	}
	return "ok", nil
}

func (f *fakeExecutor) EnsureServing(context.Context) error { return nil }

func (f *fakeExecutor) Screenshot(context.Context) ([]byte, error) { return f.shot, nil }

func (f *fakeExecutor) commandLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.commands...)
}

// testFlow builds Start → open → Success.
func testFlow(name string) flow.Flow {
	f := flow.New(name)
	f.StartNode().NextID = "open"
	f.Nodes = append(f.Nodes, flow.Node{ID: "open", Type: flow.TypeCommand, Name: "Open", Command: "open", NextID: f.SuccessID})
	return f
}

func saveFlows(t *testing.T, flows ...flow.Flow) {
	t.Helper()
	if err := flow.OpenStore(paths.FlowsFile()).Save(flows); err != nil {
		t.Fatalf("save flows: %v", err)
	}
}

func addUser(t *testing.T, sec secrets.Store, name, pan string) users.Record {
	t.Helper()
	store, err := users.Open(paths.UsersFile(), sec)
	if err != nil {
		t.Fatalf("open users: %v", err)
	}
	record := users.Record{Name: name, PAN: pan, BankName: "HDFC", UserID: "login-" + strings.ToLower(name), Password: "hunter2"}
	if err := store.Put(record, ""); err != nil {
		t.Fatalf("put user: %v", err)
	}
	return record
}

// testEnv bundles a bot and its collaborators wired to fakes.
type testEnv struct {
	bot  *bot
	out  *recorder
	logs *logBuffer
	now  time.Time
}

// newTestEnv builds a bot whose runs use exec and whose bridge client points
// at bridgeClient (nil means an unreachable bridge).
func newTestEnv(t *testing.T, exec flow.Executor, bridgeClient *bridge.Client) *testEnv {
	t.Helper()
	setupPaths(t)
	logs := &logBuffer{}
	log := newLogger(nil, logs)
	out := &recorder{}
	now := time.Now().Truncate(time.Second)
	if bridgeClient == nil {
		bridgeClient = &bridge.Client{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	}
	st := newStats(daemonctl.Status{Label: "Test", Version: "test", StartedAt: now.Add(-time.Hour)}, func(daemonctl.Status) error { return nil }, nil, log)
	b := &bot{
		label:     "Test",
		version:   "test",
		chatID:    testChatID,
		log:       log,
		out:       out,
		stats:     st,
		runner:    newRunner(log, func() flow.Executor { return exec }, flow.Options{MaxVisits: 50, ScreenshotOnFailure: true}),
		flows:     flow.OpenStore(paths.FlowsFile()),
		schedules: schedule.Open(paths.SchedulesFile()),
		secrets:   secrets.NewFileStore(paths.SecretsFile()),
		bridge:    bridgeClient,
	}
	t.Cleanup(func() {
		b.runner.stop()
		b.runner.wait()
		b.wait()
	})
	return &testEnv{bot: b, out: out, logs: logs, now: now}
}

// message builds a fresh message from the authorised chat.
func (e *testEnv) message(text string) telegram.Message {
	return telegram.Message{MessageID: 1, Date: e.now.Unix(), Chat: telegram.Chat{ID: testChatID}, Text: text}
}

// send handles text as if it had just arrived and returns the latest reply.
func (e *testEnv) send(t *testing.T, text string) string {
	t.Helper()
	before := len(e.out.allTexts())
	e.bot.handle(t.Context(), e.message(text), e.now)
	texts := e.out.allTexts()
	if len(texts) == before {
		t.Fatalf("%s: no reply", text)
	}
	return texts[len(texts)-1]
}

// fakeBridge serves /status and screenshot commands.
func fakeBridge(t *testing.T, connected bool, png []byte) *bridge.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			_ = json.NewEncoder(w).Encode(bridge.Status{Serving: true, ExtensionConnected: connected})
		case "/command":
			_ = json.NewEncoder(w).Encode(bridge.Result{OK: true, Screenshot: base64.StdEncoding.EncodeToString(png)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return &bridge.Client{BaseURL: server.URL, Token: "test", HTTP: server.Client()}
}

// waitFor polls cond until it holds or the test deadline approaches.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
