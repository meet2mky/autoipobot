package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"autoipo/internal/bridge"
)

// Timing and size limits of the bridge. Tests shorten the durations through
// serverConfig.
const (
	defaultCommandTimeout    = 45 * time.Second
	defaultHeartbeatInterval = 15 * time.Second
	defaultReadDeadline      = 45 * time.Second
	websocketWriteTimeout    = 10 * time.Second
	shutdownTimeout          = 5 * time.Second
	maxCommandBody           = 64 << 10
	maxExtensionMessage      = 64 << 20
)

// envExtensionID restricts WebSocket connections to one extension ID when set.
const envExtensionID = "AUTOIPO_EXTENSION_ID"

// Message types exchanged over the WebSocket besides bridge.Command.
const (
	messageAck          = "ack"
	messageHeartbeatAck = "heartbeat_ack"
)

// Result.Error values reported for commands the extension never answered.
const (
	reasonExtensionDisconnected = "extension disconnected"
	reasonShuttingDown          = "bridge shutting down"
)

var heartbeatMessage = []byte(`{"type":"heartbeat"}`)

// serverConfig tunes a bridgeServer. Start from defaultServerConfig.
type serverConfig struct {
	addr              string        // listen address; must be a loopback address
	token             string        // bearer token required by POST /command
	extensionID       string        // when set, the only extension ID accepted on /ws
	commandTimeout    time.Duration // how long POST /command waits for an ack
	heartbeatInterval time.Duration // interval between heartbeat messages
	readDeadline      time.Duration // inbound silence tolerated before disconnecting
	logger            *log.Logger
}

// defaultServerConfig returns the production settings without a token.
func defaultServerConfig() serverConfig {
	return serverConfig{
		addr:              bridge.Addr(),
		commandTimeout:    defaultCommandTimeout,
		heartbeatInterval: defaultHeartbeatInterval,
		readDeadline:      defaultReadDeadline,
		logger:            log.New(os.Stderr, "", log.LstdFlags),
	}
}

// pendingCommand is a command accepted by POST /command that has not been
// answered yet.
type pendingCommand struct {
	command bridge.Command
	// result receives exactly one value: the ack, or a synthetic failure.
	result chan bridge.Result
	// conn is the extension connection the command was written to, or nil
	// while it is still queued. Guarded by bridgeServer.mu.
	conn *extensionConn
}

// extensionConn is one WebSocket session with the Chrome extension.
type extensionConn struct {
	ws     *webSocketConn
	origin string
	wake   chan struct{} // signalled when new commands are queued
	done   chan struct{} // closed once the session is torn down

	closeOnce sync.Once
	closedFor string // reason recorded by close; read after done is closed
}

func newExtensionConn(ws *webSocketConn, origin string) *extensionConn {
	return &extensionConn{ws: ws, origin: origin, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// signal wakes the writer without blocking.
func (c *extensionConn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// close ends the session once: it stops the writer and closes the socket with
// a close frame. The first reason wins.
func (c *extensionConn) close(code uint16, reason string) {
	c.closeOnce.Do(func() {
		c.closedFor = reason
		close(c.done)
		_ = c.ws.close(code, reason)
	})
}

func (c *extensionConn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// bridgeServer is the loopback HTTP server that relays commands from local
// programs to the single connected Chrome extension.
type bridgeServer struct {
	cfg       serverConfig
	startedAt time.Time
	sessions  sync.WaitGroup // live WebSocket handlers

	mu            sync.Mutex
	closing       bool
	ext           *extensionConn
	pending       map[string]*pendingCommand // queued and in-flight commands by ID
	queue         []*pendingCommand          // not yet written, in arrival order
	lastCommandAt time.Time
}

func newBridgeServer(cfg serverConfig) (*bridgeServer, error) {
	if cfg.token == "" {
		return nil, errors.New("bridge session token must not be empty")
	}
	if cfg.logger == nil {
		return nil, errors.New("bridge logger must not be nil")
	}
	return &bridgeServer{cfg: cfg, startedAt: time.Now(), pending: make(map[string]*pendingCommand)}, nil
}

func (b *bridgeServer) logf(format string, args ...any) { b.cfg.logger.Printf(format, args...) }

// listen binds the configured loopback address.
func (b *bridgeServer) listen() (net.Listener, error) {
	host, _, err := net.SplitHostPort(b.cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("invalid bridge address %q: %w", b.cfg.addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("bridge address %q is not a loopback address", b.cfg.addr)
	}
	listener, err := net.Listen("tcp", b.cfg.addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", b.cfg.addr, err)
	}
	return listener, nil
}

// serve runs the HTTP server on listener until ctx is cancelled, then fails
// outstanding commands, closes the extension session and shuts down.
func (b *bridgeServer) serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           b.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          b.cfg.logger,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		return fmt.Errorf("serve bridge: %w", err)
	case <-ctx.Done():
	}
	b.logf("shutting down extension bridge")
	b.shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down bridge: %w", err)
	}
	return nil
}

func (b *bridgeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", b.handleHealth)
	mux.HandleFunc("GET /status", b.handleStatus)
	mux.HandleFunc("POST /command", b.handleCommand)
	mux.HandleFunc("GET /ws", b.handleWebSocket)
	return mux
}

// HTTP endpoints ---------------------------------------------------------

func (b *bridgeServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !b.allowOrigin(w, r) {
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		// Let the extension's cross-origin probe read the response.
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (b *bridgeServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !b.allowOrigin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, b.status())
}

func (b *bridgeServer) handleCommand(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="autoipo-bridge"`)
		http.Error(w, "missing or invalid bridge session token", http.StatusUnauthorized)
		return
	}
	if !b.allowOrigin(w, r) {
		return
	}
	var request bridge.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCommandBody)).Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "command body exceeds 64 KB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid command JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := bridge.Validate(request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.Type == bridge.TypeSetURL {
		normalized, err := bridge.ValidateURL(request.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request.URL = normalized
	}
	pc, err := b.enqueue(request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	timer := time.NewTimer(b.cfg.commandTimeout)
	defer timer.Stop()
	select {
	case result := <-pc.result:
		status := http.StatusOK
		if !result.OK {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, result)
	case <-timer.C:
		b.cancel(pc)
		b.logf("command %s (%s) timed out", pc.command.ID, pc.command.Type)
		writeJSON(w, http.StatusGatewayTimeout, bridge.Result{OK: false, Error: b.timeoutMessage()})
	case <-r.Context().Done():
		b.cancel(pc)
		b.logf("command %s (%s) dropped: caller went away", pc.command.ID, pc.command.Type)
	}
}

func (b *bridgeServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	id, ok := extensionOrigin(origin)
	if !ok {
		b.logf("rejected websocket from origin %q", origin)
		http.Error(w, "a chrome-extension:// Origin header is required", http.StatusForbidden)
		return
	}
	if b.cfg.extensionID != "" && id != b.cfg.extensionID {
		b.logf("rejected websocket from extension %s: not the configured %s", id, envExtensionID)
		http.Error(w, "this extension is not allowed to use the bridge", http.StatusForbidden)
		return
	}
	if err := validateUpgrade(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ws, err := upgradeWebSocket(w, r, webSocketOptions{
		readLimit:    maxExtensionMessage,
		readTimeout:  b.cfg.readDeadline,
		writeTimeout: websocketWriteTimeout,
	})
	if err != nil {
		b.logf("websocket upgrade from %s failed: %v", origin, err)
		return
	}
	ext := newExtensionConn(ws, origin)
	previous, ok := b.attach(ext)
	if !ok {
		ext.close(closeGoingAway, reasonShuttingDown)
		return
	}
	defer b.sessions.Done()
	if previous != nil {
		previous.close(closeNormal, "replaced by a newer extension connection")
		b.logf("extension reconnected from %s; replacing the previous connection", origin)
	} else {
		b.logf("extension connected from %s", origin)
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		b.writeLoop(ext)
	}()
	err = b.readLoop(ext)
	switch {
	case ext.closed():
		// Closed locally (replaced, write failure or shutdown); reason recorded.
	case errors.Is(err, os.ErrDeadlineExceeded):
		ext.close(closeGoingAway, fmt.Sprintf("no message from the extension for %s", b.cfg.readDeadline))
	default:
		ext.close(closeNormal, readErrorReason(err))
	}
	<-writerDone
	failed := b.detach(ext)
	b.logf("extension disconnected (%s); %d command(s) failed", ext.closedFor, failed)
}

// Request helpers ------------------------------------------------------------

// authorized checks the bearer token in constant time.
func (b *bridgeServer) authorized(r *http.Request) bool {
	const scheme = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	token := strings.TrimSpace(header[len(scheme):])
	return subtle.ConstantTimeCompare([]byte(token), []byte(b.cfg.token)) == 1
}

// allowOrigin rejects browser contexts other than Chrome extensions with 403.
// Requests without an Origin header (plain HTTP clients) are allowed.
func (b *bridgeServer) allowOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if _, ok := extensionOrigin(origin); !ok {
		b.logf("rejected %s %s from origin %q", r.Method, r.URL.Path, origin)
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return false
	}
	return true
}

// extensionOrigin returns the extension ID named by an Origin header value of
// the form chrome-extension://<id>.
func extensionOrigin(origin string) (string, bool) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "chrome-extension" || parsed.User != nil || parsed.Host == "" {
		return "", false
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	for _, r := range parsed.Host {
		if r < 'a' || r > 'z' {
			return "", false
		}
	}
	return parsed.Host, true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (b *bridgeServer) status() bridge.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := bridge.Status{
		Serving:         true,
		PendingCommands: len(b.pending),
		UptimeSeconds:   int64(time.Since(b.startedAt).Seconds()),
		ProtocolVersion: bridge.ProtocolVersion,
	}
	if b.ext != nil {
		status.ExtensionConnected = true
		status.ExtensionOrigin = b.ext.origin
	}
	if !b.lastCommandAt.IsZero() {
		status.LastCommandAt = b.lastCommandAt.UTC().Format(time.RFC3339)
	}
	return status
}

func (b *bridgeServer) timeoutMessage() string {
	b.mu.Lock()
	connected := b.ext != nil
	b.mu.Unlock()
	if connected {
		return fmt.Sprintf("the Chrome extension did not respond within %s", b.cfg.commandTimeout)
	}
	return fmt.Sprintf("no Chrome extension connected to the bridge within %s; open Chrome with the AutoIPO extension installed and enabled", b.cfg.commandTimeout)
}

// Pending bookkeeping --------------------------------------------------------

// enqueue registers a command and wakes the extension writer, if any.
func (b *bridgeServer) enqueue(request bridge.Request) (*pendingCommand, error) {
	pc := &pendingCommand{
		command: bridge.Command{ID: rand.Text(), Request: request},
		result:  make(chan bridge.Result, 1),
	}
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return nil, errors.New(reasonShuttingDown)
	}
	b.pending[pc.command.ID] = pc
	b.queue = append(b.queue, pc)
	ext := b.ext
	b.mu.Unlock()
	if ext != nil {
		ext.signal()
	}
	return pc, nil
}

// cancel forgets a command whose caller stopped waiting. A queued command is
// skipped by the writer; a late ack for an in-flight one is ignored.
func (b *bridgeServer) cancel(pc *pendingCommand) {
	b.mu.Lock()
	delete(b.pending, pc.command.ID)
	b.mu.Unlock()
}

// nextQueued hands the oldest live queued command to ext's writer, or nil
// when the queue is empty or ext is no longer the current connection.
func (b *bridgeServer) nextQueued(ext *extensionConn) *pendingCommand {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ext != ext {
		return nil
	}
	for len(b.queue) > 0 {
		pc := b.queue[0]
		b.queue[0] = nil
		b.queue = b.queue[1:]
		if _, live := b.pending[pc.command.ID]; !live {
			continue
		}
		pc.conn = ext
		return pc
	}
	b.queue = nil
	return nil
}

// complete delivers an ack to its command. It reports false for unknown IDs.
func (b *bridgeServer) complete(ack bridge.Ack) bool {
	b.mu.Lock()
	pc, ok := b.pending[ack.ID]
	if ok {
		delete(b.pending, ack.ID)
		b.lastCommandAt = time.Now()
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	pc.result <- ack.Result
	return true
}

// attach makes ext the current connection and returns the one it replaces.
// It reports false while the bridge is shutting down.
func (b *bridgeServer) attach(ext *extensionConn) (*extensionConn, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return nil, false
	}
	previous := b.ext
	b.ext = ext
	b.sessions.Add(1)
	ext.signal() // deliver commands queued while no extension was connected
	return previous, true
}

// detach fails every command written to ext with "extension disconnected".
// When ext was still the current connection the queued commands fail too;
// when it had been replaced they stay queued for the new connection. It
// returns the number of commands failed.
func (b *bridgeServer) detach(ext *extensionConn) int {
	b.mu.Lock()
	current := b.ext == ext
	if current {
		b.ext = nil
	}
	var failed []*pendingCommand
	for id, pc := range b.pending {
		if pc.conn == ext || (current && pc.conn == nil) {
			delete(b.pending, id)
			failed = append(failed, pc)
		}
	}
	if current {
		b.queue = nil
	}
	b.mu.Unlock()
	for _, pc := range failed {
		pc.result <- bridge.Result{OK: false, Error: reasonExtensionDisconnected}
	}
	return len(failed)
}

// shutdown fails all outstanding commands, closes the extension session and
// waits (bounded) for its handler to finish.
func (b *bridgeServer) shutdown() {
	b.mu.Lock()
	b.closing = true
	ext := b.ext
	failed := make([]*pendingCommand, 0, len(b.pending))
	for id, pc := range b.pending {
		delete(b.pending, id)
		failed = append(failed, pc)
	}
	b.queue = nil
	b.mu.Unlock()
	for _, pc := range failed {
		pc.result <- bridge.Result{OK: false, Error: reasonShuttingDown}
	}
	if ext != nil {
		ext.close(closeGoingAway, reasonShuttingDown)
	}
	sessionsDone := make(chan struct{})
	go func() {
		b.sessions.Wait()
		close(sessionsDone)
	}()
	select {
	case <-sessionsDone:
	case <-time.After(shutdownTimeout):
		b.logf("extension session did not finish within %s", shutdownTimeout)
	}
}

// WebSocket session loops ------------------------------------------------------

// writeLoop forwards queued commands and heartbeats to ext until the session
// ends. A failed write closes the session so that detach fails the command.
func (b *bridgeServer) writeLoop(ext *extensionConn) {
	heartbeat := time.NewTicker(b.cfg.heartbeatInterval)
	defer heartbeat.Stop()
	for {
		if err := b.flushQueue(ext); err != nil {
			b.logf("sending to extension failed: %v", err)
			ext.close(closeInternalError, "failed to send a command")
			return
		}
		select {
		case <-ext.done:
			return
		case <-ext.wake:
		case <-heartbeat.C:
			if err := ext.ws.writeText(heartbeatMessage); err != nil {
				b.logf("sending heartbeat failed: %v", err)
				ext.close(closeInternalError, "failed to send a heartbeat")
				return
			}
		}
	}
}

// flushQueue writes every queued command to ext.
func (b *bridgeServer) flushQueue(ext *extensionConn) error {
	for {
		pc := b.nextQueued(ext)
		if pc == nil {
			return nil
		}
		payload, err := json.Marshal(pc.command)
		if err != nil {
			return fmt.Errorf("encode command %s: %w", pc.command.ID, err)
		}
		if err := ext.ws.writeText(payload); err != nil {
			return fmt.Errorf("write command %s: %w", pc.command.ID, err)
		}
		b.logf("command %s (%s) sent to extension", pc.command.ID, pc.command.Type)
	}
}

// readLoop dispatches inbound messages until the connection fails.
func (b *bridgeServer) readLoop(ext *extensionConn) error {
	for {
		payload, err := ext.ws.readMessage()
		if err != nil {
			return err
		}
		var ack bridge.Ack
		if err := json.Unmarshal(payload, &ack); err != nil {
			b.logf("ignoring malformed extension message: %v", err)
			continue
		}
		switch ack.Type {
		case messageAck:
			ack.Normalize()
			if !b.complete(ack) {
				b.logf("ignoring ack for unknown or expired command %q", ack.ID)
				continue
			}
			if ack.OK {
				b.logf("command %s completed", ack.ID)
			} else {
				b.logf("command %s failed: %s", ack.ID, ack.Error)
			}
		case messageHeartbeatAck:
			// Liveness only; the read deadline was refreshed on arrival.
		default:
			b.logf("ignoring extension message of unknown type %q", ack.Type)
		}
	}
}

// readErrorReason describes why readMessage failed, for logs.
func readErrorReason(err error) string {
	var closeErr *closeError
	switch {
	case errors.As(err, &closeErr):
		return closeErr.Error()
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed):
		return "connection closed"
	default:
		return err.Error()
	}
}
