// Package outputlog keeps a bounded, timestamped transcript for the desktop
// app's text panes (command output, app events). It is safe for concurrent
// use; UI code subscribes to changes with SetOnChange.
package outputlog

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultCapacity is the line cap used when New is called with a
// non-positive capacity.
const DefaultCapacity = 2000

const stampLayout = "15:04:05"

// Log is a thread-safe ring of timestamped lines.
type Log struct {
	mu       sync.Mutex
	capacity int
	lines    []string
	now      func() time.Time
	onChange func()
}

// New returns an empty log that keeps at most capacity lines.
func New(capacity int) *Log {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Log{capacity: capacity, now: time.Now}
}

// SetOnChange registers fn to be called, outside the log's lock, after every
// mutation. Pass nil to unsubscribe.
func (l *Log) SetOnChange(fn func()) {
	l.mu.Lock()
	l.onChange = fn
	l.mu.Unlock()
}

// Append records message. Multi-line messages keep their line breaks: the
// first line carries the timestamp and continuation lines are indented so the
// text stays aligned.
func (l *Log) Append(message string) {
	stamp := l.now().Format(stampLayout)
	indent := strings.Repeat(" ", len(stamp))
	parts := strings.Split(strings.TrimRight(message, "\r\n"), "\n")
	l.mu.Lock()
	for i, part := range parts {
		prefix := indent
		if i == 0 {
			prefix = stamp
		}
		l.lines = append(l.lines, prefix+" "+strings.TrimRight(part, "\r"))
	}
	if excess := len(l.lines) - l.capacity; excess > 0 {
		l.lines = append([]string(nil), l.lines[excess:]...)
	}
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Appendf formats and appends a message.
func (l *Log) Appendf(format string, args ...any) {
	l.Append(fmt.Sprintf(format, args...))
}

// Lines returns a copy of the stored lines, oldest first.
func (l *Log) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// Len returns the number of stored lines.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

// Filter returns the stored lines joined by newlines. A non-blank query keeps
// only lines containing it (case-insensitive).
func (l *Log) Filter(query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	l.mu.Lock()
	defer l.mu.Unlock()
	if query == "" {
		return strings.Join(l.lines, "\n")
	}
	var matches []string
	for _, line := range l.lines {
		if strings.Contains(strings.ToLower(line), query) {
			matches = append(matches, line)
		}
	}
	return strings.Join(matches, "\n")
}

// Clear removes every line.
func (l *Log) Clear() {
	l.mu.Lock()
	l.lines = nil
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
}
