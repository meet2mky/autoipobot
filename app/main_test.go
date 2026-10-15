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

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
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

func TestMissingRequiredSettingsReportsEmptyTokenAndChatID(t *testing.T) {
	missing := missingRequiredSettings(daemonSettings{HostLabel: "MyMac", Token: "", ChatID: ""})
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing settings, got %d: %#v", len(missing), missing)
	}
	if !contains(missing, "TG_TOKEN") || !contains(missing, "TG_CHAT_ID") {
		t.Fatalf("missing settings should include token and chat id: %#v", missing)
	}
}

func TestUserStoreLoadsPanKeyedRecords(t *testing.T) {
	data := []byte(`{"ABCDE1234F":{"name":"Alice","pan":"ABCDE1234F","email":"alice@example.com","phone":"9999999999"}}`)
	store, err := decodeUserStore(data)
	if err != nil {
		t.Fatalf("decodeUserStore() unexpected error: %v", err)
	}
	if len(store) != 1 {
		t.Fatalf("expected 1 user, got %d", len(store))
	}
	if _, ok := store["ABCDE1234F"]; !ok {
		t.Fatalf("expected user keyed by PAN: %#v", store)
	}
}

func TestLoadSettingsIncludesChromeHelperToggle(t *testing.T) {
	t.Setenv("CHROME_HELPER_ENABLED", "true")
	cfg := loadSettings()
	if !cfg.ChromeHelperEnabled {
		t.Fatal("expected CHROME_HELPER_ENABLED=true to enable Chrome helper settings")
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
