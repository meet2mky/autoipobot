package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectRootHonoursEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvRoot, dir)
	if got := detectRoot(); got != dir {
		t.Fatalf("detectRoot = %q, want %q", got, dir)
	}
}

func TestDetectRootFindsDaemonDirectoryInParent(t *testing.T) {
	t.Setenv(EnvRoot, "")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "app")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	got := detectRoot()
	// macOS temp dirs may be symlinked; compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Fatalf("detectRoot = %q, want %q", got, root)
	}
}

func TestWellKnownPathsLiveUnderData(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	for name, fn := range map[string]func() string{
		"env": EnvFile, "users": UsersFile, "flows": FlowsFile, "pid": DaemonPIDFile,
		"offset": TelegramOffsetFile, "lock": DaemonLockFile, "applock": AppLockFile,
	} {
		if rel, err := filepath.Rel(DataDir(), fn()); err != nil || rel == "" || rel[0] == '.' {
			t.Errorf("%s = %q is not inside %q", name, fn(), DataDir())
		}
	}
}
