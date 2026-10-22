package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoipo/internal/paths"
	"autoipo/internal/secrets"
)

func setup(t *testing.T) secrets.Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv(paths.EnvRoot, root)
	paths.ResetForTesting()
	t.Cleanup(paths.ResetForTesting)
	for _, key := range []string{EnvToken, EnvChatID, EnvLabel} {
		t.Setenv(key, "")
	}
	return secrets.NewFileStore(filepath.Join(root, "secrets.json"))
}

func TestLoadMigratesTokenOutOfEnvFile(t *testing.T) {
	sec := setup(t)
	_ = os.MkdirAll(paths.DataDir(), 0o700)
	_ = os.WriteFile(paths.EnvFile(), []byte("TG_TOKEN=123:abc\nTG_CHAT_ID=42\nHOST_LABEL=Mac\n"), 0o600)

	settings := Load(sec)
	if settings.Token != "123:abc" || settings.ChatID != "42" || settings.HostLabel != "Mac" {
		t.Fatalf("Load = %+v", settings)
	}
	data, _ := os.ReadFile(paths.EnvFile())
	if strings.Contains(string(data), "TG_TOKEN") {
		t.Fatalf("token should have been removed from env file: %s", data)
	}
	if token, err := sec.Get(secrets.KeyTelegramToken); err != nil || token != "123:abc" {
		t.Fatalf("token not migrated: %q %v", token, err)
	}
	// Second load reads the token from secrets.
	if again := Load(sec); again.Token != "123:abc" {
		t.Fatalf("second Load = %+v", again)
	}
}

func TestEnvironmentWinsAndSaveRoundTrip(t *testing.T) {
	sec := setup(t)
	if err := Save(Settings{Token: " t ", ChatID: "7", HostLabel: ""}, sec); err != nil {
		t.Fatal(err)
	}
	loaded := Load(sec)
	if loaded.Token != "t" || loaded.ChatID != "7" || loaded.HostLabel != DefaultLabel {
		t.Fatalf("Load after Save = %+v", loaded)
	}
	t.Setenv(EnvChatID, "99")
	if Load(sec).ChatID != "99" {
		t.Fatal("environment should override the env file")
	}
	if err := Save(Settings{Token: "", ChatID: "7"}, sec); err == nil {
		t.Fatal("Save without token must fail")
	}
	if err := Save(Settings{Token: "t", ChatID: "abc"}, sec); err == nil {
		t.Fatal("Save with non-numeric chat ID must fail")
	}
	s := Settings{Token: "x", ChatID: "12"}
	if id, err := s.ChatIDInt(); err != nil || id != 12 || !s.Complete() {
		t.Fatal("ChatIDInt/Complete")
	}
	if missing := (Settings{}).Missing(); len(missing) != 2 {
		t.Fatalf("Missing = %v", missing)
	}
}
