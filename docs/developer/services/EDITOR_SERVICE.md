# editor-service

The editor-service owns the **semantic PDF Document Object Model (sPDOM)** and the **revision / comment / form** persistence for the Fyredocs editor. It is the Phase 1 anchor service from the [product plan §4.3.1](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md) — every editor feature (text edits, annotations, form fields, comments, version history, search indexing) lives here.

| Property | Value |
|---|---|
| Port | `8090` |
| Framework | gin (REST) |
| DB | PostgreSQL (own schema, owned tables: `documents`, `revisions`, `comments`) |
| Cache | Redis (optional today; presence + idempotency cache lands in Phase 2) |
| Events | NATS JetStream — produces `EDIT_EVENTS`; consumes nothing today |
| Storage | local `/files/` per [STORAGE.md](../architecture/STORAGE.md) |
| Status | **v0 live** — `POST /v1/documents/{id}/edit` ships every roadmap sPDOM op: `page.rotate`, `page.delete`, `page.insert`, `annotation.add` (kinds: highlight, underline, strikeout, squiggly, square, sticky, freehand, callout), `text.replace` (single Tj literal, uncompressed content stream), `redact.apply` (with v2 per-item TJ surgery), `table.cell.edit` (rect-targeted scrub + replace), `text.insert` (append `BT … Td (text) Tj ET` at a caller-supplied anchor in a caller-supplied resource font), and `text.delete` (rect-targeted scrub without the redact overlay). Layout follow-ups still tracked separately. |

---

## Service responsibility

Owns these bounded contexts (per plan §4.3.1, including capabilities folded in from the eliminated `form-service` and `search-service`):

1. **sPDOM operations** — text replace/insert/delete, annotations, page-level mutations, table-cell edits, redactions. v0 ships the full Phase-1 wire surface: `page.rotate`, `page.delete`, `page.insert`, `annotation.add` (eight kinds), `text.replace`, `text.insert`, `text.delete`, `redact.apply`, and `table.cell.edit`.
2. **Revisions** — append-only history of edits. Each revision stores Yjs CRDT update bytes + an optional incremental-PDF patch reference under `/files/`.
3. **Comments** — span-anchored / region-anchored comment threads with resolve.
4. **Forms (AcroForm CRUD)** — absorbed from the cancelled `form-service` (plan §4.3.2). Same data model, lives here.
5. **Document indexing** — Phase 2: a background goroutine emits Meilisearch index updates on `EDIT_EVENTS`.

Out of scope (owned elsewhere):

- Real-time collaboration websockets — `collab-service` (Phase 2).
- File bytes on disk — workers already write to `/files/`; editor-service references storage keys but does not own the bytes.
- AI features over documents — `ai-service` (Phase 3 / deferred per `TODO-AI`).

## Design constraints

- **Microservice boundary** — does not import code from any other service ([CLAUDE.md §1](../../../CLAUDE.md)). The only shared imports are `fyredocs/shared/*` utility packages.
- **Own DB models** — `Document`, `Revision`, `Comment` are defined here ([internal/models/document.go](../../../editor-service/internal/models/document.go)). Other services that reference documents (e.g., `collab-service` once it lands) must call the REST API, not share Go types.
- **Defense-in-depth auth** — editor-service runs its own JWT verifier ([internal/authverify/](../../../editor-service/internal/authverify/), ported from job-service). Every `/v1/*` request is gated by the authverify gin middleware which (a) verifies the JWT signature and claims with dual-key fallback (see [SECRETS.md §3](../architecture/SECRETS.md)), (b) populates a verified `AuthContext` on the request context, and (c) optionally checks the JWT denylist via Redis. Handlers read `authverify.GetGinAuth(c)` rather than trusting the raw `X-User-ID` header.
- **PDF byte mutations live in [`internal/pdfwriter`](../../../editor-service/internal/pdfwriter/doc.go)** — a focused L1 primitive that takes (objNum → object body) replacements/insertions and emits append-only incremental updates per ISO 32000-1 §7.5.6. Original bytes are preserved verbatim across every update, so first-revision signatures stay valid. The writer reads both classic xref tables and stream-form xref objects (`/Type /XRef`, §7.5.8), and emits a MATCHING form on output: classic-form prior → classic incremental, stream-form prior → `/Type /XRef` incremental object with `/W [1 4 2]` field widths and packed `/Index` subsections. Mixing forms is legal but flagged by strict preflight; matching keeps strict validators happy. The plan §5.3 still names Rust as the long-term home for this code (for WASM client parity); the Go v0 lets us ship server-side editing today and swap engines later behind the same API.

## Internal architecture

```
editor-service/
├── main.go                  # entrypoint: config → logger → telemetry → DB → NATS → Redis → gin
├── handlers/
│   ├── auth.go              # X-User-ID extraction, requireUser helper
│   ├── documents.go         # POST/GET/DELETE /v1/documents, EditDocument, ListRevisions
│   ├── documents_test.go
│   ├── comments.go          # AddComment, ListComments, ResolveComment
│   ├── comments_test.go
│   ├── spdom.go             # GET /v1/documents/:id/spdom — runs the parser
│   ├── spdom_test.go
│   ├── download.go          # GET /v1/documents/:id/download + /revisions/:revId/download
│   ├── download_test.go
│   └── storage.go           # StorageDir + resolveStoragePath (path-traversal-guarded)
├── internal/
│   ├── authverify/          # JWT verifier (ported from job-service) — dual-key fallback
│   ├── models/
│   │   ├── database.go      # Connect + Migrate; pool tuning per service
│   │   ├── document.go      # Document, Revision, Comment GORM models
│   │   └── document_test.go
│   ├── pdfwriter/           # L1 incremental-update writer — append-only PDF mutations
│   │   ├── doc.go           # package docs: ISO 32000-1 §7.5.6, scope, boundaries
│   │   ├── reader.go        # Discover() — parses trailer/startxref/Size/Root/Info
│   │   ├── writer.go        # Update.Set + Update.Bytes — emits append section
│   │   ├── writer_test.go   # invariant + subsection-coalescing + /Prev chain tests
│   │   └── roundtrip_test.go# proves output is parseable by pdfcpu + sPDOM
│   ├── pdfedit/             # op → object-mutation translators (sits on pdfwriter)
│   │   ├── doc.go           # package docs: layering, what does + doesn't live here
│   │   ├── rotate.go        # RotatePage(pdf, pageNum, degrees) — first op wired E2E
│   │   ├── rotate_test.go   # rotation round-trips through pdfcpu + sPDOM
│   │   ├── annotation.go    # AddAnnotation(pdf, kind, page, rect, color?, contents?) — highlight/underline/strikeout/squiggly/square
│   │   ├── annotation_test.go # per-kind round-trip + stacking on top of rotation
│   │   ├── page.go          # DeletePage(pdf, pageNum) + InsertBlankPage(pdf, afterPage)
│   │   └── page_test.go     # delete + insert at each position, chained-with-rotate
│   ├── editops/             # wire-format dispatch (sPDOM op JSON → pdfedit calls)
│   │   ├── doc.go           # package docs: layering + v0 scope (1 op per request)
│   │   ├── apply.go         # Apply(ops, pdf) + sentinel errors mapped to 400/500
│   │   └── apply_test.go    # per-op-type validation + classification
│   └── spdom/               # sPDOM parser/builder (L1→L4) — see RENDERING.md
├── routes/
│   ├── routes.go            # SetupRouter — /healthz, /readyz, /v1/* group registrations
│   └── routes_test.go
├── go.mod
├── go.sum
└── Dockerfile               # multi-stage golang:1.25-alpine → scratch
```

## Routes

All routes accept `X-User-ID` (set by api-gateway after JWT verification). Requests without it receive `401 UNAUTHENTICATED`.

### Infrastructure

| Method | Path | Purpose | Status |
|---|---|---|---|
| GET | `/healthz` | Liveness (no deps) | live |
| GET | `/readyz` | DB + Redis check; 200 ready / 503 not | live |
| GET | `/metrics` | Prometheus scrape | live |

### Documents

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/documents` | Create a document record pointing at a pre-uploaded file |
| GET | `/v1/documents` | List the caller's documents (paginated; `?page=&limit=`) |
| GET | `/v1/documents/{id}` | Get one document |
| DELETE | `/v1/documents/{id}` | Soft-delete (sets `status='deleted'`) |
| POST | `/v1/documents/{id}/edit` | Apply 1–64 sPDOM ops as one Revision. Supported types: `page.rotate` (`{page, rotation}`), `page.delete` (`{page}`), `page.insert` (`{afterPage}` — 0 inserts before the first page), `annotation.add` (`{page, kind: highlight\|underline\|strikeout\|squiggly\|square\|sticky\|freehand\|callout, rect:[x0,y0,x1,y1], color?:[r,g,b], contents?:string, strokes?, anchor?}`), `text.replace` (`{page, find, replace}`), `text.insert` (`{page, x, y, text, font, sizePt}` — appends a fresh `BT /<font> <sizePt> Tf <x> <y> Td (<text>) Tj ET` block; font resource must already exist on the page), `text.delete` (`{page, rect}` — same scrub as `redact.apply` but no black overlay), `redact.apply` (`{page, rect}`), and `table.cell.edit` (TWO addressing forms: `{page, rect, text}` scrubs every text-show op whose glyph bbox overlaps `rect`, OR `{page, region, row, col, text}` runs `spdom.DetectTableGrid` over `region` and snaps to the matching cell — rect form wins when both are supplied. Either form then re-emits `text` at the original anchor in the original font with multi-line wrapping when the new text overflows the cell width). Ops run in order, each chaining onto the prior op's bytes; the request produces one Revision row with N stacked incremental sections. Reads the document bytes, runs them through `editops.Apply` → `pdfedit` → `pdfwriter`, writes the new revision to `users/<uid>/docs/<docId>/edits/<revId>.pdf` under [StorageDir](#storage), persists a `Revision`, and bumps `Document.CurrentRevID`. Errors past the first op carry an `ops[i]:` prefix. Unknown op type or annotation kind → 400 `INVALID_OP`. Bad args / garbage PDF / deleting the last page / empty or over-limit request / empty cell / empty delete rect → 400 `INVALID_INPUT`. |
| GET | `/v1/documents/{id}/download` | Stream the document's current PDF bytes as `application/pdf` with `Content-Disposition: attachment`. Serves the latest revision if any edits have been applied, otherwise the original upload. Range requests + `If-Modified-Since` honoured. 401 if unauthed; 404 if not found / not owned / file missing from storage. |
| GET | `/v1/documents/{id}/revisions` | List revisions for a document |
| GET | `/v1/documents/{id}/revisions/{revId}/download` | Stream a specific revision's PDF bytes. Caller must own the parent document and the revision must belong to it (a foreign revId returns 404 — by design, no leakage of revision ownership). |
| POST | `/v1/documents/{id}/revisions/{revId}/restore` | Creates a NEW revision whose bytes copy `revId`'s bytes and points `Document.CurrentRevID` at it. Restore is additive — the target revision and every other historical entry remain accessible. Same ownership rules as download. |
| GET | `/v1/documents/{id}/spdom` | Parse the stored PDF into the semantic Document Object Model. The parser walks each page's content stream and tries the position-aware path first (LayoutPassFull=2 — real BBoxes per Block/Line/Run, computed from a PDF text-state machine that tracks Tm/Td/TD/T\*/Tf/TL/Tc/Tw plus the Tj/'/"/TJ show operators). Pages that use rotated or skewed text matrices fall back to LayoutPassText=1 (text only, mediabox-sized block bbox). Pages with no resolvable /Contents stay at LayoutPassGeometry=0. The `Page.layoutPass` field always tells the consumer which fields are trustworthy. |

### Comments

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/documents/{id}/comments` | Add a comment (requires `revId`, opaque `anchor` JSON, `body`; optional `parentCommentId` for a reply) |
| GET | `/v1/documents/{id}/comments` | List comments (`?resolved=true|false` optional) |
| POST | `/v1/documents/{id}/comments/{commentId}/resolve` | Mark resolved |

### Internal endpoints (`/internal/v1/*`)

These bypass the auth middleware and are NOT proxied by api-gateway. Trust boundary is the cluster network. Today only collab-service calls them.

| Method | Path | Purpose |
|---|---|---|
| GET | `/internal/v1/snapshots/{docID}` | Stream the most recent Yjs checkpoint bytes for the document. Returns the latest Revision row with `yjs_update_key` set; 404 if none exists or the file is missing. Bytes are AES-256-GCM-sealed when the row's `WrappedDEK` is set — the handler unseals before streaming, transparent to the caller. |
| PUT | `/internal/v1/snapshots/{docID}` | Accept an opaque snapshot blob (collab-service's length-prefixed framing — editor-service does NOT parse it) and persist as a new Revision row + file under `users/{owner}/docs/{docID}/snapshots/{revID}.yjs`. Body cap: 16 MiB. Returns 201 with the new `revId`. When `EDITOR_SNAPSHOT_KEK_HEX` is set, the file on disk is AES-256-GCM-sealed via [`internal/encat/`](../../../editor-service/internal/encat/encat.go) and the per-snapshot wrapped DEK lands in `revisions.wrapped_dek`. |

The wire format is owned by collab-service ([`internal/persister/framing.go`](../../../collab-service/internal/persister/framing.go)); editor-service just streams bytes in and out.

**Encryption-at-rest** ([`internal/encat/`](../../../editor-service/internal/encat/encat.go)). Snapshot bytes are sealed via [`shared/keystore`](../../../shared/keystore/envelope.go) when a master KEK is configured (env: `EDITOR_SNAPSHOT_KEK_HEX` — hex-encoded 32-byte AES-256 key). Each snapshot gets a fresh DEK (verified by `TestSealSnapshot_FreshDEKPerCall`); the DEK is AES-256-GCM-wrapped with the master KEK and stored in `revisions.wrapped_dek` (60 bytes: nonce + ciphertext + tag). On read, the handler unwraps the DEK and opens the sealed file before streaming. When the env var is unset the helper passes plain through (handlers and DB schema unchanged) — pre-keystore rows + KEK-off deploys both keep working. A mismatched KEK on a sealed row surfaces as `encat.ErrEncryptionDisabled` (operator misconfiguration) or `keystore.ErrAuthFailed` (wrong KEK / tampered bytes); never a partial decrypt. Per-tenant KEKs (plan §4.4.6) replace the single-master shape when auth-service exposes a KEK-resolution callback.

**Threading.** Comments support **single-depth replies**: a comment with `parentCommentId` set is a child of the referenced top-level comment. The handler rejects replies-to-replies with 400 `NESTED_REPLY` and unknown parents with 404 `PARENT_NOT_FOUND` (the parent must also belong to the same document). Resolving a parent does NOT cascade — each reply has its own resolve state.

**Author display names.** AddComment / ListComments enrich each row with `authorDisplayName` resolved from auth-service's `/internal/users/{id}/profile` endpoint (via [`internal/authclient/`](../../../editor-service/internal/authclient/authclient.go)). Dedupe + concurrent lookup with a cap of 8 keeps a 50-comment list from hammering auth-service. Best-effort: lookups failing leave the field empty and the frontend falls back to a truncated UUID. Configure with `AUTH_SERVICE_URL`; unset disables enrichment.

**Live updates.** On AddComment and ResolveComment, editor-service publishes a JSON event to NATS subject `editor.comments.<docID>` ([`handlers/events.go`](../../../editor-service/handlers/events.go)). collab-service subscribes via [its eventbridge](../../../collab-service/internal/eventbridge/eventbridge.go) and forwards the payload as a WS frame to every client in the doc's room. The frontend's `CommentsList` parses the JSON and folds it into local state, deduping by comment id so a user's own optimistic write isn't double-added when the wire echo arrives.

**Webhook fanout.** On `CreateDocument`, `EditDocument`, and `RestoreRevision` the handler calls `publishDocumentLifecycleDomainEvent` ([`handlers/events.go`](../../../editor-service/handlers/events.go)), which emits a public [`DomainEvent`](../../../shared/queue/domain_event.go) on `notify.event.document.created` / `notify.event.document.updated`. notify-service's fanout consumer expands each event into one webhook delivery per matching [`WebhookSubscription`](../../../notify-service/internal/models/webhook_subscription.go), HMAC-signed with the subscription's per-row secret. The public payload `documentLifecyclePayload` is tighter than the internal `Document` / `Revision` rows — `omitempty` ensures `document.created` carries `title` but never `revId`, and `document.updated` carries `revId` + `opCount` but never internal fields like `storageKey` or `currentRevId`. Best-effort: NATS unavailable / publish failure is logged at Warn and the user-facing write still succeeds. `document.signed` is reserved for organize-pdf to emit when PAdES signing wires the fanout side (tracked separately).

## Test corpus

[`internal/corpus/`](../../../editor-service/internal/corpus/) is the single source of test PDFs every editor package exercises. The package generates fixtures in Go (no LFS, no binary churn): `Minimal()` (1 blank page), `MultiPage(n)` (n blank pages), `WithText([…])` (1 page rendering each line as a Helvetica run). 5 previously-duplicated `minimalPDF()` helpers across pdfedit / pdfwriter / spdom / editops tests now alias `corpus.Minimal()`.

[`internal/corpus/golden_test.go`](../../../editor-service/internal/corpus/golden_test.go) is the central conformance suite: every shipped op (page.rotate / page.delete / page.insert / annotation.add — all 8 kinds) runs against every fixture, asserting (a) the output validates through pdfcpu, (b) the original prefix bytes are preserved verbatim (incremental-update invariant — critical for signature survival per plan §3.10), and (c) per-op post-conditions hold (page count, annot count). A multi-op chain test asserts that ops compose correctly. New op translators add one switch case here and the suite catches the next regression.

## DB schema

```sql
CREATE TABLE documents (
    id              UUID PRIMARY KEY,
    owner_user_id   UUID        NOT NULL,
    title           TEXT        NOT NULL,
    current_rev_id  UUID,
    size_bytes      BIGINT      NOT NULL DEFAULT 0,
    page_count      INT         NOT NULL DEFAULT 0,
    storage_key     TEXT        NOT NULL,           -- relative path under /files/
    status          TEXT        NOT NULL DEFAULT 'ready',  -- ready | locked | quarantined | deleted
    created_at      TIMESTAMP   DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP   DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_documents_owner_updated ON documents (owner_user_id, updated_at DESC);

CREATE TABLE revisions (
    id              UUID PRIMARY KEY,
    document_id     UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    parent_rev_id   UUID,
    author_user_id  UUID        NOT NULL,
    message         TEXT,
    yjs_update_key  TEXT,                            -- /files/ path to compressed Yjs binary update
    pdf_patch_key   TEXT,                            -- /files/ path to incremental PDF patch bytes
    wrapped_dek     BYTEA,                           -- AES-256-GCM-wrapped DEK for the snapshot file; nil = plaintext on disk
    created_at      TIMESTAMP   DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_revisions_document_created ON revisions (document_id, created_at DESC);

CREATE TABLE comments (
    id                 UUID PRIMARY KEY,
    document_id        UUID     NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    rev_id             UUID     NOT NULL,
    anchor             JSONB    NOT NULL,            -- opaque, frontend-owned
    body               TEXT     NOT NULL,
    author_user_id     UUID     NOT NULL,
    parent_comment_id  UUID,                          -- nullable; non-null = reply
    resolved           BOOL     NOT NULL DEFAULT false,
    created_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_comments_document_resolved ON comments (document_id, resolved);
CREATE INDEX idx_comments_parent ON comments (parent_comment_id);

-- Status whitelist:
ALTER TABLE documents ADD CONSTRAINT chk_document_status
    CHECK (status IN ('ready','locked','quarantined','deleted'));
```

### `anchor` JSON shape

Documented at the handler layer (not DB-validated, so it can evolve without migration). The current convention:

```json
{"type":"span","nodeId":"p7r3","offsetStart":12,"offsetEnd":24}
```

or:

```json
{"type":"region","page":3,"rect":[120,400,300,420]}
```

## Sequence diagrams

See [docs/developer/mermaid/service-editor-service-architecture.md](../mermaid/service-editor-service-architecture.md) for the component diagram, and [docs/developer/mermaid/service-editor-service-sequence.md](../mermaid/service-editor-service-sequence.md) for the per-flow sequences.

## Error flows

The service uses the standard envelope ([shared/response](../../../shared/response/)). Codes documented today:

| HTTP | Code | When |
|---|---|---|
| 400 | `INVALID_INPUT` | request-body validation failed |
| 400 | `INVALID_PARAM` | path parameter not a valid UUID |
| 400 | `INVALID_QUERY` | bad `?resolved=` value |
| 401 | `UNAUTHENTICATED` | no `X-User-ID` (placeholder until JWT verifier ports in) |
| 404 | `DOCUMENT_NOT_FOUND` | document missing or not owned by caller |
| 404 | `COMMENT_NOT_FOUND` | comment missing under that document |
| 500 | `DB_*` | DB op failed (create/count/list/get/delete/update) |
| 400 | `INVALID_OP` | `POST /v1/documents/{id}/edit` — op type not implemented in v0 |

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8090` | gin listen port |
| `DATABASE_URL` | _required_ | Postgres DSN; `config.ApplyPostgresDSNDefaults` adds `sslmode` etc. |
| `REDIS_ADDR` | `localhost:6379` | optional; service runs without Redis if unreachable |
| `REDIS_PASSWORD` | _empty_ | optional |
| `NATS_URL` | per `shared/natsconn` defaults | optional; events skipped if unreachable |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | per `shared/telemetry` defaults | optional |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | comma-separated CIDRs trusted by gin for `X-Forwarded-For` |
| `LOG_MODE` | _empty_ | `json` for structured logs (CI/prod) |

## Scaling constraints

- **Stateless HTTP layer** — horizontally scale by instance count.
- **DB pool** — defaults to 25 open / 10 idle per instance; tune via `PoolConfig` in [main.go](../../../editor-service/main.go) if a noisy neighbour saturates Postgres.
- **Revision writes** — every edit op writes one `revisions` row + one Yjs blob to `/files/`. At sustained > 1k ops/s/document, switch to in-memory batching (Phase 2 alongside `collab-service`).
- **Redis is optional** — readiness still passes if Redis is down. Presence + idempotency caches gracefully degrade to "no cache" until Redis recovers.

## Deployment

The service ships as a `scratch`-based image at `ghcr.io/<owner>/fyredocs-editor-service`. Built by [`.github/workflows/docker.yml`](../../../.github/workflows/docker.yml) on every push to `main` and on version tags.

Local dev:

```bash
cd editor-service
DATABASE_URL=postgresql://localhost:5432/fyredocs?sslmode=disable go run main.go
```

## Phase-1 follow-up tasks (tracked in the main todo list)

1. **Done — L3 position-aware layout.** The parser tracks the PDF text state machine (Tm/Td/TD/T\*/Tf/TL/Tc/Tw) and the four show operators (Tj/'/"/TJ), groups events into Run → Line → Block, and emits real BBoxes. Pages with translation-only, orthogonally-rotated, OR skewed/non-orthogonal text all land at `layoutPass == 2` — every text-bearing page now reaches LayoutPassFull regardless of Tm shape. See [`spdom/layout.go`](../../../../editor-service/internal/spdom/layout.go) and the test coverage in [`spdom/layout_test.go`](../../../../editor-service/internal/spdom/layout_test.go).
   Recursive XY-cut shipped in [`internal/spdom/xycut.go`](../../../editor-service/internal/spdom/xycut.go) (with the 2-column shortcut helpers still in [`internal/spdom/columns.go`](../../../editor-service/internal/spdom/columns.go)). At each recursion step the parser projects events onto both axes, finds the widest qualifying whitespace strip (heuristics: gap ≥ max(5% of region dim, 1.5× mean font size); gap centre in the central 60%; text on both sides), and cuts along the wider gap. Halves recurse independently — depth capped at 6. Reading order: top→bottom for horizontal cuts, left→right for vertical cuts. The algorithm handles 3+ column layouts, header banners over a 2-column body, two-up reports, and sidebar/aside floats in one pass; single-column pages short-circuit to the single-pass clusterer. AFM-driven per-glyph widths shipped in [`internal/spdom/widths.go`](../../../editor-service/internal/spdom/widths.go): the parser builds a resource-name → BaseFont map from each page's `/Resources/Font` dict (subset prefixes like `BCDEEE+Helvetica` stripped), and `approxWidth` reaches pdfcpu's bundled AFM tables via `CharAdvance` for the 14 PDF core fonts. Opaque/non-core fonts still fall back to the 0.5em-per-glyph average. **Orthogonal-rotation bbox math (closing symmetry with redact v1):** the emitter classifies Tm into one of four orthogonal rotations (0°/90°/180°/270°) with uniform scale, advances the text-matrix origin along the rotated baseline (`tm[4] += w·a/scale; tm[5] += w·b/scale` — same formula `scrub.go` uses), and the clusterer partitions events by orientation. **Per-orientation clustering ([clusterEvents](../../../editor-service/internal/spdom/layout.go)):** rotated events are projected from page space into a per-orientation logical horizontal frame via [`rotatedToLogical`](../../../editor-service/internal/spdom/layout.go) (`90°: (x,y) → (y, -x)`, `180°: (x,y) → (-x, -y)`, `270°: (x,y) → (-y, x)`) so that the rotated baseline runs along +lx and the rotated height extends along +ly, exactly the convention the horizontal clusterer expects. The shared `clusterEvents` runs against the logical events to produce Run → Line → Block nesting; the resulting bboxes are projected back to page space via [`logicalToPageRect`](../../../editor-service/internal/spdom/layout.go), and every Block in a rotated pass is tagged with `Block.Orientation = orient`. The upshot: rotated paragraphs cluster into one Block with multiple Lines (verified by `TestBuildBlocksFromEvents_RotatedMultiLineClusters`), and two glyphs on the same rotated baseline merge into one Run (`TestBuildBlocksFromEvents_RotatedTwoGlyphsSameBaselineMergeIntoOneLine`) — the same clustering quality horizontal text gets. The `Orientation` field on [`spdom.Block`](../../../editor-service/internal/spdom/model.go) (omitted from JSON when 0) tells frontends and AI consumers which axis is the baseline. NodeID block-indices are kept stable by iterating orientations in fixed order {0, 90, 180, 270, -1} and threading a `startIdx` through each pass. **Skew / non-orthogonal / mirror support ([buildSkewedBlocks](../../../editor-service/internal/spdom/layout.go)):** any Tm 2×2 that doesn't match the orthogonal classification is now emitted as a `textEvent` with `orient = OrientationSkewed (-1)` and the raw 2×2 [a b c d] stored on the event. `buildSkewedBlocks` materialises one Block per skewed event with `Block.Orientation = OrientationSkewed`; the bbox comes from [`skewedEventBBox`](../../../editor-service/internal/spdom/layout.go) which projects the four text-space corners of `[0, w_text] × [0, h_text]` through the raw 2×2 and takes the AABB. Italic shears, y-axis mirrors, and non-uniform scale all flow through this path — verified by `TestSkewedEventBBox_HandlesMirrorAndShear` (three matrices, exact expected AABBs) and `TestBuildBlocksFromEvents_SkewedEventBecomesSkewedBlock` (end-to-end through the L4 materialiser). Skewed blocks ship the correct AABB (good enough for redact/search/AI extraction) but expose no axis-aligned baseline — consumers that need precise selection geometry on slanted text should treat the bbox as approximate; full-transform exposure on the Block is the tracked v2 follow-up. Still ahead in the same area: (a) cross-event clustering for skewed text (v1 is one-block-per-event because per-glyph Tm is the norm); (b) glyph-level capture for write-back overflow handling. See plan §5.4.
2. **sPDOM ops — Phase-1 wire surface complete.** `POST /v1/documents/{id}/edit` reads the document, applies 1–64 ops (chained), persists a `Revision` row, and bumps `Document.CurrentRevID`. Every roadmap op now ships: `page.rotate`, `page.delete`, `page.insert`, `text.replace`, `text.insert`, `text.delete`, `redact.apply`, `table.cell.edit`, and `annotation.add` (kinds: `highlight`, `underline`, `strikeout`, `squiggly`, `square`, `sticky`, `freehand`, `callout`). **`text.insert`** ([`internal/pdfedit/textinsert.go`](../../../editor-service/internal/pdfedit/textinsert.go)) appends a fresh `BT /<font> <sizePt> Tf <x> <y> Td (<text>) Tj ET` block to the page's content stream via the shared [`spdom.BuildTextBlock`](../../../editor-service/internal/spdom/scrub.go) builder (also used by `table.cell.edit`'s replacement path — same byte format for both ops). Caller picks the resource name (which must already exist in `/Resources/Font`; v0 doesn't add fonts), point size, and page-space anchor. **`text.delete`** ([`internal/pdfedit/textdelete.go`](../../../editor-service/internal/pdfedit/textdelete.go)) is `redact.apply` minus the overlay: it routes through [`spdom.ScrubRectChanged`](../../../editor-service/internal/spdom/scrub.go), which returns `(scrubbed, changed)` so the translator can surface `ErrTextDeleteNoOverlap` (mapped to 400) when the caller's rect doesn't contain any text. Both ops share the same single-stream + uncompressed constraints as the rest of the pdfedit translators. `redact.apply` ([`internal/pdfedit/redact.go`](../../../editor-service/internal/pdfedit/redact.go)) walks the page's content stream and scrubs every text-show op (Tj, ', ", TJ) whose rendered glyph bbox overlaps the redact rect — the string operand is replaced with `()`/`[]` so the underlying glyphs no longer round-trip through copy-paste or text extraction, then a black-fill rectangle is appended as a visual overlay. v1 closes the privacy holes from v0: (a) any-glyph-overlap semantics replace "origin in rect" so a long Tj starting outside but extending INTO the rect is now scrubbed (no more under-redaction at the boundary); (b) rotated, skewed, and mirrored text are scrubbed correctly — the bbox math projects the four text-space corners through Tm, takes the AABB, and tests overlap against the rect; the page-space advance walks along the rotated baseline too. Same single-stream + uncompressed constraints as `text.replace`. **v2 ships per-item TJ surgery:** when the operand is a TJ array (`[ (a) -100 (b) -50 (c) ] TJ`), the scrubber walks each item independently, tracking the running cursor through the kerning numbers, and replaces ONLY the `(...)` byte ranges whose individual glyph bbox overlaps the rect. Non-overlapping neighbours survive verbatim (`(long first string here)` is kept when only items 2 and 3 of a 3-item array fall inside the redact zone — see `TestScrubRect_TJArrayPartialOverlapScrubsOnlyOverlappingItems`). Kerning numbers and the surrounding `[ ]` stay in place so the array is still well-formed and the surviving neighbours close ranks the same way an emptied plain Tj would. `ReplaceRectFirstAnchor` threads the first-overlap anchor through this same walk, so `table.cell.edit` positioned at a TJ item starts at the correct cursor (`TestReplaceRectFirstAnchor_TJArrayCapturesFirstOverlappingItem` pins the exact offset). Scrubber lives in [`internal/spdom/scrub.go`](../../../editor-service/internal/spdom/scrub.go). **`table.cell.edit`** ([`internal/pdfedit/tablecell.go`](../../../editor-service/internal/pdfedit/tablecell.go)) is the same scrub primitive extended with replacement: the wire op `{type, page, rect, text}` runs through [`spdom.ReplaceRectFirstAnchor`](../../../editor-service/internal/spdom/scrub.go), which (a) collects scrub ranges for every text-show op whose glyph rect overlaps `rect`, (b) captures the `(font, size, anchor)` of the FIRST such op (via a single state-machine pass shared with `ScrubRect`), (c) splices the empty operands into the original bytes, and (d) appends a fresh `BT /<fontRes> <size> Tf <x> <y> Td (<newText>) Tj ET` block at the end of the content stream. No table detection — the caller identifies the cell by its page-space rect, typically derived from an L4 sPDOM Block bbox. `ErrCellEmpty` surfaces as 400 INVALID_INPUT when the rect doesn't overlap any text. **v1 ships cross-orientation replacement:** `recordFirst` now captures the Tm 2×2 alongside the anchor; `buildReplacementBT` dispatches between [`spdom.BuildTextBlock`](../../../editor-service/internal/spdom/scrub.go) (Td form, identity matrix) and [`spdom.BuildOrientedTextBlock`](../../../editor-service/internal/spdom/scrub.go) (full Tm form, anything else) via `isIdentity2x2`. The upshot: a 90°-rotated cell's replacement renders 90°-rotated (verified by `TestReplaceRectFirstAnchor_RotatedCellReplacementUsesMatchingTm`); a Tm-scaled cell preserves rendered size (`TestReplaceRectFirstAnchor_ScaledCellPreservesRenderedSize`); horizontal cells still take the shorter Td form so the appended bytes stay minimal (`TestReplaceRectFirstAnchor_HorizontalCellStillUsesTd` pins this regression). **v1 ALSO ships multi-line wrapping**: [`spdom.ReplaceRectFirstAnchorWrapped`](../../../editor-service/internal/spdom/scrub.go) is the new emitter `EditTableCell` calls through (the original `ReplaceRectFirstAnchor` is preserved for non-table callers that want single-line). When `newText` is wider than the cell at the captured font/size, [`spdom.WrapTextToWidth`](../../../editor-service/internal/spdom/widths.go) breaks it on whitespace boundaries (greedy first-fit, no mid-word hyphenation — corrupting copy/paste with a `-` would be worse than visible overflow) and the emitter stacks one `BT/ET` block per line at a 1.2 × fontSize default leading. Wrap target is `rect.X1 - anchor.x` (the cell's right-side width from the captured start). Hard newlines in the input are honoured as paragraph breaks. Rotated cells project the per-line y offset through the captured Tm 2×2 so wrapped lines ride the cell's orientation. Lines that overflow `rect.Y0` are still emitted (silent truncation would be a data-loss hazard; visible overflow signals the caller to widen the cell). Remaining caveats: font/size still match the FIRST overlapping op only. **Grid auto-detection landed as a separate, callable function**: [`spdom.DetectTableGrid(stream, fontMap, region)`](../../../editor-service/internal/spdom/tablegrid.go) returns a flat slice of `Cell{Row, Col, Rect}` when the region looks like a regular grid. Algorithm: filter to horizontal events in-region, cluster into rows on a baseline tolerance of `0.4 × meanFontSize`, derive column anchors from the union of per-row event start-Xs clustered at 4pt, validate row-pitch regularity (stdev/mean ≤ 25%) + column-coverage (each row hits all but ≤ 1 anchor), emit cells. Conservative thresholds — accepts typical invoice/schedule/data tables, rejects paragraph blocks that happen to share X anchors. Returns `(nil, false)` for: stream with unsupported text-matrix shapes, < 2 rows, < 2 cols, irregular row pitch, rows missing too many cells. v0 punts on multi-line wrapped cells (each wrapped line shows up as its own row and the row-pitch check rejects the region). **Wire-up landed**: `pdfedit.EditTableCellByCoord(original, page, region, row, col, newText)` is the in-process entry point, called by the dispatcher when an op carries `{region, row, col}` instead of `{rect}`. Both rect-form `EditTableCell` and coord-form `EditTableCellByCoord` share a `loadPageStream` helper for the /Contents parse so the two paths don't duplicate boilerplate. New typed sentinels `ErrGridNotDetected` (region isn't a recognisable table) and `ErrCoordOutOfRange` (row/col past detected bounds) both map to 400 INVALID_INPUT via `classifyTableCellEditErr` — distinct so callers can differentiate "region is wrong" from "row/col is wrong" without parsing message strings.
   - **Done — L1 incremental writer.** [`internal/pdfwriter`](../../../editor-service/internal/pdfwriter/doc.go) parses the prior revision's trailer (Discover), accumulates object replacements/insertions via `Update.Set`, and emits append-only bytes via `Update.Bytes`. Original bytes preserved verbatim. Round-trip tested against pdfcpu (`roundtrip_test.go`). Both classic xref-table and stream-form (`/Type /XRef`) emit paths now ship; the form is auto-selected to match the prior revision.
   - **Done — first op translator.** [`internal/pdfedit.RotatePage`](../../../editor-service/internal/pdfedit/rotate.go) reads a PDF, resolves the leaf Page dict + IndirectRef via pdfcpu, writes `/Rotate N` onto the on-page dict (always on the leaf — overriding any inherited rotation rather than relying on it), and emits the new revision through `pdfwriter`. Round-trip test confirms pdfcpu re-reads `/Rotate = 90` after the update, and double-rotate proves the incremental chain (rev1 ⊂ rev2).
   - **Done — op dispatch + HTTP handler.** [`internal/editops`](../../../editor-service/internal/editops/doc.go) owns the wire-format → translator mapping, with sentinel errors (`ErrNoOps`, `ErrOnlyOneOpSupported`, `ErrUnknownOp`, `ErrInvalidArgs`) the handler maps to status codes. The handler ([`handlers/documents.go.EditDocument`](../../../editor-service/handlers/documents.go)) auths the caller, loads the document, resolves the source path under [StorageDir](#storage), reads bytes, runs `editops.Apply`, writes the new bytes under `users/<uid>/docs/<docId>/edits/<revId>.pdf`, inserts a `Revision` row, and updates `Document.CurrentRevID`. The pattern (read → look up object → rewrite body → write through pdfwriter) is the template the remaining ops follow.
3. **Wire the indexer** — background goroutine that consumes `EDIT_EVENTS` and pushes Meilisearch index updates.
4. **Add NATS event emission** — every create/edit/delete should publish to `EDIT_EVENTS` for downstream consumers (audit, search, AI later).

## Related documentation

- [Plan blueprint §4.3 — service inventory](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)
- [Plan blueprint §4.6 — DB schema reference](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)
- [Plan blueprint §5 — sPDOM parser/writer architecture](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)
- [STORAGE.md](../architecture/STORAGE.md) — where `storage_key` and `pdf_patch_key` point
- [CI_CD.md](../architecture/CI_CD.md) — how new services land in the pipeline
- [OPENAPI.md](../architecture/OPENAPI.md) — spec maintenance contract
- [FONTS.md](../architecture/FONTS.md) — the font catalog + substitution registry (PDF-14 + Croscore) that the edit op-handler will consume when text changes introduce out-of-subset glyphs
