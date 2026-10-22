package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"autoipo/internal/telegram"
)

const (
	sendQueueSize  = 64
	sendTimeout    = 30 * time.Second
	sendRetryDelay = time.Second
)

// transport is the part of telegram.Client the sender needs.
type transport interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
	SendDocument(ctx context.Context, chatID int64, filename string, content []byte, caption string) error
}

// outbound is one queued message or document.
type outbound struct {
	text     string
	filename string
	content  []byte
	caption  string
}

func (o outbound) kind() string {
	if o.filename != "" {
		return "document"
	}
	return "message"
}

// sender delivers outbound Telegram traffic from a single goroutine so that
// replies keep their order and slow deliveries cannot pile up goroutines.
// The queue is bounded; when it is full the oldest entry is dropped.
type sender struct {
	transport  transport
	chatID     int64
	log        *logger
	retryDelay time.Duration

	queue   chan outbound
	stop    chan struct{}
	done    chan struct{}
	started atomic.Bool
	once    sync.Once
	ctx     context.Context
	cancel  context.CancelFunc
}

// newSender returns a sender for chatID; call start before enqueueing.
func newSender(t transport, chatID int64, log *logger) *sender {
	ctx, cancel := context.WithCancel(context.Background())
	return &sender{
		transport:  t,
		chatID:     chatID,
		log:        log,
		retryDelay: sendRetryDelay,
		queue:      make(chan outbound, sendQueueSize),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// start launches the delivery goroutine.
func (s *sender) start() {
	if s.started.CompareAndSwap(false, true) {
		go s.run()
	}
}

// text queues a plain-text message.
func (s *sender) text(text string) {
	s.enqueue(outbound{text: text})
}

// document queues a file upload with an optional caption.
func (s *sender) document(filename string, content []byte, caption string) {
	s.enqueue(outbound{filename: filename, content: content, caption: caption})
}

func (s *sender) enqueue(item outbound) {
	select {
	case <-s.stop:
		s.log.Printf("dropping outbound %s: sender is shut down", item.kind())
		return
	default:
	}
	for {
		select {
		case s.queue <- item:
			return
		default:
		}
		select {
		case dropped := <-s.queue:
			s.log.Printf("outbound queue full; dropped oldest %s", dropped.kind())
		default:
		}
	}
}

func (s *sender) run() {
	defer close(s.done)
	for {
		select {
		case item := <-s.queue:
			s.deliver(item)
		case <-s.stop:
			for {
				select {
				case item := <-s.queue:
					s.deliver(item)
				default:
					return
				}
			}
		}
	}
}

// deliver sends one item, retrying once after a transport (non-API) error.
func (s *sender) deliver(item outbound) {
	ctx, cancel := context.WithTimeout(s.ctx, sendTimeout)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		err = s.send(ctx, item)
		var apiErr *telegram.APIError
		if err == nil || errors.As(err, &apiErr) || attempt == 2 || !sleep(ctx, s.retryDelay) {
			break
		}
	}
	if err != nil {
		s.log.Printf("telegram %s delivery failed: %v", item.kind(), err)
	}
}

func (s *sender) send(ctx context.Context, item outbound) error {
	if item.filename != "" {
		return s.transport.SendDocument(ctx, s.chatID, item.filename, item.content, item.caption)
	}
	return s.transport.SendMessage(ctx, s.chatID, item.text)
}

// close stops accepting messages, lets the goroutine drain the queue for up
// to timeout, then abandons whatever is still pending. It returns once the
// goroutine has exited.
func (s *sender) close(timeout time.Duration) {
	s.once.Do(func() {
		close(s.stop)
		defer s.cancel()
		if !s.started.Load() {
			return
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			s.log.Printf("outbound queue did not drain within %s; abandoning pending messages", timeout)
			s.cancel()
			<-s.done
		}
	})
}
