package format

import (
	"testing"
	"time"
)

func TestMaskPAN(t *testing.T) {
	cases := map[string]string{
		"ABCDE1234F":  "ABCDE••••F",
		" abcde1234f": "ABCDE••••F",
		"SHORT":       "••••T",
		"X":           "X",
		"":            "",
	}
	for in, want := range cases {
		if got := MaskPAN(in); got != want {
			t.Fatalf("MaskPAN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskUserID(t *testing.T) {
	cases := map[string]string{"user1234": "us••••", "ab": "••", "a": "•", "": ""}
	for in, want := range cases {
		if got := MaskUserID(in); got != want {
			t.Fatalf("MaskUserID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "never"},
		{now.Add(-5 * time.Second), "5s ago"},
		{now.Add(-3 * time.Minute), "3m ago"},
		{now.Add(-90 * time.Minute), "1h 30m ago"},
		{now.Add(-49 * time.Hour), "2d ago"},
		{now.Add(time.Minute), "0s ago"},
	}
	for _, tc := range cases {
		if got := Age(tc.at, now); got != tc.want {
			t.Fatalf("Age(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Second:                      "0s",
		45 * time.Second:                  "45s",
		5*time.Minute + 3*time.Second:     "5m 03s",
		2*time.Hour + 5*time.Minute:       "2h 05m",
		3*24*time.Hour + 2*time.Hour:      "3d 02h",
		1500 * time.Millisecond:           "2s",
		59*time.Minute + 59*time.Second:   "59m 59s",
		23*time.Hour + 59*time.Minute + 1: "23h 59m",
	}
	for in, want := range cases {
		if got := Duration(in); got != want {
			t.Fatalf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTimestamp(t *testing.T) {
	if Timestamp(time.Time{}) != "—" {
		t.Fatalf("zero time should render as a dash")
	}
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.Local)
	if got := Timestamp(at); got != "2026-03-04 05:06:07" {
		t.Fatalf("Timestamp = %q", got)
	}
}
