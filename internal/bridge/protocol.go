// Package bridge defines the wire protocol between the local extension bridge
// (chrome-extension-cli serve), the Chrome extension, and the Go programs that
// drive it (the desktop app, the daemon and the CLI). It also provides the Go
// HTTP client used by all of them.
package bridge

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// DefaultPort is the loopback port the bridge listens on. Override with
// AUTOIPO_BRIDGE_PORT.
const DefaultPort = 18765

// EnvPort names the environment variable that overrides DefaultPort.
const EnvPort = "AUTOIPO_BRIDGE_PORT"

// Port returns the configured bridge port.
func Port() int {
	if raw := strings.TrimSpace(os.Getenv(EnvPort)); raw != "" {
		if port, err := strconv.Atoi(raw); err == nil && port > 0 && port < 65536 {
			return port
		}
	}
	return DefaultPort
}

// Addr returns the host:port the bridge binds to (always loopback).
func Addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(Port())) }

// BaseURL returns the HTTP base URL of the bridge.
func BaseURL() string { return "http://" + Addr() }

// WebSocketURL returns the URL the extension connects to.
func WebSocketURL() string { return "ws://" + Addr() + "/ws" }

// Command types understood by the extension.
const (
	TypeSetURL        = "set_url"
	TypeOpen          = "open"
	TypeClick         = "click"
	TypeFill          = "fill"
	TypeDropdown      = "dropdown"
	TypeChoose        = "choose"
	TypeListSelectors = "list_selectors"
	TypePageContains  = "page_contains"
	TypePressKey      = "press_key"
	TypeReadText      = "read_text"
	TypeScreenshot    = "screenshot"
)

// Request is a command submitted to POST /command.
type Request struct {
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
	Key      string `json:"key,omitempty"`
}

// Command is a Request with an ID, as delivered to the extension.
type Command struct {
	ID string `json:"id"`
	Request
}

// Choice is one selectable option of a dropdown or autocomplete field.
type Choice struct {
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
}

// String renders a choice the way the CLI prints it.
func (c Choice) String() string {
	if c.Value != "" && c.Value != c.Label {
		return c.Label + " [" + c.Value + "]"
	}
	return c.Label
}

// ParseChoice is the inverse of Choice.String for legacy "Label [value]" text.
func ParseChoice(display string) Choice {
	display = strings.TrimSpace(display)
	if open := strings.LastIndex(display, " ["); open >= 0 && strings.HasSuffix(display, "]") {
		return Choice{Label: display[:open], Value: display[open+2 : len(display)-1]}
	}
	return Choice{Label: display, Value: display}
}

// SelectorMetadata describes one interactive element found by list_selectors.
type SelectorMetadata struct {
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context,omitempty"`
	Selector    string `json:"selector"`
}

// Result is the outcome of a command, as returned by POST /command.
type Result struct {
	OK         bool               `json:"ok"`
	Error      string             `json:"error,omitempty"`
	Output     string             `json:"output,omitempty"`
	Choices    []Choice           `json:"choices,omitempty"`
	Elements   []SelectorMetadata `json:"elements,omitempty"`
	Screenshot string             `json:"screenshot,omitempty"` // base64 PNG
	TabID      int                `json:"tab_id,omitempty"`
}

// Ack is the message the extension sends back over the WebSocket.
type Ack struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Result
	// LegacyOptions accepts the pre-protocol-v2 "Label [value]" strings.
	LegacyOptions []string `json:"options,omitempty"`
}

// Normalize converts legacy fields into their current equivalents.
func (a *Ack) Normalize() {
	if len(a.Choices) == 0 && len(a.LegacyOptions) > 0 {
		for _, option := range a.LegacyOptions {
			a.Choices = append(a.Choices, ParseChoice(option))
		}
	}
	a.LegacyOptions = nil
}

// Status is returned by GET /status.
type Status struct {
	Serving            bool   `json:"serving"`
	ExtensionConnected bool   `json:"extension_connected"`
	ExtensionOrigin    string `json:"extension_origin,omitempty"`
	PendingCommands    int    `json:"pending_commands"`
	UptimeSeconds      int64  `json:"uptime_seconds"`
	LastCommandAt      string `json:"last_command_at,omitempty"`
	ProtocolVersion    int    `json:"protocol_version"`
}

// ProtocolVersion is bumped whenever the WebSocket message shape changes.
const ProtocolVersion = 2

// CommandError is returned by Client.Send when the bridge or extension
// reports a failed command (as opposed to a transport failure).
type CommandError struct {
	Type    string
	Message string
	Status  int
}

func (e *CommandError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s failed", e.Type)
	}
	return e.Message
}

// Human-facing CLI command names and their bridge types.
var cliCommands = map[string]struct {
	Type string
	Args int
}{
	"set-url":        {TypeSetURL, 1},
	"open":           {TypeOpen, 0},
	"click":          {TypeClick, 1},
	"fill":           {TypeFill, 2},
	"dropdown":       {TypeDropdown, 1},
	"choose":         {TypeChoose, 2},
	"list-selectors": {TypeListSelectors, 0},
	"page-contains":  {TypePageContains, 1},
	"press-key":      {TypePressKey, 1},
	"read-text":      {TypeReadText, 1},
	"screenshot":     {TypeScreenshot, 0},
}

// CLICommands lists the bridge-backed command names in display order.
var CLICommands = []string{
	"set-url", "open", "click", "fill", "dropdown", "choose",
	"press-key", "read-text", "list-selectors", "page-contains", "screenshot",
}

// CommandArgCount returns how many positional arguments a CLI command takes.
func CommandArgCount(name string) (int, bool) {
	spec, ok := cliCommands[name]
	return spec.Args, ok
}

// ArgPlaceholders describes the arguments of a CLI command for UI hints.
func ArgPlaceholders(name string) []string {
	switch name {
	case "set-url":
		return []string{"URL"}
	case "click", "dropdown", "page-contains", "read-text":
		return []string{"CSS selector"}
	case "fill":
		return []string{"CSS selector", "Text"}
	case "choose":
		return []string{"CSS selector", "Option label or value"}
	case "press-key":
		return []string{"Key (Escape, Enter, Tab, ArrowDown…)"}
	case "random-sleep":
		return []string{"Maximum seconds"}
	default:
		return nil
	}
}

// BuildRequest converts a CLI-style command and arguments into a Request.
func BuildRequest(name string, args []string) (Request, error) {
	spec, ok := cliCommands[name]
	if !ok {
		return Request{}, fmt.Errorf("unknown command %q", name)
	}
	if len(args) != spec.Args {
		return Request{}, fmt.Errorf("%s expects %d argument(s), got %d", name, spec.Args, len(args))
	}
	request := Request{Type: spec.Type}
	switch spec.Type {
	case TypeSetURL:
		validated, err := ValidateURL(args[0])
		if err != nil {
			return Request{}, err
		}
		request.URL = validated
	case TypeClick, TypeDropdown, TypePageContains, TypeReadText:
		request.Selector = args[0]
	case TypeFill, TypeChoose:
		request.Selector, request.Value = args[0], args[1]
	case TypePressKey:
		request.Key = args[0]
	}
	return request, Validate(request)
}

// Validate checks a Request independently of how it was built.
func Validate(request Request) error {
	switch request.Type {
	case TypeOpen, TypeListSelectors, TypeScreenshot:
		return nil
	case TypeSetURL:
		_, err := ValidateURL(request.URL)
		return err
	case TypeClick, TypeFill, TypeDropdown, TypeChoose, TypePageContains, TypeReadText:
		if strings.TrimSpace(request.Selector) == "" || len(request.Selector) > 2048 {
			return fmt.Errorf("selector must contain 1 to 2048 characters")
		}
		if len(request.Value) > 65536 {
			return fmt.Errorf("value is too long")
		}
		return nil
	case TypePressKey:
		if strings.TrimSpace(request.Key) == "" || len(request.Key) > 32 {
			return fmt.Errorf("key name must contain 1 to 32 characters")
		}
		return nil
	default:
		return fmt.Errorf("unknown command type %q", request.Type)
	}
}

// RedactedArgs returns args suitable for logging: fill values are hidden.
func RedactedArgs(name string, args []string) []string {
	if name != "fill" || len(args) < 2 {
		return args
	}
	redacted := append([]string(nil), args...)
	redacted[1] = "[value redacted]"
	return redacted
}

// FormatCommand renders a command line for logs with secrets redacted.
func FormatCommand(name string, args []string) string {
	parts := []string{"chrome-extension-cli", name}
	for _, arg := range RedactedArgs(name, args) {
		if arg == "[value redacted]" {
			parts = append(parts, arg)
			continue
		}
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

// FormatResult renders a successful Result as human-readable text.
func FormatResult(request Request, result Result) string {
	switch request.Type {
	case TypeListSelectors:
		if len(result.Elements) == 0 {
			return "No visible clickable or fillable elements found"
		}
		var lines []string
		for index, element := range result.Elements {
			line := fmt.Sprintf("%d. %s · %s", index+1, element.Kind, firstNonEmpty(element.Label, element.Description))
			if element.Context != "" {
				line += " (for: " + element.Context + ")"
			}
			line += "\n   " + element.Selector
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	case TypeDropdown:
		if len(result.Choices) == 0 {
			return "No dropdown options found"
		}
		var lines []string
		for index, choice := range result.Choices {
			lines = append(lines, fmt.Sprintf("%d. %s", index+1, choice.String()))
		}
		return strings.Join(lines, "\n")
	case TypePageContains, TypeReadText:
		return result.Output
	case TypeScreenshot:
		return "screenshot captured"
	default:
		if strings.TrimSpace(result.Output) != "" {
			return result.Output
		}
		return "Command completed"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// DecodeScreenshot returns the PNG bytes of a screenshot result.
func DecodeScreenshot(result Result) ([]byte, error) {
	encoded := strings.TrimSpace(result.Screenshot)
	if encoded == "" {
		return nil, nil
	}
	if comma := strings.IndexByte(encoded, ','); comma >= 0 && strings.HasPrefix(encoded, "data:") {
		encoded = encoded[comma+1:]
	}
	return base64.StdEncoding.DecodeString(encoded)
}
