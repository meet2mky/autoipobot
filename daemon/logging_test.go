package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoipo/internal/telegram"
)

func TestRotatingWriterRotatesAndKeepsThree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "autoipo.log")
	w, err := newRotatingWriter(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 12; i++ {
		line := fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 30))
		if n, err := w.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("write %d = %d, %v", i, n, err)
		}
	}
	for _, name := range []string{"autoipo.log", "autoipo.log.1", "autoipo.log.2", "autoipo.log.3"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > 100 {
			t.Fatalf("%s is %d bytes, exceeds the limit", name, info.Size())
		}
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("autoipo.log.4 should not exist: %v", err)
	}
	current, _ := os.ReadFile(path)
	previous, _ := os.ReadFile(path + ".1")
	if !strings.HasPrefix(string(current), "line 10 ") || !strings.HasPrefix(string(previous), "line 08 ") {
		t.Fatalf("unexpected rotation order: current=%q previous=%q", current, previous)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %v, %v", info.Mode(), err)
	}
}

func TestRotatingWriterAppendsAndReopensAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autoipo.log")
	w, err := newRotatingWriter(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatalf("write after close should reopen: %v", err)
	}
	_ = w.Close()
	again, err := newRotatingWriter(path, 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Write([]byte("third\n")); err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
	data, _ := os.ReadFile(path)
	if string(data) != "first\nsecond\nthird\n" {
		t.Fatalf("log content = %q", data)
	}
}

func TestRotatingWriterConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autoipo.log")
	w, err := newRotatingWriter(path, 2000, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Go(func() {
			for i := 0; i < 50; i++ {
				if _, err := w.Write([]byte(strings.Repeat("y", 40) + "\n")); err != nil {
					t.Errorf("concurrent write: %v", err)
				}
			}
		})
	}
	wg.Wait()
}

func TestLoggerScrubsSecretsAndWritesEverywhere(t *testing.T) {
	first, second := &logBuffer{}, &logBuffer{}
	log := newLogger(scrubber{"123456:SECRET", ""}, first, second)
	log.Printf("Get %q failed: %d", "https://api.telegram.org/bot123456:SECRET/getUpdates", 42)
	for _, out := range []*logBuffer{first, second} {
		line := out.String()
		if strings.Contains(line, "SECRET") || !strings.Contains(line, "bot[redacted]/getUpdates\" failed: 42") {
			t.Fatalf("log line = %q", line)
		}
		if _, err := time.Parse(time.RFC3339, strings.Fields(line)[0]); err != nil {
			t.Fatalf("line is not timestamped: %q", line)
		}
	}
}

func TestSenderPreservesOrderAndDropsOldestWhenFull(t *testing.T) {
	rec := &recorder{}
	logs := &logBuffer{}
	s := newSender(rec, testChatID, newLogger(nil, logs))
	for i := 0; i < sendQueueSize+6; i++ {
		s.text(fmt.Sprintf("msg %02d", i))
	}
	s.document("shot.png", []byte{1, 2}, "cap")
	s.start()
	s.close(5 * time.Second)

	texts := rec.allTexts()
	if len(texts) != sendQueueSize-1 || texts[0] != "msg 07" || texts[len(texts)-1] != fmt.Sprintf("msg %02d", sendQueueSize+5) {
		t.Fatalf("delivered %d texts, first %q last %q", len(texts), texts[0], texts[len(texts)-1])
	}
	for i := 1; i < len(texts); i++ {
		if texts[i] <= texts[i-1] {
			t.Fatalf("order broken at %d: %q after %q", i, texts[i], texts[i-1])
		}
	}
	if docs := rec.allDocs(); len(docs) != 1 || docs[0].name != "shot.png" || docs[0].caption != "cap" {
		t.Fatalf("docs = %+v", docs)
	}
	if n := strings.Count(logs.String(), "outbound queue full; dropped oldest message"); n != 7 {
		t.Fatalf("expected 7 drop log lines, got %d", n)
	}
	s.text("late")
	if !strings.Contains(logs.String(), "sender is shut down") || rec.lastText() == "late" {
		t.Fatal("messages after close must be dropped with a log line")
	}
}

// flakyTransport fails the first n sends with a transport error, then
// delegates to the recorder.
type flakyTransport struct {
	recorder
	remaining atomic.Int32
	apiError  bool
}

func (f *flakyTransport) SendMessage(ctx context.Context, chatID int64, text string) error {
	if f.remaining.Add(-1) >= 0 {
		if f.apiError {
			return &telegram.APIError{Code: 400, Description: "Bad Request"}
		}
		return errors.New("dial tcp: connection refused")
	}
	return f.recorder.SendMessage(ctx, chatID, text)
}

func TestSenderRetriesTransportErrorsOnce(t *testing.T) {
	logs := &logBuffer{}
	flaky := &flakyTransport{}
	flaky.remaining.Store(1)
	s := newSender(flaky, testChatID, newLogger(nil, logs))
	s.retryDelay = time.Millisecond
	s.start()
	s.text("hello")
	s.close(5 * time.Second)
	if texts := flaky.allTexts(); len(texts) != 1 || texts[0] != "hello" {
		t.Fatalf("texts after retry = %v", texts)
	}
	if strings.Contains(logs.String(), "delivery failed") {
		t.Fatalf("successful retry must not log a failure:\n%s", logs.String())
	}

	logs = &logBuffer{}
	api := &flakyTransport{apiError: true}
	api.remaining.Store(1)
	s = newSender(api, testChatID, newLogger(nil, logs))
	s.retryDelay = time.Millisecond
	s.start()
	s.text("rejected")
	s.close(5 * time.Second)
	if len(api.allTexts()) != 0 || !strings.Contains(logs.String(), "message delivery failed: telegram api error 400") {
		t.Fatalf("API errors must not be retried; texts=%v logs=%s", api.allTexts(), logs.String())
	}
}

func TestSenderCloseWithoutStart(t *testing.T) {
	s := newSender(&recorder{}, testChatID, newLogger(nil, &logBuffer{}))
	done := make(chan struct{})
	go func() {
		s.close(time.Second)
		s.close(time.Second) // idempotent
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close without start must not block")
	}
}
