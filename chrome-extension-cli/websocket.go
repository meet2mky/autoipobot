package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
const maxWebSocketMessage = 1 << 20

type webSocketConn struct {
	conn     net.Conn
	reader   *bufio.Reader
	writer   *bufio.Writer
	writeMux sync.Mutex
	closeMux sync.Once
}

type extensionMessage struct {
	Type     string             `json:"type"`
	ID       string             `json:"id,omitempty"`
	OK       bool               `json:"ok,omitempty"`
	Error    string             `json:"error,omitempty"`
	Options  []string           `json:"options,omitempty"`
	Elements []selectorMetadata `json:"elements,omitempty"`
	Output   string             `json:"output,omitempty"`
}

func (b *bridge) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") == "" {
		http.Error(w, "Chrome extension origin required", http.StatusForbidden)
		return
	}
	if !b.authorize(w, r) {
		return
	}
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}

	b.clientMu.Lock()
	previous := b.client
	b.client = ws
	b.clientMu.Unlock()
	if previous != nil {
		_ = previous.close()
	}
	log.Printf("Chrome extension connected")

	writerDone := make(chan struct{})
	go b.writeWebSocketMessages(ws, writerDone)
	defer func() {
		close(writerDone)
		_ = ws.close()
		b.clientMu.Lock()
		if b.client == ws {
			b.client = nil
		}
		b.clientMu.Unlock()
		log.Printf("Chrome extension disconnected")
	}()

	for {
		payload, opcode, err := ws.readMessage()
		if err != nil {
			return
		}
		if opcode != 0x1 {
			continue
		}
		var message extensionMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			log.Printf("invalid extension message: %v", err)
			continue
		}
		if message.Type == "ack" {
			b.completeCommand(commandAck{ID: message.ID, OK: message.OK, Error: message.Error, Options: message.Options, Elements: message.Elements, Output: message.Output})
		}
	}
}

func (b *bridge) writeWebSocketMessages(ws *webSocketConn, done <-chan struct{}) {
	defer ws.conn.Close()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-done:
			return
		case item := <-b.queue:
			b.mu.Lock()
			_, stillPending := b.pending[item.command.ID]
			b.mu.Unlock()
			if !stillPending {
				continue
			}
			payload, err := json.Marshal(item.command)
			if err != nil || ws.writeMessage(0x1, payload) != nil {
				return
			}
		case <-heartbeat.C:
			if ws.writeMessage(0x1, []byte(`{"type":"heartbeat"}`)) != nil {
				return
			}
		}
	}
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*webSocketConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, fmt.Errorf("missing websocket upgrade headers")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "unsupported websocket version", http.StatusBadRequest)
		return nil, fmt.Errorf("unsupported websocket version")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	decodedKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decodedKey) != 16 {
		http.Error(w, "invalid websocket key", http.StatusBadRequest)
		return nil, fmt.Errorf("invalid websocket key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket upgrade unsupported", http.StatusInternalServerError)
		return nil, fmt.Errorf("http connection cannot be hijacked")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	acceptHash := sha1.Sum([]byte(key + websocketGUID))
	accept := base64.StdEncoding.EncodeToString(acceptHash[:])
	if _, err := fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &webSocketConn{conn: conn, reader: rw.Reader, writer: rw.Writer}, nil
}

func headerHasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (ws *webSocketConn) readMessage() ([]byte, byte, error) {
	for {
		var header [2]byte
		if _, err := io.ReadFull(ws.reader, header[:]); err != nil {
			return nil, 0, err
		}
		fin := header[0]&0x80 != 0
		opcode := header[0] & 0x0f
		masked := header[1]&0x80 != 0
		length := uint64(header[1] & 0x7f)
		if length == 126 {
			var extended [2]byte
			if _, err := io.ReadFull(ws.reader, extended[:]); err != nil {
				return nil, 0, err
			}
			length = uint64(binary.BigEndian.Uint16(extended[:]))
		} else if length == 127 {
			var extended [8]byte
			if _, err := io.ReadFull(ws.reader, extended[:]); err != nil {
				return nil, 0, err
			}
			length = binary.BigEndian.Uint64(extended[:])
		}
		if !masked || length > maxWebSocketMessage {
			return nil, 0, fmt.Errorf("invalid websocket frame")
		}
		var mask [4]byte
		if _, err := io.ReadFull(ws.reader, mask[:]); err != nil {
			return nil, 0, err
		}
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(ws.reader, payload); err != nil {
			return nil, 0, err
		}
		for i := range payload {
			payload[i] ^= mask[i%len(mask)]
		}
		if opcode == 0x8 {
			return nil, opcode, io.EOF
		}
		if opcode == 0x9 {
			if err := ws.writeMessage(0xA, payload); err != nil {
				return nil, 0, err
			}
			continue
		}
		if opcode == 0xA {
			continue
		}
		if !fin || (opcode != 0x1 && opcode != 0x2) {
			return nil, 0, fmt.Errorf("unsupported websocket frame")
		}
		return payload, opcode, nil
	}
}

func (ws *webSocketConn) writeMessage(opcode byte, payload []byte) error {
	if len(payload) > maxWebSocketMessage {
		return fmt.Errorf("websocket message is too large")
	}
	ws.writeMux.Lock()
	defer ws.writeMux.Unlock()
	if err := ws.writer.WriteByte(0x80 | opcode); err != nil {
		return err
	}
	switch length := len(payload); {
	case length < 126:
		if err := ws.writer.WriteByte(byte(length)); err != nil {
			return err
		}
	case length <= 0xffff:
		if err := ws.writer.WriteByte(126); err != nil {
			return err
		}
		var extended [2]byte
		binary.BigEndian.PutUint16(extended[:], uint16(length))
		if _, err := ws.writer.Write(extended[:]); err != nil {
			return err
		}
	default:
		if err := ws.writer.WriteByte(127); err != nil {
			return err
		}
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		if _, err := ws.writer.Write(extended[:]); err != nil {
			return err
		}
	}
	if _, err := ws.writer.Write(payload); err != nil {
		return err
	}
	return ws.writer.Flush()
}

func (ws *webSocketConn) close() error {
	var closeErr error
	ws.closeMux.Do(func() {
		_ = ws.writeMessage(0x8, nil)
		closeErr = ws.conn.Close()
	})
	return closeErr
}
