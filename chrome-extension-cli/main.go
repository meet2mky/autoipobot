// Command chrome-extension-cli runs the loopback bridge between local Go
// programs and the AutoIPO Chrome extension ("serve") and is the command line
// client for that bridge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/paths"
)

// Exit codes returned by run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

const (
	jsonFlag        = "--json"
	maxSleepSeconds = 86400
)

const usageText = `Usage: chrome-extension-cli [--json] <command> [arguments]

Bridge:
  serve                         Run the local bridge the Chrome extension connects to
  status                        Show whether the bridge and the extension are up

Extension commands (need "serve" running and Chrome open with the extension):
  set-url <url>                 Save the website URL the extension opens
  open                          Open the saved URL in a new tab
  click <selector>              Click the element matching a CSS selector
  fill <selector> <value>       Type a value into the matching element
  dropdown <selector>           List the choices of a dropdown or autocomplete field
  choose <selector> <value>     Pick a choice by label or value
  press-key <key>               Press a key (Escape, Enter, Tab, ArrowDown, ...)
  read-text <selector>          Print the text of the matching element
  list-selectors                List the visible clickable and fillable elements
  page-contains <selector>      Print true or false
  screenshot [--out <file>]     Save a PNG of the page and print its path
                                (default: .data/screenshots/<YYYYMMDD-HHMMSS>.png)

Local:
  random-sleep <max-seconds>    Sleep for a random time up to max-seconds (<= 86400)
  help                          Show this help

Options:
  --json                        Print the raw bridge result (or status) as JSON;
                                for screenshot the "output" field holds the saved path

Environment:
  AUTOIPO_BRIDGE_PORT           Loopback port of the bridge (default 18765)
  AUTOIPO_EXTENSION_ID          Only accept WebSocket connections from this extension ID
  AUTOIPO_HOME                  Root directory holding .data (session token, screenshots)

Exit codes: 0 success, 1 bridge or command failure, 2 usage error.`

// newClient creates the bridge client used by the commands; tests replace it.
var newClient = bridge.NewClient

// screenshotDir returns the default directory for screenshots; tests replace it.
var screenshotDir = func() string { return filepath.Join(paths.DataDir(), "screenshots") }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with args (without the program name) and returns the
// process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	args, asJSON := stripFlag(args, jsonFlag)
	p := printer{stdout: stdout, stderr: stderr, asJSON: asJSON}
	if len(args) == 0 {
		fmt.Fprintln(stderr, usageText)
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	name, rest := args[0], args[1:]
	switch name {
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, usageText)
		return exitOK
	case "serve":
		if len(rest) != 0 {
			return p.usageError(errors.New("serve takes no arguments"))
		}
		return runServe(ctx, stderr)
	case "status":
		if len(rest) != 0 {
			return p.usageError(errors.New("status takes no arguments"))
		}
		return runStatus(ctx, p)
	case "random-sleep":
		return runRandomSleep(ctx, rest, p)
	case "screenshot":
		return runScreenshot(ctx, rest, p)
	default:
		return runBridgeCommand(ctx, name, rest, p)
	}
}

// stripFlag removes every occurrence of flag from args and reports whether it
// was present.
func stripFlag(args []string, flag string) ([]string, bool) {
	kept := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		kept = append(kept, arg)
	}
	return kept, found
}

// printer renders command outcomes as human-readable text or as JSON.
type printer struct {
	stdout io.Writer
	stderr io.Writer
	asJSON bool
}

// success prints text, or result as JSON, and returns exitOK.
func (p printer) success(result bridge.Result, text string) int {
	if p.asJSON {
		return p.json(result)
	}
	fmt.Fprintln(p.stdout, text)
	return exitOK
}

// fail reports err on stderr and returns exitFailure. In JSON mode the failed
// result (or one synthesized from err) is printed on stdout first so callers
// always receive one JSON object.
func (p printer) fail(result bridge.Result, err error) int {
	if p.asJSON {
		if result.Error == "" {
			result = bridge.Result{OK: false, Error: err.Error()}
		}
		p.json(result)
	}
	var commandErr *bridge.CommandError
	switch {
	case errors.As(err, &commandErr):
		fmt.Fprintf(p.stderr, "extension command failed: %v\n", commandErr)
	case errors.Is(err, bridge.ErrUnreachable):
		fmt.Fprintf(p.stderr, "chrome-extension-cli: %v\nStart the bridge first: chrome-extension-cli serve\n", err)
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(p.stderr, "chrome-extension-cli: interrupted")
	default:
		fmt.Fprintf(p.stderr, "chrome-extension-cli: %v\n", err)
	}
	return exitFailure
}

// usageError reports a command line mistake and returns exitUsage.
func (p printer) usageError(err error) int {
	fmt.Fprintf(p.stderr, "chrome-extension-cli: %v\nRun \"chrome-extension-cli help\" for usage.\n", err)
	return exitUsage
}

// json prints value as one JSON line on stdout.
func (p printer) json(value any) int {
	data, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(p.stderr, "chrome-extension-cli: encode JSON: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(p.stdout, string(data))
	return exitOK
}

// Commands -------------------------------------------------------------------

// runServe runs the bridge until ctx is cancelled (SIGINT/SIGTERM).
func runServe(ctx context.Context, stderr io.Writer) int {
	logger := log.New(stderr, "", log.LstdFlags)
	token, err := bridge.EnsureToken()
	if err != nil {
		logger.Printf("create bridge session token: %v", err)
		return exitFailure
	}
	cfg := defaultServerConfig()
	cfg.token = token
	cfg.extensionID = strings.TrimSpace(os.Getenv(envExtensionID))
	cfg.logger = logger
	server, err := newBridgeServer(cfg)
	if err != nil {
		logger.Printf("configure bridge: %v", err)
		return exitFailure
	}
	listener, err := server.listen()
	if err != nil {
		logger.Printf("%v (is another bridge already running?)", err)
		return exitFailure
	}
	logger.Printf("extension bridge listening on %s (protocol v%d)", listener.Addr(), bridge.ProtocolVersion)
	logger.Printf("session token file: %s", paths.BridgeTokenFile())
	if cfg.extensionID != "" {
		logger.Printf("accepting only extension %s", cfg.extensionID)
	}
	logger.Printf("press Ctrl+C to stop")
	if err := server.serve(ctx, listener); err != nil {
		logger.Printf("%v", err)
		return exitFailure
	}
	return exitOK
}

// runBridgeCommand sends one of the bridge.CLICommands to the extension.
func runBridgeCommand(ctx context.Context, name string, args []string, p printer) int {
	if _, known := bridge.CommandArgCount(name); !known {
		return p.usageError(fmt.Errorf("unknown command %q", name))
	}
	request, err := bridge.BuildRequest(name, args)
	if err != nil {
		return p.usageError(err)
	}
	result, err := newClient().Send(ctx, request)
	if err != nil {
		return p.fail(result, err)
	}
	return p.success(result, bridge.FormatResult(request, result))
}

func runStatus(ctx context.Context, p printer) int {
	client := newClient()
	status, err := client.Status(ctx)
	if err != nil {
		return p.fail(bridge.Result{}, err)
	}
	if p.asJSON {
		return p.json(status)
	}
	fmt.Fprintln(p.stdout, formatStatus(status, client.BaseURL))
	return exitOK
}

// formatStatus renders a bridge.Status as a few human-readable lines.
func formatStatus(status bridge.Status, baseURL string) string {
	extension := "not connected"
	if status.ExtensionConnected {
		extension = "connected"
		if status.ExtensionOrigin != "" {
			extension += " (" + status.ExtensionOrigin + ")"
		}
	}
	last := status.LastCommandAt
	if last == "" {
		last = "never"
	}
	uptime := time.Duration(status.UptimeSeconds) * time.Second
	return strings.Join([]string{
		fmt.Sprintf("bridge: serving at %s (protocol v%d, up %s)", baseURL, status.ProtocolVersion, uptime),
		"extension: " + extension,
		fmt.Sprintf("pending commands: %d", status.PendingCommands),
		"last command: " + last,
	}, "\n")
}

// runRandomSleep sleeps for a uniformly random duration in (0, max-seconds].
func runRandomSleep(ctx context.Context, args []string, p printer) int {
	if len(args) != 1 {
		return p.usageError(errors.New("random-sleep expects 1 argument: <max-seconds>"))
	}
	maxSeconds, err := strconv.ParseFloat(args[0], 64)
	if err != nil || !(maxSeconds > 0 && maxSeconds <= maxSleepSeconds) {
		return p.usageError(fmt.Errorf("max-seconds must be a number greater than 0 and at most %d", maxSleepSeconds))
	}
	duration := time.Duration(rand.Float64() * maxSeconds * float64(time.Second))
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return p.fail(bridge.Result{}, ctx.Err())
	}
	text := fmt.Sprintf("Slept for %.2f seconds", duration.Seconds())
	return p.success(bridge.Result{OK: true, Output: text}, text)
}

// runScreenshot captures the page and stores the PNG; the saved path is
// printed and also returned in Result.Output for JSON consumers.
func runScreenshot(ctx context.Context, args []string, p printer) int {
	outPath, err := parseScreenshotArgs(args)
	if err != nil {
		return p.usageError(err)
	}
	result, err := newClient().Send(ctx, bridge.Request{Type: bridge.TypeScreenshot})
	if err != nil {
		return p.fail(result, err)
	}
	png, err := bridge.DecodeScreenshot(result)
	if err != nil {
		return p.fail(bridge.Result{}, fmt.Errorf("decode screenshot: %w", err))
	}
	if len(png) == 0 {
		return p.fail(bridge.Result{}, errors.New("the extension returned no screenshot data"))
	}
	path, err := saveScreenshot(outPath, png, time.Now())
	if err != nil {
		return p.fail(bridge.Result{}, err)
	}
	result.Output = path
	return p.success(result, path)
}

// parseScreenshotArgs extracts the optional --out <file> (or --out=<file>).
func parseScreenshotArgs(args []string) (string, error) {
	var outPath string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--out":
			if i+1 >= len(args) {
				return "", errors.New("--out requires a file path")
			}
			i++
			outPath = args[i]
		case strings.HasPrefix(arg, "--out="):
			outPath = strings.TrimPrefix(arg, "--out=")
		default:
			return "", fmt.Errorf("screenshot: unexpected argument %q", arg)
		}
		if outPath == "" {
			return "", errors.New("--out requires a file path")
		}
	}
	return outPath, nil
}

// saveScreenshot writes png (0600) to path, or to a timestamped file in
// screenshotDir when path is empty, and returns the file written.
func saveScreenshot(path string, png []byte, now time.Time) (string, error) {
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", fmt.Errorf("create screenshot directory: %w", err)
		}
		if err := os.WriteFile(path, png, 0o600); err != nil {
			return "", fmt.Errorf("write screenshot: %w", err)
		}
		return path, nil
	}
	dir := screenshotDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create screenshot directory: %w", err)
	}
	stamp := now.Format("20060102-150405")
	for attempt := 0; attempt < 1000; attempt++ {
		name := stamp + ".png"
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d.png", stamp, attempt)
		}
		candidate := filepath.Join(dir, name)
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create screenshot file: %w", err)
		}
		_, writeErr := file.Write(png)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return "", fmt.Errorf("write screenshot: %w", err)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no free screenshot file name for %s in %s", stamp, dir)
}
