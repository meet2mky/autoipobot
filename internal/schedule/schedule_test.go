package schedule

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLifecycle(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "schedules.json"))
	now := time.Now()
	later, err := store.Add(Entry{FlowID: "f1", FlowName: "Apply", UserPAN: "ABCDE1234F", At: now.Add(time.Hour)})
	if err != nil || later.ID == "" || later.Status != StatusPending {
		t.Fatalf("Add = %+v, %v", later, err)
	}
	due, err := store.Add(Entry{FlowID: "f2", FlowName: "Login", At: now.Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(Entry{FlowName: "no flow", At: now}); err == nil {
		t.Fatal("Add without flow should fail")
	}

	claimed, ok, err := store.ClaimDue(now)
	if err != nil || !ok || claimed.ID != due.ID || claimed.Status != StatusRunning {
		t.Fatalf("ClaimDue = %+v, %v, %v", claimed, ok, err)
	}
	if _, ok, _ := store.ClaimDue(now); ok {
		t.Fatal("second ClaimDue should find nothing due")
	}
	if err := store.Finish(claimed.ID, true, "reached Success"); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(later.ID[:4]); err != nil {
		t.Fatalf("Cancel by prefix: %v", err)
	}
	if err := store.Cancel(later.ID); err == nil {
		t.Fatal("cancelling a cancelled entry should fail")
	}
	entries, err := store.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	if entries[0].Status != StatusDone || entries[1].Status != StatusCancelled {
		t.Fatalf("statuses = %s, %s", entries[0].Status, entries[1].Status)
	}
	if err := store.Remove("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Remove unknown = %v", err)
	}
	if err := store.Prune(0); err != nil {
		t.Fatal(err)
	}
	entries, _ = store.List()
	if len(entries) != 0 {
		t.Fatalf("Prune should drop finished entries, got %d", len(entries))
	}
}

func TestParseAt(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 30, 0, 0, time.Local)
	cases := map[string]time.Time{
		"10:05":            time.Date(2026, 10, 22, 10, 5, 0, 0, time.Local),
		"09:00":            time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local),
		"+15m":             now.Add(15 * time.Minute),
		"+2h":              now.Add(2 * time.Hour),
		"2026-11-01 10:00": time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local),
		"2026-11-01T10:00": time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local),
	}
	for input, want := range cases {
		got, err := ParseAt(input, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseAt(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, bad := range []string{"", "25:00", "tomorrow", "+5d"} {
		if _, err := ParseAt(bad, now); err == nil {
			t.Errorf("ParseAt(%q) should fail", bad)
		}
	}
}
