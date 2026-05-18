// Package plans is the in-code registry of subscription tiers.
//
// Plans live in source code rather than a `plans` DB table on
// purpose: pricing changes are revenue-critical and the PR review
// flow is a more reviewable change-control mechanism than a SQL
// migration. The Subscription DB table stores a `plan_code`
// reference to entries here; orphaned codes (a user on a since-
// retired plan) are tolerated by [Lookup] returning the zero
// value + ok=false, and the caller falls back to the Free tier.
//
// Per the product plan §7.1 there are five tiers (Free, Pro,
// Teams, Business, Enterprise). Per-seat tiers (Teams, Business)
// price PER USER PER MONTH — billing-service multiplies the
// monthly price by seat count when generating an invoice.
// Enterprise is a custom contract: the Plan entry is present so
// the registry stays complete, but pricing is `null` and
// /v1/billing/me/subscribe refuses to switch a user to it via
// self-serve.
package plans

// Plan is one subscription tier in the public registry.
type Plan struct {
	Code        string           `json:"code"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	// MonthlyPriceCents is the per-user-per-month price in USD
	// cents. -1 = "contact sales" (Enterprise).
	MonthlyPriceCents int  `json:"monthlyPriceCents"`
	YearlyPriceCents  int  `json:"yearlyPriceCents,omitempty"`
	PerSeat           bool `json:"perSeat"`
	// SelfServe gates whether a user can switch to this plan via
	// POST /v1/billing/me/subscribe. Enterprise = false (sales-led).
	SelfServe bool `json:"selfServe"`
	// Limits is a free-form set of per-period caps the rest of
	// the platform enforces at the gateway and worker layers.
	// Keys mirror BillableEvent.EventType: `op.ocr` quota in
	// ops/month, `ai.tokens` in tokens/month, etc.
	Limits map[string]int64 `json:"limits"`
}

// FreeCode / ProCode / etc. are stable identifiers other services
// (gateway rate limiter, plan-limit checks in job-service) match
// against. Don't rename — they're persisted in the subscriptions
// table and used in audit log messages.
const (
	FreeCode       = "free"
	ProCode        = "pro"
	TeamsCode      = "teams"
	BusinessCode   = "business"
	EnterpriseCode = "enterprise"
)

// All returns the full registry in display order (cheapest first,
// Enterprise last). Callers MUST NOT mutate the returned slice;
// it shares the underlying array with the package-private source
// of truth.
func All() []Plan {
	return registry
}

// Lookup returns the Plan for `code`. ok=false when the code is
// unknown — callers (e.g., billing-service handlers) should
// fall back to Free or surface a "subscription on retired plan"
// error per their context.
func Lookup(code string) (Plan, bool) {
	for _, p := range registry {
		if p.Code == code {
			return p, true
		}
	}
	return Plan{}, false
}

// DefaultPlan is the tier a user lands on when no subscription
// row exists yet. Returning a real Plan (not a sentinel) keeps
// the /v1/billing/me response shape uniform whether or not the
// user has explicitly subscribed.
func DefaultPlan() Plan {
	free, _ := Lookup(FreeCode)
	return free
}

// registry is the package-private source of truth. New plans
// MUST be appended (not inserted) so the display order remains
// stable for existing screenshots / docs.
var registry = []Plan{
	{
		Code:              FreeCode,
		Name:              "Free",
		Description:       "5 docs/day, 25MB max, watermark on free conversions.",
		MonthlyPriceCents: 0,
		PerSeat:           false,
		SelfServe:         true,
		Limits: map[string]int64{
			"op.merge":     5,
			"op.split":     5,
			"op.compress":  5,
			"op.ocr":       3,
			"op.edit":      0, // editor is paid-tier only
			"doc.parse":    25,
			"file.bytes":   25 * 1024 * 1024,
		},
	},
	{
		Code:              ProCode,
		Name:              "Pro",
		Description:       "Unlimited utility, 1GB storage, full editor, 100 AI credits/mo.",
		MonthlyPriceCents: 1500, // $15/mo (monthly billing)
		YearlyPriceCents:  14400, // $12/mo when billed annually
		PerSeat:           false,
		SelfServe:         true,
		Limits: map[string]int64{
			"op.merge":    -1, // unlimited
			"op.split":    -1,
			"op.compress": -1,
			"op.ocr":      -1,
			"op.edit":     -1,
			"ai.tokens":   100_000,
			"file.bytes":  1024 * 1024 * 1024,
		},
	},
	{
		Code:              TeamsCode,
		Name:              "Teams",
		Description:       "Collab, comments, shared workspaces, 100GB pooled, 500 AI credits/user/mo.",
		MonthlyPriceCents: 2000, // $20/user/mo
		PerSeat:           true,
		SelfServe:         true,
		Limits: map[string]int64{
			"op.merge":    -1,
			"op.split":    -1,
			"op.compress": -1,
			"op.ocr":      -1,
			"op.edit":     -1,
			"ai.tokens":   500_000,
			"file.bytes":  100 * 1024 * 1024 * 1024,
		},
	},
	{
		Code:              BusinessCode,
		Name:              "Business",
		Description:       "SSO, audit, DLP, advanced workflows, 2000 AI credits/user/mo.",
		MonthlyPriceCents: 3500, // $35/user/mo
		PerSeat:           true,
		SelfServe:         true,
		Limits: map[string]int64{
			"op.merge":    -1,
			"op.split":    -1,
			"op.compress": -1,
			"op.ocr":      -1,
			"op.edit":     -1,
			"ai.tokens":   2_000_000,
			"file.bytes":  -1,
		},
	},
	{
		Code:              EnterpriseCode,
		Name:              "Enterprise",
		Description:       "HIPAA, BYOK, data residency, dedicated support, custom SLA.",
		MonthlyPriceCents: -1, // contact sales
		PerSeat:           true,
		SelfServe:         false,
		Limits:            map[string]int64{}, // negotiated per contract
	},
}
