# billing-service

## Service Responsibility
Owns subscription state (`subscriptions` table) and exposes a public
plan registry. Reads in-period usage from [analytics-service](./ANALYTICS_SERVICE.md)
via the internal usage endpoint and merges it into the
`/v1/billing/me` response. Stripe integration + invoice generation
are tracked follow-ups; v0 ships the read/write surface that
makes those iterations trivial to slot in.

## Design Constraints
- Plans live in code ([`internal/plans`](../../../billing-service/internal/plans/plans.go)), not the DB. Pricing
  changes go through PR review; the `subscriptions` table stores
  a `plan_code` reference.
- Subscriptions are unique per user (`uniqueIndex:idx_sub_user`).
  Plan switches UPDATE in place — period resets on every change in
  v0; Stripe will replace this with proration when wired.
- Usage data is non-critical for billing display: if
  analytics-service is unreachable, `/v1/billing/me` renders
  without the usage section rather than failing.

## Internal Architecture
```
fyredocs_backend/billing-service/
├── main.go                     # entry: gin + DB + usageclient wiring
├── handlers/
│   ├── billing.go              # ListPlans, Me, Subscribe
│   └── system.go               # /healthz, /readyz
├── routes/routes.go            # gin route table
├── internal/
│   ├── models/
│   │   ├── database.go         # Postgres + GORM connect/migrate
│   │   └── subscription.go     # Subscription row + BeforeCreate
│   ├── invoice/                # invoice line-item math + HTML / plain-text / PDF renderers
│   │   ├── invoice.go          # Invoice / LineItem / Party types + Compute() + FormatMoneyCents()
│   │   ├── render.go           # RenderHTML() (inline-styled for email) + RenderPlainText()
│   │   ├── render_pdf.go       # RenderPDF() — paginated US-Letter, stdlib-only synthesis
│   │   ├── tounicode.go        # WinAnsi → Unicode CMap builder (shared /ToUnicode stream for F1 + F2)
│   │   ├── ttffont.go          # CIDFontType2 composite-font wire emitter (parser-deferred scaffolding)
│   │   └── sequence.go         # NextNumber() — atomic UPSERT-with-RETURNING for `FYR-YYYY-NNNN` identifiers
│   ├── plans/plans.go          # in-code plan registry (Free/Pro/Teams/Business/Enterprise)
│   ├── revshare/
│   │   ├── revshare.go         # 70/30 marketplace split calculator + ledger Entry type
│   │   └── persist.go          # Record() — INSERTs an Entry into revshare_entries with (source, source_ref) dedup
│   ├── feereconcile/
│   │   ├── feereconcile.go    # BackfillStripeFees — one-pass back-fill of stripe_fee_cents
│   │   └── runner.go          # periodic driver wired from main.go behind BILLING_FEE_RECONCILE_ENABLED
│   ├── stripeauth/webhook.go   # Stripe webhook signature verifier (HMAC + timestamp tolerance)
│   ├── stripeclient/           # outbound Stripe REST client (Checkout Session today)
│   │   └── stripeclient.go
│   └── usageclient/usageclient.go # HTTP client for analytics-service
└── Dockerfile
```

## Routes
| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/v1/billing/plans` | none | Public plan tier list — drives pricing pages. |
| GET | `/v1/billing/me` | `X-User-ID` | Current user's plan + subscription + period usage (usage section omitted when analytics-service is unreachable). |
| POST | `/v1/billing/me/subscribe` | `X-User-ID` | Switch self-serve plan. Body: `{planCode, seats?}`. Returns the updated `Subscription`. Enterprise + future sales-led plans rejected with 400 `INVALID_PLAN`. |
| GET | `/v1/billing/me/marketplace-earnings` | `X-User-ID` | Developer's revshare entries — what they've earned through marketplace plugin sales. Query params: `?status=pending\|payable\|paid\|reversed`, `?limit=N` (default 50, max 500). Returns a curated public shape — internal fields (`platformShareCents`, `stripeFeeCents`, `sourceRef`) DO NOT leak. Includes a page-scoped `totalEarnedCents` for quick at-a-glance. Newest first by `recorded_at`. |
| POST | `/v1/billing/checkout/session` | `X-User-ID` | Create a Stripe Checkout Session for a paid plan. Body: `{planCode, seats?}`. Returns `{sessionId, url}` — the SPA navigates to `url` for the hosted card-entry flow. Refuses `free` (use `/me/subscribe` directly), Enterprise (sales-led), and `business`-class plans whose `STRIPE_PRICE_IDS` mapping is missing (500 `STRIPE_NOT_CONFIGURED`). Stripe-side errors surface as 502 `STRIPE_ERROR` with the upstream message preserved. JWT-gated at the gateway. |
| POST | `/v1/billing/stripe/webhook` | `Stripe-Signature` HMAC | Stripe event receiver — JWT-exempt at the gateway (`PublicPaths` in [api-gateway/main.go](../../../api-gateway/main.go)). Verifies signature, deduplicates via `processed_stripe_events`, dispatches `customer.subscription.{created,updated,deleted}` and `invoice.{paid,payment_failed}`. Always 200 on a verified event (Stripe retries non-2xx for 3 days); 400 on signature failures, 503 if `STRIPE_WEBHOOK_SECRET` is unset. Unknown event types are recorded + acked. |
| GET | `/healthz` | none | Liveness probe. |
| GET | `/readyz` | none | Readiness probe. Checks Postgres (`SELECT 1`) AND NATS reachability via the shared [`natsconn.HealthChecker`](../../../shared/natsconn/healthcheck.go). 503 on Postgres failure OR NATS-configured-but-disconnected (audit + `subscription.*` fanout publishers go silent on a NATS outage — pod shouldn't look healthy while audit rows disappear). `nats: disabled` (NATS not configured at all → HTTP-only mode) DOES NOT fail readyz. |
| GET | `/metrics` | none | Prometheus scrape endpoint. |

Caller identity is the `X-User-ID` header set by api-gateway after
JWT verification — same pattern as analytics-service.

## DB Schema

### subscriptions
| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key (UUIDv7). |
| user_id | UUID | Owner; **uniquely indexed** (one sub per user). |
| plan_code | TEXT | FK-by-string into the in-code plan registry. |
| status | TEXT | `active`, `canceled`, or `past_due`. |
| seats | INT | Seat count for per-seat plans (Teams, Business). 1 for individual plans. |
| current_period_start | TIMESTAMP | Billing-cycle start (UTC). |
| current_period_end | TIMESTAMP | Billing-cycle end (UTC). |
| stripe_subscription_id | TEXT | Nullable; uniquely indexed where not null. Set by the Stripe webhook handler on `customer.subscription.created`. |
| stripe_customer_id | TEXT | Nullable; indexed where not null. Set alongside the subscription id; used by the webhook handler to look up rows for `invoice.*` events that only carry the customer reference. |
| created_at | TIMESTAMP | Row creation. |
| updated_at | TIMESTAMP | Last mutation. |

### processed_stripe_events
Idempotency table for the Stripe webhook handler. Stripe retries any non-2xx for 3 days, and each retry carries the same `event_id` — the handler INSERTs the id before applying side effects, so a duplicate hits the unique-PK path and surfaces as `"duplicate": true` in the 200 response without re-mutating any row.

| Column | Type | Description |
|--------|------|-------------|
| event_id | TEXT | Primary key — Stripe's `evt_…` identifier, stored verbatim. |
| event_type | TEXT | Mirrors `event.type` (e.g., `customer.subscription.created`). Indexed so operators can spot-check recent event volume by type without consulting Stripe Dashboard. |
| processed_at | TIMESTAMP | Indexed. Wall-clock UTC of the handler's successful insert. |

## Plan Registry
Hard-coded in [`internal/plans/plans.go`](../../../billing-service/internal/plans/plans.go) per plan §7.1:

| Code | Price | Per-seat | Self-serve | Notes |
|------|-------|----------|------------|-------|
| `free` | $0 | no | yes | 5 docs/day, 25MB max, watermark on conversions. |
| `pro` | $15/mo ($12 annual) | no | yes | Unlimited utility, 1GB storage, full editor, 100 AI credits/mo. |
| `teams` | $20/user/mo | yes | yes | Collab, shared workspaces, 100GB pooled, 500 AI credits/user/mo. |
| `business` | $35/user/mo | yes | yes | SSO, audit, DLP, advanced workflows, 2000 AI credits/user/mo. |
| `enterprise` | contact sales | yes | **no** | HIPAA, BYOK, data residency, dedicated support, custom SLA. |

Limits are denominated in `BillableEvent.EventType` keys (`op.merge`, `op.ocr`, `ai.tokens`, `file.bytes`); `-1` = unlimited.

## Environment Variables
| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8099` | Listen port. |
| `DATABASE_URL` | (required) | Postgres DSN. |
| `ANALYTICS_SERVICE_URL` | (unset → no usage) | Base URL of analytics-service (e.g., `http://analytics-service:8087`). When unset, `/v1/billing/me` renders without the `usage` field. |
| `NATS_URL` | (unset → no audit) | NATS endpoint. When set, every successful `POST /v1/billing/me/subscribe` publishes `subscription.created` (first time) or `subscription.changed` (update) on `audit.events.subscription.*`; analytics-service appends a hash-chained row. Unset = audit publishing disabled (HTTP path still works). |
| `STRIPE_WEBHOOK_SECRET` | (unset → webhook 503s) | Stripe Dashboard → Webhooks → "Signing secret" (`whsec_…`). Required for the Stripe webhook endpoint to accept events; the handler verifies every request's `Stripe-Signature` against this secret via [`internal/stripeauth`](../../../billing-service/internal/stripeauth/webhook.go). |
| `STRIPE_API_KEY` | (unset → checkout 503s) | Stripe `sk_test_…` / `sk_live_…` secret key. Required for outbound calls (Checkout Session creation today, more endpoints later). Lives in the cluster secret store, never in code. |
| `STRIPE_PRICE_IDS` | (unset → checkout 500s on first paid request) | JSON map `{"pro":"price_...","teams":"price_..."}` of plan-code → Stripe price id. Per-env value (test vs live). `free` and `enterprise` are NEVER in this map. |
| `STRIPE_CHECKOUT_SUCCESS_URL` | (unset → checkout 503s) | Absolute URL Stripe redirects to after successful payment. Typically `https://app.fyredocs.com/billing/success?session_id={CHECKOUT_SESSION_ID}` (Stripe substitutes the placeholder). |
| `STRIPE_CHECKOUT_CANCEL_URL` | (unset → checkout 503s) | Absolute URL Stripe redirects to when the user abandons the Checkout flow. |
| `LOG_MODE` | (default slog) | Forwarded to `shared/logger.Init`. |

## Audit Events Emitted

| Action | Resource | Metadata | When |
|---|---|---|---|
| `subscription.created` | (empty — userID is the actor) | `{oldPlan: "", newPlan, seats}` | First-time `POST /v1/billing/me/subscribe` succeeds. |
| `subscription.changed` | (empty) | `{oldPlan, newPlan, seats}` | Update of an existing subscription row succeeds. |

Distinct from auth-service's `plan.changed` event — billing-service publishes `subscription.*` because the seat count is meaningful here whereas auth-service doesn't have that field. The chain is single-writer per actor, so both events for the same user are interleaved cleanly in `audit_events`.

## Domain Events Emitted (webhook fanout)

Parallel to the audit chain above, billing-service publishes [`DomainEvent`](../../../shared/queue/domain_event.go)s on `notify.event.subscription.*` so notify-service's fanout consumer can deliver one webhook per matching [`WebhookSubscription`](../../../notify-service/internal/models/webhook_subscription.go). Distinct from audit because the public payload is curated for external integrations (Zapier, customer scripts) — Stripe customer / subscription IDs DO NOT leak; only the user-meaningful `oldPlan` / `newPlan` / `seats` cross the wire.

| Event Type | Payload (`data`) | When |
|---|---|---|
| `subscription.created` | `{newPlan, seats}` | First-time `POST /v1/billing/me/subscribe` succeeds, OR `customer.subscription.created` from Stripe webhook upserts a new row. |
| `subscription.changed` | `{oldPlan, newPlan, seats}` | Existing row's `plan_code` changes via `/me/subscribe` OR `customer.subscription.updated` (status-only flips do NOT emit — those have dedicated events). |
| `subscription.canceled` | `{oldPlan, seats}` | `customer.subscription.deleted` from Stripe webhook flips the row to `canceled`. |

Each event carries the standard envelope (`eventId`, `eventType`, `userId`, `occurredAt`, `data`). Best-effort: NATS down / publish failure logs at Warn and drops; the Subscription row is already committed and a subscriber that didn't fire can refetch state via `/v1/billing/me`.

## Invoice library

[`internal/invoice/`](../../../billing-service/internal/invoice/invoice.go) is the pure renderer for monthly subscription invoices, marketplace payout statements, and any future flow that needs an HTML / plain-text receipt from a structured line-item model. Lives in billing-service because every billable flow already routes through here — splitting it into its own service would just trade an import for a network hop.

Surface:
- `New(Invoice) → (Invoice, error)` constructs + computes in one call. Validates: at least one line item, every line has a non-empty Description, `TaxBps ∈ [0, 10000]`. Currency is normalised to upper-case + trimmed.
- `Compute(Invoice) → Invoice` is idempotent: re-running on a previously-computed invoice produces the same totals. Pulled out as a separate function so callers that need to mutate-then-recompute don't have to round-trip through `New`'s validation.
- `Invoice.RenderHTML() → (string, error)` emits a self-contained HTML5 document with inline styles. Drops cleanly into an email's HTML part — no external CSS to strip. Every user-controlled field (party name, line description, memo) is HTML-escaped — verified by `TestRenderHTML_EscapesPartyAndLineDescriptions`.
- `Invoice.RenderPlainText() → (string, error)` emits a fixed-column ASCII version for SMTP plain-text parts, terminal display, and audit-log archives. 50-char description column wraps onto continuation rows.
- `Invoice.RenderPDF() → ([]byte, error)` emits a paginated US-Letter PDF. Pure-stdlib synthesis — no external library and no embedded font files (Helvetica + Helvetica-Bold base Type1 fonts only). Both fonts declare `/Encoding /WinAnsiEncoding` explicitly so PDF readers don't fall back to StandardEncoding (whose 0x80-0x9F range is undefined and would mis-render curly quotes / em-dashes / the euro sign). A shared `/ToUnicode` CMap ([`tounicode.go`](../../../billing-service/internal/invoice/tounicode.go)) is wired into both fonts at object 5 — covers the WinAnsi codepoint → Unicode mapping so copy/paste, text search, accessibility tools, and PDF/A conformance flows recover the right characters. Subset-embedded TTF/CFF support (for glyphs outside WinAnsi — CJK, emoji, custom logos) is a tracked follow-up; the CMap establishes the wire format that subset embedding will plug into. Output bytes are deterministic for fixed inputs, so callers can golden-test or hash for cache keys. Round-trips cleanly through [`shared/pdftext`](../../../shared/pdftext/extract.go) (verified by `TestRenderPDF_RoundTripsThroughPDFText` + `TestRenderPDF_StillRoundTripsAfterCMapAddition`). Pagination caps are documented in [`render_pdf.go`](../../../billing-service/internal/invoice/render_pdf.go): page 1 (full header) holds 20 line items, interior pages 42, the last page 30 (reserves room for totals/memo). Single-page invoices (≤ 22 items) suppress the page footer. Only past `pdfMaxTotalLines` (500) does the renderer truncate with `ErrLineItemsTruncated` — a guard against an adversarial caller submitting a 100k-row "invoice" that would balloon the PDF byte buffer.
- `FormatMoneyCents(cents, currency)` is the shared money formatter — `USD -123.45` shape — exposed for any caller that needs the same presentation in custom templates.
- `NextNumber(ctx, db, prefix, period) → string` allocates the next unique identifier for the given `(prefix, period)` pair and persists the increment atomically. Format: `{prefix}-{period}-{NNNN}` zero-padded to 4 digits (widens naturally past 9999). Concurrency-safe via single-statement `INSERT ... ON CONFLICT ... DO UPDATE ... RETURNING` — a 50-goroutine concurrent test verifies no duplicates. Independent counters per `(prefix, period)` so `FYR-2025-N` and `FYR-2026-N` don't collide, and `MKT-2026-N` (marketplace payouts) stays separate from subscription numbers.

### invoice_sequences
Per-`(prefix, period)` counter table backing `NextNumber`. One row per stream.

| Column | Type | Description |
|--------|------|-------------|
| prefix | TEXT | Identifier family (`FYR`, `MKT`, …). Composite-PK with `period`. |
| period | TEXT | Time bucket the sequence resets in (`2026`, `2026-04`, …). Caller-chosen resolution. |
| next_seq | BIGINT | The value the next `NextNumber` call will return AND advance past. BIGINT supports the lifetime of any conceivable Fyredocs. |
| updated_at | TIMESTAMP | Last increment. |

Math invariants pinned in tests:
- Line totals = `Quantity × UnitPriceCents`. Negative `Quantity` or `UnitPriceCents` represents a discount (verified by `TestCompute_DiscountsAsNegativeLineItems`).
- Tax is integer-divide-toward-zero of `subtotal × TaxBps / 10000`. The customer benefits from sub-cent truncation, matching standard accounting practice.
- A negative subtotal (refund invoice) produces zero tax — the IRS doesn't refund tax it never collected.
- `Total = Subtotal + Tax`, always — no float drift.

Rendering invariants:
- The HTML output is XSS-safe by construction: every user-controlled string passes through `html.EscapeString` before emission.
- The plain-text output uses a fixed 50/16 column layout (`%-50s %16s`) so amounts always right-align.
- The tax row is omitted entirely from both renderings when `TaxBps == 0` — no noise lines.

What the library does NOT do (tracked):
- Subset-embedded fonts for glyphs outside WinAnsi. The renderer ships an explicit `/Encoding /WinAnsiEncoding` + shared `/ToUnicode` CMap (covers Latin-1 + the curly-quote / euro / em-dash family), so accented Latin and the common punctuation set render AND copy-paste correctly. Glyphs outside WinAnsi (CJK, emoji, custom logos) still show as glyph-not-found. **The wire-format scaffolding has landed**: [`ttffont.go`](../../../billing-service/internal/invoice/ttffont.go) exports `TTFFont` + `NewTTFFont(psName, data, metrics)` + `EmitObjects(startObj, toUnicodeObj)` + `ToUnicodeCMap(cmap)` — these emit the full 4-object cluster (Type0, CIDFontType2, FontDescriptor, FontFile2) per PDF 1.7 §9.7 in the correct cross-referenced shape, with a 2-byte Identity-H ToUnicode CMap. What's still deferred: an actual TTF parser to fill `TTFFontMetrics` (cmap rune→GID, hmtx widths, head bbox, OS/2 ascent/descent). The follow-up cycle vendors `golang.org/x/image/font/sfnt` (or an equivalent), parses the source TTF into metrics, then plumbs `TTFFont` into `RenderPDF`'s font-resource path so callers can request a CJK / emoji font per invoice. The wire-shape work is finished — the next cycle is data-flow only.
- Multi-currency invoices in one document. Out of scope; refunds in a different currency get their own invoice.
- Per-line tax rates. v0 supports one document-level rate; mixed-rate lines (US states with use tax + city tax) is a tracked follow-up.

## Stripe-webhook signature verifier

[`internal/stripeauth/`](../../../billing-service/internal/stripeauth/webhook.go) is the pure verifier that gates every Stripe-receiving handler. Stripe webhook validation is famously easy to get wrong (timing attacks, missing replay window, accepting on partial matches); this package owns the canonical implementation so each future Stripe handler calls it identically and the test surface lives in one place.

Surface:
- `Verify(header, body, secret, now, tolerance) → error` — single entry point. Returns nil iff (a) at least one `v1=` HMAC-SHA256 matches `<t>.<rawBody>` in constant time, and (b) `t=` sits within `tolerance` of `now` (defaults to `DefaultTolerance = 5 * time.Minute` when zero).
- `ComputeHMAC(secret, timestamp, body) → string` — hex-encoded HMAC for test helpers that need to construct valid headers without re-implementing the scheme.
- Typed sentinels: `ErrSignatureMissing` / `ErrSignatureMalformed` (4xx on the wire), `ErrTimestampTooOld` (replay window — 400, log + drop), `ErrSignatureMismatch` (every `v1=` failed constant-time compare — 400), `ErrEmptySecret` (programming bug at caller — fail loud rather than accepting every payload).

Pinned invariants (see [`webhook_test.go`](../../../billing-service/internal/stripeauth/webhook_test.go)):
- Tolerance is bidirectional — too-far-in-the-future timestamps are rejected the same as too-old (clock-skew + replay defence land in one check).
- Multiple `v1=` entries are tried in order; any match accepts. Supports Stripe's documented secret-rotation window where both old + new signatures appear in the same header.
- `v0=` (legacy SHA-1) and unknown keys are silently ignored — header parsing is forgiving as long as at least one `t=` and one `v1=` are present.
- Non-hex `v1=` entries are skipped, not panic'd. A header that's entirely malformed lands at `ErrSignatureMismatch` (no `v1=` matched), not at an internal-server-error.
- `ComputeHMAC` output changes when any of (secret, timestamp, body) change — pins the regression that would otherwise let the secret protect only one of those.

Caller pattern (Stripe webhook handler):
```go
body, _ := io.ReadAll(c.Request.Body)
if err := stripeauth.Verify(
    c.GetHeader("Stripe-Signature"), body,
    os.Getenv("STRIPE_WEBHOOK_SECRET"),
    time.Now(), 0,
); err != nil {
    response.BadRequest(c, "BAD_SIGNATURE", err.Error())
    return
}
// body is now trusted — decode + dispatch
```

The library is pure (caller supplies `time.Now()`), so each Stripe handler's tests stay deterministic without injecting fakes.

## Stripe webhook handler

[`handlers/stripe.go`](../../../billing-service/handlers/stripe.go) is the dispatch layer that turns verified Stripe events into Subscription-row mutations. Wired at `POST /v1/billing/stripe/webhook`; the gateway treats this path as public (HMAC signature is the auth — a JWT-protected webhook is unreachable for Stripe).

Event types handled:
- `customer.subscription.created` / `customer.subscription.updated` — upsert the matching `Subscription` row. Plan code comes from `metadata.plan_code`; user id from `metadata.user_id` (required for the FIRST event, before any Stripe link is on the row). Status, period start/end mirror Stripe.
- `customer.subscription.deleted` — flip status to `canceled` (the period dates are preserved; entitlements remain until `current_period_end`).
- `invoice.payment_failed` — flip status to `past_due`.
- `invoice.paid` — restore a `past_due` row back to `active`; leaves already-active rows alone.
- `charge.succeeded` — when the charge carries `metadata.plugin_id` + `metadata.developer_user_id`, the handler runs `revshare.Calculate` + `revshare.Record` to land one row in `revshare_entries`. Non-marketplace charges (no metadata) skip silently — those flow through `invoice.paid` for subscription bookkeeping. When the charge object carries a `balance_transaction` id, the handler GETs `/v1/balance_transactions/{id}` via `stripeclient.GetBalanceTransaction` and populates `revshare_entries.stripe_fee_cents` with the fee. The lookup is failure-tolerant: a missing id, missing API key, network error, 4xx, or 5xx logs a warning and records the row with `stripe_fee_cents=0` rather than blocking the write (an under-stated fee is fixable by a future reconciliation pass; a stuck webhook is not — Stripe would redeliver forever and the user would see missing earnings).

Skip semantics (event recorded + 200 ack'd, but no row mutation):
- Subscription event missing `plan_code` metadata (operator misconfigured Stripe Checkout).
- Subscription event naming an unknown `plan_code`.
- Subscription event for a customer/user with no matching row + no `user_id` to seed a fresh row.
- Invoice event with no matching `stripe_subscription_id` or `stripe_customer_id`.
- `charge.succeeded` without marketplace metadata, with malformed `developer_user_id`, or with zero/negative gross.
- Any event type outside the five above.

Idempotency: the handler INSERTs the event id into `processed_stripe_events` before applying side effects. A duplicate delivery (Stripe retries for 3 days) trips the unique constraint and surfaces as `200 {"received": true, "duplicate": true}` without re-running the dispatch. On dispatch failure the idempotency row is rolled back so Stripe's retry gets another shot.

Status normalisation (`mapStripeStatus`): Stripe's eight subscription statuses fold into our three. `incomplete` / `unpaid` / `incomplete_expired` → `past_due` (all payment-failure states); `trialing` → `active` (entitlements during trial); `canceled` → `canceled`; unrecognised → `active` (defensive default — better to keep service than wrongfully terminate).

## Stripe outbound client

[`internal/stripeclient/`](../../../billing-service/internal/stripeclient/stripeclient.go) is the minimal HTTP wrapper for Stripe's REST API. We deliberately do NOT pull in the official `stripe-go` SDK — it's ~30k LOC, surfaces every Stripe resource, auto-retries on its own backoff, and ties releases to Stripe's cadence. A focused wrapper keeps the dep surface zero and the test surface tiny.

Surface (v0):
- `Client{SecretKey, BaseURL, HTTPClient}` — configured struct. BaseURL defaults to `https://api.stripe.com`; tests override it with an httptest server URL.
- `CreateCheckoutSession(ctx, req) → (*CheckoutSessionResponse, error)` — the only outbound call today. Form-encodes the request, sets Bearer auth + `Stripe-Version: 2024-06-20`, forwards an optional `Idempotency-Key`, and validates that `response.url` came back populated (a Stripe schema change that drops the field would otherwise silently break the checkout flow).
- `StripeError` — structured error wrapping the HTTP status + Stripe's `error.type`/`error.code`/`error.message`. Callers `errors.As` to map to user-visible strings. Non-JSON error bodies (e.g., a 502 HTML page from a gateway between us and Stripe) collapse to `type=unknown` rather than panicking.

Metadata-on-creation contract: every `Metadata[k]=v` pair gets written to BOTH `metadata[k]` (lives on the Checkout Session) AND `subscription_data[metadata][k]` (lives on the eventual Subscription, and Stripe deep-clones it onto every invoice). The webhook handler reads `subscription.metadata` — without this dual-write the webhook couldn't resolve back to our `user_id` / `plan_code`.

Test pattern: each unit test stands up `httptest.NewServer` with a handler asserting on the captured request body, then points `Client.BaseURL` at it. No interface mocking, no SDK fakes. See [`stripeclient_test.go`](../../../billing-service/internal/stripeclient/stripeclient_test.go).

## Checkout Session handler

[`handlers/checkout.go`](../../../billing-service/handlers/checkout.go) is the user-facing route that creates a Stripe Checkout Session and returns the redirect URL. JWT-gated like the other `/me/*` routes — the inbound caller is the SPA after the user clicks "Upgrade".

Validation pipeline (in order; each rejection has its own error code):
- `INVALID_INPUT` — malformed JSON body.
- `INVALID_PLAN` — unknown plan_code, non-self-serve (Enterprise), Free (which has no Stripe presence and should hit `/me/subscribe` directly), or `seats > 1` on a non-per-seat plan.
- `STRIPE_NOT_CONFIGURED` — operator launched a new plan without adding its Stripe price id to `STRIPE_PRICE_IDS`. 500 because the SPA can't fix this.
- `CHECKOUT_DISABLED` — `STRIPE_API_KEY` or the success/cancel URL pair is unset. 503 — operator needs to set the env.
- `STRIPE_ERROR` — Stripe-side rejection (bad price id, card declined on a saved-card flow, etc.). 502 with Stripe's `error.message` preserved so the SPA can show a useful string.

Idempotency: every call generates a fresh UUIDv7 as the `Idempotency-Key` header. A user double-clicking "Subscribe" reuses the same key (no — each request gets a new key, but Stripe's idempotency window prevents Stripe from creating two real sessions if the SAME key is reused; the v0 design is "let Stripe dedupe at-most-once attempt-per-click rather than us trying to be clever"). Concretely: the SPA should debounce its own Subscribe button. The Idempotency-Key here protects against transient transport-level retries (e.g., the client times out mid-response but Stripe completed the create).

Outbound client injection: tests swap the factory via `SetStripeClientFactoryForTest(f)` so the handler points at an `httptest.NewServer` running a Stripe-shaped stub. Real code uses `defaultStripeClient` which reads `STRIPE_API_KEY` from env.

## Revenue-share library

[`internal/revshare/`](../../../billing-service/internal/revshare/revshare.go) is the pure calculator that splits a marketplace transaction's gross between a third-party developer and Fyredocs the platform. Lives here (rather than in a dedicated `marketplace-service`) because the v0 marketplace ships through billing-service's Stripe webhook path and shares its money primitives.

Surface:
- `DefaultSplit()` returns the canonical 70/30 split with platform-absorbs-Stripe-fee semantics (per plan §7.4).
- `NewSplit(developerBps, minimumDevCents, FeeMode)` constructs a validated custom policy. Out-of-range basis points / negative floor / unknown FeeMode are rejected.
- `Calculate(Transaction, SplitPolicy) → (Entry, error)` is the pure splitter — all-cents integer math, no rounding leaks. The invariant `dev + platform + stripeFee == gross` holds for every input (tested across all 999 single-cent grosses up to $9.99 in `TestCalculate_ManyOddSumsAllReconcile`).

Edge-case behaviours pinned in tests:
- Odd-cent rounding favours the platform (developer gets integer-divide-toward-zero share; platform absorbs the remainder).
- Floor swap: if the developer's computed share is below `MinimumDeveloperShareCents`, they get the full gross and platform takes zero. Avoids penalising sub-dollar template purchases.
- Stripe-fee absorption: `FeePlatformAbsorbs` (default), `FeeProRata` (each side absorbs proportional to share), `FeeDeveloperAbsorbs`.
- Clamps: if the absorbed fee would drive a share negative, that share clamps to zero and the other side eats the deficit — totals still reconcile to gross.

`Entry` is the ledger row produced by Calculate. Persistence lives in [`persist.go`](../../../billing-service/internal/revshare/persist.go) — `Record(ctx, db, entry, opts)` INSERTs into `revshare_entries` with `(source, source_ref)` dedup so a Stripe-webhook redelivery doesn't double-record a charge. A duplicate returns `ErrDuplicateSource` + the original row's id so the caller treats it as a no-op success. Status flow: `pending` → `payable` (after chargeback window) → `paid` (after Stripe Connect transfer) → optional `reversed` on refund/chargeback.

### revshare_entries
Auto-migrated alongside `subscriptions` and `processed_stripe_events`.

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key (UUIDv7). |
| transaction_id | TEXT | The developer-supplied id from `revshare.Transaction` (typically `ch_…` / `pi_…`). Indexed. |
| developer_user_id | UUID | Payee. Indexed. JOIN-able to the auth-service users table. |
| plugin_id | TEXT | Marketplace plugin id. Composite-indexed with `developer_user_id` for the per-plugin payout breakdown. |
| source | TEXT | `stripe_charge` (default) \| `manual_credit` \| future sources. Forms a composite unique index with `source_ref`. |
| source_ref | TEXT | External ref (`ch_…`, manual-note id). When non-empty, composite-unique with `source` — defends against Stripe-webhook redelivery and other replay scenarios. |
| gross_cents | BIGINT | Total before split. |
| developer_share_cents | BIGINT | Developer's take. |
| platform_share_cents | BIGINT | Platform's take. Sums with developer share to exactly `gross_cents` (calculator invariant). |
| stripe_fee_cents | BIGINT | The Stripe fee absorbed per `FeeMode`. |
| currency | TEXT | ISO-4217 (uppercased on INSERT). |
| status | TEXT | `pending` \| `payable` \| `paid` \| `reversed`. Indexed for the payout-run query. |
| recorded_at | TIMESTAMP | INSERT time. Indexed for chronological scans. |
| updated_at | TIMESTAMP | Last mutation (status promotions / reversals). |

## Out of Scope for v0 (tracked follow-ups)
- Stripe integration (PaymentIntent, customer portal, webhooks)
- PDF invoice emitter (HTML + plain-text renderers ship today; PDF lands when a customer asks)
- Past-due / dunning state machine
- Per-seat seat management (team admin → assign seats)
- Proration on mid-cycle plan changes
- Payout-reconciliation pass: [`feereconcile.BackfillStripeFees`](../../../billing-service/internal/feereconcile/feereconcile.go) is the function — scans `revshare_entries WHERE source='stripe_charge' AND stripe_fee_cents=0 AND source_ref LIKE 'ch_%' AND recorded_at < now-MinAge`, chains `stripeclient.GetCharge` → `stripeclient.GetBalanceTransaction` for each, idempotent UPDATE with `WHERE stripe_fee_cents=0` so a concurrent pass can't double-write. Per-row Stripe errors log + continue (count surfaces in `Stats.LookupErrors`); only DB-level failures abort. Default 5-minute MinAge cooldown stops the pass from racing the webhook handler. **The periodic driver** is [`feereconcile.Runner`](../../../billing-service/internal/feereconcile/runner.go), wired from `main.go` behind the `BILLING_FEE_RECONCILE_ENABLED` env gate (off in dev/staging so the same binary doesn't burn Stripe quota by default; production flips it on). Defaults: 30s initial delay (lets the service finish binding + the DB pool warm before the first call), 10-minute interval, default pass options (5-minute MinAge, 100 MaxRows). Single goroutine; per-tick factory call so a `STRIPE_API_KEY` rotation mid-run picks up on the next pass; per-tick errors NEVER stop the loop. `Stop()` blocks until the in-flight pass finishes.

## Scaling Constraints
Stateless API container; horizontal scale-out via load balancer.
The unique-per-user index on `subscriptions` doubles as a
concurrency guard — racing `POST /v1/billing/me/subscribe` calls
either insert once or update an existing row, never produce
duplicates.
