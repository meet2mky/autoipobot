package main

import (
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

func TestChromeProfileConfigUsesDedicatedAutoIPOProfile(t *testing.T) {
	config := chromeProfileConfig()
	if config.userDataDir == "" {
		t.Fatal("expected a Chrome user-data-dir to be configured")
	}
	if !strings.Contains(config.userDataDir, "/Google/Chrome") {
		t.Fatalf("expected Google Chrome user-data-dir, got %q", config.userDataDir)
	}
	if config.profileDir != "AutoIPO" {
		t.Fatalf("expected AutoIPO profile directory, got %q", config.profileDir)
	}
}
