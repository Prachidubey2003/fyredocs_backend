# collab-service

The collab-service owns **multiplayer document sessions** for the editor — websocket connections, room membership, and update fan-out between connected clients. It is the Phase 2 anchor service per plan §4.3.1.

| Property | Value |
|---|---|
| Port | `8091` |
| Framework | stdlib `net/http` + `github.com/gorilla/websocket` for the upgrade |
| DB | none yet — v0 is purely in-memory; Yjs checkpoint persistence lands when the Y-bytes write path arrives (plan §4.4.3 `revisions/{rev_id}.yjs`) |
| Cache | none — NATS core pub/sub handles cross-replica fan-out instead of Redis (lighter, already in the platform) |
| Events | Outbound: `collab.broadcast.<docID>` (cross-replica Yjs frames). Inbound: `editor.comments.<docID>` from editor-service, forwarded as WS frames to local rooms. Both core pub/sub, no JetStream. |
| Storage | none yet |
| Status | **horizontally scalable** — Hub + healthz/readyz + JWT-gated websocket upgrade + Prometheus `/metrics` + NATS cross-replica fan-out. Yjs checkpoint persistence still tracked as follow-up |

## Service responsibility

Owns these bounded contexts (per plan §4.3.1):

1. **Connection management.** Long-lived websocket fleet, one connection per editor tab.
2. **Room membership.** One [`room.Room`](../../../collab-service/internal/room/hub.go) per document, tracking active connections.
3. **Update fan-out.** Yjs sync-protocol messages from any client are relayed to every other connection in the same room. v0 is a dumb relay — clients merge via Yjs locally.
4. **Presence** (future). Cursors, selections, named users. Awareness payloads ride the same fan-out path.

Out of scope (owned elsewhere):

- Document bytes / revision persistence — `editor-service`.
- Comment threads — `editor-service` (REST today; live updates land here when the awareness API arrives).
- AI features — `ai-service` (Phase 3, deferred).

## Design constraints

- **Microservice boundary** — does not import code from any other service ([CLAUDE.md §1](../../../CLAUDE.md)). Only `fyredocs/shared/*` utility packages.
- **Single-process simplifying assumption** — the in-memory Hub lives in one process. Horizontal scale-out arrives via Redis pubsub between hubs in different processes; the Hub itself is NOT sharded. This trade-off is documented inline so the next person scaling the service knows the design intent.
- **Dumb-relay v0** — the server doesn't parse Yjs update bytes. Yjs's sync protocol works over a star topology where the server just forwards. This keeps the server stateless w.r.t. CRDT semantics and lets us swap to a real `y-crdt` Rust binding later (plan §4.5) without changing the wire protocol the frontend already understands.
- **Channel-serialised room state.** Each `Room` owns a goroutine; all mutations (join/leave/broadcast/size) flow through `events`. Correctness comes from channel semantics rather than a maze of mutexes. The Hub uses a single `RWMutex` to guard the global rooms map.

## Internal architecture

```
collab-service/
├── main.go                     # entrypoint: NATS connect → routes.Register → ListenAndServe → graceful shutdown
├── main_test.go                # middleware-integration test (unit tests live next to their packages)
├── handlers/
│   ├── connect.go              # GET /v1/docs/{id}/connect — websocket upgrade + room.Join + NATS publish
│   └── connect_test.go         # docID parser + origin policy + end-to-end ws broadcast test
├── routes/
│   ├── routes.go               # Register(mux, opts) — healthz / readyz / metrics / /v1/docs/ + SetReady on shutdown
│   └── routes_test.go          # registered-paths assertion + auth-middleware integration
├── internal/
│   ├── authverify/
│   │   ├── verifier.go         # JWT verifier (HS256/RS256, dual-key rotation, denylist hook)
│   │   ├── claims.go           # Claims + ScopeList (accepts space-delim string or array)
│   │   ├── context.go          # AuthContext + ctx plumbing
│   │   ├── middleware.go       # stdlib net/http middleware (token > cookie > query)
│   │   └── middleware_test.go  # 10 tests across header/cookie/query/expiry/guest/gateway paths
│   ├── metrics/
│   │   ├── metrics.go          # collab_rooms_total / collab_connections_total / collab_broadcast_bytes_total
│   │   └── metrics_test.go     # Bind + Inc/Dec + Counter accumulation
│   ├── eventbridge/
│   │   ├── eventbridge.go      # Inbound NATS bridge: editor.comments.<docID> → Room.BroadcastAll
│   │   └── eventbridge_test.go # Subject routing + missing-room drop + bad-subject + nil-receiver
│   ├── persister/
│   │   ├── persister.go        # Persister interface + Noop + InMemory impls
│   │   ├── persister_test.go   # round-trip + alias-safety + Has helper
│   │   ├── framing.go          # length-prefixed [][]byte wire format (Encode/Decode)
│   │   ├── framing_test.go     # round-trip + truncated-body + oversized-frame rejection
│   │   ├── http.go             # HTTP persister: PUT/GET /internal/v1/snapshots/{docID} with timeouts
│   │   └── http_test.go        # httptest.Server proves Save body + Load decode + 404/500/timeout paths
│   ├── presence/
│   │   ├── presence.go         # NATS bridge: 16-byte replica-id envelope + Publish/Receive + loop-prevention
│   │   └── presence_test.go    # 9 tests across encode/decode/echo-drop/dispatch/error paths
│   ├── room/
│   │   ├── doc.go              # package docs: design constraints + scope
│   │   ├── hub.go              # Hub + Room + Connection primitive (race-clean shutdown)
│   │   └── hub_test.go         # stub-conn-driven hub tests + concurrent FindOrCreate
│   └── wsconn/
│       ├── conn.go             # *websocket.Conn → room.Connection adapter (read/write pumps)
│       └── conn_test.go        # Send queueing, Close idempotency, OnMessage delivery
└── go.mod
```

## Routes (v0)

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness (no deps) |
| GET | `/readyz` | Reports hub room count in JSON; 503 when not ready |
| GET | `/metrics` | Prometheus scrape (collab-specific + shared HTTP histograms) |
| GET | `/v1/docs/{id}/connect` | Websocket upgrade. Joins (or creates) the room for `{id}` and relays binary frames to every other client in the room. Origin policy from `COLLAB_ALLOWED_ORIGINS`. |

JWT verification of the upgrade request is enforced via the `authverify.Middleware` wrapped around the `/v1/docs/` route. Defense-in-depth on top of the gateway: even when the gateway is bypassed (direct in-cluster traffic, local dev, future internal callers), the upgrade is still gated.

### Auth precedence

The middleware tries each source in order; the first match wins:

1. **`X-User-ID` gateway header** — only when `AUTH_TRUST_GATEWAY_HEADERS=true`. Production path: the gateway already verified the token and re-signed identity into trusted headers.
2. **`Authorization: Bearer <jwt>`** — non-browser clients (CLI, server-to-server tests).
3. **`Cookie: access_token=<jwt>`** — browser path (auth-service sets this on login).
4. **`?access_token=<jwt>` query parameter** — browser-WS escape hatch. Browsers can't set custom headers on `new WebSocket(url)`, and CORS-restricted iframes may not send cookies. This is the documented fallback.

`is_guest=true` claims are refused — multiplayer requires an owned identity.

### Configuration

| Env | Default | Effect |
|---|---|---|
| `PORT` | `8091` | Listen port |
| `COLLAB_ALLOWED_ORIGINS` | _empty_ (any) | Comma-separated CORS allowlist for the WS upgrade. Production MUST set this — empty means accept any origin and is only safe for dev. |
| `JWT_ALLOWED_ALGS` | `HS256` | Comma-separated JWT algorithms accepted. Add `RS256` to enable asymmetric verification. |
| `JWT_HS256_SECRET` / `JWT_SECRET` | _empty_ | HMAC secret for HS256 verification. |
| `JWT_HS256_SECRET_PREVIOUS` | _empty_ | Previous HMAC secret (verify-only). Used during zero-downtime secret rotation (see SECRETS.md §3). |
| `JWT_RS256_PUBLIC_KEY` | _empty_ | PEM-encoded RSA public key for RS256. |
| `JWT_ISSUER` / `JWT_AUDIENCE` | _empty_ | Optional iss/aud claim enforcement. |
| `JWT_CLOCK_SKEW` | `60s` | Leeway for exp/iat validation. |
| `AUTH_TRUST_GATEWAY_HEADERS` | `false` | When `true`, accept `X-User-ID` from the gateway in lieu of JWT. |
| `AUTH_ACCESS_COOKIE_NAME` | `access_token` | Override cookie name. |
| `AUTH_ACCESS_QUERY_PARAM` | `access_token` | Override query-parameter name. |
| `NATS_URL` | `nats://nats:4222` | NATS endpoint for cross-replica presence. Service tolerates `NATS_URL` being unreachable and falls back to single-replica mode. |
| `EDITOR_SERVICE_URL` | _empty_ (Noop persister) | Base URL of editor-service (e.g. `http://editor-service:8090`). When set, the Hub uses `persister.HTTP` to checkpoint room state via `/internal/v1/snapshots/{docID}`. Unset means no durability — fine for single-replica dev. |

## Hub design (load-bearing primitive)

### `Hub`

Process-wide registry. Methods:

- `FindOrCreate(docID)` — returns the room, creating on first use. Double-checked locking so the hot path stays cheap.
- `Find(docID)` — returns existing room or nil. For metrics.
- `RoomCount()` — drives the `/metrics` gauge.

### `Room`

Per-document multiplayer session. Methods:

- `Join(conn)` — adds a connection AND replays the in-memory frame log to the new joiner (see "Replay buffer" below). If the room is shutting down, closes the conn so the caller doesn't leak.
- `Leave(connID)` — idempotent removal.
- `Broadcast(senderID, payload)` — appends to the replay log THEN relays to every connection EXCEPT the sender. Sender exclusion is required by Yjs's sync protocol — re-delivering would double-apply or waste bandwidth.
- `BroadcastAll(payload)` — used by the NATS bridge for frames originating on other replicas; no sender exclusion since the originator is on another process.
- `Size()` — synchronous member count via reply-channel event. Cheap enough for metrics, not a hot-path op.

### Replay buffer

Every frame `Broadcast` ships through is appended to an in-memory log on the room. When a new client `Join`s, the run-loop replays the log to that client BEFORE any subsequent broadcast — so a late joiner sees the recent history before live traffic resumes. Yjs's sync protocol is idempotent (re-applying an update a client already has is harmless), so the simple "send everything" strategy is correct.

Bounded by **both** count and bytes (`SetMaxLogFrames` / `SetMaxLogBytes`, default 1024 frames / 8 MiB). Oldest entries evict first.

### Persistence (Persister interface)

The Hub accepts a `Persister` (`Load(docID) → [][]byte`, `Save(docID, [][]byte)`). When a room is constructed the run-loop's first action is `Load(docID)` — frames returned are seeded into the replay log BEFORE any Join is processed, so the first joiner sees them. When a room self-destructs (last Leave), the run-loop calls `Save(docID, log)` before invoking `onEmpty` (which removes the room from the hub map). Both calls are best-effort: a Load failure means "start empty", a Save failure means "we lose the checkpoint, recoverable on a busy doc".

Shipped implementations:
- **`persister.Noop`** — discards saves, returns nothing on load. Fallback when `EDITOR_SERVICE_URL` is unset.
- **`persister.InMemory`** — single-process map, useful for tests + single-replica dev.
- **`persister.HTTP`** — calls editor-service's `/internal/v1/snapshots/{docID}` endpoints. Frames are length-prefixed (`[4-byte BE uint32 frame-len][frame bytes]`, repeating) and stored under `users/{owner}/docs/{docId}/snapshots/{revId}.yjs` (plan §4.4.3). Best-effort: a Load failure means "start empty"; a Save failure means "lose this checkpoint, recoverable on the next room close". Per-request timeouts (5s Load / 10s Save by default) keep the room run-loop from blocking forever on a sick editor-service.

Wire format constants are defined in `internal/persister/framing.go`; the editor-service side does NOT need to decode — it just streams the opaque blob to/from disk.

A room self-destructs when its last member leaves: it invokes the Hub-supplied `onEmpty` callback and closes its `done` channel. Future events become no-ops; late `Join` calls close the connection so the caller doesn't leak.

### `Connection` interface

```go
type Connection interface {
    ID() string
    Send(payload []byte) error
    Close() error
}
```

Send errors evict the connection — a stuck socket can't backpressure the rest of the room. The handler layer (next iteration) implements this interface over `gorilla/websocket`; tests implement it over a channel.

## Sequence (planned, plan §4.5)

```mermaid
sequenceDiagram
  participant A as Client A (Yjs)
  participant B as Client B (Yjs)
  participant CS as collab-service Hub
  A->>CS: WS /v1/docs/:id/connect (auth: Bearer or fyr_)
  CS->>CS: Hub.FindOrCreate(docID).Join(connA)
  B->>CS: WS /v1/docs/:id/connect
  CS->>CS: Room.Join(connB)
  A->>CS: yjs update bytes
  CS->>B: Room.Broadcast (relay; A excluded)
  B->>A: yjs awareness ping
  CS->>A: Room.Broadcast (relay)
```

## Phase 2 follow-up tasks (tracked in the main todo list)

1. ~~**Websocket upgrade handler**~~ — shipped (`handlers/connect.go` + `internal/wsconn/`). Read-pump + write-pump pair with ping/pong, slow-consumer eviction (`ErrSlowConsumer`), and origin allowlist via `COLLAB_ALLOWED_ORIGINS`.
2. ~~**JWT verifier middleware**~~ — shipped (`internal/authverify/`). Stdlib net/http middleware with header/cookie/query token extraction, guest-claim rejection, and `AUTH_TRUST_GATEWAY_HEADERS` fast-path. API-key support (`fyr_` tokens) follows the same oracle pattern as api-gateway → auth-service.
3. ~~**Routes + registered-paths test**~~ — shipped (`routes/routes.go` + `routes_test.go`). `Register(mux, opts)` is now the single seam; `RegisteredPaths` is the canonical list the test asserts against. main.go also calls `routes.SetReady(false)` on SIGTERM so load balancers stop sending traffic before drain.
4. **Yjs checkpoint persistence** — every N updates or every M seconds, snapshot the merged Yjs state to `revisions/{rev_id}.yjs` under StorageDir. Hub gains a `Persister` interface; the impl talks to editor-service over REST.
5. ~~**NATS `PRESENCE` stream**~~ — shipped (`internal/presence/`). Subject `collab.broadcast.<docID>`, core pub/sub (not JetStream), 16-byte replica-id envelope prefix for loop prevention. Graceful degradation: if NATS is unreachable at startup the bridge stays nil and the service runs in single-replica mode. Each `roomReceiver.OnMessage` does local Broadcast + `bridge.Publish`; an inbound subscriber calls `bridge.Receive` which invokes `BroadcastAll` on local rooms (no sender exclusion — the original sender is on a different replica).
6. ~~**Metrics**~~ — shipped (`internal/metrics/`). `collab_rooms_total` (GaugeFunc bound to `Hub.RoomCount`), `collab_connections_total` (Inc/Dec on Join/close), `collab_broadcast_bytes_total` (per-recipient byte counter). Scrape at `GET /metrics`.
7. ~~**Frontend wiring**~~ — shipped (`fyredocs_frontend/src/hooks/useCollab.ts`). Transport-only hook; Yjs binding lands in a follow-up.

## Related documentation

- [Plan blueprint §4.3.1 — service inventory](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)
- [Plan blueprint §4.5 — realtime collab architecture diagram](../../../../../../.claude/plans/i-want-you-to-keen-orbit.md)
- [EDITOR_SERVICE.md](EDITOR_SERVICE.md) — the document-state authority. collab-service relays edits; editor-service stores them.
