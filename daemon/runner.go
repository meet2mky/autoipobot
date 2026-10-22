package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"autoipo/internal/flow"
)

// errRunBusy is returned by runner.start while this process is already
// executing a flow.
var errRunBusy = errors.New("a flow run is already in progress")

// busyText is the reply when a run cannot start because another one (in this
// process or in the desktop app) holds the run lock.
const busyText = "busy: another flow run is in progress — send /stop to cancel it or wait for it to finish"

// runner executes at most one flow at a time and keeps the cancel function
// of the active run so that /stop can end it.
type runner struct {
	log         *logger
	newExecutor func() flow.Executor
	options     flow.Options

	mu     sync.Mutex
	active *activeRun
	wg     sync.WaitGroup
}

type activeRun struct {
	info   runInfo
	cancel context.CancelFunc
}

// runInfo describes the run in progress for status output.
type runInfo struct {
	flow      string
	user      string
	startedAt time.Time
}

// newRunner returns a runner that builds a fresh executor per run.
func newRunner(log *logger, newExecutor func() flow.Executor, options flow.Options) *runner {
	return &runner{log: log, newExecutor: newExecutor, options: options}
}

// start reserves the run slot and executes req in the background. done is
// invoked exactly once, from that goroutine, after the slot has been released.
// errRunBusy is returned when a run is already active.
func (r *runner) start(parent context.Context, req flow.RunRequest, done func(flow.RunResult, error)) error {
	r.mu.Lock()
	if r.active != nil {
		r.mu.Unlock()
		return errRunBusy
	}
	ctx, cancel := context.WithCancel(parent)
	r.active = &activeRun{info: runInfo{flow: req.Flow.Name, user: req.UserLabel, startedAt: time.Now()}, cancel: cancel}
	r.wg.Add(1)
	r.mu.Unlock()

	r.log.Printf("run starting: %s (source %s)", describeRun(req), req.Source)
	go func() {
		defer r.wg.Done()
		defer cancel()
		result, err := flow.Run(ctx, req, r.newExecutor(), r.options)
		r.mu.Lock()
		r.active = nil
		r.mu.Unlock()
		r.logOutcome(req, result, err)
		done(result, err)
	}()
	return nil
}

// runSync is start for callers that want to block until the run finishes.
func (r *runner) runSync(ctx context.Context, req flow.RunRequest) (flow.RunResult, error) {
	type outcome struct {
		result flow.RunResult
		err    error
	}
	done := make(chan outcome, 1)
	if err := r.start(ctx, req, func(result flow.RunResult, err error) { done <- outcome{result, err} }); err != nil {
		return flow.RunResult{}, err
	}
	o := <-done
	return o.result, o.err
}

// stop cancels the active run and reports whether there was one.
func (r *runner) stop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return false
	}
	r.active.cancel()
	return true
}

// busy reports whether a run is active in this process.
func (r *runner) busy() bool {
	_, ok := r.current()
	return ok
}

// current returns the active run, if any.
func (r *runner) current() (runInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return runInfo{}, false
	}
	return r.active.info, true
}

// wait blocks until every started run has finished and reported.
func (r *runner) wait() {
	r.wg.Wait()
}

func (r *runner) logOutcome(req flow.RunRequest, result flow.RunResult, err error) {
	if err != nil {
		r.log.Printf("run not executed: %s: %v", describeRun(req), err)
		return
	}
	r.log.Printf("run finished: %s", runSummary(req, result))
}

// describeRun renders "Flow as User" (or just the flow name).
func describeRun(req flow.RunRequest) string {
	if req.UserLabel == "" {
		return req.Flow.Name
	}
	return req.Flow.Name + " as " + req.UserLabel
}

// runSummary renders a finished run the way flow.LastRun does, plus the step
// count and duration.
func runSummary(req flow.RunRequest, result flow.RunResult) string {
	last := flow.LastRun{
		FlowID: req.Flow.ID, FlowName: req.Flow.Name, RunID: result.RunID, User: req.UserLabel, Source: req.Source,
		StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		Success: result.Success, Cancelled: result.Cancelled, Reason: result.Reason, Steps: result.Steps,
	}
	return fmt.Sprintf("%s · %d step(s) in %s", last.Summary(), result.Steps, formatDuration(result.FinishedAt.Sub(result.StartedAt)))
}

// runOutcome condenses a run into the (success, note) pair stored on a
// schedule entry.
func runOutcome(result flow.RunResult, err error) (bool, string) {
	switch {
	case errors.Is(err, errRunBusy), errors.Is(err, flow.ErrAnotherRunActive):
		return false, "another run was in progress"
	case err != nil:
		return false, err.Error()
	case result.Success:
		return true, "succeeded in " + formatDuration(result.FinishedAt.Sub(result.StartedAt))
	case result.Cancelled:
		return false, "cancelled"
	default:
		return false, result.Reason
	}
}

// reportRun sends the outcome of a run to the chat: the summary line and,
// for failures that captured the page, the screenshot with the reason as
// its caption.
func reportRun(out outbox, req flow.RunRequest, result flow.RunResult, err error) {
	switch {
	case errors.Is(err, errRunBusy), errors.Is(err, flow.ErrAnotherRunActive):
		out.text(busyText)
		return
	case err != nil:
		out.text(fmt.Sprintf("%s could not run: %v", describeRun(req), err))
		return
	}
	out.text(runSummary(req, result))
	if !result.Success && !result.Cancelled && len(result.Screenshot) > 0 {
		name := "screenshot.png"
		if result.ScreenshotPath != "" {
			name = filepath.Base(result.ScreenshotPath)
		}
		out.document(name, result.Screenshot, result.Reason)
	}
}
