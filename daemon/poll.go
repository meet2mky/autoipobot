package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"autoipo/internal/envfile"
)

const (
	pollTimeout     = 30 * time.Second
	resumeThreshold = 3 * time.Minute
	backoffBase     = time.Second
	backoffMax      = 60 * time.Second
)

// pollLoop long-polls Telegram until ctx ends, persisting the update offset
// after every update and dispatching messages to the bot. Poll failures are
// logged once when they begin and once when they end, with exponential
// backoff in between.
func (d *daemon) pollLoop(ctx context.Context) {
	offset, err := d.offsets.load()
	if err != nil {
		d.log.Printf("telegram offset unreadable, starting from 0: %v", err)
	}
	prev := d.now().Round(0)
	failures := 0
	for ctx.Err() == nil {
		updates, err := d.tg.GetUpdates(ctx, offset, pollTimeout)
		now := d.now().Round(0)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.stats.recordPollError(now, err)
			if failures == 0 {
				d.log.Printf("telegram poll failing: %s (retrying with backoff)", errText(err))
			}
			failures++
			sleep(ctx, d.backoff(failures-1))
			continue
		}
		if failures > 0 {
			d.log.Printf("telegram poll recovered after %d failed attempt(s)", failures)
			failures = 0
		}
		d.stats.recordPoll(now)
		if gap, resumed := resumeGap(prev, now); resumed {
			d.log.Printf("resumed after a %s gap", formatDuration(gap))
			d.out.text(fmt.Sprintf("%s RESUMED after %s", d.label, formatDuration(gap)))
		}
		prev = now
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
				if err := d.offsets.save(offset); err != nil {
					d.log.Printf("cannot persist telegram offset: %v", err)
				}
			}
			if update.Message != nil {
				d.bot.handle(ctx, *update.Message, now)
			}
		}
	}
}

// resumeGap reports the wall-clock time between two loop iterations and
// whether it is long enough to count as a resume after sleep or a network
// outage. Monotonic readings are stripped because they may not advance while
// the machine sleeps.
func resumeGap(prev, now time.Time) (time.Duration, bool) {
	if prev.IsZero() {
		return 0, false
	}
	gap := now.Round(0).Sub(prev.Round(0))
	return gap, gap > resumeThreshold
}

// backoffDelay returns the pause before retry number n (counted from 0):
// 1s, 2s, 4s, … capped at backoffMax.
func backoffDelay(n int) time.Duration {
	delay := backoffBase
	for ; n > 0 && delay < backoffMax; n-- {
		delay *= 2
	}
	return min(delay, backoffMax)
}

// sleep waits for d or until ctx ends; it reports whether the full duration
// elapsed.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// errText renders an error for logs and the status file, bounded in length
// because transport errors may quote whole response bodies.
func errText(err error) string {
	const limit = 300
	runes := []rune(err.Error())
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}

// offsetStore persists the getUpdates offset so that a restart does not
// replay commands that were already handled.
type offsetStore struct {
	path string
}

// load returns the stored offset, or 0 when none has been written yet.
func (o offsetStore) load() (int64, error) {
	data, err := os.ReadFile(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", o.path, err)
	}
	return offset, nil
}

// save atomically writes offset.
func (o offsetStore) save(offset int64) error {
	if err := os.MkdirAll(filepath.Dir(o.path), 0o700); err != nil {
		return err
	}
	return envfile.WriteFileAtomic(o.path, []byte(strconv.FormatInt(offset, 10)+"\n"), 0o600)
}
