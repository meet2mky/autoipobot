package main

import (
	"io"
	"os"
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

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{name: "strip mention and trim", raw: " /PING@autoipobot ", expected: "ping"},
		{name: "lowercase command", raw: "Status", expected: "status"},
		{name: "status command", raw: "/status@autoipobot", expected: "status"},
		{name: "help command", raw: "/help@autoipobot", expected: "help"},
		{name: "empty text", raw: "   ", expected: ""},
		{name: "empty after mention", raw: "@autoipobot", expected: ""},
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
