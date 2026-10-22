// Package envfile reads and writes simple KEY=value files such as .data/env.
package envfile

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Parse reads KEY=value lines. Blank lines and lines starting with '#' are
// ignored. Keys and values are trimmed; surrounding single or double quotes
// around a value are removed.
func Parse(r io.Reader) map[string]string {
	values := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = unquote(strings.TrimSpace(value))
	}
	return values
}

func unquote(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// Load parses the file at path. A missing file yields os.ErrNotExist.
func Load(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Parse(file), nil
}

// LoadOrEmpty is Load but treats a missing file as empty.
func LoadOrEmpty(path string) map[string]string {
	values, err := Load(path)
	if err != nil {
		return map[string]string{}
	}
	return values
}

// Write atomically writes values to path with owner-only permissions.
// Keys listed in order are written first (in that order); remaining keys are
// appended sorted.
func Write(path string, values map[string]string, order ...string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	var builder strings.Builder
	written := make(map[string]bool, len(values))
	for _, key := range order {
		if value, ok := values[key]; ok {
			fmt.Fprintf(&builder, "%s=%s\n", key, value)
			written[key] = true
		}
	}
	rest := make([]string, 0, len(values))
	for key := range values {
		if !written[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		fmt.Fprintf(&builder, "%s=%s\n", key, values[key])
	}
	return WriteFileAtomic(path, []byte(builder.String()), 0o600)
}

// WriteFileAtomic writes data to a temporary file in the same directory and
// renames it over path, so readers never observe a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Lookup returns the first non-empty value for key from the process
// environment, then from the provided file values.
func Lookup(key string, fileValues map[string]string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return strings.TrimSpace(fileValues[key])
}
