// Package config owns the on-disk CLI config file at
// $XDG_CONFIG_HOME/fyredocs/config.json (default ~/.config/fyredocs/
// on Linux, ~/Library/Application Support/fyredocs/ on macOS via
// the XDG override most users set explicitly).
//
// The format is intentionally tiny — just an API key and a base
// URL. Future fields (e.g., default org for multi-tenant accounts)
// land here without a migration; unknown fields are tolerated.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Config is the on-disk shape.
type Config struct {
	// APIKey is the `fyr_…` token stored verbatim. Empty means
	// "logged out" (the caller should re-run `fyredocs login`).
	APIKey string `json:"apiKey"`
	// BaseURL overrides the default API endpoint. Empty falls
	// back to https://api.fyredocs.com — set this for
	// self-hosted deployments or staging environments.
	BaseURL string `json:"baseUrl,omitempty"`
}

// DefaultBaseURL is the production API endpoint.
const DefaultBaseURL = "https://api.fyredocs.com"

// ErrNotLoggedIn is returned by Load when the config file is
// missing OR present but contains no API key. Commands that need
// auth should print a "run `fyredocs login` first" message on
// this error rather than a raw filesystem error.
var ErrNotLoggedIn = errors.New("not logged in")

// Path returns the absolute path to the CLI config file. The
// directory is created lazily by Save, so this can be called
// safely without side effects.
func Path() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fyredocs", "config.json"), nil
}

// Load reads + parses the config file. Returns ErrNotLoggedIn
// when the file doesn't exist or has an empty apiKey. Anything
// else (permission denied, malformed JSON) is returned
// verbatim so the caller can surface the diagnostic.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", p, err)
	}
	if cfg.APIKey == "" {
		return nil, ErrNotLoggedIn
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	return &cfg, nil
}

// Save writes the config to disk with 0600 permissions (the API
// key is a long-lived secret; 0600 is the same posture every
// other CLI uses for credentials). Creates the parent directory
// if missing.
func Save(cfg *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file then rename so a half-written config
	// can't be observed by a concurrent `fyredocs` invocation.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Delete removes the config file. Returns nil on success or when
// the file didn't exist (idempotent — `fyredocs logout` on a
// fresh machine should not error).
func Delete() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// configDir returns the OS config directory. Honors
// $XDG_CONFIG_HOME on Unix; falls back to ~/.config on Linux and
// ~/Library/Application Support on macOS via os.UserConfigDir.
//
// Pulled into its own function so tests can override via the
// $XDG_CONFIG_HOME env var without monkey-patching os.UserConfigDir.
func configDir() (string, error) {
	if override := os.Getenv("XDG_CONFIG_HOME"); override != "" {
		return override, nil
	}
	return os.UserConfigDir()
}
