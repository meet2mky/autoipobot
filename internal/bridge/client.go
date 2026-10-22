package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"autoipo/internal/envfile"
	"autoipo/internal/paths"
)

// ValidateURL accepts absolute http(s) URLs. A missing scheme defaults to
// https:// so that "example.com" is accepted.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("enter a website address")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("URL must be an absolute http:// or https:// address")
	}
	return parsed.String(), nil
}

// ErrUnreachable is returned when the bridge is not listening.
var ErrUnreachable = errors.New("extension bridge is not running")

// ErrExtensionNotConnected is returned when the bridge is up but Chrome is not.
var ErrExtensionNotConnected = errors.New("Chrome extension is not connected to the bridge")

// Client talks to a running bridge over HTTP.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a client for the configured bridge address. The session
// token is read lazily from .data/bridge.token on each request so that a
// bridge restarted with a new token keeps working.
func NewClient() *Client {
	return &Client{BaseURL: BaseURL(), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

func (c *Client) token() string {
	if c.Token != "" {
		return c.Token
	}
	token, _ := LoadToken()
	return token
}

func (c *Client) do(ctx context.Context, method, path string, body any, timeout time.Duration) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		// The caller reads the body; tie cancellation to body close via wrapper.
		response, err := c.doRequest(ctx, method, path, reader)
		if err != nil {
			cancel()
			return nil, err
		}
		response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
		return response, nil
	}
	return c.doRequest(ctx, method, path, reader)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := c.token(); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w (%s)", ErrUnreachable, err)
	}
	return response, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// Health reports whether the bridge is listening.
func (c *Client) Health(ctx context.Context) error {
	response, err := c.do(ctx, http.MethodGet, "/health", nil, 1500*time.Millisecond)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("bridge health returned %s", response.Status)
	}
	return nil
}

// Reachable is a convenience boolean form of Health.
func (c *Client) Reachable(ctx context.Context) bool { return c.Health(ctx) == nil }

// Status fetches the bridge status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	response, err := c.do(ctx, http.MethodGet, "/status", nil, 1500*time.Millisecond)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("bridge status returned %s", response.Status)
	}
	var status Status
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode bridge status: %w", err)
	}
	return status, nil
}

// WaitForExtension blocks until the extension is connected, the context ends,
// or timeout elapses.
func (c *Client) WaitForExtension(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := c.Status(ctx)
		if err == nil && status.ExtensionConnected {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return ErrExtensionNotConnected
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Send submits a command and waits for the extension's result. A failed
// command yields a *CommandError; transport problems yield other errors.
func (c *Client) Send(ctx context.Context, request Request) (Result, error) {
	if err := Validate(request); err != nil {
		return Result{}, err
	}
	response, err := c.do(ctx, http.MethodPost, "/command", request, 0)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read bridge response: %w", err)
	}
	var result Result
	if len(body) > 0 && json.Unmarshal(body, &result) == nil && (result.OK || result.Error != "") {
		if !result.OK {
			return result, &CommandError{Type: request.Type, Message: result.Error, Status: response.StatusCode}
		}
		return result, nil
	}
	message := strings.TrimSpace(string(body))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return Result{}, &CommandError{Type: request.Type, Message: "bridge rejected the request (" + message + "); restart serve from this app so both sides share a session token", Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, &CommandError{Type: request.Type, Message: fmt.Sprintf("bridge returned %s: %s", response.Status, message), Status: response.StatusCode}
	}
	return Result{}, fmt.Errorf("bridge returned an unreadable response")
}

// Exec runs a CLI-style command by name and returns the result.
func (c *Client) Exec(ctx context.Context, name string, args []string) (Result, error) {
	request, err := BuildRequest(name, args)
	if err != nil {
		return Result{}, err
	}
	return c.Send(ctx, request)
}

// PageContains reports whether the target page has an element matching selector.
func (c *Client) PageContains(ctx context.Context, selector string) (bool, error) {
	result, err := c.Send(ctx, Request{Type: TypePageContains, Selector: selector})
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(result.Output)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("page-contains returned %q", result.Output)
	}
}

// Session token -----------------------------------------------------------

// LoadToken returns the bridge session token, or "" when none exists.
func LoadToken() (string, error) {
	data, err := os.ReadFile(paths.BridgeTokenFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// EnsureToken returns the existing session token or creates a new random one
// in .data/bridge.token (0600).
func EnsureToken() (string, error) {
	token, err := LoadToken()
	if err != nil {
		return "", err
	}
	if token != "" {
		return token, nil
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw[:])
	if err := paths.EnsureDataDir(); err != nil {
		return "", err
	}
	if err := envfile.WriteFileAtomic(paths.BridgeTokenFile(), []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// Process supervision -----------------------------------------------------

// Process is a bridge child process started by this program.
type Process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// StartServe launches `<binary> serve` as a child process. stdout/stderr go
// to output (may be nil).
func StartServe(binary string, output io.Writer) (*Process, error) {
	if _, err := os.Stat(binary); err != nil {
		return nil, fmt.Errorf("bridge binary not found at %s: %w", binary, err)
	}
	cmd := exec.Command(binary, "serve")
	cmd.Dir = paths.Root()
	if output != nil {
		cmd.Stdout = output
		cmd.Stderr = output
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start extension bridge: %w", err)
	}
	process := &Process{cmd: cmd, done: make(chan struct{})}
	go func() {
		process.err = cmd.Wait()
		close(process.done)
	}()
	return process, nil
}

// PID returns the child's process ID.
func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Done is closed when the child exits.
func (p *Process) Done() <-chan struct{} {
	if p == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return p.done
}

// Running reports whether the child is still alive.
func (p *Process) Running() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Err returns the exit error once Done is closed.
func (p *Process) Err() error {
	if p == nil {
		return nil
	}
	<-p.done
	return p.err
}

// Stop asks the child to shut down gracefully (SIGINT, which serve handles),
// killing it if it does not exit within timeout.
func (p *Process) Stop(timeout time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil || !p.Running() {
		return nil
	}
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		_ = p.cmd.Process.Kill()
		<-p.done
		return nil
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		return nil
	}
}

// EnsureServing makes sure a bridge is reachable, starting one from binary if
// necessary. It returns the child process when it started one (nil when a
// bridge was already running).
func EnsureServing(ctx context.Context, client *Client, binary string, output io.Writer, timeout time.Duration) (*Process, error) {
	if client.Reachable(ctx) {
		return nil, nil
	}
	process, err := StartServe(binary, output)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if client.Reachable(ctx) {
			return process, nil
		}
		select {
		case <-process.Done():
			return nil, fmt.Errorf("extension bridge exited before becoming ready: %v", process.Err())
		case <-ctx.Done():
			_ = process.Stop(time.Second)
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = process.Stop(time.Second)
			return nil, fmt.Errorf("extension bridge did not become ready within %s", timeout)
		}
	}
}
