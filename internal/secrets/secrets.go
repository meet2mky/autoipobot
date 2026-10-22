// Package secrets stores small secrets (bot token, user passwords).
//
// On macOS, when the `security` tool is available, secrets are kept in the
// login keychain under the service name "autoipo". Everywhere else — or when
// AUTOIPO_SECRETS=file is set — they are kept in .data/secrets.json (0600).
// Secret values are passed to `security` hex-encoded over stdin so they never
// appear in process listings.
package secrets

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"autoipo/internal/envfile"
	"autoipo/internal/paths"
)

// ErrNotFound is returned when a key has no stored value.
var ErrNotFound = errors.New("secret not found")

// Store is a key/value secret store.
type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	// Backend names the implementation ("keychain" or "file") for diagnostics.
	Backend() string
}

// Well-known keys.
const (
	KeyTelegramToken = "telegram-token"
	UserPasswordKey  = "user-password:" // + PAN
)

// EnvBackend forces a backend: "file" or "keychain".
const EnvBackend = "AUTOIPO_SECRETS"

const keychainService = "autoipo"

var validKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Open returns the best available store for this machine.
func Open() Store {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvBackend))) {
	case "file":
		return NewFileStore(paths.SecretsFile())
	case "keychain":
		return &keychainStore{}
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("security"); err == nil {
			return &keychainStore{}
		}
	}
	return NewFileStore(paths.SecretsFile())
}

func checkKey(key string) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("invalid secret key %q", key)
	}
	return nil
}

// File store ---------------------------------------------------------------

// FileStore keeps secrets in a JSON file with owner-only permissions.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore returns a FileStore at path.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) Backend() string { return "file" }

func (s *FileStore) load() (map[string]string, error) {
	values := map[string]string{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return values, nil
}

func (s *FileStore) save(values map[string]string) error {
	if err := os.MkdirAll(dirOf(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return envfile.WriteFileAtomic(s.path, append(data, '\n'), 0o600)
}

func (s *FileStore) Get(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return "", err
	}
	value, ok := values[key]
	if !ok {
		return "", ErrNotFound
	}
	return value, nil
}

func (s *FileStore) Set(key, value string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	values[key] = value
	return s.save(values)
}

func (s *FileStore) Delete(key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := values[key]; !ok {
		return nil
	}
	delete(values, key)
	return s.save(values)
}

func dirOf(path string) string {
	if index := strings.LastIndex(path, string(os.PathSeparator)); index > 0 {
		return path[:index]
	}
	return "."
}

// Keychain store -----------------------------------------------------------

type keychainStore struct{}

func (keychainStore) Backend() string { return "keychain" }

// runSecurity executes the `security` tool. When stdin is non-empty the tool
// runs in interactive mode (`security -i`) and reads the command from stdin,
// which keeps secret material out of argv and process listings.
func runSecurity(stdin string, args ...string) (stdout, stderr string, code int, err error) {
	var cmd *exec.Cmd
	if stdin != "" {
		cmd = exec.Command("security", "-i")
		cmd.Stdin = strings.NewReader(stdin + "\n")
	} else {
		cmd = exec.Command("security", args...)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return stdout, stderr, exitErr.ExitCode(), nil
		}
		return stdout, stderr, -1, runErr
	}
	return stdout, stderr, 0, nil
}

func isNotFound(code int, stderr string) bool {
	return code == 44 || strings.Contains(stderr, "could not be found")
}

func (keychainStore) Get(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	// Only the account name goes on the command line; -w prints the password
	// alone on stdout. Reading through the `security` binary avoids per-app
	// keychain prompts because `security` created the item and is on its ACL.
	stdout, stderr, code, err := runSecurity("", "find-generic-password", "-a", key, "-s", keychainService, "-w")
	if err != nil {
		return "", fmt.Errorf("keychain lookup: %w", err)
	}
	if code != 0 {
		if isNotFound(code, stderr) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain lookup failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	return strings.TrimRight(stdout, "\r\n"), nil
}

func (s keychainStore) Set(key, value string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	encoded := hex.EncodeToString([]byte(value))
	command := fmt.Sprintf("add-generic-password -a %s -s %s -U -X %s", key, keychainService, encoded)
	_, stderr, code, err := runSecurity(command)
	if err != nil {
		return fmt.Errorf("keychain store: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("keychain store failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	// Interactive mode does not reliably propagate the inner command's exit
	// status, so confirm the write by reading the value back.
	stored, err := s.Get(key)
	if err != nil {
		return fmt.Errorf("keychain store could not be verified: %w (%s)", err, strings.TrimSpace(stderr))
	}
	if stored != value {
		return fmt.Errorf("keychain store verification failed for %s", key)
	}
	return nil
}

func (keychainStore) Delete(key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	_, stderr, code, err := runSecurity("", "delete-generic-password", "-a", key, "-s", keychainService)
	if err != nil {
		return fmt.Errorf("keychain delete: %w", err)
	}
	if code != 0 && !isNotFound(code, stderr) {
		return fmt.Errorf("keychain delete failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	return nil
}
