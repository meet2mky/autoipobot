package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAppAutoStartPlistPathMatchesLaunchAgentLocation(t *testing.T) {
	path := appAutoStartPlistPath()
	if !strings.HasSuffix(path, filepath.Join("Library", "LaunchAgents", appAutoStartLabel()+".plist")) {
		t.Fatalf("unexpected launch agent path: %s", path)
	}
}

func TestAppLaunchAgentPlistIncludesStartupSettings(t *testing.T) {
	plist := appLaunchAgentPlistContent("/tmp/autoipoapp")
	if !strings.Contains(plist, appAutoStartLabel()) {
		t.Fatalf("plist missing launch agent label: %s", plist)
	}
	if !strings.Contains(plist, "RunAtLoad") || !strings.Contains(plist, "KeepAlive") {
		t.Fatalf("plist missing startup triggers: %s", plist)
	}
	if !strings.Contains(plist, "/tmp/autoipoapp") {
		t.Fatalf("plist missing executable path: %s", plist)
	}
}
