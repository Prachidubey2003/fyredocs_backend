# fyredocs-go

Official Go SDK for the Fyredocs API. Sister to [`@fyredocs/sdk`](../typescript/) (TypeScript) — same envelope-unwrapping, same Bearer header, same error mapping, same wire shapes.

## Install

```bash
go get github.com/fyredocs/fyredocs-go
```

Inside this repo, the package is wired into [`go.work`](../../go.work) so editor-service / CLI / etc. can import it without a release cycle.

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/fyredocs/fyredocs-go"
)

func main() {
    client := fyredocs.New(fyredocs.Options{
        APIKey: os.Getenv("FYREDOCS_KEY"),
    })

    ctx := context.Background()

    me, err := client.Billing.Me(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Plan: %s — status: %s\n", me.Plan.Name, me.Subscription.Status)

    rev, err := client.Documents.Edit(ctx, "doc_01HV…", fyredocs.EditRequest{
        Ops: []fyredocs.EditorOp{
            {Type: fyredocs.OpPageRotate, Page: 1, Rotation: 90},
            {Type: fyredocs.OpRedactApply, Page: 1, Rect: []float64{50, 100, 300, 130}},
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("New revision:", rev.ID)
}
```

## Surface

| Namespace | Methods | Endpoints |
|---|---|---|
| `client.APIKeys` | `List`, `Issue`, `Revoke` | `/auth/api-keys/*` |
| `client.Billing` | `Plans`, `Me`, `Subscribe` | `/api/billing/v1/billing/*` |
| `client.Usage` | `Me` | `/v1/usage/me` |
| `client.Documents` | `List`, `Get`, `Revisions`, `Edit`, `Download`, `Delete` | `/api/editor/v1/documents/*` |

Use `client.Request(...)` and `client.RequestStream(...)` directly to hit endpoints the SDK doesn't yet wrap — both go through the same envelope-unwrapping / Bearer-auth / `*Error` mapping logic.

## Errors

Every API call returns either a typed result or an `*fyredocs.Error`. Use `errors.As` to inspect status + code without parsing the message:

```go
_, err := client.Documents.Get(ctx, id)
var apiErr *fyredocs.Error
if errors.As(err, &apiErr) {
    switch {
    case apiErr.Status == 401:
        // re-auth flow
    case apiErr.Status == 429:
        // back off
    case apiErr.Code == "INVALID_INPUT":
        // user-fixable
    }
}
```

`Status == 0` means the request never reached the server (network failure, DNS, timeout); `Code` is then `NETWORK` or `READ_FAILED`.

## Edit ops

`EditorOp` is one flat struct rather than a discriminated-union interface — fields irrelevant to a given `Type` are zero-valued and skipped on the wire via `omitempty`. Use the `Op*` constants for `Type` so the compiler catches typos:

```go
afterPage := 2
xPtr, yPtr := 100.0, 700.0
ops := []fyredocs.EditorOp{
    {Type: fyredocs.OpPageRotate, Page: 1, Rotation: 90},
    {Type: fyredocs.OpPageInsert, AfterPage: &afterPage},
    {
        Type: fyredocs.OpAnnotationAdd,
        Page: 1, Kind: "highlight",
        Rect:  []float64{50, 100, 300, 120},
        Color: []float64{1, 0.92, 0.23},
    },
    {Type: fyredocs.OpTextReplace, Page: 1, Find: "DRAFT", Replace: "FINAL"},
    {Type: fyredocs.OpRedactApply, Page: 1, Rect: []float64{60, 200, 400, 220}},
    {
        Type:   fyredocs.OpTextInsert,
        Page:   1, X: &xPtr, Y: &yPtr,
        Text:   "Reviewed by ops",
        Font:   "F1",
        SizePt: 12,
    },
    {Type: fyredocs.OpTableCellEdit, Page: 2, Rect: []float64{72, 600, 240, 620}, Text: "New value"},
    // table.cell.edit also accepts a coord form — pass the
    // table's bounding box + cell (row, col) instead of a
    // per-cell rect. The server runs DetectTableGrid and snaps.
    // Row/Col are *int so JSON `0` is distinguishable from
    // missing — (0, 0) is a legal top-left selection.
    {
        Type:   fyredocs.OpTableCellEdit,
        Page:   2,
        Region: []float64{72, 540, 540, 700},
        Row:    intPtr(1),
        Col:    intPtr(2),
        Text:   "$950",
    },
}
// helper used above: func intPtr(v int) *int { return &v }
```

The full per-op field grammar is in [`docs/developer/swagger/openapi.yaml`](../../docs/developer/swagger/openapi.yaml).

## Versioning

v0.x — surface may change between minor versions while the API contract stabilises. v1.0 will pin the surface and follow [semver](https://semver.org/).
