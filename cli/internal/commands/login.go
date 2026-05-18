package commands

import (
	"errors"
	"flag"
	"strings"

	"fyredocs-cli/internal/config"
)

// Login stores an API key + optional base URL in the local
// config file.
//
//	fyredocs login --api-key fyr_live_... [--base-url https://...]
//
// We deliberately do NOT prompt for the key on stdin in v0 —
// passing a secret on argv is the standard pattern other CLIs
// use (e.g., `gh auth login --with-token`), and it's easier to
// script in CI. A future iteration can add a `--stdin` flag for
// secret-management hooks.
func Login(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	apiKey := fs.String("api-key", "", "API key (`fyr_…`) from /account/api-keys")
	baseURL := fs.String("base-url", "", "API base URL; defaults to "+config.DefaultBaseURL)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	key := strings.TrimSpace(*apiKey)
	if key == "" {
		errorf(ctx, "login: --api-key is required")
		return 2
	}
	cfg := &config.Config{APIKey: key, BaseURL: strings.TrimSpace(*baseURL)}
	if err := config.Save(cfg); err != nil {
		errorf(ctx, "login: save config: %v", err)
		return 1
	}
	infof(ctx, "Logged in.")
	return 0
}

// Logout removes the on-disk credentials. Idempotent: running it
// on a fresh machine exits 0 without complaining.
//
//	fyredocs logout
func Logout(ctx Ctx, _ []string) int {
	if err := config.Delete(); err != nil {
		errorf(ctx, "logout: %v", err)
		return 1
	}
	infof(ctx, "Logged out.")
	return 0
}

// IsNotLoggedIn lets the dispatcher tell "no credentials" apart
// from other config errors without re-importing the config
// package's sentinel. Exported so the dispatcher can pretty-print
// "run `fyredocs login` first" messages.
func IsNotLoggedIn(err error) bool {
	return errors.Is(err, config.ErrNotLoggedIn)
}
