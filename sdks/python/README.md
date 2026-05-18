# fyredocs (Python)

Official Python SDK for the Fyredocs API. Sister to [`@fyredocs/sdk`](../typescript/) (TypeScript) and [`fyredocs-go`](../go/) (Go) — same envelope-unwrapping, same Bearer header, same wire shapes.

## Install

```bash
pip install fyredocs
```

Zero third-party install-time deps. Uses stdlib (`urllib`) for HTTP and `dataclasses` for types — aligns with the TS SDK (`fetch`) and Go SDK (`net/http`).

## Quick start

```python
import os
from fyredocs import Client
from fyredocs._types import OP_PAGE_ROTATE, OP_REDACT_APPLY

client = Client(api_key=os.environ["FYREDOCS_KEY"])

# Snake_case methods, dataclass returns.
me = client.billing.me()
print(f"Plan: {me.plan.name} — status: {me.subscription.status}")

rev = client.documents.edit("doc_01HV…", ops=[
    {"type": OP_PAGE_ROTATE, "page": 1, "rotation": 90},
    {"type": OP_REDACT_APPLY, "page": 1, "rect": [50, 100, 300, 130]},
])
print(f"New revision: {rev.id}")
```

## Surface

| Namespace | Methods | Endpoints |
|---|---|---|
| `client.api_keys` | `list`, `issue`, `revoke` | `/auth/api-keys/*` |
| `client.billing` | `plans`, `me`, `subscribe` | `/api/billing/v1/billing/*` |
| `client.usage` | `me` | `/v1/usage/me` |
| `client.documents` | `list`, `get`, `revisions`, `edit`, `download`, `delete` | `/api/editor/v1/documents/*` |

Use `client.request(...)` and `client.request_stream(...)` directly to hit endpoints the SDK doesn't yet wrap — both go through the same envelope-unwrapping / Bearer-auth / `FyredocsError` mapping.

## Errors

Every API call returns either a typed result or raises `FyredocsError`. Inspect status + code without parsing the message:

```python
from fyredocs import FyredocsError

try:
    client.documents.get(doc_id)
except FyredocsError as e:
    if e.status == 401:
        # re-auth
        ...
    elif e.status == 429:
        # back off
        ...
    elif e.code == "INVALID_INPUT":
        # user-fixable
        ...
```

`status == 0` means the request never reached the server (network failure, DNS, timeout); `code` is then `NETWORK` or `READ_FAILED`.

## Edit ops

`client.documents.edit(...)` takes an iterable of plain dicts so new server-side op types don't force an SDK release. Use the `OP_*` string constants in `fyredocs._types` for compile-time-checkable type names:

```python
from fyredocs._types import (
    OP_PAGE_ROTATE, OP_PAGE_INSERT, OP_ANNOTATION_ADD,
    OP_TEXT_REPLACE, OP_TEXT_INSERT, OP_REDACT_APPLY, OP_TABLE_CELL_EDIT,
)

ops = [
    {"type": OP_PAGE_ROTATE, "page": 1, "rotation": 90},
    {"type": OP_PAGE_INSERT, "afterPage": 2},
    {
        "type": OP_ANNOTATION_ADD,
        "page": 1, "kind": "highlight",
        "rect": [50, 100, 300, 120],
        "color": [1, 0.92, 0.23],
    },
    {"type": OP_TEXT_REPLACE, "page": 1, "find": "DRAFT", "replace": "FINAL"},
    {"type": OP_REDACT_APPLY, "page": 1, "rect": [60, 200, 400, 220]},
    {
        "type": OP_TEXT_INSERT,
        "page": 1, "x": 100, "y": 700,
        "text": "Reviewed by ops",
        "font": "F1", "sizePt": 12,
    },
    {"type": OP_TABLE_CELL_EDIT, "page": 2, "rect": [72, 600, 240, 620], "text": "New value"},
    # table.cell.edit also accepts a coord form: pass the
    # table's overall bounding box + cell (row, col) and the
    # server runs spdom.DetectTableGrid to snap to the cell.
    # Useful when the caller has the table's bbox but doesn't
    # want to compute per-cell rects. Rect form (above) wins
    # precedence when both are supplied.
    {
        "type": OP_TABLE_CELL_EDIT,
        "page": 2,
        "region": [72, 540, 540, 700],
        "row": 1,
        "col": 2,
        "text": "$950",
    },
]
client.documents.edit(doc_id, ops=ops)
```

The full per-op field grammar is in [`docs/developer/swagger/openapi.yaml`](../../docs/developer/swagger/openapi.yaml).

## Streaming downloads

```python
with open("revision.pdf", "wb") as f:
    client.documents.download(doc_id, rev_id="rev_xyz", dst=f)
```

`dst` accepts any binary file-like object (open file, `io.BytesIO`, a socket wrapper). When omitted, the body is returned as one `bytes` value — fine for small documents, use `dst` for large ones.

## Versioning

v0.x — surface may change between minor versions while the API contract stabilises. v1.0 will pin the surface and follow [semver](https://semver.org/).

## Development

```bash
cd sdks/python
python3 -m venv .venv && source .venv/bin/activate
pip install -e ".[dev]"
PYTHONPATH=src python3 -m pytest tests/ -v
```

Tests use stdlib `http.server` for the API stub — no third-party test dependencies beyond `pytest`.
