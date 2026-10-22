package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/config"
	"autoipo/internal/daemonctl"
	"autoipo/internal/flow"
	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/telegram"
)

const (
	heartbeatEvery = 10 * time.Minute
	lockWait       = 2 * time.Second
	shutdownDrain  = 5 * time.Second
)

// daemon wires the long-running loops together: Telegram polling, the
// scheduler and the heartbeat. All of them stop when the context passed to
// run ends; run then drains outbound messages and publishes a final status.
type daemon struct {
	label   string
	version string
	log     *logger
	tg      *telegram.Client
	out     *sender
	bot     *bot
	stats   *stats
	runner  *runner
	sched   *scheduler
	bridges *bridgeProcesses
	offsets offsetStore
	now     func() time.Time
	backoff func(retry int) time.Duration
}

// runDaemon is the entry point of `autoipo` without a subcommand. It returns
// the process exit code.
func runDaemon(stderr io.Writer, sec secrets.Store) int {
	settings, chatID, err := loadSettings(sec)
	if err != nil {
		fmt.Fprintf(stderr, "autoipo: cannot start: %v\nRun `autoipo configure` with %s and %s set, or add them to %s.\n",
			err, config.EnvToken, config.EnvChatID, paths.EnvFile())
		return 1
	}
	scrub := scrubber{settings.Token}
	log, closeLog := openLogger(stderr, scrub)
	defer closeLog()

	// Liveness probes (desktop app, deploy.sh) take and release this lock
	// momentarily, so wait briefly instead of failing on the first attempt.
	lock, err := instancelock.Wait(paths.DaemonLockFile(), lockWait)
	if errors.Is(err, instancelock.ErrTimeout) {
		log.Printf("another autoipo daemon is already running (lock %s); exiting", paths.DaemonLockFile())
		return 0
	}
	if err != nil {
		log.Printf("cannot acquire daemon lock: %v", err)
		return 1
	}
	defer func() {
		if err := lock.Release(); err != nil {
			log.Printf("cannot release daemon lock: %v", err)
		}
	}()
	// The PID inside the lock file is what the desktop app and deploy.sh
	// trust (only the holder can write it); .data/autoipo.pid stays for humans.
	if err := lock.WritePID(os.Getpid()); err != nil {
		log.Printf("cannot record PID in lock file: %v", err)
		return 1
	}
	if err := daemonctl.WritePIDFile(os.Getpid()); err != nil {
		log.Printf("cannot write PID file: %v", err)
		return 1
	}
	defer func() {
		if err := daemonctl.RemovePIDFile(); err != nil {
			log.Printf("cannot remove PID file: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		// Restore default signal handling once shutdown has begun so that a
		// second SIGINT/SIGTERM terminates the process immediately.
		<-ctx.Done()
		stop()
	}()
	return newDaemon(settings, chatID, sec, scrub, log).run(ctx)
}

// loadSettings resolves and validates the Telegram settings.
func loadSettings(sec secrets.Store) (config.Settings, int64, error) {
	settings := config.Load(sec)
	if err := settings.Validate(); err != nil {
		return config.Settings{}, 0, err
	}
	chatID, err := settings.ChatIDInt()
	if err != nil {
		return config.Settings{}, 0, err
	}
	return settings, chatID, nil
}

// openLogger returns a logger writing to stderr and the rotating daemon log
// file, falling back to stderr alone when the file cannot be opened.
func openLogger(stderr io.Writer, scrub scrubber) (*logger, func()) {
	file, err := newRotatingWriter(paths.DaemonLogFile(), logMaxSize, logKeep)
	if err != nil {
		log := newLogger(scrub, stderr)
		log.Printf("cannot open %s: %v (logging to stderr only)", paths.DaemonLogFile(), err)
		return log, func() {}
	}
	return newLogger(scrub, stderr, file), func() { _ = file.Close() }
}

func newDaemon(settings config.Settings, chatID int64, sec secrets.Store, scrub scrubber, log *logger) *daemon {
	tg := telegram.New(settings.Token)
	out := newSender(tg, chatID, log)
	st := newStats(daemonctl.Status{
		PID: os.Getpid(), Version: version, Label: settings.HostLabel, ChatID: chatID, StartedAt: time.Now(),
	}, daemonctl.WriteStatus, scrub, log)
	bridges := &bridgeProcesses{log: log}
	run := newRunner(log, bridges.executor, flow.DefaultOptions())
	flows := flow.OpenStore(paths.FlowsFile())
	schedules := schedule.Open(paths.SchedulesFile())
	return &daemon{
		label:   settings.HostLabel,
		version: version,
		log:     log,
		tg:      tg,
		out:     out,
		bot: &bot{
			label: settings.HostLabel, version: version, chatID: chatID, log: log, out: out, stats: st,
			runner: run, flows: flows, schedules: schedules, secrets: sec, bridge: bridge.NewClient(),
		},
		stats:   st,
		runner:  run,
		sched:   &scheduler{store: schedules, flows: flows, secrets: sec, runner: run, out: out, log: log},
		bridges: bridges,
		offsets: offsetStore{path: paths.TelegramOffsetFile()},
		now:     time.Now,
		backoff: backoffDelay,
	}
}

// run executes the daemon until ctx ends and returns the exit code.
func (d *daemon) run(ctx context.Context) int {
	d.log.Printf("autoipo %s starting (label %s, data %s)", d.version, d.label, paths.DataDir())
	d.stats.flush(d.now(), true)
	d.out.start()
	d.out.text(fmt.Sprintf("%s BOOT (autoipo %s)", d.label, d.version))

	var wg sync.WaitGroup
	wg.Go(func() { d.pollLoop(ctx) })
	wg.Go(func() { d.sched.run(ctx) })
	wg.Go(func() { d.heartbeat(ctx) })
	wg.Wait()

	d.log.Printf("shutting down")
	d.runner.wait()
	d.bot.wait()
	d.out.close(shutdownDrain)
	d.bridges.stopAll(shutdownDrain)
	d.stats.flush(d.now(), true)
	d.log.Printf("stopped")
	return 0
}

// heartbeat logs one liveness line every heartbeatEvery.
func (d *daemon) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			snapshot := d.stats.snapshot()
			d.log.Printf("alive uptime=%s polls=%d commands=%d", formatDuration(now.Sub(snapshot.StartedAt)), snapshot.Polls, snapshot.Commands)
		}
	}
}

// bridgeProcesses tracks `chrome-extension-cli serve` children started on
// demand by flow runs so they are stopped when the daemon exits.
type bridgeProcesses struct {
	log   *logger
	mu    sync.Mutex
	procs []*bridge.Process
}

// executor builds the production flow executor for one run.
func (b *bridgeProcesses) executor() flow.Executor {
	exec := flow.NewBridgeExecutor(paths.CLIBinary())
	exec.Started = b.add
	return exec
}

func (b *bridgeProcesses) add(process *bridge.Process) {
	b.mu.Lock()
	b.procs = append(b.procs, process)
	b.mu.Unlock()
	b.log.Printf("started extension bridge (pid %d)", process.PID())
}

// stopAll terminates every bridge this daemon started.
func (b *bridgeProcesses) stopAll(timeout time.Duration) {
	b.mu.Lock()
	procs := b.procs
	b.procs = nil
	b.mu.Unlock()
	for _, process := range procs {
		if !process.Running() {
			continue
		}
		b.log.Printf("stopping extension bridge (pid %d)", process.PID())
		if err := process.Stop(timeout); err != nil {
			b.log.Printf("cannot stop extension bridge: %v", err)
		}
	}
}
