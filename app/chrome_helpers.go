package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

type copyableError struct {
	msg string
}

func newCopyableError(msg string) error {
	return copyableError{msg: msg}
}

func (e copyableError) Error() string {
	return e.msg
}

func (e copyableError) String() string {
	return e.msg
}

func (e copyableError) Copy() string {
	return e.msg
}

type chromeProfileSettings struct {
	userDataDir string
	profileDir  string
}

func chromeProfileConfig() chromeProfileSettings {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return chromeProfileSettings{userDataDir: "/Users/mohityadav/Library/Application Support/Google/Chrome", profileDir: "Default"}
	}
	return chromeProfileSettings{
		userDataDir: filepath.Join(home, "Library", "Application Support", "Google", "Chrome"),
		profileDir:  "Default",
	}
}

func chromeExecAllocatorOptions() []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	if runtime.GOOS == "darwin" {
		cfg := chromeProfileConfig()
		opts = append(opts,
			chromedp.ExecPath("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
			chromedp.Flag("user-data-dir", cfg.userDataDir),
			chromedp.Flag("profile-directory", cfg.profileDir),
			chromedp.NoFirstRun,
			chromedp.NoDefaultBrowserCheck,
		)
	}
	return opts
}

// ChromeHelper gives practical browser automation helpers for common workflows.
type ChromeHelper struct {
	ctx      context.Context
	cancel   context.CancelFunc
	timeout  time.Duration
	closeMux sync.Once
}

func NewChromeHelper() (*ChromeHelper, error) {
	baseCtx, baseCancel := context.WithCancel(context.Background())
	allocCtx, allocCancel := chromedp.NewExecAllocator(baseCtx, chromeExecAllocatorOptions()...)
	chromeCtx, chromeCancel := chromedp.NewContext(allocCtx)

	h := &ChromeHelper{
		ctx:     chromeCtx,
		timeout: 30 * time.Second,
	}
	h.cancel = func() {
		h.closeMux.Do(func() {
			chromeCancel()
			allocCancel()
			baseCancel()
		})
	}
	return h, nil
}

func (h *ChromeHelper) Close() {
	if h != nil && h.cancel != nil {
		h.cancel()
	}
}

func normalizeChromeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "://") {
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Host != "" {
		return parsed.String()
	}
	return "https://" + trimmed
}

func detectLoginSelectors(siteHint string) map[string]string {
	hint := strings.ToLower(strings.TrimSpace(siteHint))
	selectors := map[string]string{
		"userID": "#username, input[name='username'], input[id*='user'], input[name*='userid'], input[id*='userid'], input[autocomplete='username']",
		"password": "#password, input[type='password'], input[name='password'], input[id*='password']",
		"submit": "#kc-login, button[type='submit'], input[type='submit']",
	}
	if strings.Contains(hint, "hdfc") {
		selectors["userID"] = "#username, input[name='username'], input[id*='user'], input[name*='userid'], input[id*='userid'], input[autocomplete='username']"
		selectors["password"] = "#password, input[type='password'], input[name='password'], input[id*='password']"
		selectors["submit"] = "#kc-login, button[type='submit'], input[type='submit']"
	}
	if strings.Contains(hint, "axis") {
		selectors["userID"] = "#username, input[name='userId'], input[id*='userId'], input[name='username'], input[id*='username']"
		selectors["password"] = "#password, input[type='password'], input[name='password'], input[id*='password']"
		selectors["submit"] = "#login-button, button[type='submit'], input[type='submit']"
	}
	return selectors
}

func OpenChromeTab(url string) error {
	normalized := normalizeChromeURL(url)
	if normalized == "" {
		return newCopyableError("a URL is required to open a Chrome tab")
	}

	switch runtime.GOOS {
	case "darwin":
		cfg := chromeProfileConfig()
		if cfg.profileDir != "Default" {
			profileDir := filepath.Join(cfg.userDataDir, cfg.profileDir)
			if err := os.MkdirAll(profileDir, 0o755); err != nil {
				return newCopyableError(fmt.Sprintf("create Chrome profile directory: %v", err))
			}
		}
		cmd := exec.Command(
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"--user-data-dir="+cfg.userDataDir,
			"--profile-directory="+cfg.profileDir,
			normalized,
		)
		if err := cmd.Start(); err != nil {
			return newCopyableError(fmt.Sprintf("start Chrome: %v", err))
		}
		return nil
	case "windows":
		if err := exec.Command("cmd", "/C", "start", "", normalized).Run(); err != nil {
			return newCopyableError(fmt.Sprintf("open Chrome tab: %v", err))
		}
		return nil
	default:
		for _, browser := range []string{"google-chrome", "chromium", "chromium-browser"} {
			if _, err := exec.LookPath(browser); err == nil {
				if err := exec.Command(browser, normalized).Run(); err != nil {
					return newCopyableError(fmt.Sprintf("open tab with %s: %v", browser, err))
				}
				return nil
			}
		}
		if err := exec.Command("xdg-open", normalized).Run(); err != nil {
			return newCopyableError(fmt.Sprintf("open browser: %v", err))
		}
		return nil
	}
}

func (h *ChromeHelper) OpenTab(url string) error {
	if h == nil {
		return newCopyableError("chrome helper is nil")
	}
	if err := chromedp.Run(h.ctx, chromedp.Navigate(normalizeChromeURL(url))); err != nil {
		return newCopyableError(fmt.Sprintf("open tab: %v", err))
	}
	return nil
}

func detectBlockedAccessReason(title, body string) string {
	text := strings.ToLower(strings.TrimSpace(title + "\n" + body))
	if strings.Contains(text, "403") && strings.Contains(text, "forbidden") {
		return "403 Forbidden: the target site is rejecting automated browser access"
	}
	if strings.Contains(text, "access temporarily restricted") || strings.Contains(text, "too many requests") || strings.Contains(text, "blocked") {
		return "the site is blocking automated access with an anti-bot protection page"
	}
	return ""
}

func (h *ChromeHelper) ConfirmLoaded(timeout time.Duration) error {
	if h == nil {
		return newCopyableError("chrome helper is nil")
	}
	if timeout <= 0 {
		timeout = h.timeout
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var readyState string
		err := chromedp.Run(h.ctx, chromedp.Evaluate(`document.readyState`, &readyState))
		if err == nil && readyState == "complete" {
			var title, body string
			if err := chromedp.Run(h.ctx,
				chromedp.Title(&title),
				chromedp.Evaluate(`document.body ? document.body.innerText || '' : ''`, &body),
			); err == nil {
				if reason := detectBlockedAccessReason(title, body); reason != "" {
					return newCopyableError(fmt.Sprintf("page load reached an anti-bot page: %s", reason))
				}
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return newCopyableError(fmt.Sprintf("page did not finish loading within %s", timeout))
}

func (h *ChromeHelper) FillKnownCredentials(siteHint, userID, password string) error {
	if h == nil {
		return newCopyableError("chrome helper is nil")
	}
	selectors := detectLoginSelectors(siteHint)
	if userID != "" {
		if err := chromedp.Run(h.ctx,
			chromedp.WaitVisible(selectors["userID"], chromedp.ByQuery),
			chromedp.SetValue(selectors["userID"], userID, chromedp.ByQuery),
		); err != nil {
			return newCopyableError(fmt.Sprintf("fill user id: %v", err))
		}
	}
	if password != "" {
		if err := chromedp.Run(h.ctx,
			chromedp.WaitVisible(selectors["password"], chromedp.ByQuery),
			chromedp.SetValue(selectors["password"], password, chromedp.ByQuery),
		); err != nil {
			return newCopyableError(fmt.Sprintf("fill password: %v", err))
		}
	}
	return nil
}

func (h *ChromeHelper) RightClick(selector string) error {
	if h == nil {
		return newCopyableError("chrome helper is nil")
	}
	if selector == "" {
		return newCopyableError("selector is required")
	}
	if err := chromedp.Run(h.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var nodes []*cdp.Node
		if err := chromedp.Nodes(selector, &nodes, chromedp.ByQuery).Do(ctx); err != nil {
			return err
		}
		if len(nodes) == 0 {
			return fmt.Errorf("selector %q did not match any element", selector)
		}
		return chromedp.MouseClickNode(nodes[0], chromedp.ButtonRight).Do(ctx)
	})); err != nil {
		return newCopyableError(fmt.Sprintf("right click selector %q: %v", selector, err))
	}
	return nil
}
