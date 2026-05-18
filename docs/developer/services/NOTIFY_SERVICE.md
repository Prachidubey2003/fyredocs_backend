# notify-service

## Service Responsibility
Outbound notifications fan-out: consumes the `NOTIFY` JetStream
(`notify.send.{email,webhook,push,slack}`) and dispatches each
event through the matching channel implementation. Persists one
[`notify_deliveries`](../../../notify-service/internal/models/delivery.go)
row per attempt for the dev console audit trail. Also exposes a
synchronous HTTP endpoint for service-to-service calls that need
an immediate success/failure return value (e.g., the
password-reset flow).

## Design Constraints
- One Delivery row per consumed NotifyEvent, irrespective of
  channel outcome. The row's `status` column tells the audit
  story (`pending`, `delivered`, `failed`, `skipped`).
- JetStream is the single retry mechanism for transient failures.
  Channel-send errors are recorded as `failed` and acked — they
  do NOT requeue (otherwise a perma-broken subscriber would loop
  forever).
- `idempotency_key` is uniquely indexed and collapses duplicates
  so an upstream service that retries the same publish call
  produces exactly one delivery row.
- Channels are stateless. State that needs to survive a restart
  (e.g., webhook retry-after backoff in a future iteration) lives
  in the dispatcher's persistence path, not in channel state.

## Internal Architecture
```
fyredocs_backend/notify-service/
├── main.go                    # gin + dispatcher wiring + NATS subscriber boot
├── handlers/
│   ├── notify.go              # /v1/notify/deliveries, /internal/v1/notify/send
│   ├── webhooks.go            # /v1/notify/webhooks CRUD — third-party subscription registry (Zapier, customer integrations)
│   └── system.go              # /healthz, /readyz
├── routes/routes.go
├── subscriber/subscriber.go   # Two NATS consumers: `notify.send.>` → dispatcher.Dispatch, `notify.event.>` → fanout.Fanout
├── internal/
│   ├── encat/                 # service-local wrapper around shared/keystore (envelope-encrypts subscription secrets)
│   │   └── encat.go
│   ├── fanout/                # domain event → matching subscriptions → per-subscriber dispatch (per-row HMAC signing + circuit breaker)
│   ├── metrics/               # Prometheus counters for the fanout pipeline (events, deliveries, auto-disables)
│   │   └── fanout.go
│   ├── models/
│   │   ├── database.go
│   │   ├── delivery.go        # Delivery row + status constants
│   │   └── webhook_subscription.go  # WebhookSubscription registry row (third-party event subscriptions)
│   ├── channels/
│   │   ├── channel.go         # Channel interface + ErrUnsupportedChannel
│   │   ├── email.go           # SMTP transport; falls back to log-only when SMTP_HOST is unset
│   │   ├── webhook.go         # HMAC-signed HTTPS POST + signature-verify helper
│   │   ├── slack.go           # Slack incoming-webhook POST; rejects non-JSON payloads locally
│   │   └── push.go            # Expo Push HTTPS POST (wraps APNs + FCM); injects req.Target as `to`
│   └── dispatcher/dispatcher.go  # routes events → channels + persists Delivery
└── Dockerfile
```

## Routes
| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/v1/notify/deliveries?channel=&limit=` | `X-User-ID` | Caller's recent deliveries (newest first; defaults limit=50, max 200). |
| POST | `/v1/notify/webhooks` | `X-User-ID` | Create a webhook subscription. Body: `{eventType, targetUrl}`. Returns the new row + a plaintext `secret` SHOWN ONCE (stored as bcrypt). Refuses unknown event types (`INVALID_EVENT_TYPE`), plaintext-http targets to non-localhost hosts (`INVALID_TARGET_URL`), and callers past `maxWebhooksPerUser` (`SUBSCRIPTION_QUOTA`, 429). |
| GET | `/v1/notify/webhooks` | `X-User-ID` | List the caller's active subscriptions (newest first). Hash + plaintext secret are NEVER in the response. `secretPrefix` (first 8 chars of plaintext) is shown so users can identify a key during rotation. |
| DELETE | `/v1/notify/webhooks/:id` | `X-User-ID` | Soft-delete a subscription. 404 when the row is missing OR owned by another user (no information leak). The row stays in DB for audit attribution of past deliveries. |
| POST | `/v1/notify/webhooks/:id/enable` | `X-User-ID` | Re-enable a subscription the circuit breaker auto-disabled. Resets `failure_count` to 0 + flips `status` to `active`. Idempotent — hitting on an already-active row succeeds and still resets the counter (useful after investigating a near-miss). Soft-deleted rows are NOT resurrectable through this endpoint (404); create a fresh subscription instead. Same 404 semantics as DELETE for cross-user attempts. |
| POST | `/v1/notify/webhooks/:id/test` | `X-User-ID` | Fire a synthetic `webhook.test` event at the subscription's target URL using the recovered per-row signing secret. Lets users verify their receiver works without waiting for a real event (same affordance Stripe, GitHub, Slack offer). Returns the resulting `Delivery` row inline so the UI can render success/failure feedback. Dispatches even when the subscription is `disabled` (testing whether the receiver is fixed before re-enabling). Does NOT touch the circuit-breaker `failure_count` — a test failure is a UX signal, not "your subscriber is broken". Same 404 semantics for cross-user attempts. |
| POST | `/v1/notify/webhooks/:id/rotate-secret` | `X-User-ID` | Generate a fresh signing secret for an existing subscription. Returns the new plaintext ONCE (same disclosure contract as create) plus the row. The old secret becomes invalid immediately — no grace window, no dual-key dispatch. Preserves all other row fields: `status`, `failure_count`, `event_type`, `target_url` are untouched (use this when you suspect the previous secret was leaked, not when you want to reset the breaker — that's `/enable`). Soft-deleted rows are 404. Same 404 semantics as DELETE for cross-user attempts (prevents DoS-by-rotation against legitimate integrations). |
| POST | `/internal/v1/notify/send` | mesh-private | Synchronous send. Body matches `queue.NotifyEvent`. Returns the resulting Delivery row. |
| GET | `/healthz` | none | Liveness probe. |
| GET | `/readyz` | none | Readiness probe. Checks Postgres (`SELECT 1`) AND NATS reachability (`Conn.IsConnected()`). Returns 503 on Postgres failure OR NATS-configured-but-disconnected (the fanout consumer + send consumer both go silent on a NATS outage, so the pod shouldn't look healthy). NATS that isn't configured at all reports `nats: disabled` and DOES NOT fail readyz — operators on a deliberately HTTP-only deploy don't get false negatives. |
| GET | `/metrics` | none | Prometheus scrape. |

The public-facing webhook routes are also documented in the [OpenAPI spec](../../swagger/openapi.yaml) under the `Webhooks` tag with request/response schemas — that's the canonical contract for external integrators (Zapier, customer scripts) consuming the API. Keep this table and the spec in sync; the spec is what generates the SDK + dev-docs.

## NATS Subjects

Two subject families share the `NOTIFY` JetStream. Each has its own durable consumer in notify-service with a matching `FilterSubject`; adding a third family is a stream-config + subscriber-goroutine edit.

### `notify.send.>` (pre-routed delivery requests)
The legacy single-target path. Publishers know exactly which transport + which target. Consumed by `notify-service` consumer → `dispatcher.Dispatch`.

| Subject | Description |
|---------|-------------|
| `notify.send.email` | Email delivery requests. Payload = `{subject, text?, html?}`. |
| `notify.send.webhook` | HTTPS POST to a single subscriber URL. Payload is JSON-marshalled verbatim. Signed with the global `NOTIFY_WEBHOOK_SECRET` (no per-subscription key — that's the fanout path). |
| `notify.send.push` | Mobile push via Expo Push. Payload = `{title?, body?, data?, sound?, badge?, ttl?, priority?, channelId?, mutableContent?}`. The channel injects `to: target` (an `ExponentPushToken[…]`) — caller-supplied `to` fields are overwritten so a misbehaving publisher can't address a different device. |
| `notify.send.slack` | Slack incoming-webhook. Payload is forwarded verbatim to `target` (the workspace webhook URL); see Slack's Block Kit docs for shape. |

Publishers use [`queue.PublishNotifyEvent`](../../../shared/queue/notify.go).

### `notify.event.<eventType>` (raw domain events for fanout)
The publisher emits a Stripe-style domain event WITHOUT knowing who's subscribed. Consumed by `notify-service-fanout` consumer → `fanout.Fanout` → one `DispatchWithSecret` per matching `WebhookSubscription`. Each subscriber's POST is HMAC-signed with its own per-row secret (recovered from `secret_ciphertext` via `encat.OpenSecret`).

| Subject | Description |
|---------|-------------|
| `notify.event.job.completed` | A processing job finished successfully. Payload (in `data`): `{jobId, tool, outputUrl}`. |
| `notify.event.job.failed` | A job failed. Payload: `{jobId, tool, error}`. |
| `notify.event.document.created` | A new document was added. Payload: `{docId, title}`. |
| `notify.event.document.updated` | A document was edited. Payload: `{docId, revId}`. |
| `notify.event.document.signed` | A signature was applied. Emitted by [job-service's eventbridge](../../../job-service/internal/eventbridge/eventbridge.go) when a `sign-pdf` JobCompleted event flows through, ALONGSIDE the generic `job.completed`. Payload extends the generic shape with two required sign-specific fields: `{jobId, tool: "sign-pdf", outputPath, fileSize, signerId, signMode}`. `signerId` is the user who applied the signature (mirrors the job submitter in v0; reserved so future delegated-signing flows can populate a different uuid without a schema break). `signMode` is the assurance label: `"image"` today (visual-stamp signature via pdfcpu, not cryptographic) — when cryptographic PAdES support lands, this graduates to `"pades-b-b"` / `"pades-b-t"` / `"pades-b-lt"` / `"pades-b-lta"` so subscribers can filter on assurance level without parsing the output PDF. The field is always present (no `omitempty`) so receivers can rely on the key existing. |
| `notify.event.subscription.created` | First-time paid subscription. Payload: `{planCode, seats}`. |
| `notify.event.subscription.changed` | Plan switch or seat change. Payload: `{oldPlan, newPlan, seats}`. |
| `notify.event.subscription.canceled` | Subscription cancelled. Payload: `{planCode}`. |

The set MUST stay in lockstep with `allowedEventTypes` in [`handlers/webhooks.go`](../../../notify-service/handlers/webhooks.go) — accepting subscriptions to an event type that no publisher emits would produce silent dead-letter subscriptions.

Subscribers receive the canonical envelope (see [`queue.DomainEvent`](../../../shared/queue/domain_event.go)):
```json
{
  "eventId":   "evt_01HW…",
  "eventType": "job.completed",
  "userId":    "uuid",
  "occurredAt":"2026-05-17T...Z",
  "data":      { ... }
}
```
The HTTP request from notify-service carries:
- `Content-Type: application/json`
- `X-Fyredocs-Signature: sha256=<hex>` (HMAC-SHA256 of the body with the subscription's plaintext secret — the one the API returned ONCE at creation)
- `X-Fyredocs-User-Id: <uuid>` (informational)

Subscribers MUST deduplicate on `eventId`. JetStream redelivers can fire the same event id twice; the per-subscription idempotency key (`fanout:<eventId>:<subId>`) collapses repeats on our side, but a subscriber that doesn't dedupe will see double-POSTs in edge cases.

Publishers use [`queue.PublishDomainEvent`](../../../shared/queue/domain_event.go).

## DB Schema

### notify_deliveries
| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key (UUIDv7). |
| user_id | UUID | Owner; nullable for system-to-system deliveries (e.g., a webhook fired by an internal cron). |
| channel | TEXT | `email` \| `webhook` \| `push` \| `slack`. |
| target | TEXT | Channel-specific destination (address, URL, device token, slack webhook URL). |
| status | TEXT | `pending`, `delivered`, `failed`, `skipped`. |
| attempts | INT | Times the channel was called. v0 always 0 (skipped) or 1 (delivered/failed). Reserved for the per-row retry follow-up. |
| payload | JSONB | The channel-specific payload, persisted as-is for audit. |
| last_error | TEXT | Truncated channel error string on `failed`. |
| idempotency_key | TEXT | Uniquely indexed when present; collapses duplicate publishes. |
| created_at | TIMESTAMP | Row creation. |
| updated_at | TIMESTAMP | Last status update. |

### webhook_subscriptions
Third-party-registered subscriptions for the fanout dispatcher (Zapier, customer-side integrations). Created via `POST /v1/notify/webhooks`; the future fanout consumer reads this table when an internal service publishes a domain event and POSTs to every matching `target_url`.

| Column | Type | Description |
|--------|------|-------------|
| id | UUID | Primary key (UUIDv7). |
| user_id | UUID | Owner. Composite-indexed with `event_type` for the lookup-on-fanout. |
| event_type | TEXT | One of the values in `allowedEventTypes` (job.completed, document.signed, subscription.changed, …). |
| target_url | TEXT | HTTPS URL the dispatcher POSTs to (http://localhost is allowed for dev). |
| secret_ciphertext | BYTEA | AES-256-GCM-sealed plaintext signing secret (envelope-encrypted via [`internal/encat`](../../../notify-service/internal/encat/encat.go)). When the service runs without `NOTIFY_SECRET_KEK_HEX` configured, these bytes ARE the plaintext (pass-through; documented as a dev-only mode). |
| secret_wrapped_dek | BYTEA | Per-row Data Encryption Key, wrapped with the service master KEK. Exactly `keystore.WrappedDEKSize` (60) bytes when sealed, nil in pass-through mode. The fanout dispatcher unwraps this to recover the plaintext for HMAC signing — this is why we store an envelope (recoverable) rather than a bcrypt hash (one-way). Mirrors industry practice (Stripe, GitHub, Slack). |
| secret_prefix | TEXT | First 8 chars of the plaintext secret. User-visible so a customer can identify a key during rotation. |
| status | TEXT | `active` \| `disabled`. `disabled` rows are filtered out at fanout-lookup time. Flipped to `disabled` by the [fanout circuit breaker](../../../notify-service/internal/fanout/fanout.go) (`fanout.FailureThreshold` consecutive failures) — operator action re-enables. |
| failure_count | INT | Consecutive failed deliveries since the last success. Reset to 0 on `delivered`; incremented atomically (`gorm.Expr("failure_count + 1")`) on `failed`. When the post-increment value crosses `fanout.FailureThreshold` (10), the breaker flips `status` to `disabled` in a follow-up UPDATE. |
| last_delivery_at | TIMESTAMP | Most-recent attempt (success or failure). |
| created_at | TIMESTAMP | Row creation. |
| updated_at | TIMESTAMP | Last mutation. |
| deleted_at | TIMESTAMP | gorm soft-delete timestamp. Default scope hides deleted rows; audit queries use `.Unscoped()`. |

## Channel Configuration

### Email (SMTP)
| Variable | Default | Description |
|----------|---------|-------------|
| `SMTP_HOST` | (unset → log-only) | `host:port`. Empty disables SMTP and Send logs the would-be delivery; Delivery row still lands `delivered`. |
| `SMTP_USER` | (unset → no auth) | PLAIN auth username. |
| `SMTP_PASSWORD` | (unset) | PLAIN auth password. |
| `EMAIL_FROM` | `no-reply@fyredocs.com` | Envelope sender + `From:` header. |

### Webhook
| Variable | Default | Description |
|----------|---------|-------------|
| `NOTIFY_WEBHOOK_SECRET` | (unset → unsigned) | HMAC-SHA256 key used by the LEGACY `/internal/v1/notify/send` path that doesn't have a per-subscription secret. When set, every such POST carries `X-Fyredocs-Signature: sha256=<hex>` over the request body. Subscribers verify via [`channels.VerifySignature`](../../../notify-service/internal/channels/webhook.go). The per-subscription signing key (preferred path, used by the future fanout dispatcher) lives on the `webhook_subscriptions` row and is unwrapped at send time. |
| `NOTIFY_SECRET_KEK_HEX` | (unset → pass-through, dev only) | Hex-encoded 32-byte master key used to envelope-encrypt webhook-subscription signing secrets at rest. Unset means the `secret_ciphertext` column stores plaintext — acceptable for local development, NOT for production. Production deploys MUST set this to a 64-char hex string (32 bytes of `crypto/rand` output). |

### Push (Expo)
| Variable | Default | Description |
|----------|---------|-------------|
| `EXPO_ACCESS_TOKEN` | (unset → no auth header) | Optional Expo access token. Required only for Expo projects that opted into "Enhanced Security for Push" — most projects authenticate solely via the per-device push token (which is the event's `target`), and the access token stays unset. When set, the channel sends it as `Authorization: Bearer …`. |

The Push channel ([`channels.Push`](../../../notify-service/internal/channels/push.go)) wraps both APNs (iOS) and FCM (Android) via Expo's HTTPS API (`https://exp.host/--/api/v2/push/send`) so notify-service doesn't have to carry per-vendor cert / service-account plumbing. Per-tenant tokens are stored client-side (mobile app's `Notifications.getExpoPushTokenAsync` result) and passed back to the publisher.

Expo errors come in two shapes, both treated as delivery failures and surfaced in `last_error`:
- **Top-level**: `{"errors": [{"code": "VALIDATION_ERROR", "message": "..."}]}` — auth / payload-shape failures.
- **Per-ticket**: 200 OK with `{"data": {"status": "error", "details": {"error": "DeviceNotRegistered"}}}` — the device unregistered, the push token rotated, etc. `DeviceNotRegistered` is the signal to remove the token from your store.

The channel decodes the caller's payload through a typed `expoMessage` struct, which means unknown fields are silently dropped — a defence-in-depth posture that prevents callers from sneaking a `to` field that addresses a different device.

Direct APNs / FCM transports (skipping Expo, for tenants that want it out of their trust chain) are a tracked follow-up.

### Slack
The Slack channel ([`channels.Slack`](../../../notify-service/internal/channels/slack.go)) needs no environment variable — each event's `target` IS the workspace-scoped secret (an `https://hooks.slack.com/services/T…/B…/…` incoming-webhook URL). Callers must:
- Store the webhook URL securely (per-tenant secret store).
- Pass it as the event's `target`.
- Send a payload that's valid JSON. The channel rejects malformed bytes locally with `slack: payload is not valid JSON` rather than letting Slack reply with the opaque `invalid_payload` 400.

Slack 4xx responses (`invalid_payload`, `channel_not_found`, `no_service`, `channel_is_archived`) are surfaced verbatim in `last_error` — the truncated body excerpt makes it easy to grep audit rows by failure mode. The Slack channel does NOT sign requests (the URL itself is the auth token); rotating a webhook URL is the on-tenant remediation if a key leaks.

## Out of Scope for v0 (tracked follow-ups)
- Direct APNs / FCM transports (without the Expo intermediary).
- Per-row retry/backoff with `attempts` increment + `next_retry_at`.
- Per-tenant SMTP / sender pools.
- DLQ inspection UI for `failed` deliveries.

## Webhook Fanout Metrics

[`internal/metrics/`](../../../notify-service/internal/metrics/metrics.go) exposes three Prometheus counters scraped at `GET /metrics`. They answer questions the audit log can't (proportions, rates) and back the on-call alerts on the fanout pipeline.

| Metric | Labels | Description |
|---|---|---|
| `notify_fanout_events_total` | `event_type`, `result` | One increment per DomainEvent. `result` is `matched` (subscriptions fired), `no_subscriber` (zero matches — the common case), or `error` (DB lookup or marshal failed; redelivered by JetStream). |
| `notify_fanout_deliveries_total` | `event_type`, `status` | One increment per PER-SUBSCRIPTION delivery. `status` is `delivered` (channel send returned nil), `failed` (channel returned an error), or `skipped` (per-row decrypt of the signing secret failed; dispatcher was NOT called). |
| `notify_fanout_subscriptions_disabled_total` | (none) | One increment every time the circuit breaker flips a subscription to `disabled` after `fanout.FailureThreshold` consecutive failures. A sudden jump is the canonical "a subscriber is broken" alert signal. |

Cardinality: 8 event types × 3 outcomes = 24 series for `events_total` and same for `deliveries_total`. Well within Prometheus comfort range; safe to chart per-event-type breakdowns in Grafana.

## Scaling Constraints
Stateless API container; horizontal scale-out via load balancer.
NATS JetStream's durable consumer (`notify-service`) load-
balances across replicas — each event is delivered to exactly
one consumer instance. Adding replicas linearly increases
throughput until the Postgres `INSERT INTO notify_deliveries`
becomes the bottleneck (a connection-pool tune at that point;
the table is append-mostly so no contention beyond row locks).
