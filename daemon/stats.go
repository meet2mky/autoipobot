package main

import (
	"sync"
	"time"

	"autoipo/internal/daemonctl"
)

// statusEvery bounds how often event-driven status writes hit the disk.
const statusEvery = 5 * time.Second

// stats tracks poll and command counters and publishes them to the daemon
// status file. Event-driven writes are rate-limited to one per statusEvery;
// flush with force bypasses the limit (start-up and shutdown).
type stats struct {
	scrub scrubber
	log   *logger
	write func(daemonctl.Status) error

	mu        sync.Mutex
	status    daemonctl.Status
	lastWrite time.Time
}

func newStats(initial daemonctl.Status, write func(daemonctl.Status) error, scrub scrubber, log *logger) *stats {
	return &stats{scrub: scrub, log: log, write: write, status: initial}
}

// recordPoll notes a successful getUpdates call.
func (s *stats) recordPoll(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Polls++
	s.status.LastPollAt = now
	s.status.LastPollError = ""
	s.flushLocked(now, false)
}

// recordPollError notes a failed getUpdates call.
func (s *stats) recordPollError(now time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastPollError = s.scrub.clean(errText(err))
	s.flushLocked(now, false)
}

// recordCommand notes a handled Telegram command (name only, no arguments).
func (s *stats) recordCommand(now time.Time, command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Commands++
	s.status.LastCommand = command
	s.status.LastCommandAt = now
	s.flushLocked(now, false)
}

// flush publishes the status, honouring the rate limit unless force is set.
func (s *stats) flush(now time.Time, force bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked(now, force)
}

func (s *stats) flushLocked(now time.Time, force bool) {
	if !force && now.Sub(s.lastWrite) < statusEvery {
		return
	}
	s.lastWrite = now
	if err := s.write(s.status); err != nil {
		s.log.Printf("cannot publish daemon status: %v", err)
	}
}

// snapshot returns a copy of the current status.
func (s *stats) snapshot() daemonctl.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// uptime is the time elapsed since the daemon started.
func (s *stats) uptime(now time.Time) time.Duration {
	return now.Sub(s.snapshot().StartedAt)
}
