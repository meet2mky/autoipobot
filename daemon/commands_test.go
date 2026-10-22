package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"autoipo/internal/flow"
	"autoipo/internal/schedule"
	"autoipo/internal/telegram"
)

func TestPingHelpAndUnknown(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	if got := env.send(t, "/ping@autoipo_bot"); got != "pong Test · up 1h0m0s" {
		t.Fatalf("/ping = %q", got)
	}
	if got := env.send(t, "/help"); !strings.Contains(got, "/run <flow> [user]") || !strings.Contains(got, "/unschedule <id>") {
		t.Fatalf("/help = %q", got)
	}
	if got := env.send(t, "/frobnicate now"); got != "unknown command /frobnicate — send /help for the list" {
		t.Fatalf("unknown = %q", got)
	}
	if snapshot := env.bot.stats.snapshot(); snapshot.Commands != 3 || snapshot.LastCommand != "frobnicate" {
		t.Fatalf("stats = %+v", snapshot)
	}
	if !strings.Contains(env.logs.String(), "command /ping") {
		t.Fatalf("log missing command line:\n%s", env.logs.String())
	}
}

func TestUnauthorizedChatIsIgnoredAndLoggedOnce(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	foreign := telegram.Message{MessageID: 7, Date: env.now.Unix(), Chat: telegram.Chat{ID: 99}, Text: "/ping"}
	env.bot.handle(t.Context(), foreign, env.now)
	env.bot.handle(t.Context(), foreign, env.now.Add(time.Minute))
	if texts := env.out.allTexts(); len(texts) != 0 {
		t.Fatalf("unauthorized chat received replies: %v", texts)
	}
	if n := strings.Count(env.logs.String(), "unauthorized chat 99"); n != 1 {
		t.Fatalf("expected one unauthorized log line, got %d:\n%s", n, env.logs.String())
	}
	env.bot.handle(t.Context(), foreign, env.now.Add(2*time.Hour))
	if n := strings.Count(env.logs.String(), "unauthorized chat 99"); n != 2 {
		t.Fatalf("expected the chat to be logged again after an hour, got %d lines", n)
	}
	if env.bot.stats.snapshot().Commands != 0 {
		t.Fatal("unauthorized messages must not count as commands")
	}
}

func TestStaleCommandsAreIgnoredExceptPingStatusHelp(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	old := env.message("/flows")
	old.Date = env.now.Add(-10 * time.Minute).Unix()
	env.bot.handle(t.Context(), old, env.now)
	if got := env.out.lastText(); got != "STALE ignored (age 10m0s)" {
		t.Fatalf("stale reply = %q", got)
	}
	old.Text = "/ping"
	env.bot.handle(t.Context(), old, env.now)
	if got := env.out.lastText(); !strings.HasPrefix(got, "pong") {
		t.Fatalf("old /ping should still be answered, got %q", got)
	}
	undated := env.message("/flows")
	undated.Date = 0
	env.bot.handle(t.Context(), undated, env.now)
	if got := env.out.lastText(); strings.HasPrefix(got, "STALE") {
		t.Fatalf("undated message must not be stale, got %q", got)
	}
	env.bot.handle(t.Context(), env.message(""), env.now)
	env.bot.handle(t.Context(), env.message("/"), env.now)
	if n := len(env.out.allTexts()); n != 3 {
		t.Fatalf("empty messages must be ignored, got %d replies", n)
	}
}

func TestStatusCommand(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, fakeBridge(t, true, nil))
	env.bot.stats.recordPoll(env.now.Add(-12 * time.Second))
	got := env.send(t, "/status")
	for _, want := range []string{
		"Test · autoipo test",
		"up 1h0m0s",
		"last poll 12s ago · polls 1 · commands 1",
		"bridge: reachable · extension connected",
		"flow run: none",
		"last run: no flow has run yet",
		"schedules: 0 pending",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/status missing %q:\n%s", want, got)
		}
	}
	unreachable := newTestEnv(t, &fakeExecutor{}, nil)
	if got := unreachable.send(t, "/status"); !strings.Contains(got, "bridge: not reachable") || !strings.Contains(got, "last poll: never") {
		t.Fatalf("/status without bridge:\n%s", got)
	}
}

func TestFlowsAndUsersCommands(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	if got := env.send(t, "/flows"); !strings.HasPrefix(got, "no flows yet") {
		t.Fatalf("/flows empty = %q", got)
	}
	if got := env.send(t, "/users"); !strings.HasPrefix(got, "no users yet") {
		t.Fatalf("/users empty = %q", got)
	}
	linty := flow.New("Zeta")
	saveFlows(t, testFlow("Apply HDFC"), linty)
	record := addUser(t, env.bot.secrets, "Bob Builder", testPAN)

	got := env.send(t, "/flows")
	if !strings.Contains(got, "flows (2):\n• Apply HDFC\n• Zeta (1 lint issue(s))") {
		t.Fatalf("/flows = %q", got)
	}
	got = env.send(t, "/users")
	if !strings.Contains(got, "users (1):\n• Bob Builder · ABCDE••••F · HDFC") {
		t.Fatalf("/users = %q", got)
	}
	for _, secret := range []string{record.UserID, record.Password, testPAN} {
		if strings.Contains(got, secret) {
			t.Fatalf("/users leaked %q: %s", secret, got)
		}
	}
}

func TestRunCommandSuccess(t *testing.T) {
	exec := &fakeExecutor{}
	env := newTestEnv(t, exec, nil)
	f := testFlow("Apply HDFC")
	saveFlows(t, f, testFlow("Apply Axis"))
	addUser(t, env.bot.secrets, "Bob", testPAN)

	if got := env.send(t, "/run Nope"); !strings.HasPrefix(got, `unknown flow "Nope"`) {
		t.Fatalf("unknown flow = %q", got)
	}
	if got := env.send(t, "/run Apply HDFC Zed"); !strings.HasPrefix(got, `unknown user "Zed"`) {
		t.Fatalf("unknown user = %q", got)
	}
	if got := env.send(t, "/run"); !strings.HasPrefix(got, "usage:") {
		t.Fatalf("usage = %q", got)
	}
	if got := env.send(t, "/run Apply HDFC Bob"); !strings.HasPrefix(got, "started Apply HDFC as Bob") {
		t.Fatalf("start reply = %q", got)
	}
	env.bot.runner.wait()
	got := env.out.lastText()
	if !strings.Contains(got, "Apply HDFC succeeded at") || !strings.Contains(got, "(as Bob)") || !strings.Contains(got, "3 step(s)") {
		t.Fatalf("summary = %q", got)
	}
	if env.bot.runner.busy() || flow.RunActive() {
		t.Fatal("run lock must be released after the run")
	}
	if cmds := exec.commandLog(); len(cmds) != 1 || cmds[0] != "open " {
		t.Fatalf("executor commands = %v", cmds)
	}
	last, err := flow.ReadLastRun()
	if err != nil || last.FlowID != f.ID || last.Source != "telegram" || last.User != "Bob" || !last.Success {
		t.Fatalf("last run = %+v, %v", last, err)
	}
	if got := env.send(t, "/status"); !strings.Contains(got, "last run: Apply HDFC succeeded") {
		t.Fatalf("/status after run:\n%s", got)
	}
}

func TestRunCommandFailureSendsScreenshot(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{fail: true, shot: []byte("png-bytes")}, nil)
	saveFlows(t, testFlow("Apply"))
	if got := env.send(t, "/run apply"); !strings.HasPrefix(got, "started Apply —") {
		t.Fatalf("start reply = %q", got)
	}
	env.bot.runner.wait()
	if got := env.out.lastText(); !strings.Contains(got, "Apply failed at") || !strings.Contains(got, "open failed: element not found") {
		t.Fatalf("summary = %q", got)
	}
	docs := env.out.allDocs()
	if len(docs) != 1 || string(docs[0].content) != "png-bytes" || docs[0].caption != "open failed: element not found" || !strings.HasSuffix(docs[0].name, ".png") {
		t.Fatalf("docs = %+v", docs)
	}
	if env.bot.runner.busy() || flow.RunActive() {
		t.Fatal("run lock must be released after a failure")
	}
}

func TestRunBusyAndStopCancels(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{block: true}, nil)
	saveFlows(t, testFlow("Slow"))
	if got := env.send(t, "/stop"); got != "no run in progress" {
		t.Fatalf("/stop idle = %q", got)
	}
	if got := env.send(t, "/run Slow"); !strings.HasPrefix(got, "started Slow") {
		t.Fatalf("start reply = %q", got)
	}
	if got := env.send(t, "/run Slow"); got != busyText {
		t.Fatalf("second /run = %q", got)
	}
	if got := env.send(t, "/status"); !strings.Contains(got, "flow run: Slow running for") {
		t.Fatalf("/status during run:\n%s", got)
	}
	if got := env.send(t, "/stop"); got != "stopping the current run…" {
		t.Fatalf("/stop = %q", got)
	}
	env.bot.runner.wait()
	if got := env.out.lastText(); !strings.Contains(got, "Slow was cancelled") {
		t.Fatalf("cancel summary = %q", got)
	}
	if env.bot.runner.busy() || flow.RunActive() {
		t.Fatal("run lock must be released after cancellation")
	}
	if got := env.send(t, "/stop"); got != "no run in progress" {
		t.Fatalf("/stop after cancel = %q", got)
	}
}

func TestRunnerRunSyncReportsBusy(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{block: true}, nil)
	f := testFlow("Slow")
	saveFlows(t, f)
	env.send(t, "/run Slow")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := env.bot.runner.runSync(ctx, flow.RunRequest{Flow: f}); err != errRunBusy {
		t.Fatalf("runSync while busy = %v, want errRunBusy", err)
	}
	if info, ok := env.bot.runner.current(); !ok || info.flow != "Slow" {
		t.Fatalf("current = %+v, %v", info, ok)
	}
}

func TestScheduleCommands(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	f := testFlow("Apply HDFC")
	saveFlows(t, f)
	addUser(t, env.bot.secrets, "Bob", testPAN)

	if got := env.send(t, "/schedules"); got != "no scheduled runs" {
		t.Fatalf("/schedules empty = %q", got)
	}
	if got := env.send(t, "/schedule Apply HDFC Bob"); !strings.HasPrefix(got, "usage:") {
		t.Fatalf("missing time = %q", got)
	}
	if got := env.send(t, "/schedule Apply HDFC Bob soon"); !strings.Contains(got, "could not parse time") {
		t.Fatalf("bad time = %q", got)
	}
	if got := env.send(t, "/schedule Apply HDFC Bob 2020-01-01 10:00"); !strings.HasSuffix(got, "is in the past") {
		t.Fatalf("past time = %q", got)
	}
	if got := env.send(t, "/schedule Apply HDFC Zed +15m"); !strings.HasPrefix(got, `unknown user "Zed"`) {
		t.Fatalf("unknown user = %q", got)
	}
	got := env.send(t, "/schedule Apply HDFC Bob +15m")
	want := "Apply HDFC as Bob at " + env.now.Add(15*time.Minute).Local().Format("2006-01-02 15:04")
	if !strings.HasPrefix(got, "scheduled ") || !strings.HasSuffix(got, want) {
		t.Fatalf("/schedule = %q, want suffix %q", got, want)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(got, "scheduled "), ": "+want)
	if got := env.send(t, "/schedule Apply HDFC - 2099-01-01 09:30"); !strings.Contains(got, "Apply HDFC at 2099-01-01 09:30") {
		t.Fatalf("/schedule without user = %q", got)
	}
	entries, err := env.bot.schedules.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	if entries[0].ID != id || entries[0].FlowID != f.ID || entries[0].UserPAN != testPAN || entries[0].UserName != "Bob" || entries[0].Source != "telegram" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if entries[1].UserPAN != "" {
		t.Fatalf("entry without user = %+v", entries[1])
	}
	if got := env.send(t, "/schedules"); !strings.HasPrefix(got, "schedules (2 pending, 2 total):") || !strings.Contains(got, id+" · Apply HDFC as Bob") {
		t.Fatalf("/schedules = %q", got)
	}
	if got := env.send(t, "/unschedule nope"); !strings.HasPrefix(got, `no schedule matches "nope"`) {
		t.Fatalf("/unschedule unknown = %q", got)
	}
	if got := env.send(t, "/unschedule "+id[:4]); got != "cancelled schedule "+id[:4] {
		t.Fatalf("/unschedule = %q", got)
	}
	if got := env.send(t, "/unschedule "+id); !strings.HasPrefix(got, "cannot cancel:") {
		t.Fatalf("/unschedule twice = %q", got)
	}
	entries, _ = env.bot.schedules.List()
	if entries[0].Status != schedule.StatusCancelled {
		t.Fatalf("entry after cancel = %+v", entries[0])
	}
	if got := env.send(t, "/schedules"); !strings.HasPrefix(got, "schedules (1 pending, 2 total):") {
		t.Fatalf("/schedules after cancel = %q", got)
	}
}

func TestScreenshotCommand(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, fakeBridge(t, true, []byte("png!")))
	env.bot.handle(t.Context(), env.message("/screenshot"), env.now)
	env.bot.wait()
	docs := env.out.allDocs()
	if len(docs) != 1 || string(docs[0].content) != "png!" || docs[0].caption != "Test screenshot" || !strings.HasPrefix(docs[0].name, "screenshot-") {
		t.Fatalf("docs = %+v", docs)
	}

	empty := newTestEnv(t, &fakeExecutor{}, fakeBridge(t, true, nil))
	empty.bot.handle(t.Context(), empty.message("/screenshot"), empty.now)
	empty.bot.wait()
	if got := empty.out.lastText(); got != "screenshot failed: the extension returned an empty screenshot" {
		t.Fatalf("empty screenshot reply = %q", got)
	}

	down := newTestEnv(t, &fakeExecutor{}, nil)
	down.bot.handle(t.Context(), down.message("/screenshot"), down.now)
	down.bot.wait()
	if got := down.out.lastText(); !strings.HasPrefix(got, "screenshot failed: extension bridge is not running") {
		t.Fatalf("bridge down reply = %q", got)
	}
}

func TestMatchFlowPrefersLongestName(t *testing.T) {
	env := newTestEnv(t, &fakeExecutor{}, nil)
	saveFlows(t, testFlow("Apply"), testFlow("Apply HDFC"), testFlow("Login"))
	cases := []struct {
		args []string
		flow string
		rest string
	}{
		{[]string{"Apply", "HDFC", "Bob"}, "Apply HDFC", "Bob"},
		{[]string{"Apply", "Bob"}, "Apply", "Bob"},
		{[]string{"apply", "hdfc"}, "Apply HDFC", ""},
		{[]string{"Log", "Bob", "Smith"}, "Login", "Bob Smith"},
	}
	for _, tc := range cases {
		f, rest, err := env.bot.matchFlow(tc.args)
		if err != nil || f.Name != tc.flow || strings.Join(rest, " ") != tc.rest {
			t.Errorf("matchFlow(%v) = %q, %v, %v; want %q, %q", tc.args, f.Name, rest, err, tc.flow, tc.rest)
		}
	}
	if _, _, err := env.bot.matchFlow([]string{"Nothing"}); err == nil {
		t.Fatal("matchFlow should fail for unknown names")
	}
}

func TestMaskPAN(t *testing.T) {
	if got := maskPAN("abcde1234f"); got != "ABCDE••••F" {
		t.Fatalf("maskPAN = %q", got)
	}
	if got := maskPAN("short"); strings.ContainsAny(got, "short") {
		t.Fatalf("invalid PAN must be fully masked, got %q", got)
	}
}
