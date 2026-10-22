package outputlog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func fixedClock(l *Log) {
	l.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
}

func TestAppendStampsFirstLineAndIndentsContinuations(t *testing.T) {
	log := New(10)
	fixedClock(log)
	log.Append("first\nsecond\n")
	lines := log.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), lines)
	}
	if lines[0] != "03:04:05 first" {
		t.Fatalf("unexpected first line %q", lines[0])
	}
	if lines[1] != "         second" {
		t.Fatalf("unexpected continuation line %q", lines[1])
	}
}

func TestCapacityKeepsNewestLines(t *testing.T) {
	log := New(3)
	fixedClock(log)
	for i := 1; i <= 5; i++ {
		log.Appendf("line-%d", i)
	}
	lines := log.Lines()
	if len(lines) != 3 || log.Len() != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"line-3", "line-4", "line-5"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Fatalf("line %d = %q, want suffix %q", i, lines[i], want)
		}
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	log := New(0)
	fixedClock(log)
	log.Append("Bridge started")
	log.Append("command failed")
	log.Append("bridge stopped")
	got := log.Filter("  BRIDGE ")
	if strings.Count(got, "\n") != 1 || strings.Contains(got, "command") {
		t.Fatalf("unexpected filter output: %q", got)
	}
	if log.Filter("") != strings.Join(log.Lines(), "\n") {
		t.Fatalf("empty query should return everything")
	}
	if log.Filter("nothing") != "" {
		t.Fatalf("expected empty result for no matches")
	}
}

func TestClearAndOnChange(t *testing.T) {
	log := New(5)
	changes := 0
	log.SetOnChange(func() { changes++ })
	log.Append("a")
	log.Clear()
	if log.Len() != 0 {
		t.Fatalf("expected empty log after Clear")
	}
	if changes != 2 {
		t.Fatalf("expected 2 change notifications, got %d", changes)
	}
}

func TestDefaultCapacity(t *testing.T) {
	log := New(-1)
	for i := 0; i < DefaultCapacity+10; i++ {
		log.Append(fmt.Sprint(i))
	}
	if log.Len() != DefaultCapacity {
		t.Fatalf("expected %d lines, got %d", DefaultCapacity, log.Len())
	}
}
