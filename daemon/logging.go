package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	logMaxSize = 5 << 20 // rotate the daemon log at 5 MiB
	logKeep    = 3       // keep autoipo.log.1 … autoipo.log.3
)

// scrubber removes secrets from free text before it reaches logs or the
// status file. HTTP transport errors, for example, embed the request URL,
// which for the Bot API contains the token.
type scrubber []string

// clean replaces every occurrence of a known secret with a placeholder.
func (s scrubber) clean(text string) string {
	for _, secret := range s {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

// logger writes RFC 3339 timestamped lines to every configured writer. It is
// safe for concurrent use.
type logger struct {
	scrub scrubber
	mu    sync.Mutex
	outs  []io.Writer
}

// newLogger returns a logger that writes each line to all of outs.
func newLogger(scrub scrubber, outs ...io.Writer) *logger {
	return &logger{scrub: scrub, outs: outs}
}

// Printf formats and writes one line. Write failures are ignored: there is
// nowhere better to report them.
func (l *logger) Printf(format string, args ...any) {
	line := time.Now().Format(time.RFC3339) + " " + l.scrub.clean(fmt.Sprintf(format, args...)) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, out := range l.outs {
		_, _ = io.WriteString(out, line)
	}
}

// rotatingWriter appends to a file and rotates it once it would exceed
// maxSize bytes, keeping path.1 (newest) … path.keep (oldest). It is safe
// for concurrent use.
type rotatingWriter struct {
	path    string
	maxSize int64
	keep    int

	mu   sync.Mutex
	file *os.File
	size int64
}

// newRotatingWriter opens (creating if needed) the log file at path. keep is
// clamped to at least one archived file.
func newRotatingWriter(path string, maxSize int64, keep int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &rotatingWriter{path: path, maxSize: maxSize, keep: max(keep, 1)}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file, w.size = file, info.Size()
	return nil
}

// Write implements io.Writer. A single write never straddles two files.
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts path.N to path.N+1 (dropping the oldest), moves the current
// file to path.1 and opens a fresh file. It always attempts to reopen so that
// logging continues even if a rename failed.
func (w *rotatingWriter) rotate() error {
	var firstErr error
	note := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	note(w.file.Close())
	w.file = nil
	for i := w.keep - 1; i >= 1; i-- {
		note(os.Rename(w.archive(i), w.archive(i+1)))
	}
	note(os.Rename(w.path, w.archive(1)))
	note(w.open())
	return firstErr
}

func (w *rotatingWriter) archive(n int) string {
	return fmt.Sprintf("%s.%d", w.path, n)
}

// Close closes the current file; later writes reopen it.
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
