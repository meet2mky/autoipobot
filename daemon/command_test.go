package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogLine(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	logLine("hello world")
	w.Close()
	buf, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	out := string(buf)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("logLine() output = %q, want to contain %q", out, "hello world")
	}
}

func TestResolveLogPathUsesAppDirectory(t *testing.T) {
	exePath := filepath.Join(t.TempDir(), "bin", "autoipo")
	if err := os.MkdirAll(filepath.Dir(exePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	want := filepath.Join(filepath.Dir(exePath), "logs", "autoipo.log")
	if got, err := resolveLogPathFor(exePath); err != nil || got != want {
		t.Fatalf("resolveLogPathFor(%q) = (%q, %v), want (%q, nil)", exePath, got, err, want)
	}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{name: "strip mention and trim", raw: " /PING@autoipo ", expected: "ping"},
		{name: "lowercase command", raw: "Status", expected: "status"},
		{name: "status command", raw: "/status@autoipo", expected: "status"},
		{name: "help command", raw: "/help@autoipo", expected: "help"},
		{name: "empty text", raw: "   ", expected: ""},
		{name: "empty after mention", raw: "@autoipo", expected: ""},
		{name: "status without slash", raw: "STATUS", expected: "status"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCommand(tc.raw); got != tc.expected {
				t.Fatalf("parseCommand(%q) = %q, want %q", tc.raw, got, tc.expected)
			}
		})
	}
}
