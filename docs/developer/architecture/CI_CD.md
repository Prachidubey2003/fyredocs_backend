# CI/CD Architecture

This document describes the continuous integration and delivery pipelines for the Fyredocs backend. The frontend has its own equivalent in `fyredocs_frontend/.github/`.

## Goals

1. **Every PR is built and tested** — no merge without green CI.
2. **Per-service isolation** — each microservice is built, vetted, and tested independently in a matrix job, respecting the boundaries set in [CLAUDE.md](../../../CLAUDE.md) §1.
3. **Security baseline enforced in CI** — static analysis (gosec, staticcheck), vulnerability scan (govulncheck), secret scan (gitleaks), and image scan (Trivy) all run on every change.
4. **SBOM generated on every build** — required for SOC2 and HIPAA evidence.
5. **Reproducible Docker images** — pushed to GitHub Container Registry with deterministic tags.

## Workflows

All workflows live under [`.github/workflows/`](../../../.github/workflows/).

### `ci.yml` — pull request gate
Runs on every push and pull request to `main` / `develop`.

Jobs:
| Job | Purpose | Failure blocks merge? |
|---|---|---|
| `build-test` (matrix over 10 services) | `go build`, `go vet`, `go test -race -coverprofile` per service | yes |
| `lint` | `gofmt`, `staticcheck` | yes |
| `security` | `gosec` (SARIF → GitHub Security tab), `govulncheck`, `gitleaks` | gosec/gitleaks: yes; govulncheck: warn (logged) |
| `sbom` | `syft` SPDX SBOM uploaded as artifact (14-day retention) | no |
| `openapi` | Lints `docs/developer/swagger/openapi.yaml` against [`redocly.yaml`](../swagger/redocly.yaml), bundles to a single-file artifact, and renders standalone Redoc HTML | yes (lint failure) |

Per-service test coverage is uploaded as an artifact and can be combined for codecov-style reports later.

### `security.yml` — deep security scans
Runs on every push, every PR, and a weekly Monday 07:00 UTC cron (so we catch newly-disclosed vulnerabilities in unchanged code).

Jobs:
| Job | Purpose | Severity gate |
|---|---|---|
| `codeql` | GitHub's CodeQL with `security-extended` + `security-and-quality` query packs, against all 10 Go services | findings appear in the Security tab |
| `osv-scanner` | Google's OSV-Scanner against the OSV.dev database (broader than govulncheck — covers OS packages, npm pulled in by tooling, etc.) | SARIF upload, non-blocking |
| `scorecard` | OpenSSF Scorecard — measures repo health (branch protection, signed releases, dependency-update tooling, SAST coverage) | SARIF upload, non-blocking |

All three publish SARIFs with stable `category` values so the Security tab deduplicates findings across runs.

The companion vulnerability-disclosure policy lives at [`SECURITY.md`](../../../SECURITY.md). The secret-rotation runbook (one section per secret, with zero-downtime procedures where possible) lives at [`SECRETS.md`](SECRETS.md).

### `docker.yml` — image build & publish
Runs on push to `main` and on version tags (`v*.*.*`).

For each service:
1. Sets up QEMU + Buildx (multi-arch ready).
2. Logs into GHCR (`ghcr.io/<owner>/fyredocs-<service>`).
3. Builds the service-specific Dockerfile with the **repo root as build context** (required by the existing Dockerfiles which copy `go.work` + all `go.mod` files).
4. Pushes images tagged with: branch name, commit SHA, semver tag (if applicable), `latest` (only on default branch).
5. Runs Trivy on the published image and uploads SARIF to the GitHub Security tab.

GitHub Actions cache (`type=gha`) is scoped per service to maximize cache hits without cross-contamination.

### `dependabot.yml` — dependency updates
- Weekly `gomod` updates per service (grouped into a single PR per service).
- Weekly `docker` updates (base image bumps).
- Weekly `github-actions` updates.

## Concurrency

Each workflow uses `concurrency.group = <workflow>-<ref>` with `cancel-in-progress: true` on `ci.yml` (cheap) and `false` on `docker.yml` (we don't want to abort mid-publish).

## Branch protection (manual GitHub setting, not in code)

After this pipeline is merged, configure branch protection on `main`:
- Required status checks: `build-test (each service)`, `lint`, `security`.
- Require PR review.
- Disallow force-pushes.

## Local equivalents

Developers can reproduce CI locally:
```bash
# from fyredocs_backend/
./test.sh                          # all services
./test.sh -v api-gateway           # one service, verbose
make test                          # alternative test runner
gofmt -l .                         # formatting check
go vet ./api-gateway/...           # vet one service
go test -race ./shared/...         # race detector
```

## Adding a new service to CI

When you add a new service (e.g., `editor-service` in Phase 1):
1. Add it to the workspace ([`go.work`](../../../go.work)).
2. Add it to the service list in [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml) matrix.
3. Add it to [`.github/workflows/docker.yml`](../../../.github/workflows/docker.yml) matrix.
4. Add it to [`.github/dependabot.yml`](../../../.github/dependabot.yml).
5. Add it to [`test.sh`](../../../test.sh) and the [`Makefile`](../../../Makefile) `SERVICES` list.
6. Update [`README.md`](../../../README.md) per [CLAUDE.md](../../../CLAUDE.md) §5.1.

## SOC 2 mapping

CI/CD evidence directly supports the following SOC 2 controls:
- **CC7.1 / CC7.2** (system monitoring) — workflow runs are immutable evidence of every code change being tested.
- **CC8.1 / CC8.3** (change management + change testing) — every change goes through PR + required status checks.
- **CC6.6 / CC6.8** (logical access / unauthorized software) — gosec, govulncheck, Trivy.
- **CC2.1** (information security) — gitleaks blocks secret leakage.
- **CC4.1** (ongoing evaluation) — scheduled weekly cron in [`security.yml`](../../../.github/workflows/security.yml) re-scans unchanged code against the freshest vulnerability DBs.

Retain workflow run logs for ≥ 1 year (configured at repo settings level). The full criterion-by-criterion mapping with gap punch list is in [`SOC2_READINESS.md`](SOC2_READINESS.md).

## Future additions (when Phase 1+ services arrive)

- E2E suite (Playwright) wired as a separate `e2e.yml` workflow with ephemeral Postgres + Redis + NATS via service containers.
- Performance benchmark job comparing against a baseline (`criterion.rs`-style) — fail on >5% regression.
- Golden-PDF corpus check for the editor (SSIM ≥ 0.98 on unchanged pages) — see plan §5.10.
