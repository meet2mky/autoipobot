package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"autoipo/internal/daemonctl"
	"autoipo/internal/paths"
	"autoipo/internal/telegram"
)

func TestResumeGap(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		prev    time.Time
		gap     time.Duration
		resumed bool
	}{
		{now.Add(-4 * time.Minute), 4 * time.Minute, true},
		{now.Add(-3 * time.Minute), 3 * time.Minute, false},
		{now.Add(-35 * time.Second), 35 * time.Second, false},
		{now.Add(-26 * time.Hour), 26 * time.Hour, true},
		{time.Time{}, 0, false},
	}
	for _, tc := range cases {
		gap, resumed := resumeGap(tc.prev, now)
		if gap != tc.gap || resumed != tc.resumed {
			t.Errorf("resumeGap(%v, now) = %v, %v; want %v, %v", tc.prev, gap, resumed, tc.gap, tc.resumed)
		}
	}
	// Monotonic readings are stripped so the comparison is purely wall-clock.
	prev := time.Now()
	if gap, resumed := resumeGap(prev.Add(-5*time.Minute), prev); !resumed || gap.Round(time.Second) != 5*time.Minute {
		t.Fatalf("resumeGap with monotonic clock = %v, %v", gap, resumed)
	}
}

func TestBackoffDelay(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second}
	for n, delay := range want {
		if got := backoffDelay(n); got != delay {
			t.Errorf("backoffDelay(%d) = %s, want %s", n, got, delay)
		}
	}
	if got := backoffDelay(-3); got != time.Second {
		t.Errorf("backoffDelay(-3) = %s", got)
	}
	if got := backoffDelay(1000); got != backoffMax {
		t.Errorf("backoffDelay(1000) = %s", got)
	}
}

func TestOffsetStore(t *testing.T) {
	store := offsetStore{path: filepath.Join(t.TempDir(), "nested", "telegram.offset")}
	if offset, err := store.load(); err != nil || offset != 0 {
		t.Fatalf("load missing = %d, %v", offset, err)
	}
	if err := store.save(1234); err != nil {
		t.Fatal(err)
	}
	if offset, err := store.load(); err != nil || offset != 1234 {
		t.Fatalf("load = %d, %v", offset, err)
	}
	if info, err := os.Stat(store.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("offset file mode = %v, %v", info.Mode(), err)
	}
	if err := os.WriteFile(store.path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("corrupt offset must be reported")
	}
}

func TestSleepHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if sleep(ctx, time.Minute) {
		t.Fatal("sleep must return false when the context is done")
	}
	if !sleep(t.Context(), time.Millisecond) {
		t.Fatal("sleep must return true after the duration elapsed")
	}
}

// fakeTelegram is a Bot API stand-in for getUpdates; replies go through the
// recorder-backed sender, not HTTP.
type fakeTelegram struct {
	t       *testing.T
	calls   atomic.Int32
	respond func(call int32, offset string) (status int, body string)
	server  *httptest.Server
}

func newFakeTelegram(t *testing.T, respond func(call int32, offset string) (int, string)) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{t: t, respond: respond}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			http.NotFound(w, r)
			return
		}
		status, body := f.respond(f.calls.Add(1), r.URL.Query().Get("offset"))
		if body == "" {
			// Idle long poll: block until the client gives up.
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			status, body = http.StatusOK, `{"ok":true,"result":[]}`
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelegram) client() *telegram.Client {
	return &telegram.Client{Token: "tok", BaseURL: f.server.URL, HTTP: f.server.Client()}
}

// newTestDaemon wires a daemon around the test bot, the fake Bot API and a
// recorder-backed sender.
func newTestDaemon(t *testing.T, tg *telegram.Client) (*daemon, *testEnv) {
	t.Helper()
	env := newTestEnv(t, &fakeExecutor{}, nil)
	out := newSender(env.out, testChatID, env.bot.log)
	out.start()
	t.Cleanup(func() { out.close(time.Second) })
	env.bot.out = out
	d := &daemon{
		label:   "Test",
		version: "test",
		log:     env.bot.log,
		tg:      tg,
		out:     out,
		bot:     env.bot,
		stats:   env.bot.stats,
		runner:  env.bot.runner,
		offsets: offsetStore{path: paths.TelegramOffsetFile()},
		now:     time.Now,
		backoff: func(int) time.Duration { return time.Millisecond },
	}
	return d, env
}

func runPollLoop(t *testing.T, d *daemon) (cancel func()) {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.pollLoop(ctx)
	}()
	return func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("poll loop did not stop after cancellation")
		}
	}
}

func TestPollLoopDispatchesAndPersistsOffset(t *testing.T) {
	var api *fakeTelegram
	api = newFakeTelegram(t, func(call int32, offset string) (int, string) {
		switch call {
		case 1:
			if offset != "0" {
				t.Errorf("first poll offset = %s, want 0", offset)
			}
			update := fmt.Sprintf(`{"update_id":10,"message":{"message_id":1,"date":%d,"chat":{"id":%d},"text":"/ping"}}`, time.Now().Unix(), testChatID)
			noMessage := `{"update_id":11}`
			return http.StatusOK, `{"ok":true,"result":[` + update + `,` + noMessage + `]}`
		default:
			if offset != "12" {
				t.Errorf("poll %d offset = %s, want 12", call, offset)
			}
			return 0, ""
		}
	})
	d, env := newTestDaemon(t, api.client())
	if err := d.offsets.save(0); err != nil {
		t.Fatal(err)
	}
	stop := runPollLoop(t, d)
	waitFor(t, "pong reply", func() bool { return strings.HasPrefix(env.out.lastText(), "pong Test") })
	waitFor(t, "second poll", func() bool { return d.stats.snapshot().Polls >= 2 })
	stop()

	if offset, err := d.offsets.load(); err != nil || offset != 12 {
		t.Fatalf("persisted offset = %d, %v; want 12", offset, err)
	}
	if snapshot := d.stats.snapshot(); snapshot.Polls < 2 || snapshot.LastPollAt.IsZero() || snapshot.Commands != 1 || snapshot.LastPollError != "" {
		t.Fatalf("stats = %+v", snapshot)
	}
}

func TestPollLoopBackoffLogsOnceAndRecovers(t *testing.T) {
	api := newFakeTelegram(t, func(call int32, _ string) (int, string) {
		if call <= 3 {
			return http.StatusInternalServerError, `{"ok":false,"error_code":500,"description":"boom"}`
		}
		return 0, ""
	})
	d, env := newTestDaemon(t, api.client())
	stop := runPollLoop(t, d)
	waitFor(t, "recovery", func() bool { return strings.Contains(env.logs.String(), "recovered after 3 failed attempt(s)") })
	stop()

	logs := env.logs.String()
	if n := strings.Count(logs, "telegram poll failing"); n != 1 {
		t.Fatalf("expected exactly one failure log line, got %d:\n%s", n, logs)
	}
	if snapshot := d.stats.snapshot(); snapshot.LastPollError != "" || snapshot.Polls == 0 {
		t.Fatalf("stats after recovery = %+v", snapshot)
	}
}

func TestPollLoopRedactsTokenFromStatusAndLogs(t *testing.T) {
	// Transport errors embed the request URL, and with it the token.
	api := newFakeTelegram(t, func(int32, string) (int, string) { return 0, "" })
	client := api.client()
	api.server.Close()
	d, env := newTestDaemon(t, client)
	d.log = newLogger(scrubber{"tok"}, env.logs)
	d.stats = newStats(daemonctl.Status{}, func(daemonctl.Status) error { return nil }, scrubber{"tok"}, d.log)
	stop := runPollLoop(t, d)
	waitFor(t, "poll error", func() bool { return d.stats.snapshot().LastPollError != "" })
	stop()
	if got := d.stats.snapshot().LastPollError; strings.Contains(got, "bottok") || !strings.Contains(got, "[redacted]") {
		t.Fatalf("LastPollError = %q", got)
	}
	if logs := env.logs.String(); strings.Contains(logs, "bottok") {
		t.Fatalf("token leaked into logs:\n%s", logs)
	}
}

func TestPollLoopSendsResumedAfterWallClockGap(t *testing.T) {
	api := newFakeTelegram(t, func(int32, string) (int, string) { return 0, "" })
	d, env := newTestDaemon(t, api.client())
	base := time.Now()
	var ticks atomic.Int32
	d.now = func() time.Time {
		if ticks.Add(1) == 1 {
			return base // the loop's initial "previous iteration" timestamp
		}
		return base.Add(4 * time.Minute)
	}
	stop := runPollLoop(t, d)
	waitFor(t, "RESUMED message", func() bool { return env.out.lastText() == "Test RESUMED after 4m0s" })
	waitFor(t, "a few more polls", func() bool { return api.calls.Load() >= 3 })
	stop()
	if n := strings.Count(strings.Join(env.out.allTexts(), "\n"), "RESUMED"); n != 1 {
		t.Fatalf("RESUMED must be sent once, got %d", n)
	}
}

func TestStatsRateLimitsWrites(t *testing.T) {
	writes := 0
	st := newStats(daemonctl.Status{Label: "x"}, func(daemonctl.Status) error { writes++; return nil }, nil, newLogger(nil, io.Discard))
	t0 := time.Now()
	st.recordPoll(t0)
	st.recordCommand(t0.Add(time.Second), "ping")
	st.recordPollError(t0.Add(2*time.Second), fmt.Errorf("x"))
	if writes != 1 {
		t.Fatalf("writes after 3 events within 5s = %d, want 1", writes)
	}
	st.recordPoll(t0.Add(6 * time.Second))
	st.flush(t0.Add(7*time.Second), true)
	if writes != 3 {
		t.Fatalf("writes = %d, want 3", writes)
	}
	if snapshot := st.snapshot(); snapshot.Polls != 2 || snapshot.Commands != 1 || snapshot.LastPollError != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
