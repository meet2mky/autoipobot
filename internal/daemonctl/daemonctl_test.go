package daemonctl

import (
	"os"
	"testing"
	"time"

	"autoipo/internal/instancelock"
	"autoipo/internal/paths"
)

func TestRunningReflectsLock(t *testing.T) {
	t.Setenv(paths.EnvRoot, t.TempDir())
	resetPaths(t)

	if _, running := Running(); running {
		t.Fatal("Running should be false with no lock holder")
	}
	lock, acquired, err := instancelock.Acquire(paths.DaemonLockFile())
	if err != nil || !acquired {
		t.Fatalf("Acquire: %v %v", acquired, err)
	}
	if err := WritePIDFile(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	pid, running := Running()
	if !running || pid != os.Getpid() {
		t.Fatalf("Running = (%d, %v), want (%d, true)", pid, running, os.Getpid())
	}
	_ = lock.Release()
	if _, running := Running(); running {
		t.Fatal("Running should be false after release")
	}
	if err := RemovePIDFile(); err != nil {
		t.Fatal(err)
	}
	if err := RemovePIDFile(); err != nil {
		t.Fatalf("second RemovePIDFile should be a no-op: %v", err)
	}
}

func TestStatusRoundTrip(t *testing.T) {
	t.Setenv(paths.EnvRoot, t.TempDir())
	resetPaths(t)
	if _, err := ReadStatus(); !os.IsNotExist(err) {
		t.Fatalf("ReadStatus missing = %v", err)
	}
	started := time.Now().Add(-90 * time.Second)
	if err := WriteStatus(Status{PID: 42, Label: "Mac", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus()
	if err != nil || status.PID != 42 || status.Label != "Mac" || status.UpdatedAt.IsZero() {
		t.Fatalf("ReadStatus = %+v, %v", status, err)
	}
	if uptime := status.Uptime(time.Now()); uptime < 89*time.Second || uptime > 95*time.Second {
		t.Fatalf("Uptime = %s", uptime)
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	t.Setenv(paths.EnvRoot, t.TempDir())
	resetPaths(t)
	if err := Stop(time.Second); err != nil {
		t.Fatalf("Stop without daemon: %v", err)
	}
	if err := Start("/nonexistent/autoipo", nil, nil); err == nil {
		t.Fatal("Start with missing binary should fail")
	}
}
