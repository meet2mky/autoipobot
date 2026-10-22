// Command autoipo is the Telegram daemon of autoipo. It long-polls the bot
// for commands, runs automation flows through the Chrome extension bridge,
// executes scheduled runs and reports results back to the configured chat.
//
// Usage:
//
//	autoipo              run the daemon in the foreground
//	autoipo configure    save TG_TOKEN, TG_CHAT_ID and HOST_LABEL from the environment
//	autoipo check        verify the saved configuration and the bot token
//	autoipo status       show what the running daemon last reported
//	autoipo version      print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/config"
	"autoipo/internal/daemonctl"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/telegram"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = "dev"

const usageText = `Usage: autoipo [command]

Commands:
  (none)      run the daemon in the foreground
  configure   save TG_TOKEN, TG_CHAT_ID and HOST_LABEL from the environment
  check       verify the saved configuration and the bot token
  status      show what the running daemon last reported
  version     print the version
  help        show this help
`

func main() {
	os.Exit(newCLI(os.Stdout, os.Stderr).run(os.Args[1:]))
}

// cli dispatches the command-line entry points. Its constructors are fields
// so tests can substitute a fake Bot API server and a file secret store.
type cli struct {
	stdout      io.Writer
	stderr      io.Writer
	secrets     func() secrets.Store
	newTelegram func(token string) *telegram.Client
}

func newCLI(stdout, stderr io.Writer) *cli {
	return &cli{stdout: stdout, stderr: stderr, secrets: secrets.Open, newTelegram: telegram.New}
}

// run executes the command named by args and returns the process exit code.
func (c *cli) run(args []string) int {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "":
		return runDaemon(c.stderr, c.secrets())
	case "configure":
		return c.configure()
	case "check":
		return c.check()
	case "status":
		return c.status()
	case "version":
		fmt.Fprintln(c.stdout, versionString())
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(c.stdout, usageText)
		return 0
	default:
		fmt.Fprintf(c.stderr, "autoipo: unknown command %q\n\n%s", command, usageText)
		return 2
	}
}

func versionString() string {
	return fmt.Sprintf("autoipo %s (%s %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// configure saves the settings found in the environment. The token is never
// echoed.
func (c *cli) configure() int {
	settings := config.FromEnvironmentOnly()
	sec := c.secrets()
	if err := config.Save(settings, sec); err != nil {
		fmt.Fprintf(c.stderr, "autoipo configure: %v\nSet %s, %s and optionally %s in the environment.\n",
			err, config.EnvToken, config.EnvChatID, config.EnvLabel)
		return 1
	}
	label := strings.TrimSpace(settings.HostLabel)
	if label == "" {
		label = config.DefaultLabel
	}
	fmt.Fprintf(c.stdout, "Saved Telegram settings:\n  %s=%s\n  %s=%s\n  %s stored in the %s secret store\n",
		config.EnvChatID, strings.TrimSpace(settings.ChatID), config.EnvLabel, label, config.EnvToken, sec.Backend())
	return 0
}

// check reports whether the configuration is complete and the token works.
func (c *cli) check() int {
	sec := c.secrets()
	settings := config.Load(sec)
	if err := settings.Validate(); err != nil {
		fmt.Fprintf(c.stdout, "configuration: incomplete (%v)\n", err)
		fmt.Fprintf(c.stderr, "Run `autoipo configure` with %s and %s set.\n", config.EnvToken, config.EnvChatID)
		return 1
	}
	fmt.Fprintf(c.stdout, "configuration: complete (label %s, token in the %s secret store)\n", settings.HostLabel, sec.Backend())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	me, err := c.newTelegram(settings.Token).GetMe(ctx)
	if err != nil {
		fmt.Fprintf(c.stderr, "telegram: getMe failed: %s\n", scrubber{settings.Token}.clean(err.Error()))
		return 1
	}
	fmt.Fprintf(c.stdout, "bot: @%s (%s)\n", me.Username, me.FirstName)
	return 0
}

// status prints what the daemon last published plus live bridge, last-run
// and schedule information.
func (c *cli) status() int {
	now := time.Now()
	if pid, running := daemonctl.Running(); running {
		fmt.Fprintf(c.stdout, "daemon: running (pid %d)\n", pid)
	} else {
		fmt.Fprintln(c.stdout, "daemon: not running")
	}
	status, err := daemonctl.ReadStatus()
	switch {
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintln(c.stdout, "status: not published yet")
	case err != nil:
		fmt.Fprintf(c.stdout, "status: unreadable (%v)\n", err)
	default:
		fmt.Fprintf(c.stdout, "status: published %s ago\n", formatDuration(now.Sub(status.UpdatedAt)))
		for _, line := range statusLines(status, now) {
			fmt.Fprintln(c.stdout, "  "+line)
		}
	}
	fmt.Fprintln(c.stdout, bridgeLine(context.Background(), bridge.NewClient()))
	fmt.Fprintln(c.stdout, lastRunLine())
	fmt.Fprintln(c.stdout, schedulesLine(schedule.Open(paths.SchedulesFile())))
	return 0
}
