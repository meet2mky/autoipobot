package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const bridgePort = "18765"

type command struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
}

type commandRequest struct {
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
}

type commandAck struct {
	ID       string             `json:"id"`
	OK       bool               `json:"ok"`
	Error    string             `json:"error,omitempty"`
	Options  []string           `json:"options,omitempty"`
	Elements []selectorMetadata `json:"elements,omitempty"`
	Output   string             `json:"output,omitempty"`
}

type selectorMetadata struct {
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context,omitempty"`
	Selector    string `json:"selector"`
}

type commandResult struct {
	OK       bool               `json:"ok"`
	Error    string             `json:"error,omitempty"`
	Options  []string           `json:"options,omitempty"`
	Elements []selectorMetadata `json:"elements,omitempty"`
	Output   string             `json:"output,omitempty"`
}

type pendingCommand struct {
	command command
	result  chan commandResult
}

type bridge struct {
	queue    chan *pendingCommand
	mu       sync.Mutex
	pending  map[string]*pendingCommand
	clientMu sync.Mutex
	client   *webSocketConn
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "chrome-extension-cli:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "serve":
		if len(args) != 1 {
			return usage()
		}
		return serve()
	case "set-url":
		if len(args) != 2 {
			return usage()
		}
		websiteURL, err := validateURL(args[1])
		if err != nil {
			return err
		}
		return sendCommand(commandRequest{Type: "set_url", URL: websiteURL})
	case "open":
		if len(args) != 1 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "open"})
	case "click":
		if len(args) != 2 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "click", Selector: args[1]})
	case "fill":
		if len(args) != 3 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "fill", Selector: args[1], Value: args[2]})
	case "dropdown":
		if len(args) != 2 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "dropdown", Selector: args[1]})
	case "choose":
		if len(args) != 3 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "choose", Selector: args[1], Value: args[2]})
	case "list-selectors":
		if len(args) != 1 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "list_selectors"})
	case "page-contains":
		if len(args) != 2 {
			return usage()
		}
		return sendCommand(commandRequest{Type: "page_contains", Selector: args[1]})
	case "random-sleep":
		if len(args) != 2 {
			return usage()
		}
		maxSeconds, err := strconv.ParseFloat(args[1], 64)
		if err != nil || maxSeconds <= 0 || maxSeconds > 86400 {
			return fmt.Errorf("sleep duration must be a number greater than 0 and no more than 86400 seconds")
		}
		duration := time.Duration(mathrand.Float64() * maxSeconds * float64(time.Second))
		time.Sleep(duration)
		fmt.Printf("Slept for %.2f seconds\n", duration.Seconds())
		return nil
	case "help", "-h", "--help":
		fmt.Println(usageText)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usageText)
	}
}

const usageText = `Usage:
  chrome-extension-cli serve
  chrome-extension-cli set-url <http-or-https-url>
  chrome-extension-cli open
  chrome-extension-cli click <css-selector>
  chrome-extension-cli fill <css-selector> <value>
  chrome-extension-cli dropdown <css-selector>
  chrome-extension-cli choose <css-selector> <option-label-or-value>
  chrome-extension-cli list-selectors
  chrome-extension-cli page-contains <css-selector>
  chrome-extension-cli random-sleep <max-seconds>

Run "serve" continuously while using the extension.`

func usage() error { return errors.New(usageText) }

func serve() error {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", bridgePort))
	if err != nil {
		return fmt.Errorf("listen on 127.0.0.1:%s: %w", bridgePort, err)
	}

	bridgeServer := &bridge{
		queue:   make(chan *pendingCommand, 32),
		pending: make(map[string]*pendingCommand),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", bridgeServer.handleHealth)
	mux.HandleFunc("/status", bridgeServer.handleStatus)
	mux.HandleFunc("/command", bridgeServer.handleCommand)
	mux.HandleFunc("/ws", bridgeServer.handleWebSocket)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("extension bridge listening on 127.0.0.1:%s", bridgePort)
	log.Printf("press Ctrl+C to stop")

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (b *bridge) authorize(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "chrome-extension" || parsed.Host == "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return false
		}
	}
	return true
}

func (b *bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	origin := r.Header.Get("Origin")
	if !b.authorize(w, r) {
		log.Printf("rejected health-check origin %q", origin)
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (b *bridge) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	b.clientMu.Lock()
	connected := b.client != nil
	b.clientMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Serving            bool `json:"serving"`
		ExtensionConnected bool `json:"extension_connected"`
	}{Serving: true, ExtensionConnected: connected})
}

func (b *bridge) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !b.authorize(w, r) {
		return
	}
	var request commandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request); err != nil {
		http.Error(w, "invalid command JSON", http.StatusBadRequest)
		return
	}
	if request.Type != "open" && request.Type != "set_url" && request.Type != "click" && request.Type != "fill" && request.Type != "dropdown" && request.Type != "choose" && request.Type != "list_selectors" && request.Type != "page_contains" {
		http.Error(w, "unknown command type", http.StatusBadRequest)
		return
	}
	if request.Type == "set_url" {
		validated, err := validateURL(request.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request.URL = validated
	}
	if request.Type == "click" || request.Type == "fill" || request.Type == "dropdown" || request.Type == "choose" || request.Type == "page_contains" {
		if strings.TrimSpace(request.Selector) == "" || len(request.Selector) > 2048 {
			http.Error(w, "selector must contain 1 to 2048 characters", http.StatusBadRequest)
			return
		}
	}

	id, err := randomID()
	if err != nil {
		http.Error(w, "could not create command ID", http.StatusInternalServerError)
		return
	}
	item := &pendingCommand{
		command: command{ID: id, Type: request.Type, URL: request.URL, Selector: request.Selector, Value: request.Value},
		result:  make(chan commandResult, 1),
	}
	b.mu.Lock()
	b.pending[id] = item
	b.mu.Unlock()
	select {
	case b.queue <- item:
	case <-r.Context().Done():
		b.removePending(id)
		return
	default:
		b.removePending(id)
		http.Error(w, "extension command queue is full", http.StatusServiceUnavailable)
		return
	}

	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case result := <-item.result:
		w.Header().Set("Content-Type", "application/json")
		if !result.OK {
			w.WriteHeader(http.StatusBadGateway)
		}
		_ = json.NewEncoder(w).Encode(result)
	case <-timer.C:
		b.removePending(id)
		http.Error(w, "no extension responded; is Chrome open with the extension installed?", http.StatusGatewayTimeout)
	case <-r.Context().Done():
		b.removePending(id)
	}
}

func (b *bridge) removePending(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func (b *bridge) completeCommand(ack commandAck) {
	b.mu.Lock()
	item := b.pending[ack.ID]
	delete(b.pending, ack.ID)
	b.mu.Unlock()
	if item != nil {
		item.result <- commandResult{OK: ack.OK, Error: ack.Error, Options: ack.Options, Elements: ack.Elements, Output: ack.Output}
	}
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validateURL(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("URL must be an absolute http:// or https:// address")
	}
	return parsed.String(), nil
}

func sendCommand(request commandRequest) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	endpoint := "http://127.0.0.1:" + bridgePort + "/command"
	httpRequest, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 47 * time.Second}
	response, err := client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("contact extension bridge at 127.0.0.1:%s (is `serve` running?): %w", bridgePort, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("extension bridge returned %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	var result commandResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("read extension response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("extension command failed: %s", result.Error)
	}
	if len(result.Elements) > 0 || request.Type == "list_selectors" {
		if len(result.Elements) == 0 {
			fmt.Println("No visible clickable or fillable elements found")
			return nil
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result.Elements)
	} else if request.Type == "page_contains" {
		fmt.Println(result.Output)
	} else if len(result.Options) > 0 {
		for index, option := range result.Options {
			fmt.Printf("%d. %s\n", index+1, option)
		}
	} else if request.Type == "dropdown" {
		fmt.Println("No dropdown options found")
	} else {
		fmt.Println("Command completed")
	}
	return nil
}
