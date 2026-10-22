package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"autoipo/internal/bridge"
	"autoipo/internal/envfile"
	"autoipo/internal/paths"
)

// Store persists flows in a JSON array file.
type Store struct {
	path string
	mu   sync.Mutex
}

// OpenStore returns a store for path (usually paths.FlowsFile()).
func OpenStore(path string) *Store { return &Store{path: path} }

// Load reads and normalises all flows. Invalid flows are skipped and
// reported through the returned warnings.
func (s *Store) Load() ([]Flow, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() ([]Flow, []string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Flow{}, nil, nil
		}
		return nil, nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []Flow{}, nil, nil
	}
	var flows []Flow
	if err := json.Unmarshal(data, &flows); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	var warnings []string
	kept := make([]Flow, 0, len(flows))
	for _, f := range flows {
		if err := f.Validate(); err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped flow %q: %v", f.Name, err))
			continue
		}
		kept = append(kept, Normalize(f))
	}
	return kept, warnings, nil
}

// Normalize migrates legacy fields: flow-level placeholders move onto the
// start node and the editable placeholder text is derived when missing.
func Normalize(f Flow) Flow {
	if start := f.StartNode(); start != nil {
		if len(f.Placeholders) > 0 && len(start.Placeholders) == 0 {
			start.Placeholders = f.Placeholders
			start.PlaceholderText = ""
		}
		if start.PlaceholderText == "" && len(start.Placeholders) > 0 {
			start.PlaceholderText = FormatPlaceholders(start.Placeholders)
		}
		if start.Placeholders == nil {
			start.Placeholders = map[string]string{}
		}
	}
	f.Placeholders = nil
	if f.RunsToKeep < 1 {
		f.RunsToKeep = DefaultRunsToKeep
	}
	return f
}

// Save validates and atomically writes all flows.
func (s *Store) Save(flows []Flow) error {
	for _, f := range flows {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("flow %q: %w", f.Name, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(flows, "", "  ")
	if err != nil {
		return err
	}
	return envfile.WriteFileAtomic(s.path, append(data, '\n'), 0o600)
}

// Find looks a flow up by ID, exact name, or unique case-insensitive name
// prefix.
func Find(flows []Flow, query string) (Flow, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return Flow{}, false
	}
	var prefix []Flow
	for _, f := range flows {
		if f.ID == query || strings.EqualFold(f.Name, query) {
			return f, true
		}
		if strings.HasPrefix(strings.ToLower(f.Name), strings.ToLower(query)) {
			prefix = append(prefix, f)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], true
	}
	return Flow{}, false
}

// UniqueName returns base, or "base 2", "base 3", … avoiding existing names.
func UniqueName(flows []Flow, base string) string {
	exists := func(name string) bool {
		for _, f := range flows {
			if strings.EqualFold(strings.TrimSpace(f.Name), name) {
				return true
			}
		}
		return false
	}
	if !exists(base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s %d", base, n)
		if !exists(candidate) {
			return candidate
		}
	}
}

// Run logs ---------------------------------------------------------------

// RunsDir is where a flow's run logs live.
func RunsDir(flowID string) string { return filepath.Join(paths.FlowsDir(), flowID, "runs") }

// RunInfo describes one saved run log.
type RunInfo struct {
	RunID string
	Path  string
	Time  time.Time
}

// ListRuns returns run logs newest first.
func ListRuns(flowID string) []RunInfo {
	entries, err := os.ReadDir(RunsDir(flowID))
	if err != nil {
		return nil
	}
	var runs []RunInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info := RunInfo{RunID: strings.TrimSuffix(entry.Name(), ".log"), Path: filepath.Join(RunsDir(flowID), entry.Name())}
		if stat, err := entry.Info(); err == nil {
			info.Time = stat.ModTime()
		}
		runs = append(runs, info)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RunID > runs[j].RunID })
	return runs
}

// PruneRuns keeps only the newest keep run logs.
func PruneRuns(flowID string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	runs := ListRuns(flowID)
	if len(runs) <= keep {
		return nil
	}
	for _, run := range runs[keep:] {
		if err := os.Remove(run.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if shot := RunScreenshot(flowID, run.RunID); shot != "" {
			_ = os.Remove(shot)
		}
	}
	return nil
}

// DeleteRuns removes every run log of a flow.
func DeleteRuns(flowID string) error {
	return os.RemoveAll(filepath.Join(paths.FlowsDir(), flowID))
}

// LatestRunLog returns the newest run log contents, or "".
func LatestRunLog(flowID string) string {
	runs := ListRuns(flowID)
	if len(runs) == 0 {
		return ""
	}
	data, err := os.ReadFile(runs[0].Path)
	if err != nil {
		return ""
	}
	return string(data)
}

// RunLog is an open run transcript file.
type RunLog struct {
	RunID string
	Path  string
	file  *os.File
	mu    sync.Mutex
	Sink  func(line string) // optional live listener
}

// OpenRunLog creates a new run log for flowID.
func OpenRunLog(flowID string) (*RunLog, error) {
	dir := RunsDir(flowID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create run log directory: %w", err)
	}
	runID := NewID()
	path := filepath.Join(dir, runID+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open run log: %w", err)
	}
	return &RunLog{RunID: runID, Path: path, file: file}, nil
}

// Log appends a timestamped line and forwards it to Sink.
func (r *RunLog) Log(line string) {
	if r == nil {
		return
	}
	stamped := time.Now().Format(time.RFC3339) + " " + line
	r.mu.Lock()
	if r.file != nil {
		_, _ = r.file.WriteString(stamped + "\n")
	}
	sink := r.Sink
	r.mu.Unlock()
	if sink != nil {
		sink(stamped)
	}
}

// Close flushes and closes the file.
func (r *RunLog) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Last run summary ----------------------------------------------------

// LastRun is written after every run for /status and the app's status pane.
type LastRun struct {
	FlowID     string    `json:"flow_id"`
	FlowName   string    `json:"flow_name"`
	RunID      string    `json:"run_id"`
	User       string    `json:"user,omitempty"`
	Source     string    `json:"source,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Success    bool      `json:"success"`
	Cancelled  bool      `json:"cancelled,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Steps      int       `json:"steps"`
	Screenshot string    `json:"screenshot,omitempty"` // path to the end-of-run image
}

// Summary renders the last run for humans.
func (l LastRun) Summary() string {
	if l.FlowID == "" {
		return "no flow has run yet"
	}
	state := "failed"
	if l.Success {
		state = "succeeded"
	} else if l.Cancelled {
		state = "was cancelled"
	}
	line := fmt.Sprintf("%s %s at %s", l.FlowName, state, l.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	if l.User != "" {
		line += " (as " + l.User + ")"
	}
	if l.Reason != "" && !l.Success {
		line += ": " + l.Reason
	}
	return line
}

// WriteLastRun persists the summary.
func WriteLastRun(last LastRun) error {
	data, err := json.MarshalIndent(last, "", "  ")
	if err != nil {
		return err
	}
	if err := paths.EnsureDataDir(); err != nil {
		return err
	}
	return envfile.WriteFileAtomic(paths.LastRunFile(), append(data, '\n'), 0o600)
}

// ReadLastRun loads the summary; a missing file yields an empty LastRun.
func ReadLastRun() (LastRun, error) {
	data, err := os.ReadFile(paths.LastRunFile())
	if err != nil {
		if os.IsNotExist(err) {
			return LastRun{}, nil
		}
		return LastRun{}, err
	}
	var last LastRun
	if err := json.Unmarshal(data, &last); err != nil {
		return LastRun{}, err
	}
	return last, nil
}

// screenshotExtensions lists the image types a run screenshot may use.
var screenshotExtensions = []string{".png", ".jpg", ".webp"}

// SaveScreenshot stores the image next to the run log (extension chosen from
// the image bytes) and returns its path.
func SaveScreenshot(flowID, runID string, image []byte) (string, error) {
	if len(image) == 0 {
		return "", errors.New("empty screenshot")
	}
	dir := RunsDir(flowID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, runID+bridge.ImageExtension(image))
	if err := os.WriteFile(path, image, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// RunScreenshot returns the screenshot saved for a run, or "" when none exists.
func RunScreenshot(flowID, runID string) string {
	for _, ext := range screenshotExtensions {
		path := filepath.Join(RunsDir(flowID), runID+ext)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
