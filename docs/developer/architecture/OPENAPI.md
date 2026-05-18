# OpenAPI 3.1 — single source of truth

## Where the spec lives

- **[../swagger/openapi.yaml](../swagger/openapi.yaml)** — the authoritative OpenAPI 3.1.0 spec. Hand-maintained.
- **[../swagger/redocly.yaml](../swagger/redocly.yaml)** — Redocly lint config (rules tuned to this API's actual shape: cookie auth, guest tokens, envelope-based errors).
- **`openapi.bundled.yaml`** + **`openapi.html`** — generated on every CI run, uploaded as the `openapi` artifact. **Not committed** (see [`.gitignore`](../../../.gitignore)).

## Why it matters

The spec is the contract that mobile (`fyredocs_mobile`), generated SDKs (TS / Python / Go), the embeddable editor, partner integrations (Zapier), and the API gateway's request-validation layer all consume. Any drift between code and spec breaks downstream consumers silently.

## The maintenance contract

Per [CLAUDE.md](../../../CLAUDE.md) §5.5 and §8: **every change to a public API endpoint MUST update the spec in the same change**. There is no "I'll do the spec later" — incomplete docs is an incomplete task.

Concretely, when you touch routes / handlers / request bodies / response shapes:

1. Update [`openapi.yaml`](../swagger/openapi.yaml).
2. Run the lint locally:
   ```bash
   cd docs/developer/swagger
   npx --yes @redocly/cli@1 lint openapi.yaml
   ```
3. Render the docs locally to sanity-check:
   ```bash
   npx --yes @redocly/cli@1 build-docs openapi.yaml -o /tmp/openapi.html
   open /tmp/openapi.html
   ```
4. Update the matching markdown under [../api/](../api/) (`AUTH_API.md`, `JOBS_API.md`, etc.) and the [`../../../Fyredocs_API.postman_collection.json`](../../../Fyredocs_API.postman_collection.json) Postman collection.

CI re-runs the lint on every PR — drift fails the build.

## Why this is hand-maintained and not auto-generated

Auto-generation from Go code would require either:

- Annotation-based scanners (swaggo) that turn handler comments into spec — fragile, opinionated, hard to express envelope responses and cookie-based auth.
- Reflection-based generators that miss anything not statically expressible (validation tags, response transformations, mixed bearer/cookie/guest paths).

The current footprint is small enough (≈ 40 public operations) that a hand-maintained 3.1 spec wins on accuracy, reviewability, and SDK-generation quality. When the operation count grows (~ 100+), revisit this trade-off — possible options at that point: ogen, oapi-codegen with `--strict`, or annotation tooling.

## Rule configuration ([`redocly.yaml`](../swagger/redocly.yaml))

| Rule | Severity | Why |
|---|---|---|
| `no-unresolved-refs` | error | Real correctness — broken refs ship broken SDKs. |
| `operation-operationId-unique` / `operation-operationId-url-safe` | error | Required for SDK / codegen tools. |
| `operation-2xx-response` | error | Every endpoint must declare a success shape. |
| `path-params-defined` | error | Catches `/users/{id}` typos. |
| `operation-4xx-response` | warn | Errors are documented globally via the envelope; per-endpoint 4xx examples are optional. |
| `info-license` / `info-license-url` | off | License is repo-level metadata. |
| `security-defined` | off | We declare `security:` per protected operation; the rule's strict mode misreads cookie-based + guest auth. Revisit if we move to per-operation explicit `security: []` for public routes. |
| `no-server-example.com` | off | The `localhost:8080` dev server is intentional. |

If a real rule needs tuning, edit [`redocly.yaml`](../swagger/redocly.yaml) — do not introduce per-line ignore comments.

## CI surface

The `openapi` job in [`../../../.github/workflows/ci.yml`](../../../.github/workflows/ci.yml):

1. `npx --yes @redocly/cli@1 lint openapi.yaml` — fails the build on any error-level rule.
2. `npx --yes @redocly/cli@1 bundle openapi.yaml -o openapi.bundled.yaml` — produces a dereferenced single-file spec suitable for SDK generators that don't follow refs.
3. `npx --yes @redocly/cli@1 build-docs openapi.yaml -o openapi.html` — generates the standalone Redoc HTML.
4. Uploads both as the `openapi` artifact (14-day retention).

## When to add multi-API support

Today there is a single `apis:` entry in [`redocly.yaml`](../swagger/redocly.yaml). When new services land (`editor-service`, `collab-service`, `workflow-service`, `notify-service`, `billing-service`, eventually `ai-service`), each owns its own bounded API contract per [CLAUDE.md](../../../CLAUDE.md) §3. Two viable patterns:

- **Pattern A — one root spec, internal `$ref` to per-service files.** Keep `openapi.yaml` as the public surface and split it into `openapi.<service>.yaml` files referenced by tag. SDK consumers see a unified API.
- **Pattern B — one spec per service.** Each new service ships `docs/developer/swagger/<service>.openapi.yaml`. The Redocly config gets an additional `apis:` entry. SDK consumers pick the service they need.

Default to Pattern A while the gateway is the only public ingress. Switch to Pattern B if a service ever exposes itself directly (e.g., `collab-service` websockets on its own subdomain).

## SDK generation (Phase 4 — not implemented yet)

When the spec is ready to drive SDKs:

- **TypeScript**: `openapi-typescript-codegen` or `openapi-fetch` (lighter).
- **Python**: `openapi-python-client`.
- **Go**: `oapi-codegen` (matches the project ecosystem).

A `phase-4/generate-sdks.sh` script should consume `openapi.bundled.yaml` from CI and push generated client packages to per-language registries.
