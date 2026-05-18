package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/natsconn"
	"fyredocs/shared/queue"
	"fyredocs/shared/response"

	"billing-service/internal/models"
	"billing-service/internal/plans"
	"billing-service/internal/usageclient"
)

// UsageFetcher is the slice of usageclient.Client billing
// handlers actually use. Stating the interface here (rather than
// taking *usageclient.Client) lets tests inject a stub without
// pulling in a real HTTP transport. main.go wires the real client.
type UsageFetcher interface {
	GetRollup(ctx context.Context, userID, period string) (*usageclient.RollupResponse, error)
}

// Deps carries the request-time dependencies handler functions
// need. Stored on a package-level var so route registration
// stays a plain `r.GET(path, handler)` rather than every handler
// taking the deps as a parameter.
type Deps struct {
	Usage UsageFetcher
}

// deps is the package-private dependency holder. Inject via
// SetDeps from main.go (or tests).
var deps Deps

// SetDeps configures the request-time dependencies. Must be
// called before SetupRouter. A nil Usage leaves usage data out
// of the /v1/billing/me response — useful for tests that don't
// need to mock analytics-service.
func SetDeps(d Deps) { deps = d }

// ListPlans returns the public plan registry. No auth required
// — pricing pages are anonymous-readable.
//
//	GET /v1/billing/plans
func ListPlans(c *gin.Context) {
	response.OK(c, "plans retrieved", gin.H{
		"plans": plans.All(),
	})
}

// MeResponse is the wire shape for /v1/billing/me.
//
// Plan and Subscription describe entitlements. Usage is the
// in-period rollup from analytics-service — may be nil when
// the usage service is unreachable; callers (the UI) render a
// "usage unavailable" badge in that case rather than the whole
// page failing.
type MeResponse struct {
	Plan         plans.Plan                  `json:"plan"`
	Subscription *models.Subscription        `json:"subscription,omitempty"`
	Usage        *usageclient.RollupResponse `json:"usage,omitempty"`
}

// Me returns the calling user's plan, subscription record (if
// any), and current-period usage rollup.
//
//	GET /v1/billing/me
//
// Caller identity: `X-User-ID` header set by api-gateway after
// JWT verification — same pattern as analytics-service.
func Me(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}

	sub, err := findSubscription(c.Request.Context(), userID)
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}

	// Plan: from subscription row if present, else default Free.
	plan := plans.DefaultPlan()
	if sub != nil {
		if p, found := plans.Lookup(sub.PlanCode); found {
			plan = p
		}
	}

	resp := MeResponse{Plan: plan, Subscription: sub}
	if deps.Usage != nil {
		rollup, err := deps.Usage.GetRollup(c.Request.Context(), userID.String(), "")
		if err != nil {
			// Usage data is non-critical; log + omit rather
			// than failing the billing page.
			slog.Warn("billing: usage fetch failed", "user", userID, "error", err)
		} else {
			resp.Usage = rollup
		}
	}
	response.OK(c, "billing summary retrieved", resp)
}

// SubscribeRequest is the wire shape for POST /v1/billing/me/subscribe.
//
// v0: no Stripe — we just record the desired plan. The Stripe
// integration follow-up will graduate this to a real
// PaymentIntent flow with confirmation webhooks.
type SubscribeRequest struct {
	PlanCode string `json:"planCode"`
	Seats    int    `json:"seats,omitempty"`
}

// Subscribe records (or updates) the calling user's plan.
//
//	POST /v1/billing/me/subscribe
//
// Returns the resulting Subscription. Enterprise + any future
// "sales-led" plans refuse via 400 INVALID_PLAN ("contact sales").
func Subscribe(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	var req SubscribeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", "Malformed request body")
		return
	}
	req.PlanCode = strings.TrimSpace(req.PlanCode)
	plan, ok := plans.Lookup(req.PlanCode)
	if !ok {
		response.BadRequest(c, "INVALID_PLAN", "Unknown plan code")
		return
	}
	if !plan.SelfServe {
		response.BadRequest(c, "INVALID_PLAN", "This plan is not available via self-serve; please contact sales.")
		return
	}
	seats := req.Seats
	if seats < 1 {
		seats = 1
	}
	if !plan.PerSeat && seats > 1 {
		response.BadRequest(c, "INVALID_PLAN", "This plan does not support multiple seats")
		return
	}

	// Upsert. The unique index on user_id guarantees we either
	// INSERT a fresh row or UPDATE the existing one.
	sub, err := findSubscription(c.Request.Context(), userID)
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}
	now := time.Now().UTC()
	if sub == nil {
		newSub := models.Subscription{
			UserID:             userID,
			PlanCode:           plan.Code,
			Status:             models.SubStatusActive,
			Seats:              seats,
			CurrentPeriodStart: now,
		}
		if err := models.DB.WithContext(c.Request.Context()).Create(&newSub).Error; err != nil {
			response.InternalErrorf(c, "DB_FAILED", "Could not create subscription", err)
			return
		}
		publishSubscriptionAudit(c.Request.Context(), userID, "subscription.created", "", plan.Code, seats)
		publishSubscriptionDomainEvent(c.Request.Context(), userID, "subscription.created",
			subscriptionFanoutPayload{NewPlan: plan.Code, Seats: seats})
		response.Created(c, "subscription created", newSub)
		return
	}
	// Existing row: update plan + seats + period reset. v0
	// resets the period on every change — Stripe will replace
	// this with proration when wired.
	updates := map[string]any{
		"plan_code":            plan.Code,
		"status":               models.SubStatusActive,
		"seats":                seats,
		"current_period_start": now,
		"current_period_end":   nextMonthStart(now),
		"updated_at":           now,
	}
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Subscription{}).
		Where("user_id = ?", userID).
		Updates(updates).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not update subscription", err)
		return
	}
	// Reload to return the canonical post-update state.
	refreshed, _ := findSubscription(c.Request.Context(), userID)
	publishSubscriptionAudit(c.Request.Context(), userID, "subscription.changed", sub.PlanCode, plan.Code, seats)
	publishSubscriptionDomainEvent(c.Request.Context(), userID, "subscription.changed",
		subscriptionFanoutPayload{OldPlan: sub.PlanCode, NewPlan: plan.Code, Seats: seats})
	response.OK(c, "subscription updated", refreshed)
}

// publishSubscriptionAudit emits one tamper-evident audit row
// for every self-serve plan switch — both first-time subscribe
// (`subscription.created`, `oldPlan` empty) and existing-row
// update (`subscription.changed`, `oldPlan` populated).
//
// The plan change is also audited by auth-service's
// `plan.changed` event — billing-service publishes
// `subscription.*` separately because the data shape is
// different (we know seats; auth-service doesn't), and the
// auditor query for "all billing changes" is cleaner with a
// dedicated action namespace.
//
// Best-effort: NATS down → log + drop. The Subscription row is
// already committed at this point; missing the audit is a SEV2
// for on-call (the hash chain has a gap) but not a 5xx for the
// user.
func publishSubscriptionAudit(ctx context.Context, userID uuid.UUID, action, oldPlan, newPlan string, seats int) {
	if natsconn.JS == nil {
		return
	}
	meta, _ := json.Marshal(map[string]any{
		"oldPlan": oldPlan,
		"newPlan": newPlan,
		"seats":   seats,
	})
	event := queue.AuditEvent{
		Actor:      userID.String(),
		Action:     action,
		Resource:   "", // user's only own subscription — userID alone identifies it
		Metadata:   meta,
		OccurredAt: time.Now().UTC(),
	}
	if err := queue.PublishAuditEvent(ctx, natsconn.JS, event); err != nil {
		slog.Warn("billing: audit publish failed",
			"action", action, "userId", userID, "error", err)
	}
}

// subscriptionFanoutPayload is the public payload every
// `subscription.*` DomainEvent carries. Distinct from the
// internal AuditEvent metadata — `oldPlan` / `newPlan` are
// shaped for what external integrations (Zapier, customer
// scripts) actually want to surface, and we DON'T leak Stripe
// customer / subscription IDs (those are billing's internal
// audit trail).
//
// omitempty keeps `subscription.created` events lean (no
// oldPlan) and `subscription.canceled` events lean (no seats
// change context).
type subscriptionFanoutPayload struct {
	NewPlan string `json:"newPlan,omitempty"`
	OldPlan string `json:"oldPlan,omitempty"`
	Seats   int    `json:"seats,omitempty"`
}

// publishSubscriptionDomainEvent emits the public-facing
// `subscription.*` DomainEvent on the NOTIFY stream so
// notify-service's fanout consumer can deliver one webhook
// per matching `WebhookSubscription`. Distinct from the audit
// path (which writes to the tamper-evident audit log).
//
// `eventType` is one of `subscription.created` /
// `subscription.changed` / `subscription.canceled` — the same
// strings used in the audit `action` for the first two, so an
// operator scanning logs sees the same identifier across
// audit + fanout pipelines.
//
// Best-effort: NATS down / publish failure logs at Warn and
// drops. The Subscription row is already committed; missing
// the fanout is recoverable (a subscriber that didn't fire
// can refetch state via `/v1/billing/me`).
func publishSubscriptionDomainEvent(ctx context.Context, userID uuid.UUID, eventType string, payload subscriptionFanoutPayload) {
	if natsconn.JS == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("billing: fanout marshal failed",
			"event_type", eventType, "userId", userID, "error", err)
		return
	}
	event := queue.DomainEvent{
		EventType:  eventType,
		UserID:     userID.String(),
		OccurredAt: time.Now().UTC(),
		Data:       data,
	}
	if err := queue.PublishDomainEvent(ctx, natsconn.JS, event); err != nil {
		slog.Warn("billing: fanout publish failed",
			"event_type", eventType, "userId", userID, "error", err)
	}
}

// --- helpers ---------------------------------------------------------------

func requireUserID(c *gin.Context) (uuid.UUID, bool) {
	raw := strings.TrimSpace(c.GetHeader("X-User-ID"))
	if raw == "" {
		response.Err(c, http.StatusUnauthorized, "UNAUTHORIZED", "Please log in.")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_USER", "Caller identity is malformed.")
		return uuid.Nil, false
	}
	return id, true
}

func findSubscription(ctx context.Context, userID uuid.UUID) (*models.Subscription, error) {
	var sub models.Subscription
	err := models.DB.WithContext(ctx).Where("user_id = ?", userID).First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func nextMonthStart(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
}
