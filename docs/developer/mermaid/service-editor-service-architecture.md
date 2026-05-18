# editor-service -- Architecture

Internal structure and component diagram of the `editor-service` (port `8090`).

## Component Diagram

```mermaid
graph TB
    subgraph editor[" editor-service :8090 "]
        direction TB

        subgraph Middleware["Middleware Chain (Gin)"]
            TRACE["OpenTelemetry · GinTraceMiddleware"]
            METRICS["Prometheus · GinMetricsMiddleware"]
            REQID["GinRequestID"]
            LOGGER["GinRequestLogger"]
        end

        subgraph Routes["Route Groups"]
            HEALTH["/healthz · /readyz · /metrics"]
            subgraph V1["/v1 (auth via X-User-ID)"]
                DOCS["POST/GET/DELETE /documents"]
                EDIT["POST /documents/:id/edit (501 — scaffold)"]
                REVS["GET /documents/:id/revisions"]
                COMMENTS["POST/GET /documents/:id/comments<br/>POST /documents/:id/comments/:cid/resolve"]
            end
        end

        subgraph Handlers["handlers/"]
            HD["documents.go"]
            HC["comments.go"]
            HA["auth.go (X-User-ID parser)"]
        end

        subgraph Models["internal/models/"]
            DM["Document"]
            RM["Revision"]
            CM["Comment"]
            DBPKG["database.go (Connect, Migrate, pool config)"]
        end
    end

    PG[("PostgreSQL<br/>documents · revisions · comments")]
    REDIS[("Redis<br/>optional; presence cache in Phase 2")]
    NATS[("NATS JetStream<br/>produces EDIT_EVENTS")]
    FS[("/files/<br/>Yjs blobs + PDF patch bytes")]
    OTEL["OTel + Prometheus<br/>+ Loki + Grafana"]

    Middleware --> Routes
    Routes --> Handlers
    Handlers --> Models
    Models --> PG
    Handlers -. presence/idempotency (Phase 2) .-> REDIS
    Handlers -. emits (follow-up) .-> NATS
    Models -. storage_key / pdf_patch_key .-> FS
    Middleware --> OTEL
```

## Key packages

| Package | Responsibility |
|---|---|
| `main` | Service entrypoint — config → logger → telemetry → DB → NATS → Redis → gin → SIGTERM drain. |
| `handlers` | HTTP request handlers using the shared envelope; auth via `X-User-ID` header. |
| `internal/models` | GORM types (`Document`, `Revision`, `Comment`), DB connection + migrations + pool config. |
| `routes` | gin route table; isolates wiring from main so it can be exercised in tests without a real server. |

## Where things live (dataflow)

| Concept | Persistence |
|---|---|
| Document metadata | Postgres `documents` |
| Revision metadata + commit message | Postgres `revisions` |
| Yjs CRDT update bytes (per revision) | `/files/users/{user_id}/jobs/{doc_id}/revisions/{rev_id}.yjs` (see [STORAGE.md](../architecture/STORAGE.md) §4.4.3) |
| Incremental PDF patch bytes | `/files/.../edits/{rev_id}.delta` |
| Comments + anchor JSON | Postgres `comments` (anchor is opaque JSONB) |
| Index updates (Phase 2) | Meilisearch via `EDIT_EVENTS` subscriber |
| Presence + idempotency cache (Phase 2) | Redis |

## Compliance notes

- **Microservice boundaries** ([CLAUDE.md](../../../CLAUDE.md) §1) — only imports `fyredocs/shared/*`; no cross-service Go imports.
- **Standard response envelope** ([CLAUDE.md](../../../CLAUDE.md) §7) — all HTTP responses use `shared/response`.
- **Own DB schema** ([CLAUDE.md](../../../CLAUDE.md) §3) — `documents`, `revisions`, `comments` are owned by editor-service; other services must call the REST API.
- **Auth defence-in-depth** — local JWT verifier ported from job-service into [`internal/authverify/`](../../../editor-service/internal/authverify/); every `/v1/*` route is gated by the gin auth middleware (verifies signature with dual-key support, checks denylist, populates auth context).
