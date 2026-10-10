package main

import "testing"

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
