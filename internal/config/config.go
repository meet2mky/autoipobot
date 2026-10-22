// Package config loads and saves the Telegram daemon settings shared by the
// daemon and the desktop app.
//
// Precedence when loading: process environment, then .data/env, then the
// secrets store (bot token only). When saving, the token goes to the secrets
// store and is removed from .data/env; chat ID and host label stay in the
// env file. A token found in an old .data/env is migrated on first load.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"autoipo/internal/envfile"
	"autoipo/internal/paths"
	"autoipo/internal/secrets"
)

// Environment variable names.
const (
	EnvToken  = "TG_TOKEN"
	EnvChatID = "TG_CHAT_ID"
	EnvLabel  = "HOST_LABEL"
)

// DefaultLabel is used when no host label is configured.
const DefaultLabel = "MyMac"

// Settings are the daemon's credentials and identity.
type Settings struct {
	Token     string
	ChatID    string
	HostLabel string
}

// Missing lists the required settings that are empty.
func (s Settings) Missing() []string {
	var missing []string
	if strings.TrimSpace(s.Token) == "" {
		missing = append(missing, EnvToken)
	}
	if strings.TrimSpace(s.ChatID) == "" {
		missing = append(missing, EnvChatID)
	}
	return missing
}

// Complete reports whether token and chat ID are present.
func (s Settings) Complete() bool { return len(s.Missing()) == 0 }

// ChatIDInt parses the chat ID.
func (s Settings) ChatIDInt() (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s.ChatID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", EnvChatID)
	}
	return id, nil
}

// Validate checks that the settings can start a daemon.
func (s Settings) Validate() error {
	if missing := s.Missing(); len(missing) > 0 {
		return fmt.Errorf("%s required", strings.Join(missing, " and "))
	}
	_, err := s.ChatIDInt()
	return err
}

// Load resolves settings from the environment, the env file and secrets.
// A token stored in .data/env is migrated into the secrets store.
func Load(sec secrets.Store) Settings {
	fileValues := envfile.LoadOrEmpty(paths.EnvFile())
	settings := Settings{
		Token:     envfile.Lookup(EnvToken, fileValues),
		ChatID:    envfile.Lookup(EnvChatID, fileValues),
		HostLabel: envfile.Lookup(EnvLabel, fileValues),
	}
	if sec != nil {
		if legacyToken := strings.TrimSpace(fileValues[EnvToken]); legacyToken != "" {
			if err := sec.Set(secrets.KeyTelegramToken, legacyToken); err == nil {
				delete(fileValues, EnvToken)
				_ = envfile.Write(paths.EnvFile(), fileValues, EnvChatID, EnvLabel)
			}
		}
		if settings.Token == "" {
			if token, err := sec.Get(secrets.KeyTelegramToken); err == nil {
				settings.Token = strings.TrimSpace(token)
			}
		}
	}
	if settings.HostLabel == "" {
		settings.HostLabel = DefaultLabel
	}
	return settings
}

// Save persists settings: token to secrets, the rest to .data/env.
func Save(settings Settings, sec secrets.Store) error {
	settings.Token = strings.TrimSpace(settings.Token)
	settings.ChatID = strings.TrimSpace(settings.ChatID)
	settings.HostLabel = strings.TrimSpace(settings.HostLabel)
	if settings.HostLabel == "" {
		settings.HostLabel = DefaultLabel
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	if sec == nil {
		return errors.New("no secrets store available")
	}
	if err := sec.Set(secrets.KeyTelegramToken, settings.Token); err != nil {
		return fmt.Errorf("store bot token: %w", err)
	}
	if err := paths.EnsureDataDir(); err != nil {
		return err
	}
	values := envfile.LoadOrEmpty(paths.EnvFile())
	delete(values, EnvToken)
	values[EnvChatID] = settings.ChatID
	values[EnvLabel] = settings.HostLabel
	return envfile.Write(paths.EnvFile(), values, EnvChatID, EnvLabel)
}

// FromEnvironmentOnly returns settings taken strictly from the process
// environment (used by `autoipo configure`).
func FromEnvironmentOnly() Settings {
	return Settings{
		Token:     strings.TrimSpace(os.Getenv(EnvToken)),
		ChatID:    strings.TrimSpace(os.Getenv(EnvChatID)),
		HostLabel: strings.TrimSpace(os.Getenv(EnvLabel)),
	}
}
