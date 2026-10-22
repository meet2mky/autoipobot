package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoipo/internal/paths"
)

// fakeExec records commands and answers page probes from a table.
type fakeExec struct {
	mu        sync.Mutex
	commands  []string
	contains  map[string][]bool // selector -> successive answers
	outputs   map[string]string // command name -> output
	failOn    string
	served    int
	shot      []byte
	probeHits int
}

func (f *fakeExec) PageContains(_ context.Context, selector string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probeHits++
	answers := f.contains[selector]
	if len(answers) == 0 {
		return false, nil
	}
	answer := answers[0]
	if len(answers) > 1 {
		f.contains[selector] = answers[1:]
	}
	return answer, nil
}

func (f *fakeExec) Command(ctx context.Context, name string, args []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, name+" "+strings.Join(args, " "))
	if name == f.failOn {
		return "", errors.New("boom")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return f.outputs[name], nil
}

func (f *fakeExec) EnsureServing(context.Context) error { f.served++; return nil }
func (f *fakeExec) Screenshot(context.Context) ([]byte, error) {
	return f.shot, nil
}

func quiet() Options { return Options{MaxVisits: 50} }

func linearFlow() Flow {
	f := New("login")
	start := f.StartNode()
	start.PlaceholderText = "@url=https://bank.example\n@user_id=default"
	open := Node{ID: "open", Type: TypeCommand, Name: "Open", Command: "set-url", Args: []string{"@url"}, NextID: "fill"}
	fill := Node{ID: "fill", Type: TypeCommand, Name: "Fill", Command: "fill", Args: []string{"#user", "@user_id"}, NextID: "check"}
	check := Node{ID: "check", Type: TypeQualifier, Name: "Logged in?", Expression: `pageContains("#dashboard") AND NOT pageContains("#error")`, Retries: 2, SuccessID: f.SuccessID, FallbackID: f.TerminalID}
	start.NextID = "open"
	f.Nodes = append(f.Nodes, open, fill, check)
	return f
}

func TestExecuteReachesSuccessWithRetriesAndUserValues(t *testing.T) {
	f := linearFlow()
	exec := &fakeExec{contains: map[string][]bool{"#dashboard": {false, false, true}, "#error": {false}}}
	var lines []string
	outcome := Execute(context.Background(), f, map[string]string{"user_id": "alice"}, exec, quiet(), func(s string) { lines = append(lines, s) })
	if !outcome.Success || outcome.Reason != "" {
		t.Fatalf("outcome = %+v\n%s", outcome, strings.Join(lines, "\n"))
	}
	if len(exec.commands) != 2 || exec.commands[0] != "set-url https://bank.example" || exec.commands[1] != "fill #user alice" {
		t.Fatalf("commands = %v", exec.commands)
	}
	transcript := strings.Join(lines, "\n")
	if strings.Contains(transcript, "alice") {
		t.Fatalf("fill value leaked into log:\n%s", transcript)
	}
	if !strings.Contains(transcript, "attempt=3/3") || !strings.Contains(transcript, "reached Success") {
		t.Fatalf("transcript missing retry/success lines:\n%s", transcript)
	}
	if outcome.Steps != 5 {
		t.Fatalf("steps = %d", outcome.Steps)
	}
}

func TestExecuteFallbackAndFailureScreenshot(t *testing.T) {
	f := linearFlow()
	exec := &fakeExec{contains: map[string][]bool{"#dashboard": {false}}, shot: []byte("png")}
	opts := quiet()
	opts.ScreenshotOnFailure = true
	outcome := Execute(context.Background(), f, nil, exec, opts, nil)
	if outcome.Success || !strings.Contains(outcome.Reason, "reached Terminal") {
		t.Fatalf("outcome = %+v", outcome)
	}
	if string(outcome.Screenshot) != "png" {
		t.Fatal("expected failure screenshot")
	}
	if exec.probeHits != 3 {
		t.Fatalf("expected 3 probe attempts (retries=2), got %d", exec.probeHits)
	}
}

func TestExecuteCommandFailureAndSaveAs(t *testing.T) {
	f := New("read")
	f.StartNode().NextID = "read"
	f.Nodes = append(f.Nodes,
		Node{ID: "read", Type: TypeCommand, Command: "read-text", Args: []string{".total"}, SaveAs: "total", NextID: "echo"},
		Node{ID: "echo", Type: TypeCommand, Command: "fill", Args: []string{"#amount", "@total"}, NextID: f.SuccessID},
	)
	exec := &fakeExec{outputs: map[string]string{"read-text": " 42 "}}
	outcome := Execute(context.Background(), f, nil, exec, quiet(), nil)
	if !outcome.Success || outcome.Values["total"] != "42" || exec.commands[1] != "fill #amount 42" {
		t.Fatalf("outcome = %+v commands=%v", outcome, exec.commands)
	}

	exec = &fakeExec{failOn: "fill"}
	outcome = Execute(context.Background(), f, nil, exec, quiet(), nil)
	if outcome.Success || !strings.Contains(outcome.Reason, "fill failed: boom") || outcome.FinalNodeID != "echo" {
		t.Fatalf("failure outcome = %+v", outcome)
	}
}

func TestExecuteServeRandomSleepAndCancel(t *testing.T) {
	f := New("sleepy")
	f.StartNode().NextID = "serve"
	f.Nodes = append(f.Nodes,
		Node{ID: "serve", Type: TypeCommand, Command: CommandServe, NextID: "sleep"},
		Node{ID: "sleep", Type: TypeCommand, Command: CommandRandomSleep, Args: []string{"0.01"}, NextID: f.SuccessID},
	)
	exec := &fakeExec{}
	if outcome := Execute(context.Background(), f, nil, exec, quiet(), nil); !outcome.Success || exec.served != 1 {
		t.Fatalf("outcome = %+v served=%d", outcome, exec.served)
	}
	bad := f.Clone()
	bad.Nodes[bad.NodeIndex("sleep")].Args = []string{"-1"}
	if outcome := Execute(context.Background(), bad, nil, exec, quiet(), nil); outcome.Success {
		t.Fatal("negative sleep should fail")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := Execute(ctx, f, nil, exec, quiet(), nil)
	if !outcome.Cancelled || outcome.Success || outcome.Reason != "cancelled" {
		t.Fatalf("cancelled outcome = %+v", outcome)
	}
}

func TestExecuteGuardsLoopsAndInvalidExpressions(t *testing.T) {
	f := New("loop")
	f.StartNode().NextID = "a"
	f.Nodes = append(f.Nodes, Node{ID: "a", Type: TypeCommand, Command: "open", NextID: "a"})
	opts := quiet()
	opts.MaxVisits = 5
	if outcome := Execute(context.Background(), f, nil, &fakeExec{}, opts, nil); outcome.Success || !strings.Contains(outcome.Reason, "visit limit") {
		t.Fatalf("loop outcome = %+v", outcome)
	}

	g := New("badexpr")
	g.StartNode().NextID = "q"
	g.Nodes = append(g.Nodes, Node{ID: "q", Type: TypeQualifier, Expression: "pageContains(", SuccessID: g.SuccessID})
	if outcome := Execute(context.Background(), g, nil, &fakeExec{}, quiet(), nil); outcome.Success || !strings.Contains(outcome.Reason, "invalid qualifier") {
		t.Fatalf("bad expression outcome = %+v", outcome)
	}

	h := New("empty")
	if outcome := Execute(context.Background(), h, nil, &fakeExec{}, quiet(), nil); outcome.Success || outcome.Reason != "Start has no next state" {
		t.Fatalf("empty outcome = %+v", outcome)
	}
}

func TestParseExpression(t *testing.T) {
	valid := []string{
		`pageContains("#a")`,
		`PAGECONTAINS("#a") and not pagecontains(".b")`,
		`(pageContains("#a") OR false) AND true`,
		`waitFor("#late", 2.5)`,
		`NOT (pageContains("#x") OR waitFor("#y", 1))`,
		`pageContains("a \"quoted\" b")`,
	}
	for _, source := range valid {
		if _, err := ParseExpression(source); err != nil {
			t.Errorf("ParseExpression(%q) unexpected error: %v", source, err)
		}
	}
	invalid := []string{"", "pageContains()", `pageContains("#a") AND`, `pageContains("#a") banana`, `(pageContains("#a")`, `)`, `waitFor("#a", 0)`, `waitFor("#a", 9999)`, `pageContains("")`}
	for _, source := range invalid {
		if _, err := ParseExpression(source); err == nil {
			t.Errorf("ParseExpression(%q) should fail", source)
		}
	}
	expr, _ := ParseExpression(`pageContains("#a") AND waitFor("@sel", 1)`)
	if sel := expr.Selectors(); len(sel) != 2 || sel[1] != "@sel" {
		t.Fatalf("Selectors = %v", sel)
	}
}

func TestEvalPrecedenceAndWaitFor(t *testing.T) {
	ctx := context.Background()
	prober := &fakeExec{contains: map[string][]bool{"#t": {true}, "#f": {false}}}
	cases := map[string]bool{
		`false OR true AND false`:                       false, // AND binds tighter
		`(false OR true) AND true`:                      true,
		`NOT pageContains("#f") AND pageContains("#t")`: true,
		`pageContains("@sel")`:                          true,
	}
	for source, want := range cases {
		expr, err := ParseExpression(source)
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.Eval(ctx, prober, map[string]string{"sel": "#t"})
		if err != nil || got != want {
			t.Errorf("Eval(%q) = %v, %v; want %v", source, got, err, want)
		}
	}
	old := WaitPoll
	WaitPoll = time.Millisecond
	defer func() { WaitPoll = old }()
	waiting := &fakeExec{contains: map[string][]bool{"#late": {false, false, true}}}
	expr, _ := ParseExpression(`waitFor("#late", 1)`)
	if got, err := expr.Eval(ctx, waiting, nil); err != nil || !got {
		t.Fatalf("waitFor = %v, %v", got, err)
	}
	never := &fakeExec{}
	expr, _ = ParseExpression(`waitFor("#never", 0.01)`)
	if got, _ := expr.Eval(ctx, never, nil); got {
		t.Fatal("waitFor should time out as false")
	}
}

func TestPlaceholders(t *testing.T) {
	parsed, err := ParsePlaceholders("@username=alice\n# comment\n@display_name=\"My name\"\n\n@empty=")
	if err != nil || parsed["username"] != "alice" || parsed["display_name"] != "My name" || parsed["empty"] != "" {
		t.Fatalf("ParsePlaceholders = %#v, %v", parsed, err)
	}
	if _, err := ParsePlaceholders("username=alice"); err == nil {
		t.Fatal("missing @ should fail")
	}
	if _, err := ParsePlaceholders(`@x="unterminated`); err == nil {
		t.Fatal("bad quote should fail")
	}
	formatted := FormatPlaceholders(map[string]string{"b": "two words", "a": "plain", "c": ""})
	if formatted != "@a=plain\n@b=\"two words\"\n@c=\"\"" {
		t.Fatalf("FormatPlaceholders = %q", formatted)
	}
	round, _ := ParsePlaceholders(formatted)
	if round["b"] != "two words" || round["c"] != "" {
		t.Fatalf("round trip = %#v", round)
	}
	if got := Resolve("user@example.com @user and @missing @user_id", map[string]string{"user": "U", "user_id": "ID"}); got != "user@example.com U and @missing ID" {
		t.Fatalf("Resolve = %q", got)
	}
	if refs := References("@a @b @a x@c"); len(refs) != 2 || refs[0] != "a" || refs[1] != "b" {
		t.Fatalf("References = %v", refs)
	}
	f := linearFlow()
	if refs := f.ReferencedPlaceholders(); strings.Join(refs, ",") != "url,user_id" {
		t.Fatalf("ReferencedPlaceholders = %v", refs)
	}
	if redacted := RedactValues(map[string]string{"password": "x", "user_id": "u", "bank_password": "y", "pin": ""}); redacted["password"] != "[redacted]" || redacted["user_id"] != "u" || redacted["bank_password"] != "[redacted]" || redacted["pin"] != "" {
		t.Fatalf("RedactValues = %v", redacted)
	}
}

func TestModelValidateLintAndEditing(t *testing.T) {
	f := New("edit")
	if err := f.Validate(); err != nil {
		t.Fatalf("new flow should validate: %v", err)
	}
	issues := f.Lint()
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "Start has no next state") {
		t.Fatalf("Lint new = %+v", issues)
	}
	node := NewCommandNode()
	if !f.InsertAfter(f.StartID, "next", node) {
		t.Fatal("InsertAfter failed")
	}
	if f.StartNode().NextID != node.ID || f.Nodes[1].ID != node.ID {
		t.Fatalf("InsertAfter wiring wrong: %+v", f.Nodes)
	}
	q := Node{ID: NewID(), Type: TypeQualifier, Name: "Q", Expression: `pageContains("#x")`}
	if !f.InsertAfter(node.ID, "next", q) {
		t.Fatal("InsertAfter qualifier failed")
	}
	f.Nodes[f.NodeIndex(q.ID)].SuccessID = f.SuccessID
	orphan := Node{ID: "orphan", Type: TypeCommand, Command: "click", NextID: "ghost"}
	f.Nodes = append(f.Nodes, orphan)
	messages := strings.Builder{}
	for _, issue := range f.Lint() {
		messages.WriteString(issue.NodeID + ": " + issue.Message + "\n")
	}
	for _, want := range []string{"orphan: state is unreachable", "orphan: next target points to a missing state", "orphan: click needs argument 1", q.ID + ": no false target"} {
		if !strings.Contains(messages.String(), want) {
			t.Errorf("Lint missing %q in:\n%s", want, messages.String())
		}
	}
	if !f.RemoveNode(node.ID) || f.StartNode().NextID != "" {
		t.Fatal("RemoveNode should clear incoming edges")
	}
	if f.RemoveNode(f.StartID) {
		t.Fatal("fixed nodes must not be removable")
	}
	layers := f.Layers()
	if len(layers[0]) != 1 || layers[0][0].ID != f.StartID {
		t.Fatalf("Layers[0] = %+v", layers[0])
	}

	dup := f.Duplicate("copy")
	if dup.ID == f.ID || dup.StartID == f.StartID || len(dup.Nodes) != len(f.Nodes) {
		t.Fatal("Duplicate must remap IDs")
	}
	if err := dup.Validate(); err != nil {
		t.Fatalf("duplicate validate: %v", err)
	}

	broken := f.Clone()
	broken.Nodes = append(broken.Nodes, Node{ID: NewID(), Type: TypeSuccess})
	if err := broken.Validate(); err == nil {
		t.Fatal("two success nodes must fail validation")
	}
	broken = f.Clone()
	broken.Nodes[broken.NodeIndex("orphan")].Command = "nonsense"
	if err := broken.Validate(); err == nil {
		t.Fatal("unknown command must fail validation")
	}
	if CommandArgCount("fill") != 2 || CommandArgCount(CommandRandomSleep) != 1 || CommandArgCount(CommandServe) != 0 {
		t.Fatal("CommandArgCount")
	}
}

func TestStoreRoundTripAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flows.json")
	legacy := `[{"id":"f1","name":"Old","runs_to_keep":0,"placeholders":{"username":"legacy"},"start_id":"s","success_id":"ok","terminal_id":"t",
	  "nodes":[{"id":"s","type":"start"},{"id":"ok","type":"success"},{"id":"t","type":"terminal"}]},
	  {"id":"bad","name":"Broken","start_id":"","success_id":"","terminal_id":"","nodes":[]}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := OpenStore(path)
	flows, warnings, err := store.Load()
	if err != nil || len(flows) != 1 || len(warnings) != 1 {
		t.Fatalf("Load = %d flows, %v warnings, %v", len(flows), warnings, err)
	}
	f := flows[0]
	if f.RunsToKeep != DefaultRunsToKeep || f.Placeholders != nil || f.StartNode().Placeholders["username"] != "legacy" || !strings.Contains(f.StartNode().PlaceholderText, "@username=legacy") {
		t.Fatalf("migration result = %+v", f)
	}
	f.Name = "Renamed"
	if err := store.Save([]Flow{f}); err != nil {
		t.Fatal(err)
	}
	again, _, _ := store.Load()
	if again[0].Name != "Renamed" {
		t.Fatal("Save/Load round trip")
	}
	if got, ok := Find(again, "ren"); !ok || got.ID != "f1" {
		t.Fatal("Find by prefix")
	}
	if UniqueName(again, "Renamed") != "Renamed 2" || UniqueName(again, "Fresh") != "Fresh" {
		t.Fatal("UniqueName")
	}
	if err := store.Save([]Flow{{Name: "invalid"}}); err == nil {
		t.Fatal("Save must reject invalid flows")
	}
}

func TestRunWritesLogsLastRunAndHonoursLock(t *testing.T) {
	t.Setenv(paths.EnvRoot, t.TempDir())
	paths.ResetForTesting()
	t.Cleanup(paths.ResetForTesting)

	f := linearFlow()
	f.RunsToKeep = 1
	exec := &fakeExec{contains: map[string][]bool{"#dashboard": {true}, "#error": {false}}}
	var live []string
	result, err := Run(context.Background(), RunRequest{
		Flow: f, UserValues: map[string]string{"user_id": "bob", "password": "pw"}, UserLabel: "Bob", Source: "test",
		OnLine: func(line string) { live = append(live, line) },
	}, exec, quiet())
	if err != nil || !result.Success || result.RunID == "" {
		t.Fatalf("Run = %+v, %v", result, err)
	}
	data, err := os.ReadFile(result.LogPath)
	if err != nil || !strings.Contains(string(data), "finished: SUCCESS") || strings.Contains(string(data), "pw") {
		t.Fatalf("run log = %q, %v", data, err)
	}
	fileLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(live) != len(fileLines) || live[len(live)-1] != fileLines[len(fileLines)-1] {
		t.Fatalf("live sink mismatch: %d live vs %d file lines", len(live), len(fileLines))
	}
	last, err := ReadLastRun()
	if err != nil || last.FlowID != f.ID || !last.Success || last.User != "Bob" || last.Source != "test" {
		t.Fatalf("LastRun = %+v, %v", last, err)
	}
	if !strings.Contains(last.Summary(), "succeeded") {
		t.Fatalf("Summary = %s", last.Summary())
	}

	// Second run prunes down to RunsToKeep=1.
	if _, err := Run(context.Background(), RunRequest{Flow: f}, exec, quiet()); err != nil {
		t.Fatal(err)
	}
	if runs := ListRuns(f.ID); len(runs) != 1 {
		t.Fatalf("expected 1 retained run, got %d", len(runs))
	}

	// Lock contention.
	held, acquired, _ := acquireRunLock()
	if !acquired {
		t.Fatal("test could not take run lock")
	}
	if !RunActive() {
		t.Fatal("RunActive should be true while held")
	}
	_, err = Run(context.Background(), RunRequest{Flow: f}, exec, quiet())
	_ = held.Release()
	if !errors.Is(err, ErrAnotherRunActive) {
		t.Fatalf("Run under lock = %v, want ErrAnotherRunActive", err)
	}
	if err := DeleteRuns(f.ID); err != nil {
		t.Fatal(err)
	}
	if ListRuns(f.ID) != nil {
		t.Fatal("DeleteRuns should remove logs")
	}
}
