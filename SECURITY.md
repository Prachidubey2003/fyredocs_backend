# Security Policy — Fyredocs Backend

We take security of the Fyredocs platform seriously. This document describes how to report a vulnerability, our response process, and the supported branches.

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Email **security@fyredocs.com** (alias: shivam.dubey@vcommission.com while the dedicated alias is being provisioned) with:

1. A clear description of the issue.
2. Steps to reproduce (proof-of-concept code or HTTP transcript welcome).
3. The affected commit SHA / version / Docker image tag.
4. The impact you observed and any caveats.
5. Whether you have already disclosed this to a third party.

If you prefer encrypted communication, request a PGP key in your initial email — we will respond with a key fingerprint.

You can also use GitHub's [Private Security Advisories](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing/privately-reporting-a-security-vulnerability) feature on this repository (the "Report a vulnerability" button on the *Security* tab).

## What to expect

| Step | Target time |
|---|---|
| Acknowledgement of your report | **within 2 business days** |
| Initial triage and severity assignment (CVSS v3.1) | within 5 business days |
| Status update cadence during investigation | every 7 days |
| Coordinated disclosure window for confirmed issues | up to 90 days from acknowledgement, negotiable |

We follow [responsible disclosure](https://en.wikipedia.org/wiki/Coordinated_vulnerability_disclosure). We will credit reporters in the release notes for the fix unless you ask us not to.

## Scope

### In scope

- Code under this repository (any service inside `fyredocs_backend/`).
- The published Docker images (`ghcr.io/.../fyredocs-*`).
- The OpenAPI 3.1 surface defined in [`docs/developer/swagger/openapi.yaml`](docs/developer/swagger/openapi.yaml).
- Authentication / authorization logic in [`auth-service/`](auth-service/) and the verifier libraries each service embeds.
- File-handling code in [`job-service/`](job-service/), the worker services, and [`shared/storage/`](shared/storage/).

### Out of scope

- Third-party dependencies (please report directly to upstream; we will track via [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) and Dependabot).
- Findings that require root access on the host, an already-compromised account, or trivial DoS via volumetric flooding without a logic bug.
- Reports generated solely by automated scanners with no demonstrated impact.
- Social-engineering, phishing, or physical attacks against staff.
- Vulnerabilities in test fixtures, example documents, or development-only files (anything under `*_test.go`, `docs/developer/api/`, or `.env.example`).

## Safe-harbor

We will not pursue legal action against researchers who:

- Make a good-faith effort to follow this policy.
- Do not exfiltrate or destroy data.
- Do not interrupt service for other users.
- Give us a reasonable opportunity to remediate before public disclosure.

## Supported versions

| Branch | Supported |
|---|---|
| `main` | ✅ — receives security patches |
| Tagged releases ≤ 6 months old | ✅ — receives security patches |
| Older tags | ❌ — upgrade |

## Defensive baseline (what's in CI)

Every push and PR runs through these gates (defined in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) and [`.github/workflows/security.yml`](.github/workflows/security.yml)):

- **gosec** — Go SAST, SARIF uploaded to the GitHub Security tab.
- **govulncheck** — Go vulnerability database checked per service.
- **CodeQL** — GitHub's default-CodeQL queries for Go + JavaScript.
- **OSV-Scanner** — Open-source vulnerability database (broader than govulncheck).
- **gitleaks** — secret scanning across the full git history.
- **Trivy** — image scanning runs on every Docker build ([`.github/workflows/docker.yml`](.github/workflows/docker.yml)).
- **Dependabot** — weekly grouped updates for Go modules, Docker base images, and GitHub Actions.
- **SBOM (syft)** — SPDX SBOM uploaded as a 14-day artifact on every CI run.

See [`docs/developer/architecture/SECRETS.md`](docs/developer/architecture/SECRETS.md) for the secret-rotation runbook, [`docs/developer/architecture/STORAGE.md`](docs/developer/architecture/STORAGE.md) for the storage-integrity and backup model, and [`docs/developer/architecture/SOC2_READINESS.md`](docs/developer/architecture/SOC2_READINESS.md) for our SOC 2 control mapping and gap analysis.

## Acknowledgements

The names below are reporters who responsibly disclosed issues. Thank you.

<!-- Add to this list as advisories are published. Format: - Name (issue ID, CVE if assigned) -->

_None yet._
