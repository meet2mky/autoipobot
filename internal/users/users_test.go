package users

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoipo/internal/secrets"
)

func newStore(t *testing.T, initial string) (*Store, secrets.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "users")
	if initial != "" {
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sec := secrets.NewFileStore(filepath.Join(dir, "secrets.json"))
	store, err := Open(path, sec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store, sec, path
}

func TestOpenMigratesLegacyPasswords(t *testing.T) {
	legacy := `{"abcde1234f":{"name":"Alice","pan":"","bank_name":"HDFC","user_id":"alice","password":"s3cret","email":"a@x"}}`
	store, sec, path := newStore(t, legacy)

	record, ok := store.Get("ABCDE1234F")
	if !ok || record.Password != "s3cret" || record.PAN != "ABCDE1234F" {
		t.Fatalf("Get after migration = %+v, %v", record, ok)
	}
	if password, err := sec.Get(secrets.UserPasswordKey + "ABCDE1234F"); err != nil || password != "s3cret" {
		t.Fatalf("secret not migrated: %q, %v", password, err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), `"password"`) {
		t.Fatalf("users file still contains password: %s", data)
	}
}

func TestOpenRefusesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	_, err := Open(path, secrets.NewFileStore(filepath.Join(dir, "s.json")))
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open corrupt = %v, want ErrCorrupt", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{not json" {
		t.Fatal("corrupt file must not be modified")
	}
}

func TestPutValidateAndRename(t *testing.T) {
	store, sec, path := newStore(t, "")
	err := store.Put(Record{Name: "Bob", PAN: "bad", BankName: "hdfc", UserID: "bob", Password: "pw"}, "")
	if err == nil || !strings.Contains(err.Error(), "ABCDE1234F") {
		t.Fatalf("Put invalid PAN error = %v", err)
	}
	if err := store.Put(Record{Name: "Bob", PAN: " fghij5678k ", BankName: "axis", UserID: "bob", Password: "pw"}, ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := store.Get("FGHIJ5678K")
	if !ok || got.BankName != "Axis" || got.Password != "pw" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	// Duplicate PAN for a new record is rejected.
	if err := store.Put(Record{Name: "Other", PAN: "FGHIJ5678K", BankName: "HDFC", UserID: "o", Password: "x"}, ""); err == nil {
		t.Fatal("duplicate PAN should be rejected")
	}
	// Rename PAN moves the secret.
	if err := store.Put(Record{Name: "Bob", PAN: "LMNOP1234Q", BankName: "Axis", UserID: "bob", Password: "pw2"}, "FGHIJ5678K"); err != nil {
		t.Fatalf("Put rename: %v", err)
	}
	if _, ok := store.Get("FGHIJ5678K"); ok {
		t.Fatal("old PAN should be gone")
	}
	if _, err := sec.Get(secrets.UserPasswordKey + "FGHIJ5678K"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("old secret should be deleted, got %v", err)
	}
	if got, _ := store.Get("LMNOP1234Q"); got.Password != "pw2" {
		t.Fatalf("new password = %q", got.Password)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "pw2") {
		t.Fatal("password leaked to users file")
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup not written: %v", err)
	}
}

func TestFindAndPlaceholders(t *testing.T) {
	store, _, _ := newStore(t, "")
	_ = store.Put(Record{Name: "Alice Kumar", PAN: "ABCDE1234F", BankName: "HDFC", UserID: "alice", Password: "a", Phone: "9"}, "")
	_ = store.Put(Record{Name: "Alex", PAN: "ABCDE1234G", BankName: "HDFC", UserID: "alex", Password: "b"}, "")
	if got, ok := store.Find("abcde1234f"); !ok || got.Name != "Alice Kumar" {
		t.Fatalf("Find by PAN = %+v, %v", got, ok)
	}
	if got, ok := store.Find("alice"); !ok || got.PAN != "ABCDE1234F" {
		t.Fatalf("Find by prefix = %+v, %v", got, ok)
	}
	if _, ok := store.Find("al"); ok {
		t.Fatal("ambiguous prefix should not match")
	}
	values := store.List()[0].Placeholders()
	if values["user_id"] != "alex" || values["password"] != "b" || values["bank"] != "HDFC" {
		t.Fatalf("Placeholders = %#v", values)
	}
	if err := store.Delete("ABCDE1234G"); err != nil || store.Len() != 1 {
		t.Fatalf("Delete: %v, len=%d", err, store.Len())
	}
}

func TestListSummariesSkipsSecrets(t *testing.T) {
	store, _, _ := newStore(t, "")
	_ = store.Put(Record{Name: "Zed", PAN: "ABCDE1234Z", BankName: "HDFC", UserID: "z", Password: "pw"}, "")
	_ = store.Put(Record{Name: "Amy", PAN: "ABCDE1234A", BankName: "Axis", UserID: "a", Password: "pw"}, "")
	summaries := store.ListSummaries()
	if len(summaries) != 2 || summaries[0].Name != "Amy" || summaries[1].Name != "Zed" {
		t.Fatalf("ListSummaries order = %+v", summaries)
	}
	for _, record := range summaries {
		if record.Password != "" {
			t.Fatal("summaries must not carry passwords")
		}
	}
	if full, _ := store.Get("ABCDE1234A"); full.Password != "pw" {
		t.Fatal("Get should still load the password")
	}
}
