package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppLaunchOnLoginPlistPathMatchesLaunchAgentLocation(t *testing.T) {
	path := appLaunchOnLoginPlistPath()
	if !strings.HasSuffix(path, filepath.Join("Library", "LaunchAgents", appLaunchOnLoginLabel()+".plist")) {
		t.Fatalf("unexpected launch agent path: %s", path)
	}
}

func TestAppLaunchAgentPlistIncludesStartupSettings(t *testing.T) {
	plist := appLaunchAgentPlistContent("/tmp/autoipoapp")
	if !strings.Contains(plist, appLaunchOnLoginLabel()) {
		t.Fatalf("plist missing launch agent label: %s", plist)
	}
	if !strings.Contains(plist, "RunAtLoad") || !strings.Contains(plist, "KeepAlive") {
		t.Fatalf("plist missing startup triggers: %s", plist)
	}
	if !strings.Contains(plist, "/tmp/autoipoapp") {
		t.Fatalf("plist missing executable path: %s", plist)
	}
}

func TestLogTailWindowDefaultsToLast100Lines(t *testing.T) {
	lines := make([]string, 0, 150)
	for i := 1; i <= 150; i++ {
		lines = append(lines, fmt.Sprintf("line-%03d", i))
	}
	window, start, older, newer := logTailWindow(strings.Join(lines, "\n"), 100, 0)
	if start != 50 {
		t.Fatalf("expected default start index 50, got %d", start)
	}
	if older != true {
		t.Fatalf("expected older navigation to be available")
	}
	if newer != false {
		t.Fatalf("expected newer navigation to be disabled at the tail")
	}
	if len(strings.Split(window, "\n")) != 100 {
		t.Fatalf("expected 100 tail lines, got %d", len(strings.Split(window, "\n")))
	}
	if !strings.Contains(window, "line-051") || !strings.Contains(window, "line-150") {
		t.Fatalf("window should include only the last 100 lines, got: %s", window)
	}
	if strings.Contains(window, "line-001") {
		t.Fatalf("window should not include older lines before the tail window")
	}
}
