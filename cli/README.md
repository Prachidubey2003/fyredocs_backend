# fyredocs (CLI)

Command-line interface for the Fyredocs API. Build it once, point it at a key, and you have shell access to every account-management operation the SDK exposes.

## Install

### Pre-built binary (recommended)

Each tagged `cli/v*` release on GitHub publishes archives for darwin-amd64, darwin-arm64, linux-amd64, linux-arm64, and windows-amd64, plus a `SHA256SUMS.txt` manifest. Grab one from the [releases page](https://github.com/fyredocs/fyredocs/releases?q=cli) and unpack:

```bash
# macOS / Linux — replace <ver>, <os>, <arch> with the values for your machine
curl -LO https://github.com/fyredocs/fyredocs/releases/download/cli/<ver>/fyredocs-<ver>-<os>-<arch>.tar.gz
tar -xzf fyredocs-<ver>-<os>-<arch>.tar.gz
sudo mv fyredocs /usr/local/bin/
fyredocs version
```

Windows: download the `windows-amd64.zip`, unzip `fyredocs.exe`, drop it on `%PATH%`.

The release artifacts are produced by [`.github/workflows/cli-release.yml`](../.github/workflows/cli-release.yml). Each tagged build runs a smoke-test that boots the linux-amd64 binary and asserts `fyredocs version` matches the tag — a broken `-ldflags` value fails CI before the release lands.

### From source

```bash
# from this repo, build a static binary:
cd cli
go build -trimpath -ldflags="-s -w -X main.buildVersion=dev-$(git rev-parse --short HEAD)" -o fyredocs .
mv fyredocs /usr/local/bin/
```

## Quick start

```bash
# Store a key (from /account/api-keys in the web app)
fyredocs login --api-key fyr_live_abc_secret

# Confirm credentials work
fyredocs whoami

# See current-period usage
fyredocs usage

# Manage keys
fyredocs keys list
fyredocs keys create --name "GitHub Actions" --environment live
fyredocs keys revoke <id>
```

## Configuration

The CLI stores credentials at `$XDG_CONFIG_HOME/fyredocs/config.json` (typically `~/.config/fyredocs/config.json`). The file is written with `0600` permissions because the API key is a long-lived secret.

```json
{
  "apiKey": "fyr_live_…",
  "baseUrl": "https://api.fyredocs.com"
}
```

Override the API endpoint for self-hosted or staging deployments:

```bash
fyredocs login --api-key fyr_live_abc --base-url https://api.staging.fyredocs.example
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Operational failure (auth, network, server returned 4xx/5xx) |
| 2 | Usage error (missing flag, bad subcommand) |

## Commands

| Command | Description |
|---|---|
| `login --api-key fyr_…` | Persist credentials. |
| `logout` | Remove the stored config (idempotent). |
| `whoami` | Print plan name + subscription status. |
| `usage [--period YYYY-MM]` | Tabulate the current-period rollup. |
| `keys list [--revoked]` | List active keys, or pass `--revoked` for the audit archive. |
| `keys create --name "…" [--environment live\|test]` | Mint a new key. The plaintext is printed **once**. |
| `keys revoke <id>` | Revoke a key by ID (idempotent at the server). |
| `documents list [--limit N] [--page N]` | Newest-first table of your documents. |
| `documents get <id>` | Print one document's metadata as pretty JSON (pipe to `jq`). |
| `documents revisions <id>` | List revisions for a document, oldest → newest at the server. |
| `documents download [--rev <revId>] [-o <path>] <id>` | Stream PDF bytes for the current or a specific revision. Defaults to stdout. |
| `documents edit --ops-file <path> <id>` | Apply a JSON array of sPDOM ops (read from a file or `-` for stdin) as one revision. Prints the new `revId`. |
| `documents delete --yes <id>` | Soft-delete a document. `--yes` is required for non-interactive safety. |
| `version` | Print the binary version. |
| `help` | Show the command list. |

The `docs` alias (`fyredocs docs list`, etc.) works everywhere `documents` does.

### `documents edit` example

```bash
# ops.json
cat > ops.json <<'EOF'
[
  {"type": "page.rotate", "page": 1, "rotation": 90},
  {"type": "annotation.add", "page": 1, "kind": "highlight",
   "rect": [50, 100, 300, 120]}
]
EOF

fyredocs documents edit --ops-file ops.json doc_01HV…
# → rev_01HW…
```

The ops shape mirrors `POST /api/editor/v1/documents/:id/edit`; the CLI just wraps the array in `{"ops":[…]}` and posts it. See [editor-service docs](../docs/developer/services/EDITOR_SERVICE.md) for the full op grammar. Empty arrays and malformed JSON are rejected before the request hits the server.

## Versioning

The binary version is overridden at link time via `-ldflags="-X main.buildVersion=v0.1.0"`. The unreleased dev build self-identifies as `dev`.
