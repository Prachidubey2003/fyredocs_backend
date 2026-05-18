package commands

import (
	"flag"
	"net/url"
	"strings"
)

// apiKey mirrors the auth-service shape we care about. Hand-rolled
// (not imported from the SDK) because the CLI module is a
// standalone Go module by design — no cross-imports into the
// backend tree.
type apiKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	KeyPrefix   string `json:"keyPrefix"`
	CreatedAt   string `json:"createdAt"`
	LastUsedAt  string `json:"lastUsedAt,omitempty"`
}

// Keys is the subcommand router for the `keys` family:
//   - `fyredocs keys list`
//   - `fyredocs keys create --name "CI" [--environment live|test]`
//   - `fyredocs keys revoke <id>`
//
// The "router" is a 4-case switch because the surface is tiny.
// Each leaf function takes a fresh args slice (after the
// subcommand has been peeled off).
func Keys(ctx Ctx, args []string) int {
	if len(args) == 0 {
		errorf(ctx, "keys: missing subcommand. Use one of: list, create, revoke")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return keysList(ctx, rest)
	case "create":
		return keysCreate(ctx, rest)
	case "revoke":
		return keysRevoke(ctx, rest)
	default:
		errorf(ctx, "keys: unknown subcommand %q", sub)
		return 2
	}
}

func keysList(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	revoked := fs.Bool("revoked", false, "List revoked keys (audit archive) instead of active keys")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "keys list: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "keys list: %v", err)
		return 1
	}
	q := url.Values{}
	if *revoked {
		q.Set("revoked", "true")
	}
	var keys []apiKey
	if err := c.Do("GET", "/auth/api-keys", q, nil, &keys); err != nil {
		errorf(ctx, "keys list: %v", err)
		return 1
	}
	if len(keys) == 0 {
		infof(ctx, "No keys.")
		return 0
	}
	const headerFmt = "%-36s  %-20s  %-5s  %-20s  %s"
	infof(ctx, headerFmt, "ID", "NAME", "ENV", "PREFIX", "CREATED")
	infof(ctx, headerFmt, strings.Repeat("-", 36), strings.Repeat("-", 20), "-----",
		strings.Repeat("-", 20), strings.Repeat("-", 19))
	for _, k := range keys {
		infof(ctx, headerFmt, k.ID, truncate(k.Name, 20), k.Environment, k.KeyPrefix, k.CreatedAt)
	}
	return 0
}

func keysCreate(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	name := fs.String("name", "", "Human-readable name (required)")
	env := fs.String("environment", "live", "`live` or `test`")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		errorf(ctx, "keys create: --name is required")
		return 2
	}
	if *env != "live" && *env != "test" {
		errorf(ctx, "keys create: --environment must be live or test (got %q)", *env)
		return 2
	}
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "keys create: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "keys create: %v", err)
		return 1
	}
	body := struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
	}{Name: strings.TrimSpace(*name), Environment: *env}
	var resp struct {
		Key       apiKey `json:"key"`
		Plaintext string `json:"plaintext"`
	}
	if err := c.Do("POST", "/auth/api-keys", nil, body, &resp); err != nil {
		errorf(ctx, "keys create: %v", err)
		return 1
	}
	infof(ctx, "Created %s (%s)", resp.Key.Name, resp.Key.ID)
	infof(ctx, "")
	infof(ctx, "  %s", resp.Plaintext)
	infof(ctx, "")
	infof(ctx, "This is the ONLY time the secret is shown — save it now.")
	return 0
}

func keysRevoke(ctx Ctx, args []string) int {
	fs := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
	fs.SetOutput(ctx.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		errorf(ctx, "keys revoke: expected exactly one argument <key-id>")
		return 2
	}
	id := fs.Arg(0)
	c, err := ctx.NewClient()
	if err != nil {
		if IsNotLoggedIn(err) {
			errorf(ctx, "keys revoke: not logged in. Run `fyredocs login` first.")
			return 1
		}
		errorf(ctx, "keys revoke: %v", err)
		return 1
	}
	if err := c.Do("POST", "/auth/api-keys/"+url.PathEscape(id)+"/revoke", nil, nil, nil); err != nil {
		errorf(ctx, "keys revoke: %v", err)
		return 1
	}
	infof(ctx, "Revoked %s.", id)
	return 0
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
