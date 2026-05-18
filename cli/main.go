// Command fyredocs is the official CLI for the Fyredocs API.
//
// Run `fyredocs` with no args to see the command list. The CLI
// reads its credentials from $XDG_CONFIG_HOME/fyredocs/config.json
// (created by `fyredocs login`); pass --base-url to point at a
// non-production deployment.
//
// Versioning policy: the major version of the binary matches the
// API contract version. The buildVersion var below is overridden
// via -ldflags="-X main.buildVersion=v0.1.0" in CI release builds.
package main

import (
	"fmt"
	"os"

	"fyredocs-cli/internal/commands"
)

// buildVersion is overridden at link time. Default "dev" is what
// `go run .` shows so developers know they're on an unreleased
// binary.
var buildVersion = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. main() is just a thin os.Exit
// wrapper so test code can drive every flow without spawning a
// subprocess.
func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Fprintln(stdout, buildVersion)
		return 0
	}

	ctx := commands.Ctx{
		Stdout:    stdout,
		Stderr:    stderr,
		NewClient: commands.DefaultNewClient,
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "login":
		return commands.Login(ctx, rest)
	case "logout":
		return commands.Logout(ctx, rest)
	case "whoami":
		return commands.Whoami(ctx, rest)
	case "usage":
		return commands.Usage(ctx, rest)
	case "keys":
		return commands.Keys(ctx, rest)
	case "documents", "docs":
		return commands.Documents(ctx, rest)
	default:
		fmt.Fprintf(stderr, "fyredocs: unknown command %q\n\n", sub)
		printHelp(stderr)
		return 2
	}
}

func printHelp(w *os.File) {
	fmt.Fprintln(w, `fyredocs — the Fyredocs API CLI

USAGE:
  fyredocs <command> [flags]

COMMANDS:
  login --api-key fyr_...       Store API credentials
  logout                        Remove stored credentials
  whoami                        Show current plan + subscription
  usage [--period YYYY-MM]      Show current-period usage rollup
  keys list [--revoked]         List API keys
  keys create --name "..."      Mint a new API key
  keys revoke <id>              Revoke a key
  documents list                List your documents (newest first)
  documents get <id>            Print a document's metadata as JSON
  documents revisions <id>      List revisions for a document
  documents download [--rev <revId>] [-o <path>] <id>
                                Download bytes (current or a prior revision)
  documents edit --ops-file ops.json <id>
                                Apply a JSON array of sPDOM ops as one revision
  documents delete --yes <id>   Soft-delete a document (--yes is required)
  version                       Print CLI version
  help                          Show this message

DOCS:
  https://fyredocs.com/docs/cli`)
}
