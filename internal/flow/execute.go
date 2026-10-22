package flow

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"autoipo/internal/bridge"
)

// Executor performs the side effects a flow needs. *bridge.Client satisfies
// the page-related methods; see BridgeExecutor for a complete implementation.
type Executor interface {
	Prober
	// Command runs a bridge-backed command by CLI name and returns its
	// human-readable output.
	Command(ctx context.Context, name string, args []string) (string, error)
	// EnsureServing makes sure the bridge and extension are available.
	EnsureServing(ctx context.Context) error
	// Screenshot captures the target tab as an image (PNG or JPEG bytes).
	// Implementations may return (nil, nil) when unsupported.
	Screenshot(ctx context.Context) ([]byte, error)
}

// ScreenshotMode selects when a run captures the page.
type ScreenshotMode int

const (
	// ScreenshotAlways captures when the run reaches Success or Terminal.
	ScreenshotAlways ScreenshotMode = iota
	// ScreenshotOnFailure captures only when the run ends at Terminal.
	ScreenshotOnFailure
	// ScreenshotNever disables capture.
	ScreenshotNever
)

// screenshotTimeout bounds the end-of-run capture.
const screenshotTimeout = 20 * time.Second

// Options tune execution.
type Options struct {
	// MinDelay/MaxDelay bound the random "human-like" pause inserted after
	// every command and between qualifier retries. Zero disables pauses.
	MinDelay time.Duration
	MaxDelay time.Duration
	// MaxVisits caps the number of state visits (loop protection).
	MaxVisits int
	// Screenshots selects when the page is captured at the end of a run
	// (default: on both Success and Terminal, never when cancelled).
	Screenshots ScreenshotMode
	// OnState is invoked when a state becomes current (for UI highlighting).
	OnState func(nodeID string)
}

// DefaultOptions: 2–4 s human-like pauses and a screenshot on every outcome.
func DefaultOptions() Options {
	return Options{MinDelay: 2 * time.Second, MaxDelay: 4 * time.Second, MaxVisits: 1000, Screenshots: ScreenshotAlways}
}

// wantsScreenshot reports whether the mode captures for the given outcome.
func (m ScreenshotMode) wantsScreenshot(success bool) bool {
	switch m {
	case ScreenshotAlways:
		return true
	case ScreenshotOnFailure:
		return !success
	default:
		return false
	}
}

// Outcome summarises a finished run.
type Outcome struct {
	Success       bool
	Cancelled     bool
	FinalNodeID   string
	Steps         int
	Reason        string            // why the run ended (empty on success)
	Values        map[string]string // final placeholder values incl. SaveAs outputs
	Screenshot    []byte            // page capture taken when the run ended (PNG/JPEG), if any
	ScreenshotErr string            // why no screenshot could be taken, if capture was attempted
}

// ErrCancelled is reported when the run context ends.
var ErrCancelled = errors.New("run cancelled")

// Execute runs the flow from Start until Success or Terminal. log receives
// a human-readable transcript; it must not be nil.
func Execute(ctx context.Context, f Flow, values map[string]string, exec Executor, opts Options, log func(string)) Outcome {
	if log == nil {
		log = func(string) {}
	}
	if opts.MaxVisits <= 0 {
		opts.MaxVisits = 1000
	}
	values = MergeValues(f.DefaultValues(), values)
	nodes := f.index()
	outcome := Outcome{Values: values}
	currentID := f.StartID

	finish := func(success bool, reason string) Outcome {
		outcome.Success = success
		outcome.Reason = reason
		outcome.FinalNodeID = currentID
		if !outcome.Cancelled && ctx.Err() == nil && opts.Screenshots.wantsScreenshot(success) {
			// The run context may already be exhausted; capture on its own budget.
			shotCtx, cancel := context.WithTimeout(context.Background(), screenshotTimeout)
			defer cancel()
			image, err := exec.Screenshot(shotCtx)
			switch {
			case err != nil:
				outcome.ScreenshotErr = err.Error()
				log("[Flow] screenshot unavailable: " + err.Error())
			case len(image) == 0:
				outcome.ScreenshotErr = "extension returned an empty screenshot"
				log("[Flow] screenshot unavailable: " + outcome.ScreenshotErr)
			default:
				outcome.Screenshot = image
				log(fmt.Sprintf("[Flow] captured end-of-run screenshot (%d KB)", len(image)/1024))
			}
		}
		return outcome
	}
	cancelled := func() Outcome {
		outcome.Cancelled = true
		log("[Flow] run cancelled")
		return finish(false, "cancelled")
	}

	for visits := 0; visits < opts.MaxVisits; visits++ {
		if ctx.Err() != nil {
			return cancelled()
		}
		node, exists := nodes[currentID]
		if !exists {
			log("[Terminal] transition points to missing state " + currentID)
			return finish(false, "missing state "+currentID)
		}
		outcome.Steps++
		if opts.OnState != nil {
			opts.OnState(node.ID)
		}
		log("[Flow] state " + node.Label())

		switch node.Type {
		case TypeStart:
			if node.NextID == "" {
				log("[Terminal] Start has no next state")
				return finish(false, "Start has no next state")
			}
			log("[Transition] Start → " + targetLabel(nodes, node.NextID))
			currentID = node.NextID

		case TypeSuccess:
			log("[Flow] reached Success")
			return finish(true, "")

		case TypeTerminal:
			log("[Flow] reached Terminal")
			return finish(false, "reached Terminal")

		case TypeCommand:
			output, err := runCommand(ctx, node, values, exec, opts, log)
			if err != nil {
				if ctx.Err() != nil {
					return cancelled()
				}
				log("[Response] <- FAIL " + err.Error())
				log("[Terminal] command failed")
				return finish(false, node.Command+" failed: "+err.Error())
			}
			if strings.TrimSpace(output) != "" {
				log("[Response] <- OK " + truncateForLog(output))
			} else {
				log("[Response] <- OK")
			}
			if node.SaveAs != "" {
				values[node.SaveAs] = strings.TrimSpace(output)
				log("[Flow] saved output as @" + node.SaveAs)
			}
			if node.NextID == "" {
				log("[Terminal] command has no next state")
				return finish(false, "command has no next state")
			}
			log("[Transition] command success → " + targetLabel(nodes, node.NextID))
			currentID = node.NextID
			if err := pause(ctx, opts); err != nil {
				return cancelled()
			}

		case TypeQualifier:
			expression, err := ParseExpression(node.Expression)
			if err != nil {
				log("[Terminal] invalid qualifier: " + err.Error())
				return finish(false, "invalid qualifier: "+err.Error())
			}
			matched := false
			attempts := node.Retries + 1
			for attempt := 1; attempt <= attempts; attempt++ {
				probe := &loggingProber{inner: exec, log: log}
				matched, err = expression.Eval(ctx, probe, values)
				if err != nil {
					if ctx.Err() != nil {
						return cancelled()
					}
					log("[Terminal] qualifier evaluation failed: " + err.Error())
					return finish(false, "qualifier failed: "+err.Error())
				}
				log(fmt.Sprintf("[Qualifier] result=%t attempt=%d/%d", matched, attempt, attempts))
				if matched {
					break
				}
				if attempt < attempts {
					log(fmt.Sprintf("[Qualifier] retrying; %d retries remain", attempts-attempt))
					if err := pause(ctx, opts); err != nil {
						return cancelled()
					}
				}
			}
			switch {
			case matched && node.SuccessID == "":
				log("[Terminal] qualifier has no success target")
				return finish(false, "qualifier has no success target")
			case matched:
				log("[Transition] qualifier true → " + targetLabel(nodes, node.SuccessID))
				currentID = node.SuccessID
			case node.FallbackID == "":
				log("[Terminal] qualifier false and no fallback target")
				return finish(false, "qualifier false: "+node.Expression)
			default:
				log("[Transition] qualifier false after retries → " + targetLabel(nodes, node.FallbackID))
				currentID = node.FallbackID
			}

		default:
			log("[Terminal] unknown state type " + node.Type)
			return finish(false, "unknown state type "+node.Type)
		}
	}
	log(fmt.Sprintf("[Terminal] exceeded the %d state visit safety limit", opts.MaxVisits))
	return finish(false, "exceeded state visit limit")
}

func runCommand(ctx context.Context, node Node, values map[string]string, exec Executor, opts Options, log func(string)) (string, error) {
	args := make([]string, len(node.Args))
	for i, arg := range node.Args {
		args[i] = Resolve(arg, values)
	}
	log("[Request] -> " + bridge.FormatCommand(node.Command, args))
	switch node.Command {
	case "":
		return "", errors.New("command is not set")
	case CommandServe:
		if err := exec.EnsureServing(ctx); err != nil {
			return "", err
		}
		return "serve ready", nil
	case CommandRandomSleep:
		if len(args) < 1 {
			return "", errors.New("random-sleep needs a maximum number of seconds")
		}
		maxSeconds, err := strconv.ParseFloat(strings.TrimSpace(args[0]), 64)
		if err != nil || maxSeconds <= 0 || maxSeconds > 86400 {
			return "", errors.New("random-sleep duration must be a number between 0 and 86400 seconds")
		}
		duration := time.Duration(rand.Float64() * maxSeconds * float64(time.Second))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(duration):
		}
		return fmt.Sprintf("Slept for %.2f seconds", duration.Seconds()), nil
	default:
		if !KnownCommand(node.Command) {
			return "", fmt.Errorf("unknown command %q", node.Command)
		}
		return exec.Command(ctx, node.Command, args)
	}
}

func pause(ctx context.Context, opts Options) error {
	if opts.MaxDelay <= 0 {
		return nil
	}
	min, max := opts.MinDelay, opts.MaxDelay
	if min < 0 {
		min = 0
	}
	if max < min {
		max = min
	}
	delay := min
	if span := max - min; span > 0 {
		delay += time.Duration(rand.Int63n(int64(span) + 1))
	}
	if delay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

type loggingProber struct {
	inner Prober
	log   func(string)
}

func (p *loggingProber) PageContains(ctx context.Context, selector string) (bool, error) {
	p.log("[Request] -> " + bridge.FormatCommand("page-contains", []string{selector}))
	found, err := p.inner.PageContains(ctx, selector)
	if err != nil {
		p.log("[Response] <- FAIL " + err.Error())
		return false, err
	}
	p.log(fmt.Sprintf("[Response] <- OK %t", found))
	return found, nil
}

func targetLabel(nodes map[string]Node, id string) string {
	if node, ok := nodes[id]; ok {
		return node.Label()
	}
	return "missing state " + id + " (Terminal)"
}

func truncateForLog(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 2000 {
		return text[:2000] + "… (truncated)"
	}
	return text
}

// BridgeExecutor adapts a bridge.Client to the Executor interface.
type BridgeExecutor struct {
	Client *bridge.Client
	// Binary is the chrome-extension-cli binary used to start the bridge on
	// demand; empty disables auto-start.
	Binary string
	// ExtensionTimeout bounds how long EnsureServing waits for Chrome.
	ExtensionTimeout time.Duration
	// Started receives the child process when EnsureServing launches one so
	// the caller can stop it later. May be nil.
	Started func(*bridge.Process)
}

// NewBridgeExecutor returns an executor for the default bridge address.
func NewBridgeExecutor(binary string) *BridgeExecutor {
	return &BridgeExecutor{Client: bridge.NewClient(), Binary: binary, ExtensionTimeout: 20 * time.Second}
}

func (e *BridgeExecutor) PageContains(ctx context.Context, selector string) (bool, error) {
	return e.Client.PageContains(ctx, selector)
}

func (e *BridgeExecutor) Command(ctx context.Context, name string, args []string) (string, error) {
	request, err := bridge.BuildRequest(name, args)
	if err != nil {
		return "", err
	}
	result, err := e.Client.Send(ctx, request)
	if err != nil {
		return "", err
	}
	return bridge.FormatResult(request, result), nil
}

func (e *BridgeExecutor) EnsureServing(ctx context.Context) error {
	timeout := e.ExtensionTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if !e.Client.Reachable(ctx) {
		if e.Binary == "" {
			return bridge.ErrUnreachable
		}
		process, err := bridge.EnsureServing(ctx, e.Client, e.Binary, nil, 10*time.Second)
		if err != nil {
			return err
		}
		if process != nil && e.Started != nil {
			e.Started(process)
		}
	}
	return e.Client.WaitForExtension(ctx, timeout)
}

func (e *BridgeExecutor) Screenshot(ctx context.Context) ([]byte, error) {
	result, err := e.Client.Send(ctx, bridge.Request{Type: bridge.TypeScreenshot})
	if err != nil {
		return nil, err
	}
	return bridge.DecodeScreenshot(result)
}
