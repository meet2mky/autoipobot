package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
)

// ErrAnotherRunActive is returned when the app or daemon is already running
// a flow; only one flow may drive the browser at a time.
var ErrAnotherRunActive = errors.New("another flow run is already in progress")

// RunRequest describes a run for Run.
type RunRequest struct {
	Flow Flow
	// Values are placeholder values entered for this run; they override the
	// flow's defaults and UserValues.
	Values map[string]string
	// UserValues come from a selected user record (name, pan, user_id, …).
	UserValues map[string]string
	// UserLabel names the user for logs and summaries.
	UserLabel string
	// Source is "app", "telegram" or "schedule".
	Source string
	// OnLine receives each transcript line as it is written (optional).
	OnLine func(line string)
	// OnState is forwarded to Options.OnState (optional).
	OnState func(nodeID string)
}

// RunResult is the outcome plus bookkeeping.
type RunResult struct {
	Outcome
	RunID          string
	LogPath        string
	ScreenshotPath string
	StartedAt      time.Time
	FinishedAt     time.Time
}

// Run executes a flow end to end: it takes the cross-process run lock,
// writes a run log, prunes old logs and records the last-run summary.
func Run(ctx context.Context, req RunRequest, exec Executor, opts Options) (RunResult, error) {
	lock, err := instancelock.Wait(paths.FlowRunLockFile(), time.Second)
	if err != nil {
		if errors.Is(err, instancelock.ErrTimeout) {
			return RunResult{}, ErrAnotherRunActive
		}
		return RunResult{}, fmt.Errorf("acquire run lock: %w", err)
	}
	defer func() { _ = lock.Release() }()

	runLog, err := OpenRunLog(req.Flow.ID)
	if err != nil {
		return RunResult{}, err
	}
	runLog.Sink = req.OnLine
	result := RunResult{RunID: runLog.RunID, LogPath: runLog.Path, StartedAt: time.Now()}

	values := MergeValues(req.UserValues, req.Values)
	runLog.Log(fmt.Sprintf("[Run] start %q run %s source=%s user=%q", req.Flow.Name, runLog.RunID, orDefault(req.Source, "app"), req.UserLabel))
	if len(values) > 0 {
		runLog.Log(fmt.Sprintf("[Run] values %v", RedactValues(values)))
	}
	if req.OnState != nil {
		opts.OnState = req.OnState
	}
	result.Outcome = Execute(ctx, req.Flow, values, exec, opts, runLog.Log)
	result.FinishedAt = time.Now()
	if len(result.Screenshot) > 0 {
		if path, err := SaveScreenshot(req.Flow.ID, runLog.RunID, result.Screenshot); err == nil {
			result.ScreenshotPath = path
			runLog.Log("[Run] screenshot saved to " + path)
		}
	}
	switch {
	case result.Success:
		runLog.Log(fmt.Sprintf("[Run] finished: SUCCESS in %s (%d steps)", result.FinishedAt.Sub(result.StartedAt).Round(time.Millisecond), result.Steps))
	case result.Cancelled:
		runLog.Log("[Run] finished: CANCELLED")
	default:
		runLog.Log("[Run] finished: FAILED — " + result.Reason)
	}
	_ = runLog.Close()
	if err := PruneRuns(req.Flow.ID, req.Flow.RunsToKeep); err != nil {
		runLog.Log("[warning] could not prune old run logs: " + err.Error())
	}
	_ = WriteLastRun(LastRun{
		FlowID: req.Flow.ID, FlowName: req.Flow.Name, RunID: runLog.RunID, User: req.UserLabel,
		Source: orDefault(req.Source, "app"), StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		Success: result.Success, Cancelled: result.Cancelled, Reason: result.Reason, Steps: result.Steps,
		Screenshot: result.ScreenshotPath,
	})
	return result, nil
}

// RunActive reports whether a run currently holds the run lock.
func RunActive() bool {
	lock, acquired, err := instancelock.Acquire(paths.FlowRunLockFile())
	if err != nil {
		return false
	}
	if acquired {
		_ = lock.Release()
		return false
	}
	return true
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
