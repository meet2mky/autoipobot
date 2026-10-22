package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secrets.json")
	store := NewFileStore(path)
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	if err := store.Set(KeyTelegramToken, "123:abc"); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(UserPasswordKey+"ABCDE1234F", "p\"w'd\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(UserPasswordKey + "ABCDE1234F"); err != nil || got != "p\"w'd\n" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file perms = %v, %v", info, err)
	}
	if err := store.Delete(KeyTelegramToken); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(KeyTelegramToken); err != nil {
		t.Fatalf("Delete twice: %v", err)
	}
	if _, err := store.Get(KeyTelegramToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if err := store.Set("bad key!", "x"); err == nil {
		t.Fatal("invalid key should be rejected")
	}
	if store.Backend() != "file" {
		t.Fatal("backend name")
	}
}

func TestOpenHonoursFileOverride(t *testing.T) {
	t.Setenv(EnvBackend, "file")
	if Open().Backend() != "file" {
		t.Fatal("AUTOIPO_SECRETS=file should select the file store")
	}
}
