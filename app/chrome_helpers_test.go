package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeChromeURLAddsScheme(t *testing.T) {
	if got := normalizeChromeURL("example.com"); got != "https://example.com" {
		t.Fatalf("normalizeChromeURL() = %q, want %q", got, "https://example.com")
	}
	if got := normalizeChromeURL("https://example.com/path"); got != "https://example.com/path" {
		t.Fatalf("normalizeChromeURL() = %q, want %q", got, "https://example.com/path")
	}
}

func TestLoginSelectorsRecognizeCommonFields(t *testing.T) {
	selectors := detectLoginSelectors("hdfc")
	if len(selectors) == 0 {
		t.Fatal("expected at least one selector for common login fields")
	}
	if selectors["userID"] == "" || selectors["password"] == "" {
		t.Fatal("expected user id and password selectors to be defined")
	}
	if !strings.Contains(selectors["userID"], "#username") {
		t.Fatalf("expected HDFC user selector to prefer the #username field, got %q", selectors["userID"])
	}
	if !strings.Contains(selectors["password"], "#password") {
		t.Fatalf("expected HDFC password selector to prefer the #password field, got %q", selectors["password"])
	}
}

func TestDetectBlockedAccessReason(t *testing.T) {
	t.Run("403 forbidden", func(t *testing.T) {
		reason := detectBlockedAccessReason("403 Forbidden", "Request blocked by the server")
		if !strings.Contains(reason, "403") {
			t.Fatalf("expected 403 detection, got %q", reason)
		}
	})

	t.Run("access restricted", func(t *testing.T) {
		reason := detectBlockedAccessReason("Access Temporarily Restricted", "Please try again later")
		if !strings.Contains(reason, "anti-bot") {
			t.Fatalf("expected anti-bot detection, got %q", reason)
		}
	})
}

func TestChromeProfileConfigUsesDedicatedAutomationProfile(t *testing.T) {
	config := chromeProfileConfig()
	if config.userDataDir == "" {
		t.Fatal("expected a Chrome user-data-dir to be configured")
	}
	if !strings.Contains(config.userDataDir, "/Google/Chrome") {
		t.Fatalf("expected Google Chrome user-data-dir, got %q", config.userDataDir)
	}
	if config.profileDir != "AutoIPOAutomation" {
		t.Fatalf("expected dedicated automation profile directory, got %q", config.profileDir)
	}
}

func TestResetChromeRuntimeRemovesStaleLockFiles(t *testing.T) {
	tmpDir := t.TempDir()
	profileDir := filepath.Join(tmpDir, "AutoIPOAutomation")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("create profile dir: %v", err)
	}

	rootLock := filepath.Join(tmpDir, "SingletonLock")
	if err := os.WriteFile(rootLock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale root SingletonLock: %v", err)
	}
	profileLock := filepath.Join(profileDir, "SingletonLock")
	if err := os.WriteFile(profileLock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale profile SingletonLock: %v", err)
	}

	cfg := chromeProfileSettings{userDataDir: tmpDir, profileDir: "AutoIPOAutomation"}
	if err := resetChromeRuntime(cfg); err != nil {
		t.Fatalf("resetChromeRuntime() returned error: %v", err)
	}
	if _, err := os.Stat(rootLock); !os.IsNotExist(err) {
		t.Fatalf("expected stale root SingletonLock to be removed, stat err = %v", err)
	}
	if _, err := os.Stat(profileLock); !os.IsNotExist(err) {
		t.Fatalf("expected stale profile SingletonLock to be removed, stat err = %v", err)
	}
}

func TestChromeHelperCloseIsIdempotent(t *testing.T) {
	helper, err := NewChromeHelper()
	if err != nil {
		t.Fatalf("NewChromeHelper() returned error: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close() panicked on second call: %v", r)
		}
	}()

	helper.Close()
	helper.Close()
}
