package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	input := "# comment\n\nTG_TOKEN = abc:def \nexport HOST_LABEL=\"My Mac\"\nBROKEN LINE\nEMPTY=\n=novalue\n"
	got := Parse(strings.NewReader(input))
	want := map[string]string{"TG_TOKEN": "abc:def", "HOST_LABEL": "My Mac", "EMPTY": ""}
	if len(got) != len(want) {
		t.Fatalf("Parse = %#v, want %#v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("Parse[%q] = %q, want %q", key, got[key], value)
		}
	}
}

func TestWriteAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "env")
	values := map[string]string{"B": "2", "A": "1", "TG_TOKEN": "tok"}
	if err := Write(path, values, "TG_TOKEN"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "TG_TOKEN=tok\nA=1\nB=2\n" {
		t.Fatalf("unexpected file content %q", data)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("permissions = %o, want 600", perm)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded["A"] != "1" || loaded["B"] != "2" || loaded["TG_TOKEN"] != "tok" {
		t.Fatalf("Load = %#v", loaded)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("Load missing = %v, want not-exist", err)
	}
	if got := LoadOrEmpty(filepath.Join(t.TempDir(), "missing")); len(got) != 0 {
		t.Fatalf("LoadOrEmpty = %#v", got)
	}
}

func TestLookupPrefersEnvironment(t *testing.T) {
	t.Setenv("AUTOIPO_TEST_KEY", " from-env ")
	if got := Lookup("AUTOIPO_TEST_KEY", map[string]string{"AUTOIPO_TEST_KEY": "from-file"}); got != "from-env" {
		t.Fatalf("Lookup = %q", got)
	}
	t.Setenv("AUTOIPO_TEST_KEY", "")
	if got := Lookup("AUTOIPO_TEST_KEY", map[string]string{"AUTOIPO_TEST_KEY": "from-file"}); got != "from-file" {
		t.Fatalf("Lookup = %q", got)
	}
}
