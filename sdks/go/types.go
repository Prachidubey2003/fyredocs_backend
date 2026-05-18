package fyredocs

// Wire-format types for the Fyredocs API. These mirror
// docs/developer/swagger/openapi.yaml and stay in lockstep with
// the TypeScript SDK's types.ts.
//
// Hand-maintained because the API surface is small enough that an
// OpenAPI codegen pipeline would add more friction than it solves
// for v0. Drift is caught by the test suite — every namespace
// method has an httptest fixture exercising the round-trip.

// ---------------------------------------------------------------------------
// API keys (auth-service)
// ---------------------------------------------------------------------------

// APIKeyEnvironment is the kind of traffic a key is allowed to
// see: production ("live") or sandboxed ("test").
type APIKeyEnvironment string

const (
	APIKeyLive APIKeyEnvironment = "live"
	APIKeyTest APIKeyEnvironment = "test"
)

// APIKey is the metadata for an issued key. The plaintext secret
// is only ever returned once, from IssueAPIKeyResponse.Plaintext.
type APIKey struct {
	ID          string            `json:"id"`
	OwnerUserID string            `json:"ownerUserId,omitempty"`
	Name        string            `json:"name"`
	Environment APIKeyEnvironment `json:"environment"`
	KeyPrefix   string            `json:"keyPrefix"`
	Scopes      []string          `json:"scopes,omitempty"`
	CreatedAt   string            `json:"createdAt"`
	LastUsedAt  string            `json:"lastUsedAt,omitempty"`
	RevokedAt   string            `json:"revokedAt,omitempty"`
}

// IssueAPIKeyRequest is the body for APIKeysAPI.Issue.
type IssueAPIKeyRequest struct {
	Name        string            `json:"name"`
	Environment APIKeyEnvironment `json:"environment,omitempty"`
	Scopes      []string          `json:"scopes,omitempty"`
}

// IssueAPIKeyResponse carries the new key's metadata plus the
// plaintext secret. The plaintext is shown EXACTLY ONCE; callers
// must persist it immediately — the server can't recover it after
// this response.
type IssueAPIKeyResponse struct {
	Key       APIKey `json:"key"`
	Plaintext string `json:"plaintext"`
}

// ---------------------------------------------------------------------------
// Billing (billing-service)
// ---------------------------------------------------------------------------

// Plan is a self-serve or sales-led pricing tier.
type Plan struct {
	Code              string         `json:"code"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	MonthlyPriceCents int            `json:"monthlyPriceCents"` // -1 = contact sales
	YearlyPriceCents  int            `json:"yearlyPriceCents,omitempty"`
	PerSeat           bool           `json:"perSeat"`
	SelfServe         bool           `json:"selfServe"`
	Limits            map[string]int `json:"limits"` // -1 = unlimited
}

// Subscription is the calling user's current billing subscription.
type Subscription struct {
	ID                   string `json:"id"`
	UserID               string `json:"userId"`
	PlanCode             string `json:"planCode"`
	Status               string `json:"status"` // "active" | "canceled" | "past_due"
	Seats                int    `json:"seats"`
	CurrentPeriodStart   string `json:"currentPeriodStart"`
	CurrentPeriodEnd     string `json:"currentPeriodEnd"`
	StripeSubscriptionID string `json:"stripeSubscriptionId,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

// UsageRollupRow is one event-type's accumulated metering.
type UsageRollupRow struct {
	EventType     string  `json:"eventType"`
	Unit          string  `json:"unit"`
	TotalQuantity float64 `json:"totalQuantity"`
	EventCount    int     `json:"eventCount"`
}

// UsageRollup wraps a list of UsageRollupRow scoped to one user
// and one billing period.
type UsageRollup struct {
	UserID string           `json:"userId"`
	Period string           `json:"period"`
	Items  []UsageRollupRow `json:"items"`
}

// BillingMeResponse is what BillingAPI.Me returns: a snapshot of
// the calling user's plan + subscription + current usage. Usage
// may be nil if analytics-service was unreachable at fetch time.
type BillingMeResponse struct {
	Plan         Plan          `json:"plan"`
	Subscription *Subscription `json:"subscription,omitempty"`
	Usage        *UsageRollup  `json:"usage,omitempty"`
}

// SubscribeRequest is the body for BillingAPI.Subscribe.
type SubscribeRequest struct {
	PlanCode string `json:"planCode"`
	Seats    int    `json:"seats,omitempty"`
}

// ---------------------------------------------------------------------------
// Editor (editor-service)
// ---------------------------------------------------------------------------

// OpType identifies a single edit-op kind. Use the constants
// (OpPageRotate, OpAnnotationAdd, etc.) rather than raw strings so
// the compiler catches typos.
type OpType string

const (
	OpPageRotate    OpType = "page.rotate"
	OpPageDelete    OpType = "page.delete"
	OpPageInsert    OpType = "page.insert"
	OpAnnotationAdd OpType = "annotation.add"
	OpTextReplace   OpType = "text.replace"
	OpTextInsert    OpType = "text.insert"
	OpTextDelete    OpType = "text.delete"
	OpRedactApply   OpType = "redact.apply"
	OpTableCellEdit OpType = "table.cell.edit"
)

// EditorOp is the union of every sPDOM op the API accepts. It's a
// single struct rather than a discriminated-union interface to
// keep callers' lives simple — the server only inspects the fields
// relevant to Type. Fields irrelevant to a given op type are
// omitted via omitempty. See docs/developer/swagger/openapi.yaml
// for the per-op field requirements.
type EditorOp struct {
	Type OpType `json:"type"`

	// page.rotate / page.delete / annotation.* / text.* / etc.
	Page int `json:"page,omitempty"`

	// page.rotate
	Rotation int `json:"rotation,omitempty"`

	// page.insert — pointer so JSON `0` is distinguishable from
	// "missing" (0 = insert before the first page).
	AfterPage *int `json:"afterPage,omitempty"`

	// annotation.add
	Kind     string      `json:"kind,omitempty"`
	Rect     []float64   `json:"rect,omitempty"` // [x0, y0, x1, y1]
	Color    []float64   `json:"color,omitempty"`
	Contents string      `json:"contents,omitempty"`
	Strokes  [][]float64 `json:"strokes,omitempty"`
	Anchor   *[2]float64 `json:"anchor,omitempty"`

	// text.replace
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`

	// text.insert / table.cell.edit
	Text   string   `json:"text,omitempty"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	Font   string   `json:"font,omitempty"`
	SizePt float64  `json:"sizePt,omitempty"`

	// table.cell.edit (coord form). Use this addressing form
	// when you know the table's overall bounding box but not
	// the per-cell rect — the server runs `spdom.DetectTableGrid`
	// over `Region` and snaps to the cell at `(Row, Col)`. Rect
	// (the field above, shared with annotation.add) takes
	// precedence if both forms are populated.
	//
	// Row + Col are 0-indexed: row 0 is the top, col 0 the
	// leftmost. Pointer-typed so the JSON `0` distinguishes
	// from "missing" — (0, 0) is a legal top-left selection.
	Region []float64 `json:"region,omitempty"`
	Row    *int      `json:"row,omitempty"`
	Col    *int      `json:"col,omitempty"`
}

// EditRequest is the body for DocumentsAPI.Edit.
type EditRequest struct {
	Ops     []EditorOp `json:"ops"`
	Message string     `json:"message,omitempty"`
}

// EditorDocument is the metadata for a single document.
type EditorDocument struct {
	ID           string `json:"id"`
	OwnerUserID  string `json:"ownerUserId,omitempty"`
	Title        string `json:"title"`
	CurrentRevID string `json:"currentRevId,omitempty"`
	StorageKey   string `json:"storageKey,omitempty"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
	PageCount    int    `json:"pageCount,omitempty"`
	Status       string `json:"status,omitempty"` // "ready" | "locked" | "quarantined"
	CreatedAt    string `json:"createdAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

// EditorRevision is one entry in a document's revision history.
type EditorRevision struct {
	ID           string `json:"id"`
	DocumentID   string `json:"documentId"`
	ParentRevID  string `json:"parentRevId,omitempty"`
	AuthorUserID string `json:"authorUserId,omitempty"`
	Message      string `json:"message,omitempty"`
	CreatedAt    string `json:"createdAt,omitempty"`
}

// UsageMeResponse mirrors UsageRollup — the analytics-service
// endpoint returns the same shape but routes through a separate
// path, so we keep a distinct type name for clarity at call sites.
type UsageMeResponse struct {
	UserID string           `json:"userId"`
	Period string           `json:"period"`
	Items  []UsageRollupRow `json:"items"`
}
