# editor-service -- Sequence Diagrams

Request flows through the `editor-service` (port `8090`).

## Create document

```mermaid
sequenceDiagram
    participant Client
    participant GW as api-gateway :8080
    participant ES as editor-service :8090
    participant PG as PostgreSQL
    participant FS as /files/

    Note over GW: pre-existing upload flow stored bytes at /files/.../uploads/{uploadId}/{name}
    Client->>GW: POST /api/editor/v1/documents {title, storageKey, sizeBytes, pageCount}
    GW->>ES: Proxy + X-User-ID: <uuid>
    ES->>ES: requireUser (parse X-User-ID)
    ES->>ES: validate body (title required, storageKey required, length bounds)
    ES->>PG: INSERT documents (id=UUIDv7, owner_user_id, title, storage_key, status='ready')
    ES-->>GW: 201 {success:true, message:"document created", data: {Document}}
    GW-->>Client: forward
    Note over ES,FS: FS bytes are NOT touched here; storage_key is opaque to editor-service.
```

## List documents

```mermaid
sequenceDiagram
    participant Client
    participant GW as api-gateway :8080
    participant ES as editor-service :8090
    participant PG as PostgreSQL

    Client->>GW: GET /api/editor/v1/documents?page=1&limit=25
    GW->>ES: Proxy + X-User-ID
    ES->>PG: SELECT COUNT(*) FROM documents WHERE owner_user_id=? AND status<>'deleted'
    ES->>PG: SELECT * FROM documents WHERE owner_user_id=? AND status<>'deleted'<br/>ORDER BY updated_at DESC LIMIT 25 OFFSET 0
    ES-->>GW: 200 {data: [...], meta: {page, limit, total}}
    GW-->>Client: forward
```

## Edit document (sPDOM op) — scaffolded

```mermaid
sequenceDiagram
    participant Client
    participant GW as api-gateway :8080
    participant ES as editor-service :8090

    Client->>GW: POST /api/editor/v1/documents/{id}/edit {ops:[...]}
    GW->>ES: Proxy + X-User-ID
    ES->>ES: requireUser
    Note right of ES: Phase 1 follow-up:<br/>parse sPDOM ops<br/>build Yjs delta<br/>persist revision<br/>emit EDIT_EVENTS
    ES-->>GW: 501 {error:{code:"NOT_IMPLEMENTED", details:"..."}}
    GW-->>Client: forward
```

## Add comment

```mermaid
sequenceDiagram
    participant Client
    participant GW as api-gateway :8080
    participant ES as editor-service :8090
    participant PG as PostgreSQL

    Client->>GW: POST /api/editor/v1/documents/{id}/comments<br/>{revId, anchor, body}
    GW->>ES: Proxy + X-User-ID
    ES->>ES: requireUser; validate body; validate revId is uuid
    ES->>PG: SELECT * FROM documents WHERE id=? AND owner_user_id=?
    alt document missing
        ES-->>GW: 404 {error:{code:"DOCUMENT_NOT_FOUND"}}
    else owned
        ES->>PG: INSERT comments (id=UUIDv7, document_id, rev_id, anchor, body, author_user_id, resolved=false)
        ES-->>GW: 201 {data: {Comment}}
    end
    GW-->>Client: forward
```

## List revisions

```mermaid
sequenceDiagram
    participant Client
    participant GW as api-gateway :8080
    participant ES as editor-service :8090
    participant PG as PostgreSQL

    Client->>GW: GET /api/editor/v1/documents/{id}/revisions
    GW->>ES: Proxy + X-User-ID
    ES->>PG: SELECT COUNT(*) FROM documents WHERE id=? AND owner_user_id=?
    alt not owned
        ES-->>GW: 404 DOCUMENT_NOT_FOUND
    else owned
        ES->>PG: SELECT * FROM revisions WHERE document_id=? ORDER BY created_at DESC LIMIT ? OFFSET ?
        ES-->>GW: 200 {data: [Revision], meta}
    end
```

## Readiness probe

```mermaid
sequenceDiagram
    participant K8s
    participant ES as editor-service :8090
    participant PG as PostgreSQL
    participant Redis

    K8s->>ES: GET /readyz
    ES->>PG: ping (2s deadline)
    alt redis configured
        ES->>Redis: PING (2s deadline)
    end
    alt all checks pass
        ES-->>K8s: 200 {status:"ready", checks:{postgres:"ok", redis:"ok"}}
    else any check fails
        ES-->>K8s: 503 {status:"not ready", checks:{...}}
    end
```
