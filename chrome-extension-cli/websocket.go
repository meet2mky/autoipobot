package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WebSocket opcodes (RFC 6455 §5.2).
const (
	opcodeContinuation byte = 0x0
	opcodeText         byte = 0x1
	opcodeBinary       byte = 0x2
	opcodeClose        byte = 0x8
	opcodePing         byte = 0x9
	opcodePong         byte = 0xA
)

// WebSocket close status codes (RFC 6455 §7.4.1) used by the bridge.
const (
	closeNormal        uint16 = 1000
	closeGoingAway     uint16 = 1001
	closeProtocolError uint16 = 1002
	closeNoStatus      uint16 = 1005
	closeMessageTooBig uint16 = 1009
	closeInternalError uint16 = 1011
)

const (
	websocketGUID        = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	maxControlPayload    = 125
	maxCloseReasonLength = maxControlPayload - 2
)

// closeError is returned by readMessage when the peer started the closing
// handshake. The bridge has already echoed the close frame by then.
type closeError struct {
	code   uint16
	reason string
}

func (e *closeError) Error() string {
	if e.reason == "" {
		return fmt.Sprintf("closed by peer (code %d)", e.code)
	}
	return fmt.Sprintf("closed by peer (code %d: %q)", e.code, e.reason)
}

// webSocketOptions bounds the resource usage of a webSocketConn.
type webSocketOptions struct {
	readLimit    int64         // largest accepted inbound message payload
	readTimeout  time.Duration // inbound silence tolerated between frames; 0 disables
	writeTimeout time.Duration // per-frame write deadline; 0 disables
}

// webSocketConn is a minimal server-side RFC 6455 connection: unfragmented
// text and binary messages, ping/pong and the closing handshake. Writes are
// serialized by a mutex so several goroutines may send frames concurrently;
// reads must come from a single goroutine.
type webSocketConn struct {
	conn   net.Conn
	reader *bufio.Reader
	opts   webSocketOptions

	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

type frame struct {
	opcode  byte
	payload []byte
}

// validateUpgrade checks that r is a well-formed WebSocket opening handshake.
func validateUpgrade(r *http.Request) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !headerHasToken(r.Header.Values("Connection"), "upgrade") {
		return errors.New("websocket upgrade headers are required")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return errors.New("only websocket version 13 is supported")
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		return errors.New("Sec-WebSocket-Key must be 16 base64-encoded bytes")
	}
	return nil
}

// headerHasToken reports whether any of the comma-separated header values
// contains token (case-insensitive).
func headerHasToken(values []string, token string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// upgradeWebSocket completes the opening handshake for a request that passed
// validateUpgrade and returns the hijacked connection. When the response
// writer cannot be hijacked a 500 is written and an error returned.
func upgradeWebSocket(w http.ResponseWriter, r *http.Request, opts webSocketOptions) (*webSocketConn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket upgrade is not supported on this connection", http.StatusInternalServerError)
		return nil, errors.New("response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack connection: %w", err)
	}
	// The HTTP server may have left deadlines behind; frame reads and writes
	// manage their own.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reset connection deadline: %w", err)
	}
	accept := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(accept[:]) + "\r\n\r\n"
	ws := &webSocketConn{conn: conn, reader: rw.Reader, opts: opts}
	if err := ws.write([]byte(response)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write handshake response: %w", err)
	}
	return ws, nil
}

// readMessage returns the payload of the next text or binary message. Control
// frames are handled internally: pings are answered, pongs only refresh the
// read deadline, and a close frame is echoed before a *closeError is returned.
// Protocol violations close the connection and return an error.
func (ws *webSocketConn) readMessage() ([]byte, error) {
	for {
		f, err := ws.readFrame()
		if err != nil {
			return nil, err
		}
		switch f.opcode {
		case opcodeText, opcodeBinary:
			return f.payload, nil
		case opcodePing:
			if err := ws.writeFrame(opcodePong, f.payload); err != nil {
				return nil, fmt.Errorf("answer ping: %w", err)
			}
		case opcodePong:
			// Keep-alive only; readFrame already refreshed the deadline.
		case opcodeClose:
			code, reason := parseClosePayload(f.payload)
			_ = ws.close(code, "")
			return nil, &closeError{code: code, reason: reason}
		default:
			return nil, ws.fail(closeProtocolError, fmt.Sprintf("unsupported opcode %#x", f.opcode))
		}
	}
}

// readFrame reads and unmasks one frame, refreshing the read deadline first.
func (ws *webSocketConn) readFrame() (frame, error) {
	if ws.opts.readTimeout > 0 {
		if err := ws.conn.SetReadDeadline(time.Now().Add(ws.opts.readTimeout)); err != nil {
			return frame{}, fmt.Errorf("set read deadline: %w", err)
		}
	}
	var header [2]byte
	if _, err := io.ReadFull(ws.reader, header[:]); err != nil {
		return frame{}, err
	}
	fin := header[0]&0x80 != 0
	rsv := header[0] & 0x70
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(ws.reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(ws.reader, extended[:]); err != nil {
			return frame{}, err
		}
		length = binary.BigEndian.Uint64(extended[:])
	}
	control := opcode >= opcodeClose
	switch {
	case rsv != 0:
		return frame{}, ws.fail(closeProtocolError, "reserved bits set without a negotiated extension")
	case !masked:
		return frame{}, ws.fail(closeProtocolError, "client frame is not masked")
	case opcode == opcodeContinuation || !fin:
		return frame{}, ws.fail(closeProtocolError, "fragmented messages are not supported")
	case opcode != opcodeText && opcode != opcodeBinary && opcode != opcodeClose && opcode != opcodePing && opcode != opcodePong:
		return frame{}, ws.fail(closeProtocolError, fmt.Sprintf("reserved opcode %#x", opcode))
	case control && length > maxControlPayload:
		return frame{}, ws.fail(closeProtocolError, "control frame payload exceeds 125 bytes")
	case length > uint64(ws.opts.readLimit):
		return frame{}, ws.fail(closeMessageTooBig, fmt.Sprintf("message of %d bytes exceeds the %d byte limit", length, ws.opts.readLimit))
	}
	var mask [4]byte
	if _, err := io.ReadFull(ws.reader, mask[:]); err != nil {
		return frame{}, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(ws.reader, payload); err != nil {
		return frame{}, err
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return frame{opcode: opcode, payload: payload}, nil
}

// writeText sends payload as a single unfragmented text message.
func (ws *webSocketConn) writeText(payload []byte) error {
	return ws.writeFrame(opcodeText, payload)
}

// writeFrame sends one unmasked server frame (RFC 6455 §5.2).
func (ws *webSocketConn) writeFrame(opcode byte, payload []byte) error {
	buf := make([]byte, 0, len(payload)+10)
	buf = append(buf, 0x80|opcode)
	switch length := len(payload); {
	case length < 126:
		buf = append(buf, byte(length))
	case length <= 0xffff:
		buf = append(buf, 126)
		buf = binary.BigEndian.AppendUint16(buf, uint16(length))
	default:
		buf = append(buf, 127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(length))
	}
	buf = append(buf, payload...)
	return ws.write(buf)
}

// write sends raw bytes under the write lock with the configured deadline.
func (ws *webSocketConn) write(data []byte) error {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	if ws.opts.writeTimeout > 0 {
		if err := ws.conn.SetWriteDeadline(time.Now().Add(ws.opts.writeTimeout)); err != nil {
			return fmt.Errorf("set write deadline: %w", err)
		}
	}
	if _, err := ws.conn.Write(data); err != nil {
		return err
	}
	return nil
}

// fail closes the connection with the given status and returns an error
// describing the protocol violation.
func (ws *webSocketConn) fail(code uint16, reason string) error {
	_ = ws.close(code, reason)
	return fmt.Errorf("websocket protocol error: %s", reason)
}

// close sends a close frame (best effort) and closes the underlying
// connection. It is idempotent: later calls return the first result.
func (ws *webSocketConn) close(code uint16, reason string) error {
	ws.closeOnce.Do(func() {
		_ = ws.writeFrame(opcodeClose, closePayload(code, reason))
		ws.closeErr = ws.conn.Close()
	})
	return ws.closeErr
}

// closePayload encodes a close frame body; closeNoStatus yields an empty body.
func closePayload(code uint16, reason string) []byte {
	if code == closeNoStatus {
		return nil
	}
	if len(reason) > maxCloseReasonLength {
		reason = reason[:maxCloseReasonLength]
	}
	payload := binary.BigEndian.AppendUint16(make([]byte, 0, 2+len(reason)), code)
	return append(payload, reason...)
}

// parseClosePayload decodes the status code and reason of a close frame.
func parseClosePayload(payload []byte) (uint16, string) {
	if len(payload) < 2 {
		return closeNoStatus, ""
	}
	return binary.BigEndian.Uint16(payload[:2]), string(payload[2:])
}
