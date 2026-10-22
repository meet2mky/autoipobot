// Package daemonctl lets the desktop app start, stop and observe the Telegram
// daemon without shelling out to pgrep/pkill. Liveness is determined by
// probing the daemon's advisory lock, which cannot suffer from PID reuse.
package daemonctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"autoipo/internal/envfile"
	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
)

// Status is published by the daemon to .data/daemon-status.json.
type Status struct {
	PID           int       `json:"pid"`
	Version       string    `json:"version,omitempty"`
	Label         string    `json:"label"`
	ChatID        int64     `json:"chat_id"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastPollAt    time.Time `json:"last_poll_at,omitempty"`
	LastPollError string    `json:"last_poll_error,omitempty"`
	LastCommandAt time.Time `json:"last_command_at,omitempty"`
	LastCommand   string    `json:"last_command,omitempty"`
	Polls         int64     `json:"polls"`
	Commands      int64     `json:"commands"`
}

// Uptime returns how long the daemon has been running.
func (s Status) Uptime(now time.Time) time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	return now.Sub(s.StartedAt).Round(time.Second)
}

// WriteStatus atomically publishes the daemon status.
func WriteStatus(status Status) error {
	status.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := paths.EnsureDataDir(); err != nil {
		return err
	}
	return envfile.WriteFileAtomic(paths.DaemonStatusFile(), append(data, '\n'), 0o600)
}

// ReadStatus loads the last published status (os.ErrNotExist when absent).
func ReadStatus() (Status, error) {
	data, err := os.ReadFile(paths.DaemonStatusFile())
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return Status{}, fmt.Errorf("parse daemon status: %w", err)
	}
	return status, nil
}

// WritePIDFile records the daemon's PID.
func WritePIDFile(pid int) error {
	if err := paths.EnsureDataDir(); err != nil {
		return err
	}
	return envfile.WriteFileAtomic(paths.DaemonPIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// RemovePIDFile deletes the PID file (missing is not an error).
func RemovePIDFile() error {
	err := os.Remove(paths.DaemonPIDFile())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadPID returns the recorded daemon PID.
func ReadPID() (int, error) {
	data, err := os.ReadFile(paths.DaemonPIDFile())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID file content %q", strings.TrimSpace(string(data)))
	}
	return pid, nil
}

// Running reports whether a daemon currently holds the daemon lock. The PID
// is best-effort (0 when unknown).
func Running() (pid int, running bool) {
	lock, acquired, err := instancelock.Acquire(paths.DaemonLockFile())
	if err != nil {
		// Fall back to the PID file when the lock cannot be probed.
		pid, pidErr := ReadPID()
		if pidErr != nil {
			return 0, false
		}
		return pid, processAlive(pid)
	}
	if acquired {
		_ = lock.Release()
		return 0, false
	}
	pid, _ = ReadPID()
	return pid, true
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Start launches the daemon binary detached from the caller's lifetime. The
// daemon loads its own configuration (environment, .data/env, secrets), so
// no credentials are passed on the command line. extraEnv entries override
// the inherited environment.
func Start(binary string, extraEnv map[string]string, output io.Writer) error {
	if _, running := Running(); running {
		return errors.New("daemon is already running")
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("daemon binary not found at %s: %w", binary, err)
	}
	cmd := exec.Command(binary)
	cmd.Dir = paths.Root()
	cmd.Env = os.Environ()
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if output != nil {
		cmd.Stdout = output
		cmd.Stderr = output
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	// Wait for the daemon to take its lock so callers get definitive feedback.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			if err != nil {
				return fmt.Errorf("daemon exited immediately: %w (check %s)", err, paths.DaemonLogFile())
			}
			return fmt.Errorf("daemon exited immediately (check %s)", paths.DaemonLogFile())
		default:
		}
		if _, running := Running(); running {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not report running within 5s (check %s)", paths.DaemonLogFile())
}

// Stop sends SIGTERM to the running daemon and waits for it to release its
// lock, escalating to SIGKILL after timeout. It is a no-op when not running.
func Stop(timeout time.Duration) error {
	pid, running := Running()
	if !running {
		return nil
	}
	if pid <= 0 {
		return errors.New("daemon is running but its PID file is missing; stop it manually")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal daemon %d: %w", pid, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, running := Running(); !running {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = process.Signal(syscall.SIGKILL)
	killDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(killDeadline) {
		if _, running := Running(); !running {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon %d did not exit", pid)
}

// Restart stops a running daemon (if any) and starts a new one.
func Restart(binary string, extraEnv map[string]string, output io.Writer) error {
	if err := Stop(10 * time.Second); err != nil {
		return err
	}
	return Start(binary, extraEnv, output)
}
