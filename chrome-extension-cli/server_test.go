package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"autoipo/internal/bridge"
)

const (
	testToken       = "test-session-token"
	testExtensionID = "abcdefghijklmnopabcdefghijklmnop"
	testOrigin      = "chrome-extension://" + testExtensionID
)

// Test harness -------------------------------------------------------------

// testBridge is a bridgeServer listening on an ephemeral loopback port.
type testBridge struct {
	server  *bridgeServer
	addr    string
	baseURL string
	stop    func() error
}

func startBridge(t *testing.T, configure func(*serverConfig)) *testBridge {
	t.Helper()
	cfg := defaultServerConfig()
	cfg.addr = "127.0.0.1:0"
	cfg.token = testToken
	cfg.logger = log.New(io.Discard, "", 0)
	if configure != nil {
		configure(&cfg)
	}
	server, err := newBridgeServer(cfg)
	if err != nil {
		t.Fatalf("newBridgeServer: %v", err)
	}
	listener, err := server.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- server.serve(ctx, listener) }()
	var (
		once    sync.Once
		stopErr error
	)
	stop := func() error {
		once.Do(func() {
			cancel()
			stopErr = <-served
		})
		return stopErr
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("serve returned %v", err)
		}
	})
	addr := listener.Addr().String()
	return &testBridge{server: server, addr: addr, baseURL: "http://" + addr, stop: stop}
}

func authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + testToken}
}

// do performs one HTTP request against the bridge and returns the response
// with its body already read.
func (tb *testBridge) do(t *testing.T, method, path string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, tb.baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response, data
}

type commandOutcome struct {
	status  int
	result  bridge.Result
	elapsed time.Duration
	err     error
}

// postCommand submits an authorized command in the background.
func (tb *testBridge) postCommand(request bridge.Request) <-chan commandOutcome {
	outcome := make(chan commandOutcome, 1)
	go func() {
		started := time.Now()
		body, _ := json.Marshal(request)
		httpRequest, err := http.NewRequest(http.MethodPost, tb.baseURL+"/command", bytes.NewReader(body))
		if err != nil {
			outcome <- commandOutcome{err: err}
			return
		}
		httpRequest.Header.Set("Authorization", "Bearer "+testToken)
		response, err := http.DefaultClient.Do(httpRequest)
		if err != nil {
			outcome <- commandOutcome{err: err}
			return
		}
		defer response.Body.Close()
		var result bridge.Result
		err = json.NewDecoder(response.Body).Decode(&result)
		outcome <- commandOutcome{status: response.StatusCode, result: result, elapsed: time.Since(started), err: err}
	}()
	return outcome
}

func awaitOutcome(t *testing.T, outcome <-chan commandOutcome) commandOutcome {
	t.Helper()
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("command request failed: %v", got.err)
		}
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("command did not complete in time")
		return commandOutcome{}
	}
}

func (tb *testBridge) status(t *testing.T) bridge.Status {
	t.Helper()
	response, body := tb.do(t, http.MethodGet, "/status", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /status = %d: %s", response.StatusCode, body)
	}
	var status bridge.Status
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return status
}

// waitFor polls condition until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Minimal WebSocket client -----------------------------------------------------

type wsClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

// dialWS performs the opening handshake. Entries in headers override the
// defaults; an empty value removes the header.
func dialWS(tb *testBridge, headers map[string]string) (*wsClient, *http.Response, error) {
	conn, err := net.DialTimeout("tcp", tb.addr, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	var key [16]byte
	_, _ = rand.Read(key[:])
	fields := map[string]string{
		"Host":                  tb.addr,
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Key":     base64.StdEncoding.EncodeToString(key[:]),
		"Sec-WebSocket-Version": "13",
		"Origin":                testOrigin,
	}
	for name, value := range headers {
		if value == "" {
			delete(fields, name)
			continue
		}
		fields[name] = value
	}
	var handshake strings.Builder
	handshake.WriteString("GET /ws HTTP/1.1\r\n")
	for name, value := range fields {
		fmt.Fprintf(&handshake, "%s: %s\r\n", name, value)
	}
	handshake.WriteString("\r\n")
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(handshake.String())); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		_ = conn.Close()
		return nil, response, fmt.Errorf("handshake status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsClient{conn: conn, reader: reader}, response, nil
}

func mustDialWS(t *testing.T, tb *testBridge) *wsClient {
	t.Helper()
	client, _, err := dialWS(tb, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = client.conn.Close() })
	return client
}

// writeFrame sends one client frame; mask=false produces a protocol violation.
func (c *wsClient) writeFrame(opcode byte, payload []byte, fin, mask bool) error {
	header := opcode
	if fin {
		header |= 0x80
	}
	var maskBit byte
	if mask {
		maskBit = 0x80
	}
	buf := []byte{header}
	switch length := len(payload); {
	case length < 126:
		buf = append(buf, maskBit|byte(length))
	case length <= 0xffff:
		buf = append(buf, maskBit|126)
		buf = binary.BigEndian.AppendUint16(buf, uint16(length))
	default:
		buf = append(buf, maskBit|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(length))
	}
	if mask {
		var key [4]byte
		_, _ = rand.Read(key[:])
		buf = append(buf, key[:]...)
		for i, b := range payload {
			buf = append(buf, b^key[i&3])
		}
	} else {
		buf = append(buf, payload...)
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write(buf)
	return err
}

func (c *wsClient) send(t *testing.T, opcode byte, payload []byte) {
	t.Helper()
	if err := c.writeFrame(opcode, payload, true, true); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func (c *wsClient) sendJSON(t *testing.T, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c.send(t, opcodeText, payload)
}

// readFrame reads one (unmasked) server frame.
func (c *wsClient) readFrame(timeout time.Duration) (byte, []byte, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	var header [2]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		return 0, nil, err
	}
	if header[1]&0x80 != 0 {
		return 0, nil, errors.New("server frame must not be masked")
	}
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(c.reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0] & 0x0f, payload, nil
}

func isHeartbeat(payload []byte) bool {
	var message struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(payload, &message) == nil && message.Type == "heartbeat"
}

// readCommand returns the next command sent by the bridge, skipping heartbeats.
func (c *wsClient) readCommand(t *testing.T) bridge.Command {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		opcode, payload, err := c.readFrame(time.Until(deadline))
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if opcode != opcodeText || isHeartbeat(payload) {
			continue
		}
		var command bridge.Command
		if err := json.Unmarshal(payload, &command); err != nil {
			t.Fatalf("decode command %q: %v", payload, err)
		}
		return command
	}
	t.Fatal("no command received")
	return bridge.Command{}
}

// expectClose reads until a close frame arrives and returns its status code.
func (c *wsClient) expectClose(t *testing.T, timeout time.Duration) uint16 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		opcode, payload, err := c.readFrame(time.Until(deadline))
		if err != nil {
			t.Fatalf("expected a close frame, got error %v", err)
		}
		if opcode == opcodeClose {
			code, _ := parseClosePayload(payload)
			return code
		}
	}
}

func (c *wsClient) ack(t *testing.T, id string, fields map[string]any) {
	t.Helper()
	message := map[string]any{"type": "ack", "id": id, "ok": true}
	for key, value := range fields {
		message[key] = value
	}
	c.sendJSON(t, message)
}

// Tests: HTTP endpoints ----------------------------------------------------------

func TestHealthEndpoint(t *testing.T) {
	tb := startBridge(t, nil)
	response, _ := tb.do(t, http.MethodGet, "/health", nil, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("GET /health = %d, want 204", response.StatusCode)
	}
	response, _ = tb.do(t, http.MethodGet, "/health", nil, map[string]string{"Origin": testOrigin})
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Origin") != testOrigin {
		t.Fatalf("GET /health with extension origin = %d, ACAO %q", response.StatusCode, response.Header.Get("Access-Control-Allow-Origin"))
	}
	response, _ = tb.do(t, http.MethodGet, "/health", nil, map[string]string{"Origin": "https://evil.example"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /health with web origin = %d, want 403", response.StatusCode)
	}
}

func TestStatusShape(t *testing.T) {
	tb := startBridge(t, nil)
	response, body := tb.do(t, http.MethodGet, "/status", nil, nil)
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("GET /status = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	for _, key := range []string{"serving", "extension_connected", "pending_commands", "uptime_seconds", "protocol_version"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("status is missing %q: %s", key, body)
		}
	}
	for _, key := range []string{"extension_origin", "last_command_at"} {
		if _, ok := fields[key]; ok {
			t.Errorf("status should omit empty %q: %s", key, body)
		}
	}
	status := tb.status(t)
	if !status.Serving || status.ExtensionConnected || status.PendingCommands != 0 || status.ProtocolVersion != bridge.ProtocolVersion || status.UptimeSeconds < 0 {
		t.Fatalf("unexpected status %+v", status)
	}
}

func TestCommandRequiresToken(t *testing.T) {
	tb := startBridge(t, nil)
	body := []byte(`{"type":"open"}`)
	response, _ := tb.do(t, http.MethodPost, "/command", body, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /command without token = %d, want 401", response.StatusCode)
	}
	response, _ = tb.do(t, http.MethodPost, "/command", body, map[string]string{"Authorization": "Bearer wrong"})
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /command with wrong token = %d, want 401", response.StatusCode)
	}
	response, _ = tb.do(t, http.MethodGet, "/command", nil, authHeaders())
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /command = %d, want 405", response.StatusCode)
	}
}

func TestCommandOriginRules(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.commandTimeout = 100 * time.Millisecond })
	headers := authHeaders()
	headers["Origin"] = "https://evil.example"
	response, _ := tb.do(t, http.MethodPost, "/command", []byte(`{"type":"open"}`), headers)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("POST /command with web origin = %d, want 403", response.StatusCode)
	}
	headers["Origin"] = testOrigin
	response, _ = tb.do(t, http.MethodPost, "/command", []byte(`{"type":"open"}`), headers)
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("POST /command with extension origin = %d, want 504 (accepted, no extension)", response.StatusCode)
	}
}

func TestCommandValidation(t *testing.T) {
	tb := startBridge(t, nil)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid JSON", `{"type":`, http.StatusBadRequest},
		{"unknown type", `{"type":"explode"}`, http.StatusBadRequest},
		{"missing selector", `{"type":"click","selector":"  "}`, http.StatusBadRequest},
		{"bad URL", `{"type":"set_url","url":"ftp://x"}`, http.StatusBadRequest},
		{"oversized", `{"type":"fill","selector":"#a","value":"` + strings.Repeat("x", maxCommandBody) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		response, body := tb.do(t, http.MethodPost, "/command", []byte(tc.body), authHeaders())
		if response.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, response.StatusCode, tc.want, body)
		}
	}
}

// Tests: command relay ----------------------------------------------------------

func TestCommandRoundTrip(t *testing.T) {
	tb := startBridge(t, nil)
	ws := mustDialWS(t, tb)
	waitFor(t, 2*time.Second, func() bool { return tb.status(t).ExtensionConnected }, "extension never showed as connected")
	if status := tb.status(t); status.ExtensionOrigin != testOrigin {
		t.Fatalf("status origin = %q, want %q", status.ExtensionOrigin, testOrigin)
	}

	// Protocol v2 ack with choices.
	outcome := tb.postCommand(bridge.Request{Type: bridge.TypeDropdown, Selector: "#bank"})
	command := ws.readCommand(t)
	if command.ID == "" || command.Type != bridge.TypeDropdown || command.Selector != "#bank" {
		t.Fatalf("unexpected command %+v", command)
	}
	if status := tb.status(t); status.PendingCommands != 1 {
		t.Fatalf("pending_commands = %d while in flight, want 1", status.PendingCommands)
	}
	ws.ack(t, command.ID, map[string]any{"choices": []map[string]string{{"label": "HDFC", "value": "hdfc"}}, "output": "1 choices", "tab_id": 7})
	got := awaitOutcome(t, outcome)
	if got.status != http.StatusOK || !got.result.OK || len(got.result.Choices) != 1 || got.result.Choices[0] != (bridge.Choice{Label: "HDFC", Value: "hdfc"}) || got.result.TabID != 7 {
		t.Fatalf("round trip = %d %+v", got.status, got.result)
	}

	// Legacy ack with "Label [value]" options is normalized into choices.
	outcome = tb.postCommand(bridge.Request{Type: bridge.TypeDropdown, Selector: "#bank"})
	command = ws.readCommand(t)
	ws.ack(t, command.ID, map[string]any{"options": []string{"ICICI [icici]", "Plain"}})
	got = awaitOutcome(t, outcome)
	want := []bridge.Choice{{Label: "ICICI", Value: "icici"}, {Label: "Plain", Value: "Plain"}}
	if got.status != http.StatusOK || len(got.result.Choices) != 2 || got.result.Choices[0] != want[0] || got.result.Choices[1] != want[1] {
		t.Fatalf("legacy options round trip = %d %+v", got.status, got.result)
	}

	// set_url is normalized before delivery.
	outcome = tb.postCommand(bridge.Request{Type: bridge.TypeSetURL, URL: "example.com/login"})
	command = ws.readCommand(t)
	if command.URL != "https://example.com/login" {
		t.Fatalf("delivered URL = %q", command.URL)
	}
	ws.ack(t, command.ID, nil)
	awaitOutcome(t, outcome)

	// A failed ack becomes a 502 with the extension's error.
	outcome = tb.postCommand(bridge.Request{Type: bridge.TypeClick, Selector: "#missing"})
	command = ws.readCommand(t)
	ws.ack(t, command.ID, map[string]any{"ok": false, "error": "no such element"})
	got = awaitOutcome(t, outcome)
	if got.status != http.StatusBadGateway || got.result.OK || got.result.Error != "no such element" {
		t.Fatalf("failed ack = %d %+v", got.status, got.result)
	}

	status := tb.status(t)
	if status.PendingCommands != 0 || status.LastCommandAt == "" {
		t.Fatalf("status after commands = %+v", status)
	}
	if _, err := time.Parse(time.RFC3339, status.LastCommandAt); err != nil {
		t.Fatalf("last_command_at %q is not RFC3339: %v", status.LastCommandAt, err)
	}
}

func TestPendingCommandsFailFastOnDisconnect(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.commandTimeout = 10 * time.Second })
	ws := mustDialWS(t, tb)
	first := tb.postCommand(bridge.Request{Type: bridge.TypeOpen})
	second := tb.postCommand(bridge.Request{Type: bridge.TypeListSelectors})
	ws.readCommand(t)
	ws.readCommand(t)
	_ = ws.conn.Close() // abrupt loss, no close frame
	for _, outcome := range []<-chan commandOutcome{first, second} {
		got := awaitOutcome(t, outcome)
		if got.status != http.StatusBadGateway || got.result.OK || got.result.Error != reasonExtensionDisconnected {
			t.Fatalf("outcome after disconnect = %d %+v", got.status, got.result)
		}
		if got.elapsed > 3*time.Second {
			t.Fatalf("command took %s to fail; expected an immediate failure", got.elapsed)
		}
	}
	waitFor(t, 2*time.Second, func() bool { return !tb.status(t).ExtensionConnected }, "status still reports the extension as connected")
	if status := tb.status(t); status.PendingCommands != 0 {
		t.Fatalf("pending_commands = %d after disconnect, want 0", status.PendingCommands)
	}
}

func TestNewConnectionReplacesOld(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.commandTimeout = 10 * time.Second })
	first := mustDialWS(t, tb)
	outcome := tb.postCommand(bridge.Request{Type: bridge.TypeOpen})
	first.readCommand(t)

	second := mustDialWS(t, tb)
	if code := first.expectClose(t, 3*time.Second); code != closeNormal {
		t.Fatalf("old connection close code = %d, want %d", code, closeNormal)
	}
	got := awaitOutcome(t, outcome)
	if got.status != http.StatusBadGateway || got.result.Error != reasonExtensionDisconnected {
		t.Fatalf("command on replaced connection = %d %+v", got.status, got.result)
	}

	outcome = tb.postCommand(bridge.Request{Type: bridge.TypeScreenshot})
	command := second.readCommand(t)
	second.ack(t, command.ID, map[string]any{"screenshot": "data:image/png;base64,aGVsbG8="})
	got = awaitOutcome(t, outcome)
	if got.status != http.StatusOK || got.result.Screenshot != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("command on new connection = %d %+v", got.status, got.result)
	}
}

func TestCommandTimeout(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.commandTimeout = 150 * time.Millisecond })
	got := awaitOutcome(t, tb.postCommand(bridge.Request{Type: bridge.TypeOpen}))
	if got.status != http.StatusGatewayTimeout || got.result.OK || !strings.Contains(got.result.Error, "no Chrome extension connected") {
		t.Fatalf("timeout without extension = %d %+v", got.status, got.result)
	}

	ws := mustDialWS(t, tb)
	outcome := tb.postCommand(bridge.Request{Type: bridge.TypeOpen})
	command := ws.readCommand(t) // delivered but never acknowledged
	got = awaitOutcome(t, outcome)
	if got.status != http.StatusGatewayTimeout || !strings.Contains(got.result.Error, "did not respond") {
		t.Fatalf("timeout with silent extension = %d %+v", got.status, got.result)
	}
	if status := tb.status(t); status.PendingCommands != 0 {
		t.Fatalf("pending_commands = %d after timeout, want 0", status.PendingCommands)
	}
	// A late ack for the expired command is ignored without side effects.
	ws.ack(t, command.ID, nil)
	ws.send(t, opcodePing, []byte("still here"))
	if opcode, payload, err := ws.readFrame(2 * time.Second); err != nil || opcode != opcodePong || string(payload) != "still here" {
		t.Fatalf("connection unusable after late ack: %v %d %q", err, opcode, payload)
	}
}

// Tests: liveness -----------------------------------------------------------------

func TestHeartbeatEmitted(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.heartbeatInterval = 40 * time.Millisecond })
	ws := mustDialWS(t, tb)
	opcode, payload, err := ws.readFrame(2 * time.Second)
	if err != nil || opcode != opcodeText || !isHeartbeat(payload) {
		t.Fatalf("first frame = %d %q (%v), want heartbeat", opcode, payload, err)
	}
}

func TestReadDeadlineDisconnectsSilentExtension(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) {
		cfg.heartbeatInterval = 40 * time.Millisecond
		cfg.readDeadline = 200 * time.Millisecond
	})
	ws := mustDialWS(t, tb)
	if code := ws.expectClose(t, 3*time.Second); code != closeGoingAway {
		t.Fatalf("close code = %d, want %d", code, closeGoingAway)
	}
	waitFor(t, 2*time.Second, func() bool { return !tb.status(t).ExtensionConnected }, "silent extension still reported as connected")
}

func TestHeartbeatAckRefreshesReadDeadline(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) {
		cfg.heartbeatInterval = 40 * time.Millisecond
		cfg.readDeadline = 400 * time.Millisecond
	})
	ws := mustDialWS(t, tb)
	until := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(until) {
		opcode, payload, err := ws.readFrame(time.Second)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if opcode == opcodeClose {
			t.Fatal("bridge closed a connection that answered every heartbeat")
		}
		if opcode == opcodeText && isHeartbeat(payload) {
			ws.sendJSON(t, map[string]string{"type": "heartbeat_ack"})
		}
	}
	if !tb.status(t).ExtensionConnected {
		t.Fatal("extension should still be connected")
	}
}

// Tests: WebSocket handshake and framing ------------------------------------------------

func TestWebSocketOriginRules(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.extensionID = testExtensionID })
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"missing origin", map[string]string{"Origin": ""}, http.StatusForbidden},
		{"web origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"other extension", map[string]string{"Origin": "chrome-extension://zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}, http.StatusForbidden},
		{"not an upgrade", map[string]string{"Upgrade": "", "Connection": ""}, http.StatusBadRequest},
		{"bad version", map[string]string{"Sec-WebSocket-Version": "8"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		client, response, err := dialWS(tb, tc.headers)
		if err == nil {
			_ = client.conn.Close()
			t.Errorf("%s: handshake succeeded, want %d", tc.name, tc.want)
			continue
		}
		if response == nil || response.StatusCode != tc.want {
			t.Errorf("%s: %v, want status %d", tc.name, err, tc.want)
		}
	}
	client, response, err := dialWS(tb, nil)
	if err != nil {
		t.Fatalf("configured extension rejected: %v", err)
	}
	defer client.conn.Close()
	if response.Header.Get("Sec-WebSocket-Accept") == "" || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
		t.Fatalf("handshake response headers = %v", response.Header)
	}
}

func TestWebSocketControlFrames(t *testing.T) {
	tb := startBridge(t, nil)
	ws := mustDialWS(t, tb)
	ws.send(t, opcodePing, []byte("ping!"))
	if opcode, payload, err := ws.readFrame(2 * time.Second); err != nil || opcode != opcodePong || string(payload) != "ping!" {
		t.Fatalf("pong = %d %q (%v)", opcode, payload, err)
	}
	ws.send(t, opcodeClose, closePayload(closeNormal, "bye"))
	if code := ws.expectClose(t, 2*time.Second); code != closeNormal {
		t.Fatalf("echoed close code = %d, want %d", code, closeNormal)
	}
	if _, _, err := ws.readFrame(2 * time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("after close handshake err = %v, want EOF", err)
	}
	waitFor(t, 2*time.Second, func() bool { return !tb.status(t).ExtensionConnected }, "closed extension still reported as connected")
}

func TestWebSocketRejectsInvalidFrames(t *testing.T) {
	tb := startBridge(t, nil)
	cases := []struct {
		name  string
		write func(*wsClient) error
		want  uint16
	}{
		{"unmasked", func(c *wsClient) error {
			return c.writeFrame(opcodeText, []byte(`{"type":"heartbeat_ack"}`), true, false)
		}, closeProtocolError},
		{"stray continuation", func(c *wsClient) error { return c.writeFrame(opcodeContinuation, []byte("part"), true, true) }, closeProtocolError},
		{"data frame inside fragmented message", func(c *wsClient) error {
			if err := c.writeFrame(opcodeText, []byte("part"), false, true); err != nil {
				return err
			}
			return c.writeFrame(opcodeText, []byte(`{"type":"heartbeat_ack"}`), true, true)
		}, closeProtocolError},
		{"fragmented control frame", func(c *wsClient) error { return c.writeFrame(opcodePing, []byte("p"), false, true) }, closeProtocolError},
		{"reserved opcode", func(c *wsClient) error { return c.writeFrame(0x3, nil, true, true) }, closeProtocolError},
		{"oversized", func(c *wsClient) error {
			header := []byte{0x80 | opcodeText, 0x80 | 127}
			header = binary.BigEndian.AppendUint64(header, maxExtensionMessage+1)
			_, err := c.conn.Write(header)
			return err
		}, closeMessageTooBig},
	}
	for _, tc := range cases {
		client, _, err := dialWS(tb, nil)
		if err != nil {
			t.Fatalf("%s: dial: %v", tc.name, err)
		}
		if err := tc.write(client); err != nil {
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		if code := client.expectClose(t, 2*time.Second); code != tc.want {
			t.Errorf("%s: close code = %d, want %d", tc.name, code, tc.want)
		}
		_ = client.conn.Close()
		waitFor(t, 2*time.Second, func() bool { return !tb.status(t).ExtensionConnected }, tc.name+": extension still reported as connected")
	}
}

// Chrome splits WebSocket messages larger than ~128 KiB into continuation
// frames, which is what every screenshot ack looks like. The bridge must
// reassemble them (with control frames interleaved) instead of closing the
// connection.
func TestFragmentedScreenshotAckIsReassembled(t *testing.T) {
	tb := startBridge(t, nil)
	ws := mustDialWS(t, tb)
	outcome := tb.postCommand(bridge.Request{Type: bridge.TypeScreenshot})
	command := ws.readCommand(t)

	raw := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3}, 300_000) // ~2.4 MB, like a Retina PNG
	encoded := base64.StdEncoding.EncodeToString(raw)
	payload, err := json.Marshal(map[string]any{"type": "ack", "id": command.ID, "ok": true, "screenshot": encoded, "tab_id": 7})
	if err != nil {
		t.Fatal(err)
	}
	const chunk = 128 << 10
	for offset := 0; offset < len(payload); offset += chunk {
		end := offset + chunk
		if end > len(payload) {
			end = len(payload)
		}
		opcode := byte(opcodeContinuation)
		if offset == 0 {
			opcode = opcodeText
		}
		if err := ws.writeFrame(opcode, payload[offset:end], end == len(payload), true); err != nil {
			t.Fatalf("write fragment at %d: %v", offset, err)
		}
		if offset == chunk {
			// A ping in the middle of the message must be answered, not rejected.
			ws.send(t, opcodePing, []byte("mid"))
		}
	}
	got := awaitOutcome(t, outcome)
	if got.status != http.StatusOK || !got.result.OK || got.result.TabID != 7 {
		t.Fatalf("fragmented ack outcome = %d %+v", got.status, got.result)
	}
	decoded, err := bridge.DecodeScreenshot(got.result)
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Fatalf("screenshot bytes mismatch (err=%v, %d bytes)", err, len(decoded))
	}
	if opcode, _, err := ws.readFrame(2 * time.Second); err != nil || opcode != opcodePong {
		t.Fatalf("interleaved ping was not answered: opcode=%d err=%v", opcode, err)
	}
	if !tb.status(t).ExtensionConnected {
		t.Fatal("extension must stay connected after a fragmented message")
	}
}

func TestShutdownClosesExtensionAndFailsPending(t *testing.T) {
	tb := startBridge(t, func(cfg *serverConfig) { cfg.commandTimeout = 10 * time.Second })
	ws := mustDialWS(t, tb)
	outcome := tb.postCommand(bridge.Request{Type: bridge.TypeOpen})
	ws.readCommand(t)
	if err := tb.stop(); err != nil {
		t.Fatalf("serve returned %v", err)
	}
	got := awaitOutcome(t, outcome)
	if got.status != http.StatusBadGateway || got.result.Error != reasonShuttingDown {
		t.Fatalf("command during shutdown = %d %+v", got.status, got.result)
	}
	if code := ws.expectClose(t, 2*time.Second); code != closeGoingAway {
		t.Fatalf("close code = %d, want %d", code, closeGoingAway)
	}
}

// Tests: helpers ---------------------------------------------------------------------

func TestExtensionOrigin(t *testing.T) {
	cases := []struct {
		origin string
		id     string
		ok     bool
	}{
		{testOrigin, testExtensionID, true},
		{testOrigin + "/", testExtensionID, true},
		{"chrome-extension://" + testExtensionID + "/popup.html", "", false},
		{"chrome-extension://abc:80", "", false},
		{"chrome-extension://127.0.0.1", "", false},
		{"chrome-extension://", "", false},
		{"moz-extension://" + testExtensionID, "", false},
		{"https://example.com", "", false},
		{"null", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		id, ok := extensionOrigin(tc.origin)
		if id != tc.id || ok != tc.ok {
			t.Errorf("extensionOrigin(%q) = (%q, %v), want (%q, %v)", tc.origin, id, ok, tc.id, tc.ok)
		}
	}
}

func TestNewBridgeServerRejectsMissingToken(t *testing.T) {
	cfg := defaultServerConfig()
	cfg.logger = log.New(io.Discard, "", 0)
	if _, err := newBridgeServer(cfg); err == nil {
		t.Fatal("expected an error for an empty token")
	}
	cfg.token = testToken
	cfg.addr = "0.0.0.0:0"
	server, err := newBridgeServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.listen(); err == nil {
		t.Fatal("expected listen to refuse a non-loopback address")
	}
}
