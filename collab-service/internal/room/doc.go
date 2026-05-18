// Package room owns the in-memory state of multiplayer document
// sessions for collab-service.
//
// Design constraints (plan §4.3.1 + §4.5):
//
//   - One [Room] per Document. Clients join with the doc id; the
//     hub multiplexes incoming messages from any client out to every
//     OTHER client in the same room.
//   - v0 is a DUMB RELAY: the server doesn't parse Yjs update
//     bytes, just forwards them. Yjs's sync protocol works over a
//     star topology — clients merge updates locally. This keeps the
//     server stateless w.r.t. CRDT semantics and lets us swap to a
//     real y-crdt Rust binding (plan §4.5) without changing the
//     wire protocol the frontend already understands.
//   - Concurrency model: each Room owns a goroutine that serialises
//     join/leave/broadcast events through channels. Clients never
//     touch the connection map directly, so we get correctness from
//     the runtime's channel semantics rather than from a maze of
//     mutexes.
//
// What lives elsewhere:
//
//   - Websocket upgrade + per-connection read/write pumps — that's
//     [collab-service/handlers]. The room only sees a [Connection]
//     interface so the tests can drive it without a real socket.
//   - Persistence — a future revision will checkpoint Yjs update
//     bytes to disk (plan §4.4.3 path `revisions/{rev_id}.yjs`).
//     v0 keeps the doc state purely in client RAM; if everyone
//     disconnects, the next joiner starts from the
//     editor-service-served document bytes.
//   - Auth — verified before the upgrade by the gateway-style JWT
//     middleware (defense-in-depth via the same authverify package
//     editor-service uses).
package room
