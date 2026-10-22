// Package tail reads the end of large log files without loading them fully.
package tail

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"
)

// Defaults used by the desktop app's log pane.
const (
	DefaultMaxBytes int64 = 256 << 10
	DefaultMaxLines       = 400
)

// LastLines returns at most maxLines lines from the end of the file at path,
// reading no more than maxBytes from its tail. When the file is larger than
// maxBytes the window may start in the middle of a line; that partial line is
// discarded so every returned line is complete. Non-positive limits fall back
// to the package defaults. A missing file yields os.ErrNotExist.
func LastLines(path string, maxBytes int64, maxLines int) ([]string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	// Read one extra byte before the window so we can tell whether the
	// window starts exactly on a line boundary.
	start := offset
	if offset > 0 {
		start = offset - 1
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		data = dropPartialLine(data)
	}
	return splitLines(data, maxLines), nil
}

// dropPartialLine removes the leading partial line. data[0] is the byte
// immediately before the requested window.
func dropPartialLine(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	if data[0] == '\n' {
		return data[1:]
	}
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return data[index+1:]
	}
	return nil
}

func splitLines(data []byte, maxLines int) []string {
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

// Fingerprint identifies a file's size and modification time so callers can
// skip re-reading an unchanged file.
type Fingerprint struct {
	Size    int64
	ModTime time.Time
}

// Stat returns the fingerprint of the file at path.
func Stat(path string) (Fingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Fingerprint{}, err
	}
	return Fingerprint{Size: info.Size(), ModTime: info.ModTime()}, nil
}

// Equal reports whether two fingerprints describe the same file state.
func (f Fingerprint) Equal(other Fingerprint) bool {
	return f.Size == other.Size && f.ModTime.Equal(other.ModTime)
}
