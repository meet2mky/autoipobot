// Package paths resolves the repository/install root and the well-known
// runtime locations shared by the daemon, the desktop app and the CLI.
//
// All mutable runtime state lives under <root>/.data so that it is excluded
// from version control and never depends on the OS temporary directory.
package paths

import (
	"os"
	"path/filepath"
	"sync"
)

// EnvRoot overrides automatic root detection when set.
const EnvRoot = "AUTOIPO_HOME"

var (
	rootOnce sync.Once
	rootDir  string
)

// Root returns the repository root. Detection order:
//  1. $AUTOIPO_HOME
//  2. the working directory or its parent, if it looks like the repository
//  3. the executable's directory or its parent, if it looks like the repository
//  4. the executable's parent directory when the executable lives in "bin"
//  5. the working directory
func Root() string {
	rootOnce.Do(func() { rootDir = detectRoot() })
	return rootDir
}

func detectRoot() string {
	if env := os.Getenv(EnvRoot); env != "" {
		if abs, err := filepath.Abs(env); err == nil {
			return abs
		}
		return env
	}
	var candidates []string
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd, filepath.Dir(wd))
	}
	exeDir := ""
	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir = filepath.Dir(exe)
		candidates = append(candidates, exeDir, filepath.Dir(exeDir))
	}
	for _, candidate := range candidates {
		if looksLikeRoot(candidate) {
			return candidate
		}
	}
	if exeDir != "" && filepath.Base(exeDir) == "bin" {
		return filepath.Dir(exeDir)
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func looksLikeRoot(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "daemon")); err == nil && info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, ".data")); err == nil && info.IsDir() {
		return true
	}
	return false
}

// DataDir is the private runtime state directory (<root>/.data).
func DataDir() string { return filepath.Join(Root(), ".data") }

// Data joins name onto DataDir.
func Data(name ...string) string {
	return filepath.Join(append([]string{DataDir()}, name...)...)
}

// EnsureDataDir creates DataDir with owner-only permissions.
func EnsureDataDir() error { return os.MkdirAll(DataDir(), 0o700) }

// BinDir is where built helper binaries are installed (<root>/bin).
func BinDir() string { return filepath.Join(Root(), "bin") }

// Bin returns the path of a helper binary inside BinDir.
func Bin(name string) string { return filepath.Join(BinDir(), name) }

// Well-known runtime files.
func EnvFile() string            { return Data("env") }
func UsersFile() string          { return Data("users") }
func FlowsFile() string          { return Data("flows.json") }
func FlowsDir() string           { return Data("flows") }
func SchedulesFile() string      { return Data("schedules.json") }
func LastRunFile() string        { return Data("last-run.json") }
func SecretsFile() string        { return Data("secrets.json") }
func BridgeTokenFile() string    { return Data("bridge.token") }
func DaemonPIDFile() string      { return Data("autoipo.pid") }
func DaemonLockFile() string     { return Data("autoipo-daemon.lock") }
func AppLockFile() string        { return Data("autoipoapp.lock") }
func FlowRunLockFile() string    { return Data("flow-run.lock") }
func TelegramOffsetFile() string { return Data("telegram.offset") }
func DaemonStatusFile() string   { return Data("daemon-status.json") }
func WindowStateFile() string    { return Data("window.json") }

// LogDir is the daemon log directory (<root>/daemon/logs), kept for
// compatibility with existing deployments.
func LogDir() string { return filepath.Join(Root(), "daemon", "logs") }

// DaemonLogFile is the daemon's primary log file.
func DaemonLogFile() string { return filepath.Join(LogDir(), "autoipo.log") }

// DaemonBinary and CLIBinary are the installed helper binaries.
func DaemonBinary() string { return Bin("autoipo") }
func CLIBinary() string    { return Bin("chrome-extension-cli") }
