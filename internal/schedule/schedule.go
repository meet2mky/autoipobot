// Package schedule stores one-shot flow runs ("run flow X as user Y at T").
// Entries are created by the desktop app or Telegram and executed by the
// daemon.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoipo/internal/envfile"
	"autoipo/internal/instancelock"
)

// Entry statuses.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Entry is one scheduled run.
type Entry struct {
	ID         string    `json:"id"`
	FlowID     string    `json:"flow_id"`
	FlowName   string    `json:"flow_name"`
	UserPAN    string    `json:"user_pan,omitempty"`
	UserName   string    `json:"user_name,omitempty"`
	At         time.Time `json:"at"`
	Status     string    `json:"status"`
	Result     string    `json:"result,omitempty"`
	Source     string    `json:"source,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Summary renders an entry for lists and Telegram.
func (e Entry) Summary() string {
	who := ""
	if e.UserName != "" {
		who = " as " + e.UserName
	} else if e.UserPAN != "" {
		who = " as " + e.UserPAN
	}
	line := fmt.Sprintf("%s · %s%s · %s · %s", e.ID, e.FlowName, who, e.At.Local().Format("2006-01-02 15:04"), e.Status)
	if e.Result != "" {
		line += " — " + e.Result
	}
	return line
}

// ErrNotFound is returned for unknown entry IDs.
var ErrNotFound = errors.New("schedule entry not found")

// Store is a JSON file guarded by an advisory lock so the app and daemon can
// both modify it safely.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns a store at path (the file is created on first write).
func Open(path string) *Store { return &Store{path: path} }

func (s *Store) withLock(fn func(entries []Entry) ([]Entry, bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	lock, err := instancelock.Wait(s.path+".lock", 5*time.Second)
	if err != nil {
		return fmt.Errorf("lock schedules: %w", err)
	}
	defer func() { _ = lock.Release() }()
	entries, err := s.load()
	if err != nil {
		return err
	}
	updated, changed, err := fn(entries)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return s.save(updated)
}

func (s *Store) load() ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []Entry{}, nil
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return entries, nil
}

func (s *Store) save(entries []Entry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return envfile.WriteFileAtomic(s.path, append(data, '\n'), 0o600)
}

// List returns all entries ordered by time.
func (s *Store) List() ([]Entry, error) {
	var result []Entry
	err := s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		result = append([]Entry(nil), entries...)
		return entries, false, nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].At.Before(result[j].At) })
	return result, err
}

// Add stores a new pending entry and returns it with its ID assigned.
func (s *Store) Add(entry Entry) (Entry, error) {
	if entry.FlowID == "" {
		return Entry{}, errors.New("schedule entry needs a flow")
	}
	if entry.At.IsZero() {
		return Entry{}, errors.New("schedule entry needs a time")
	}
	entry.ID = newID()
	entry.Status = StatusPending
	entry.CreatedAt = time.Now()
	err := s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		return append(entries, entry), true, nil
	})
	return entry, err
}

// Update replaces an entry by ID.
func (s *Store) Update(updated Entry) error {
	return s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		for i := range entries {
			if entries[i].ID == updated.ID {
				entries[i] = updated
				return entries, true, nil
			}
		}
		return entries, false, ErrNotFound
	})
}

// Cancel marks a pending entry cancelled (running/finished entries are left alone).
func (s *Store) Cancel(id string) error {
	return s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		for i := range entries {
			if entries[i].ID == id || strings.HasPrefix(entries[i].ID, id) {
				if entries[i].Status != StatusPending {
					return entries, false, fmt.Errorf("entry %s is %s and cannot be cancelled", entries[i].ID, entries[i].Status)
				}
				entries[i].Status = StatusCancelled
				entries[i].FinishedAt = time.Now()
				return entries, true, nil
			}
		}
		return entries, false, ErrNotFound
	})
}

// Remove deletes an entry regardless of status.
func (s *Store) Remove(id string) error {
	return s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		for i := range entries {
			if entries[i].ID == id {
				return append(entries[:i], entries[i+1:]...), true, nil
			}
		}
		return entries, false, ErrNotFound
	})
}

// Prune drops finished entries older than keep.
func (s *Store) Prune(keep time.Duration) error {
	cutoff := time.Now().Add(-keep)
	return s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		var kept []Entry
		for _, entry := range entries {
			if entry.Status != StatusPending && entry.Status != StatusRunning && !entry.FinishedAt.IsZero() && entry.FinishedAt.Before(cutoff) {
				continue
			}
			kept = append(kept, entry)
		}
		return kept, len(kept) != len(entries), nil
	})
}

// ClaimDue atomically marks the earliest due pending entry as running and
// returns it. ok is false when nothing is due.
func (s *Store) ClaimDue(now time.Time) (claimed Entry, ok bool, err error) {
	err = s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		best := -1
		for i := range entries {
			if entries[i].Status != StatusPending || entries[i].At.After(now) {
				continue
			}
			if best < 0 || entries[i].At.Before(entries[best].At) {
				best = i
			}
		}
		if best < 0 {
			return entries, false, nil
		}
		entries[best].Status = StatusRunning
		entries[best].StartedAt = now
		claimed = entries[best]
		ok = true
		return entries, true, nil
	})
	return claimed, ok, err
}

// Finish records the outcome of a claimed entry.
func (s *Store) Finish(id string, success bool, result string) error {
	return s.withLock(func(entries []Entry) ([]Entry, bool, error) {
		for i := range entries {
			if entries[i].ID == id {
				if success {
					entries[i].Status = StatusDone
				} else {
					entries[i].Status = StatusFailed
				}
				entries[i].Result = result
				entries[i].FinishedAt = time.Now()
				return entries, true, nil
			}
		}
		return entries, false, ErrNotFound
	})
}

func newID() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(raw[:])
}

var (
	relativePattern = regexp.MustCompile(`^\+(\d+)([mh])$`)
	timeOnlyPattern = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

// ParseAt understands "YYYY-MM-DD HH:MM", "YYYY-MM-DDTHH:MM", "HH:MM" (today,
// or tomorrow if already past), "+15m" and "+2h". Times are local.
func ParseAt(text string, now time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, errors.New("time is required")
	}
	if match := relativePattern.FindStringSubmatch(text); match != nil {
		amount, _ := strconv.Atoi(match[1])
		unit := time.Minute
		if match[2] == "h" {
			unit = time.Hour
		}
		return now.Add(time.Duration(amount) * unit), nil
	}
	if match := timeOnlyPattern.FindStringSubmatch(text); match != nil {
		hour, _ := strconv.Atoi(match[1])
		minute, _ := strconv.Atoi(match[2])
		if hour > 23 || minute > 59 {
			return time.Time{}, fmt.Errorf("invalid time %q", text)
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !at.After(now) {
			at = at.Add(24 * time.Hour)
		}
		return at, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", time.RFC3339} {
		if at, err := time.ParseInLocation(layout, text, now.Location()); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse time %q (use YYYY-MM-DD HH:MM, HH:MM, +15m or +2h)", text)
}
