package main

import (
	"context"
	"fmt"
	"time"

	"autoipo/internal/flow"
	"autoipo/internal/paths"
	"autoipo/internal/schedule"
	"autoipo/internal/secrets"
	"autoipo/internal/users"
)

const (
	scheduleTick  = 15 * time.Second
	schedulePrune = 24 * time.Hour
	scheduleKeep  = 7 * 24 * time.Hour
)

// scheduler executes due schedule entries through the shared runner, one at
// a time, and records their outcome on the entry.
type scheduler struct {
	store   *schedule.Store
	flows   *flow.Store
	secrets secrets.Store
	runner  *runner
	out     outbox
	log     *logger
}

// run polls for due entries every scheduleTick until ctx ends and prunes
// finished entries once a day.
func (s *scheduler) run(ctx context.Context) {
	s.recoverInterrupted()
	ticker := time.NewTicker(scheduleTick)
	defer ticker.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.Sub(lastPrune) >= schedulePrune {
				if err := s.store.Prune(scheduleKeep); err != nil {
					s.log.Printf("scheduler: cannot prune old entries: %v", err)
				}
				lastPrune = now
			}
			s.tick(ctx, now)
		}
	}
}

// recoverInterrupted fails entries left "running" by a previous daemon
// process; nothing else executes schedules, so they cannot still be live.
func (s *scheduler) recoverInterrupted() {
	entries, err := s.store.List()
	if err != nil {
		s.log.Printf("scheduler: cannot list entries: %v", err)
		return
	}
	for _, entry := range entries {
		if entry.Status == schedule.StatusRunning {
			s.log.Printf("scheduler: entry %s was interrupted by a restart", entry.ID)
			s.finish(entry, false, "interrupted by a daemon restart")
		}
	}
}

// tick claims and executes the earliest due entry. While the browser is in
// use by another run, due entries stay pending and are retried next tick.
func (s *scheduler) tick(ctx context.Context, now time.Time) {
	if s.runner.busy() || flow.RunActive() {
		return
	}
	entry, ok, err := s.store.ClaimDue(now)
	if err != nil {
		s.log.Printf("scheduler: cannot claim due entry: %v", err)
		return
	}
	if !ok {
		return
	}
	s.execute(ctx, entry)
}

func (s *scheduler) execute(ctx context.Context, entry schedule.Entry) {
	req, err := s.request(entry)
	if err != nil {
		s.log.Printf("scheduled run %s skipped: %v", entry.ID, err)
		s.finish(entry, false, err.Error())
		s.out.text(fmt.Sprintf("scheduled run %s (%s) could not start: %v", entry.ID, entry.FlowName, err))
		return
	}
	s.out.text(fmt.Sprintf("starting scheduled run %s (%s)", describeRun(req), entry.ID))
	result, err := s.runner.runSync(ctx, req)
	reportRun(s.out, req, result, err)
	success, note := runOutcome(result, err)
	s.finish(entry, success, note)
}

// request resolves the entry's flow and user into a run request.
func (s *scheduler) request(entry schedule.Entry) (flow.RunRequest, error) {
	flows, _, err := s.flows.Load()
	if err != nil {
		return flow.RunRequest{}, fmt.Errorf("read flows: %w", err)
	}
	f, ok := flow.Find(flows, entry.FlowID)
	if !ok {
		f, ok = flow.Find(flows, entry.FlowName)
	}
	if !ok {
		return flow.RunRequest{}, fmt.Errorf("flow %q no longer exists", entry.FlowName)
	}
	req := flow.RunRequest{Flow: f, Source: "schedule"}
	if entry.UserPAN == "" {
		return req, nil
	}
	store, err := users.Open(paths.UsersFile(), s.secrets)
	if err != nil {
		return flow.RunRequest{}, fmt.Errorf("read users: %w", err)
	}
	record, ok := store.Get(entry.UserPAN)
	if !ok {
		return flow.RunRequest{}, fmt.Errorf("user %s no longer exists", maskPAN(entry.UserPAN))
	}
	req.UserValues, req.UserLabel = record.Placeholders(), record.Name
	return req, nil
}

func (s *scheduler) finish(entry schedule.Entry, success bool, note string) {
	if err := s.store.Finish(entry.ID, success, note); err != nil {
		s.log.Printf("scheduler: cannot record outcome of %s: %v", entry.ID, err)
	}
}
