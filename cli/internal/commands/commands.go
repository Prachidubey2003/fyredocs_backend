// Package commands implements the individual `fyredocs` subcommands.
//
// Each command is a function with the signature
// `func(ctx Ctx, args []string) int` — return value is the exit
// code (0 = success). Ctx carries the shared dependencies: IO
// writers (so tests can capture output) and a Client factory (so
// tests can inject a mock).
//
// We hand-roll the subcommand router rather than pulling in
// Cobra/spf13 — the surface is small (≤ 10 commands), Go's
// `flag` package handles the per-command parsing, and avoiding
// the dep means the produced binary is ~3MB instead of ~12MB.
package commands

import (
	"fmt"
	"io"

	"fyredocs-cli/internal/client"
	"fyredocs-cli/internal/config"
)

// Ctx carries the injection points each command needs.
type Ctx struct {
	Stdout io.Writer
	Stderr io.Writer
	// NewClient builds the API client used by the command.
	// Tests override it to point at an httptest server; the
	// production wiring loads from disk via DefaultNewClient.
	NewClient func() (*client.Client, error)
}

// DefaultNewClient is the production NewClient factory. It loads
// the config file from disk and constructs a Client with the
// stored API key.
func DefaultNewClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return client.New(cfg.BaseURL, cfg.APIKey), nil
}

// printf is a tiny stderr-aware fprintf — saves typing across
// commands.
func errorf(ctx Ctx, format string, args ...any) {
	fmt.Fprintf(ctx.Stderr, format+"\n", args...)
}

func infof(ctx Ctx, format string, args ...any) {
	fmt.Fprintf(ctx.Stdout, format+"\n", args...)
}
