package tail

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// writeNumberedLines writes count lines of exactly 10 bytes each
// ("line-0001\n") and returns the file path.
func writeNumberedLines(t *testing.T, count int) string {
	t.Helper()
	var builder strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&builder, "line-%04d\n", i)
	}
	path := filepath.Join(t.TempDir(), "autoipo.log")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var completeLine = regexp.MustCompile(`^line-\d{4}$`)

func TestLastLinesSmallFileReturnsEverything(t *testing.T) {
	path := writeNumberedLines(t, 5)
	lines, err := LastLines(path, DefaultMaxBytes, DefaultMaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 || lines[0] != "line-0001" || lines[4] != "line-0005" {
		t.Fatalf("unexpected lines: %q", lines)
	}
}

func TestLastLinesCapsLineCount(t *testing.T) {
	path := writeNumberedLines(t, 50)
	lines, err := LastLines(path, DefaultMaxBytes, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[0] != "line-0048" || lines[2] != "line-0050" {
		t.Fatalf("unexpected lines: %q", lines)
	}
}

func TestLastLinesLargeFileDropsPartialFirstLine(t *testing.T) {
	path := writeNumberedLines(t, 1000) // 10,000 bytes
	// 95 bytes from the end starts in the middle of line-0991.
	lines, err := LastLines(path, 95, DefaultMaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 9 {
		t.Fatalf("expected 9 complete lines, got %d: %q", len(lines), lines)
	}
	for _, line := range lines {
		if !completeLine.MatchString(line) {
			t.Fatalf("partial line leaked into the result: %q", line)
		}
	}
	if lines[0] != "line-0992" || lines[len(lines)-1] != "line-1000" {
		t.Fatalf("unexpected window: %q", lines)
	}
}

func TestLastLinesWindowOnLineBoundaryKeepsFirstLine(t *testing.T) {
	path := writeNumberedLines(t, 1000)
	// Exactly 5 lines fit in 50 bytes and the window starts on a boundary.
	lines, err := LastLines(path, 50, DefaultMaxLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 || lines[0] != "line-0996" {
		t.Fatalf("expected lines 996..1000, got %q", lines)
	}
}

func TestLastLinesHandlesMissingAndEmptyFiles(t *testing.T) {
	if _, err := LastLines(filepath.Join(t.TempDir(), "missing.log"), 0, 0); !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := LastLines(empty, 0, 0)
	if err != nil || len(lines) != 0 {
		t.Fatalf("expected no lines and no error, got %q, %v", lines, err)
	}
}

func TestLastLinesTrimsCarriageReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlf.log")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := LastLines(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("unexpected lines: %q", lines)
	}
}

func TestStatDetectsChanges(t *testing.T) {
	path := writeNumberedLines(t, 2)
	before, err := Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := Stat(path)
	if !before.Equal(same) {
		t.Fatalf("fingerprint of an unchanged file should be equal")
	}
	if err := os.WriteFile(path, []byte("longer content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	after, _ := Stat(path)
	if before.Equal(after) {
		t.Fatalf("fingerprint should change after a write")
	}
	if _, err := Stat(filepath.Join(t.TempDir(), "nope")); !os.IsNotExist(err) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
