package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/flow"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/telegram"
	"autoipo/internal/users"
)

const (
	staleAfter           = 5 * time.Minute
	screenshotTimeout    = 20 * time.Second
	unauthorizedLogEvery = time.Hour
)

// alwaysFresh lists the commands answered even when the message is older
// than staleAfter: they are harmless and useful after a long sleep.
var alwaysFresh = map[string]bool{"ping": true, "status": true, "help": true}

const helpText = `Commands:
/ping — liveness check
/status — daemon, bridge, current and last run
/flows — list flows
/users — list users (masked)
/run <flow> [user] — run a flow now
/stop — cancel the running flow
/schedule <flow> <user|-> <time> — schedule a run; time is HH:MM, +15m, +2h or YYYY-MM-DD HH:MM
/schedules — list scheduled runs
/unschedule <id> — cancel a scheduled run
/screenshot — capture the target tab
/help — this list`

// outbox is the daemon's outbound Telegram channel. sender implements it;
// tests substitute a recorder.
type outbox interface {
	text(text string)
	document(filename string, content []byte, caption string)
}

// bot interprets incoming Telegram messages. handle is called from the poll
// loop; long-running work (flow runs, screenshots) happens in goroutines.
type bot struct {
	label     string
	version   string
	chatID    int64
	log       *logger
	out       outbox
	stats     *stats
	runner    *runner
	flows     *flow.Store
	schedules *schedule.Store
	secrets   secrets.Store
	bridge    *bridge.Client

	mu           sync.Mutex
	unauthorized map[int64]time.Time // chat ID → last time it was logged
	wg           sync.WaitGroup      // background command work
}

// handle filters one message (chat, staleness) and dispatches its command.
func (b *bot) handle(ctx context.Context, msg telegram.Message, now time.Time) {
	if msg.Chat.ID != b.chatID {
		b.noteUnauthorized(msg.Chat.ID, now)
		return
	}
	command, args := telegram.ParseCommand(msg.Text)
	if command == "" {
		return
	}
	if msg.Date > 0 && !alwaysFresh[command] {
		if age := now.Sub(time.Unix(msg.Date, 0)); age > staleAfter {
			b.log.Printf("ignoring stale /%s (age %s)", command, formatDuration(age))
			b.out.text(fmt.Sprintf("STALE ignored (age %s)", formatDuration(age)))
			return
		}
	}
	b.log.Printf("command /%s (%d argument(s), message %d)", command, len(args), msg.MessageID)
	b.stats.recordCommand(now, command)
	b.dispatch(ctx, command, args, now)
}

// noteUnauthorized logs a foreign chat at most once per hour per chat.
func (b *bot) noteUnauthorized(chatID int64, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if last, seen := b.unauthorized[chatID]; seen && now.Sub(last) < unauthorizedLogEvery {
		return
	}
	if b.unauthorized == nil {
		b.unauthorized = map[int64]time.Time{}
	}
	b.unauthorized[chatID] = now
	b.log.Printf("ignoring message from unauthorized chat %d", chatID)
}

func (b *bot) dispatch(ctx context.Context, command string, args []string, now time.Time) {
	switch command {
	case "ping":
		b.out.text(fmt.Sprintf("pong %s · up %s", b.label, formatDuration(b.stats.uptime(now))))
	case "status":
		b.out.text(b.statusText(ctx, now))
	case "help":
		b.out.text(helpText)
	case "flows":
		b.out.text(b.flowsText())
	case "users":
		b.out.text(b.usersText())
	case "run":
		b.runCommand(ctx, args)
	case "stop":
		b.stopCommand()
	case "schedule":
		b.scheduleCommand(args, now)
	case "schedules":
		b.out.text(b.schedulesText())
	case "unschedule":
		b.unscheduleCommand(args)
	case "screenshot":
		b.screenshotCommand(ctx, now)
	default:
		b.out.text(fmt.Sprintf("unknown command /%s — send /help for the list", command))
	}
}

// wait blocks until background command work has finished.
func (b *bot) wait() {
	b.wg.Wait()
}

func (b *bot) statusText(ctx context.Context, now time.Time) string {
	lines := statusLines(b.stats.snapshot(), now)
	lines = append(lines, bridgeLine(ctx, b.bridge), b.runLine(now), lastRunLine(), schedulesLine(b.schedules))
	return strings.Join(lines, "\n")
}

func (b *bot) runLine(now time.Time) string {
	if info, ok := b.runner.current(); ok {
		who := ""
		if info.user != "" {
			who = " as " + info.user
		}
		return fmt.Sprintf("flow run: %s%s running for %s", info.flow, who, formatDuration(now.Sub(info.startedAt)))
	}
	if flow.RunActive() {
		return "flow run: active in another process (desktop app)"
	}
	return "flow run: none"
}

func (b *bot) flowsText() string {
	flows, warnings, err := b.flows.Load()
	if err != nil {
		return "cannot read flows: " + err.Error()
	}
	if len(flows) == 0 && len(warnings) == 0 {
		return "no flows yet — create one in the desktop app"
	}
	sort.Slice(flows, func(i, j int) bool { return strings.ToLower(flows[i].Name) < strings.ToLower(flows[j].Name) })
	lines := []string{fmt.Sprintf("flows (%d):", len(flows))}
	for _, f := range flows {
		line := "• " + f.Name
		if issues := len(f.Lint()); issues > 0 {
			line += fmt.Sprintf(" (%d lint issue(s))", issues)
		}
		lines = append(lines, line)
	}
	if len(warnings) > 0 {
		lines = append(lines, fmt.Sprintf("%d invalid flow(s) skipped", len(warnings)))
	}
	return strings.Join(lines, "\n")
}

func (b *bot) usersText() string {
	store, err := b.openUsers()
	if err != nil {
		return "cannot read users: " + err.Error()
	}
	records := store.ListSummaries() // never touches the secrets store
	if len(records) == 0 {
		return "no users yet — add one in the desktop app"
	}
	lines := []string{fmt.Sprintf("users (%d):", len(records))}
	for _, record := range records {
		lines = append(lines, fmt.Sprintf("• %s · %s · %s", record.Name, maskPAN(record.PAN), record.BankName))
	}
	return strings.Join(lines, "\n")
}

func (b *bot) runCommand(ctx context.Context, args []string) {
	if len(args) == 0 {
		b.out.text("usage: /run <flow> [user]")
		return
	}
	f, rest, err := b.matchFlow(args)
	if err != nil {
		b.out.text(err.Error())
		return
	}
	req := flow.RunRequest{Flow: f, Source: "telegram"}
	if len(rest) > 0 {
		record, err := b.findUser(strings.Join(rest, " "))
		if err != nil {
			b.out.text(err.Error())
			return
		}
		req.UserValues, req.UserLabel = record.Placeholders(), record.Name
	}
	if flow.RunActive() {
		b.out.text(busyText)
		return
	}
	// start only fails with errRunBusy: the slot was taken since the check above.
	if err := b.runner.start(ctx, req, func(result flow.RunResult, err error) { reportRun(b.out, req, result, err) }); err != nil {
		b.out.text(busyText)
		return
	}
	b.out.text(fmt.Sprintf("started %s — the result will follow when it finishes", describeRun(req)))
}

func (b *bot) stopCommand() {
	switch {
	case b.runner.stop():
		b.out.text("stopping the current run…")
	case flow.RunActive():
		b.out.text("no run in progress here — the desktop app is running a flow; stop it there")
	default:
		b.out.text("no run in progress")
	}
}

func (b *bot) scheduleCommand(args []string, now time.Time) {
	const usage = "usage: /schedule <flow> <user|-> <time> (time: HH:MM, +15m, +2h or YYYY-MM-DD HH:MM)"
	if len(args) < 3 {
		b.out.text(usage)
		return
	}
	f, rest, err := b.matchFlow(args)
	if err != nil {
		b.out.text(err.Error())
		return
	}
	if len(rest) < 2 {
		b.out.text(usage)
		return
	}
	at, err := schedule.ParseAt(strings.Join(rest[1:], " "), now)
	if err != nil {
		b.out.text(err.Error())
		return
	}
	if at.Before(now) {
		b.out.text(fmt.Sprintf("%s is in the past", at.Local().Format("2006-01-02 15:04")))
		return
	}
	entry := schedule.Entry{FlowID: f.ID, FlowName: f.Name, At: at, Source: "telegram"}
	if rest[0] != "-" {
		record, err := b.findUser(rest[0])
		if err != nil {
			b.out.text(err.Error())
			return
		}
		entry.UserPAN, entry.UserName = record.PAN, record.Name
	}
	saved, err := b.schedules.Add(entry)
	if err != nil {
		b.out.text("cannot save schedule: " + err.Error())
		return
	}
	who := ""
	if saved.UserName != "" {
		who = " as " + saved.UserName
	}
	b.out.text(fmt.Sprintf("scheduled %s: %s%s at %s", saved.ID, f.Name, who, at.Local().Format("2006-01-02 15:04")))
}

func (b *bot) schedulesText() string {
	entries, err := b.schedules.List()
	if err != nil {
		return "cannot read schedules: " + err.Error()
	}
	if len(entries) == 0 {
		return "no scheduled runs"
	}
	pending := 0
	lines := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if entry.Status == schedule.StatusPending {
			pending++
		}
		lines = append(lines, entry.Summary())
	}
	header := fmt.Sprintf("schedules (%d pending, %d total):", pending, len(entries))
	return strings.Join(append([]string{header}, lines...), "\n")
}

func (b *bot) unscheduleCommand(args []string) {
	if len(args) != 1 {
		b.out.text("usage: /unschedule <id>")
		return
	}
	err := b.schedules.Cancel(args[0])
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		b.out.text(fmt.Sprintf("no schedule matches %q — send /schedules for the list", args[0]))
	case err != nil:
		b.out.text("cannot cancel: " + err.Error())
	default:
		b.out.text("cancelled schedule " + args[0])
	}
}

func (b *bot) screenshotCommand(ctx context.Context, now time.Time) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(ctx, screenshotTimeout)
		defer cancel()
		png, err := captureScreenshot(ctx, b.bridge)
		if err != nil {
			b.out.text("screenshot failed: " + err.Error())
			return
		}
		b.out.document("screenshot-"+now.Format("20060102-150405")+".png", png, b.label+" screenshot")
	}()
}

// captureScreenshot asks the extension for a PNG of the target tab.
func captureScreenshot(ctx context.Context, client *bridge.Client) ([]byte, error) {
	result, err := client.Send(ctx, bridge.Request{Type: bridge.TypeScreenshot})
	if err != nil {
		return nil, err
	}
	png, err := bridge.DecodeScreenshot(result)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot: %w", err)
	}
	if len(png) == 0 {
		return nil, errors.New("the extension returned an empty screenshot")
	}
	return png, nil
}

// matchFlow resolves the longest prefix of args that names a flow (flow
// names may contain spaces) and returns the remaining arguments.
func (b *bot) matchFlow(args []string) (flow.Flow, []string, error) {
	flows, _, err := b.flows.Load()
	if err != nil {
		return flow.Flow{}, nil, fmt.Errorf("cannot read flows: %w", err)
	}
	for n := len(args); n > 0; n-- {
		if f, ok := flow.Find(flows, strings.Join(args[:n], " ")); ok {
			return f, args[n:], nil
		}
	}
	return flow.Flow{}, nil, fmt.Errorf("unknown flow %q — send /flows for the list", strings.Join(args, " "))
}

func (b *bot) openUsers() (*users.Store, error) {
	return users.Open(paths.UsersFile(), b.secrets)
}

// findUser matches a PAN or a (prefix of a) user name.
func (b *bot) findUser(query string) (users.Record, error) {
	store, err := b.openUsers()
	if err != nil {
		return users.Record{}, fmt.Errorf("cannot read users: %w", err)
	}
	record, ok := store.Find(query)
	if !ok {
		return users.Record{}, fmt.Errorf("unknown user %q — send /users for the list", query)
	}
	return record, nil
}
