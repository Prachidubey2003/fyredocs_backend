package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"billing-service/internal/models"
	"billing-service/internal/plans"
	"billing-service/internal/revshare"
	"billing-service/internal/stripeauth"
)

// stripeMaxBodyBytes caps the inbound webhook payload. Stripe's
// own docs say events stay under 1MB; 2MB gives ample headroom
// while still blocking a misconfigured proxy or an attacker
// from streaming us a 10GB body before signature verification
// has a chance to reject the request.
const stripeMaxBodyBytes = 2 * 1024 * 1024

// stripeSecretEnvVar holds the webhook signing secret (Stripe
// Dashboard → Webhooks → "Signing secret"). Empty / unset means
// the webhook endpoint refuses every request — the alternative
// (accepting unsigned) would invite a forged-event attack.
const stripeSecretEnvVar = "STRIPE_WEBHOOK_SECRET"

// stripeEvent is the slice of Stripe's event envelope this
// service actually reads. Stripe's full schema is enormous; we
// decode only what we dispatch on. The `data.object` JSON is
// kept as raw bytes and decoded by each handler into the
// appropriate sub-shape — keeps the cold-path of unknown event
// types fast.
type stripeEvent struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	LiveMode bool            `json:"livemode"`
	Data     struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// stripeSubscriptionObject decodes `data.object` for the
// `customer.subscription.*` event family. Only the fields the
// handler actually reads land here — Stripe sends ~40 fields,
// but dispatching needs ~6.
type stripeSubscriptionObject struct {
	ID       string `json:"id"`       // sub_...
	Customer string `json:"customer"` // cus_...
	Status   string `json:"status"`   // active / past_due / canceled / trialing / incomplete / unpaid
	// Metadata is the caller-supplied key/value bag set when
	// the Subscription was created in Stripe (typically via
	// Checkout Session). We require `user_id` and `plan_code`
	// to be set so the webhook can resolve to a Subscription
	// row. Stripe enforces ≤ 500-char values, ≤ 50 keys.
	Metadata map[string]string `json:"metadata"`
	// CurrentPeriodStart/End are unix-seconds timestamps.
	// Stripe sends them on every subscription event.
	CurrentPeriodStart int64 `json:"current_period_start"`
	CurrentPeriodEnd   int64 `json:"current_period_end"`
}

// stripeChargeObject decodes `data.object` for the
// `charge.*` event family. Only the fields the marketplace
// revshare path reads land here. Stripe's full Charge has
// ~80 fields — we keep ours minimal so a schema change at
// Stripe doesn't break the decode.
//
// Marketplace charges carry two metadata keys we depend on:
//   - `plugin_id`        : the plugin earning the share
//   - `developer_user_id`: UUID of the payee
// Non-marketplace charges (the subscription path goes through
// invoice.paid, not charge.succeeded for our recordkeeping)
// have neither, and the handler skips them silently.
type stripeChargeObject struct {
	ID       string            `json:"id"`       // ch_...
	Amount   int64             `json:"amount"`   // total in smallest currency unit
	Currency string            `json:"currency"` // lowercase ISO-4217
	Customer string            `json:"customer"` // cus_... (may be empty for guest charges)
	Metadata map[string]string `json:"metadata"`
	// BalanceTransaction is `btx_…` — Stripe sends it as a
	// non-expanded string id on webhook payloads (the full
	// object is available via a separate API call). When
	// present we look it up to populate
	// `revshare_entries.stripe_fee_cents`. Empty on test
	// payouts or refunds-prior-to-balance-posting; the handler
	// degrades gracefully.
	BalanceTransaction string `json:"balance_transaction"`
}

// stripeInvoiceObject decodes `data.object` for the
// `invoice.*` event family. Used by the payment-success +
// payment-failure handlers.
type stripeInvoiceObject struct {
	Customer     string `json:"customer"`
	Subscription string `json:"subscription"` // optional — one-off invoices have none
}

// StripeWebhook handles POST /v1/billing/stripe/webhook.
//
// The api-gateway must NOT JWT-authenticate this route —
// Stripe's HMAC signature is the auth. The route lives under
// /v1/billing/ for path-namespace consistency; the gateway's
// route table is responsible for routing it to billing-service
// while skipping the auth middleware.
//
// Response policy:
//   - 200 on every successfully-verified event, including
//     ones we choose to ignore. Stripe retries on any non-2xx
//     for 3 days; turning unknown events into 200 keeps the
//     retry queue from growing on every type we don't yet
//     handle.
//   - 4xx only for signature failures (Stripe stops retrying
//     after a 400 — that's the right outcome for a bad sig).
//   - 5xx for DB failures (Stripe will retry — appropriate).
//
// Idempotency is via the `processed_stripe_events` table:
// the handler INSERTs the event id before applying side
// effects; a duplicate hits the unique-constraint path and
// returns 200 without re-processing.
func StripeWebhook(c *gin.Context) {
	secret := os.Getenv(stripeSecretEnvVar)
	if secret == "" {
		// Operator misconfiguration: a webhook endpoint without
		// a signing secret would accept every forged request.
		// Fail loud rather than fall open.
		response.Err(c, http.StatusServiceUnavailable, "WEBHOOK_DISABLED",
			"Stripe webhook is not configured on this server.")
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, stripeMaxBodyBytes+1))
	if err != nil {
		response.BadRequest(c, "READ_FAILED", "Could not read webhook body.")
		return
	}
	if len(body) > stripeMaxBodyBytes {
		response.BadRequest(c, "BODY_TOO_LARGE", "Webhook payload exceeds size cap.")
		return
	}

	if err := stripeauth.Verify(
		c.GetHeader("Stripe-Signature"), body, secret, time.Now(), 0,
	); err != nil {
		// All signature failures look the same to Stripe (a
		// 400) — never leak which class of failure it was. An
		// attacker probing for "wrong secret" vs "stale
		// timestamp" gets identical responses.
		slog.Warn("billing-service: stripe signature rejected", "error", err)
		response.BadRequest(c, "BAD_SIGNATURE", "Webhook signature verification failed.")
		return
	}

	var evt stripeEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		response.BadRequest(c, "BAD_PAYLOAD", "Webhook payload is not valid JSON.")
		return
	}
	if evt.ID == "" || evt.Type == "" {
		response.BadRequest(c, "BAD_PAYLOAD", "Webhook payload missing id or type.")
		return
	}

	// Idempotency gate. Insert-or-skip. The unique PK on
	// event_id means a re-delivery surfaces as a duplicate-key
	// error, which we translate to "already processed → 200".
	row := models.ProcessedStripeEvent{
		EventID:     evt.ID,
		EventType:   evt.Type,
		ProcessedAt: time.Now().UTC(),
	}
	createErr := models.DB.WithContext(c.Request.Context()).Create(&row).Error
	if createErr != nil {
		if isDuplicateKey(createErr) {
			slog.Info("billing-service: stripe event already processed",
				"event_id", evt.ID, "type", evt.Type)
			c.JSON(http.StatusOK, gin.H{"received": true, "duplicate": true})
			return
		}
		response.InternalErrorf(c, "DB_FAILED", "Could not record event id", createErr,
			"event_id", evt.ID, "type", evt.Type)
		return
	}

	// Dispatch. Unknown types are recorded above and ack'd
	// here — Stripe stops retrying once we 200.
	if err := dispatchStripeEvent(c.Request.Context(), evt); err != nil {
		// Roll back the idempotency row so Stripe's retry
		// gets another shot at applying the side-effect.
		// Leaving the row would silently skip the retry.
		_ = models.DB.WithContext(c.Request.Context()).
			Where("event_id = ?", evt.ID).
			Delete(&models.ProcessedStripeEvent{}).Error
		response.InternalErrorf(c, "DISPATCH_FAILED", "Could not process Stripe event", err,
			"event_id", evt.ID, "type", evt.Type)
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// dispatchStripeEvent applies the side-effect for `evt` and
// returns any non-recoverable error. Recoverable "no mapping"
// cases (e.g., subscription event for a Stripe customer that
// has no matching row here) return nil — the side-effect is
// "nothing to do", not "retry forever".
func dispatchStripeEvent(ctx context.Context, evt stripeEvent) error {
	switch evt.Type {
	case "customer.subscription.created", "customer.subscription.updated":
		return handleSubscriptionUpsert(ctx, evt)
	case "customer.subscription.deleted":
		return handleSubscriptionDeleted(ctx, evt)
	case "invoice.payment_failed":
		return handleInvoicePaymentFailed(ctx, evt)
	case "invoice.paid":
		return handleInvoicePaid(ctx, evt)
	case "charge.succeeded":
		return handleChargeSucceeded(ctx, evt)
	default:
		// Recorded + ack'd. Adding a new event type is a
		// switch-case edit + a test.
		return nil
	}
}

// handleSubscriptionUpsert applies a customer.subscription.created
// or .updated event. Find-or-create by stripe_subscription_id;
// fall back to metadata.user_id when the row doesn't yet have
// the Stripe link (first event after Checkout completes).
func handleSubscriptionUpsert(ctx context.Context, evt stripeEvent) error {
	var so stripeSubscriptionObject
	if err := json.Unmarshal(evt.Data.Object, &so); err != nil {
		return err
	}
	if so.ID == "" {
		return errors.New("stripe webhook: subscription event missing id")
	}

	planCode := so.Metadata["plan_code"]
	if planCode == "" {
		// Without a plan_code we can't pick the right tier.
		// Skip rather than guess — operator gets the event
		// in the processed log but no row mutation. Surfacing
		// this as an error would cause Stripe to retry
		// indefinitely; missing metadata is a misconfiguration
		// at checkout time, not a transient failure.
		slog.Warn("billing-service: stripe sub event missing plan_code metadata",
			"event_id", evt.ID, "stripe_sub_id", so.ID)
		return nil
	}
	if _, ok := plans.Lookup(planCode); !ok {
		slog.Warn("billing-service: stripe sub event names unknown plan_code",
			"event_id", evt.ID, "plan_code", planCode)
		return nil
	}

	sub, err := loadSubByStripeOrUserMeta(ctx, so)
	if err != nil {
		return err
	}

	periodStart := time.Unix(so.CurrentPeriodStart, 0).UTC()
	periodEnd := time.Unix(so.CurrentPeriodEnd, 0).UTC()
	status := mapStripeStatus(so.Status)
	now := time.Now().UTC()
	stripeSubID := so.ID
	stripeCustID := so.Customer

	if sub == nil {
		// No row yet. Need a user_id from metadata to anchor
		// the new row. Without it we're stuck — log + skip.
		userIDStr := so.Metadata["user_id"]
		if userIDStr == "" {
			slog.Warn("billing-service: stripe sub event has no row to update + no user_id metadata to seed one",
				"event_id", evt.ID, "stripe_sub_id", so.ID)
			return nil
		}
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			slog.Warn("billing-service: stripe sub event has malformed user_id metadata",
				"event_id", evt.ID, "user_id", userIDStr)
			return nil
		}
		newSub := models.Subscription{
			UserID:               userID,
			PlanCode:             planCode,
			Status:               status,
			Seats:                1,
			CurrentPeriodStart:   periodStart,
			CurrentPeriodEnd:     periodEnd,
			StripeSubscriptionID: &stripeSubID,
			StripeCustomerID:     &stripeCustID,
		}
		if err := models.DB.WithContext(ctx).Create(&newSub).Error; err != nil {
			return err
		}
		// Fan out — Stripe-side subscription creation flows
		// through the same public `subscription.created` event
		// as a self-serve /me/subscribe call. Subscribers see
		// one event regardless of which path landed the row.
		publishSubscriptionDomainEvent(ctx, userID, "subscription.created",
			subscriptionFanoutPayload{NewPlan: planCode, Seats: 1})
		return nil
	}

	oldPlan := sub.PlanCode
	updates := map[string]any{
		"plan_code":              planCode,
		"status":                 status,
		"current_period_start":   periodStart,
		"current_period_end":     periodEnd,
		"stripe_subscription_id": stripeSubID,
		"stripe_customer_id":     stripeCustID,
		"updated_at":             now,
	}
	if err := models.DB.WithContext(ctx).
		Model(&models.Subscription{}).
		Where("id = ?", sub.ID).
		Updates(updates).Error; err != nil {
		return err
	}
	// Stripe customer.subscription.updated → public
	// subscription.changed. We don't emit a fanout for status-
	// only flips (active ↔ past_due ↔ canceled) — those have
	// their own dedicated events (`subscription.canceled`,
	// future `subscription.past_due`); a plan-code change is
	// the user-meaningful payload here.
	if oldPlan != planCode {
		publishSubscriptionDomainEvent(ctx, sub.UserID, "subscription.changed",
			subscriptionFanoutPayload{OldPlan: oldPlan, NewPlan: planCode, Seats: sub.Seats})
	}
	return nil
}

func handleSubscriptionDeleted(ctx context.Context, evt stripeEvent) error {
	var so stripeSubscriptionObject
	if err := json.Unmarshal(evt.Data.Object, &so); err != nil {
		return err
	}
	sub, err := loadSubByStripeOrUserMeta(ctx, so)
	if err != nil {
		return err
	}
	if sub == nil {
		slog.Warn("billing-service: stripe subscription deleted but no row matches",
			"event_id", evt.ID, "stripe_sub_id", so.ID)
		return nil
	}
	if err := models.DB.WithContext(ctx).
		Model(&models.Subscription{}).
		Where("id = ?", sub.ID).
		Updates(map[string]any{
			"status":     models.SubStatusCanceled,
			"updated_at": time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	// Fan out — subscribers (Zapier, customer-side integrations)
	// commonly want to know when a paid subscription ends so
	// they can clean up downstream resources. Carries the
	// previous plan_code so the subscriber knows what the user
	// just left.
	publishSubscriptionDomainEvent(ctx, sub.UserID, "subscription.canceled",
		subscriptionFanoutPayload{OldPlan: sub.PlanCode, Seats: sub.Seats})
	return nil
}

func handleInvoicePaymentFailed(ctx context.Context, evt stripeEvent) error {
	sub, err := loadSubByInvoice(ctx, evt)
	if err != nil || sub == nil {
		return err
	}
	return models.DB.WithContext(ctx).
		Model(&models.Subscription{}).
		Where("id = ?", sub.ID).
		Updates(map[string]any{
			"status":     models.SubStatusPastDue,
			"updated_at": time.Now().UTC(),
		}).Error
}

// handleInvoicePaid restores a subscription out of past_due
// when Stripe confirms the retry payment succeeded. Doesn't
// touch active rows — they stay active.
func handleInvoicePaid(ctx context.Context, evt stripeEvent) error {
	sub, err := loadSubByInvoice(ctx, evt)
	if err != nil || sub == nil {
		return err
	}
	if sub.Status != models.SubStatusPastDue {
		return nil
	}
	return models.DB.WithContext(ctx).
		Model(&models.Subscription{}).
		Where("id = ?", sub.ID).
		Updates(map[string]any{
			"status":     models.SubStatusActive,
			"updated_at": time.Now().UTC(),
		}).Error
}

// handleChargeSucceeded records a `revshare_entries` row when
// the charge carries marketplace metadata (plugin_id +
// developer_user_id). Subscription charges have neither and
// skip silently — those flow through invoice.paid for our
// bookkeeping.
//
// Idempotency: revshare.Record dedupes on (source,
// source_ref) where source_ref is the charge id. A
// re-delivered charge.succeeded surfaces as ErrDuplicateSource
// which we treat as a no-op success.
//
// Skip semantics (Ack'd at the outer handler, NO row mutation):
//   - missing or non-marketplace metadata.
//   - malformed developer_user_id UUID.
//   - calculate failure (zero gross, missing currency, …) —
//     these would be publisher bugs at the Stripe-Checkout
//     side; logging + skipping prevents an unrecoverable
//     retry loop.
//
// Returns a non-nil error only for transient DB failures so
// JetStream retries land the entry.
func handleChargeSucceeded(ctx context.Context, evt stripeEvent) error {
	var ch stripeChargeObject
	if err := json.Unmarshal(evt.Data.Object, &ch); err != nil {
		return err
	}
	pluginID := ch.Metadata["plugin_id"]
	devUserIDStr := ch.Metadata["developer_user_id"]
	if pluginID == "" || devUserIDStr == "" {
		// Not a marketplace charge — subscription/one-off
		// payments live in invoice.paid, not here.
		return nil
	}
	developerUserID, err := uuid.Parse(devUserIDStr)
	if err != nil {
		slog.Warn("billing-service: charge.succeeded has malformed developer_user_id metadata",
			"event_id", evt.ID, "charge_id", ch.ID, "developer_user_id", devUserIDStr)
		return nil
	}

	tx := revshare.Transaction{
		ID:              ch.ID,
		DeveloperUserID: devUserIDStr,
		PluginID:        pluginID,
		GrossCents:      ch.Amount,
		Currency:        ch.Currency,
		// StripeFeeCents is filled in below from the linked
		// balance_transaction. On lookup failure we fall back
		// to 0 — see lookupChargeFeeCents below for the
		// trade-off (we MUST record the entry; a payout-
		// reconciliation job can backfill the fee later).
	}
	tx.StripeFeeCents = lookupChargeFeeCents(ctx, evt.ID, ch)
	entry, err := revshare.Calculate(tx, revshare.DefaultSplit())
	if err != nil {
		// Calculator-level rejection (zero gross, empty
		// currency). Almost certainly a publisher bug —
		// log + skip so the queue moves on. Re-delivery
		// won't help.
		slog.Warn("billing-service: revshare Calculate refused charge",
			"event_id", evt.ID, "charge_id", ch.ID, "error", err)
		return nil
	}

	_, err = revshare.Record(ctx, models.DB, entry, revshare.PersistOptions{
		DeveloperUserID: developerUserID,
		Source:          "stripe_charge",
		SourceRef:       ch.ID,
	})
	if errors.Is(err, revshare.ErrDuplicateSource) {
		// Stripe redelivered this event — the original
		// recording already landed. processed_stripe_events
		// also dedupes at the event-id level; this is
		// belt-and-suspenders.
		slog.Info("billing-service: revshare entry already recorded",
			"event_id", evt.ID, "charge_id", ch.ID)
		return nil
	}
	return err
}

// lookupChargeFeeCents resolves the Stripe processor fee for
// `ch` by GETting its linked balance_transaction. Returns 0
// (silently) when:
//
//   - the charge has no balance_transaction id (e.g. test
//     payloads, refunds-prior-to-posting), or
//   - the Stripe client is not configured (operator hasn't set
//     STRIPE_API_KEY in this env), or
//   - the Stripe API call fails (network, 5xx, expired key).
//
// In every failure mode we log + record the revshare entry with
// fee=0 rather than block the write. The trade-off:
//
//   - Blocking the write would cause Stripe JetStream redelivery,
//     which the user-facing UI would see as "earnings missing"
//     while we retry forever — much worse UX than a temporarily
//     understated fee that a payout-reconciliation pass corrects
//     later.
//   - The handler is the only place that knows the developer
//     id, so deferring the whole write isn't free either —
//     reconciliation would have to repeat all of revshare.Calc.
//
// This function never returns an error; the slog warning is the
// signal a reconciliation job can scan for.
func lookupChargeFeeCents(ctx context.Context, evtID string, ch stripeChargeObject) int64 {
	if ch.BalanceTransaction == "" {
		return 0
	}
	client, err := stripeFactory()
	if err != nil {
		slog.Warn("billing-service: stripe client unavailable; recording revshare with fee=0",
			"event_id", evtID, "charge_id", ch.ID, "balance_transaction", ch.BalanceTransaction, "error", err)
		return 0
	}
	// Short timeout: the BT lookup is on the webhook hot path
	// (Stripe will redeliver if we hang). 8s is well above
	// Stripe's p99 for this endpoint and leaves room for the
	// surrounding handler to ack within Stripe's 30s window.
	lookupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	bt, err := client.GetBalanceTransaction(lookupCtx, ch.BalanceTransaction)
	if err != nil {
		slog.Warn("billing-service: balance_transaction lookup failed; recording revshare with fee=0",
			"event_id", evtID, "charge_id", ch.ID, "balance_transaction", ch.BalanceTransaction, "error", err)
		return 0
	}
	if bt.Fee < 0 {
		// Refunds invert sign; charge.succeeded fees are
		// always >=0. A negative fee on a succeeded charge is
		// a Stripe-side anomaly — log loud + zero it out.
		slog.Warn("billing-service: balance_transaction returned negative fee; zeroing",
			"event_id", evtID, "charge_id", ch.ID, "balance_transaction", ch.BalanceTransaction, "fee", bt.Fee)
		return 0
	}
	return bt.Fee
}

// loadSubByStripeOrUserMeta tries three lookup paths in order:
//
//  1. By stripe_subscription_id — for events on subscriptions
//     we've previously synced.
//  2. By stripe_customer_id — for the FIRST event after
//     checkout, before any subscription event has populated
//     the link.
//  3. By metadata.user_id — fallback when neither stripe-side
//     link is on the row yet. (Self-serve picks that haven't
//     been migrated to Stripe yet land here.)
//
// Returns (nil, nil) when no row exists — caller treats as
// "skip this event".
func loadSubByStripeOrUserMeta(ctx context.Context, so stripeSubscriptionObject) (*models.Subscription, error) {
	var sub models.Subscription

	err := models.DB.WithContext(ctx).
		Where("stripe_subscription_id = ?", so.ID).
		First(&sub).Error
	if err == nil {
		return &sub, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if so.Customer != "" {
		err = models.DB.WithContext(ctx).
			Where("stripe_customer_id = ?", so.Customer).
			First(&sub).Error
		if err == nil {
			return &sub, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	if userIDStr := so.Metadata["user_id"]; userIDStr != "" {
		userID, perr := uuid.Parse(userIDStr)
		if perr == nil {
			err = models.DB.WithContext(ctx).
				Where("user_id = ?", userID).
				First(&sub).Error
			if err == nil {
				return &sub, nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
	}
	return nil, nil
}

func loadSubByInvoice(ctx context.Context, evt stripeEvent) (*models.Subscription, error) {
	var inv stripeInvoiceObject
	if err := json.Unmarshal(evt.Data.Object, &inv); err != nil {
		return nil, err
	}
	if inv.Subscription != "" {
		var sub models.Subscription
		err := models.DB.WithContext(ctx).
			Where("stripe_subscription_id = ?", inv.Subscription).
			First(&sub).Error
		if err == nil {
			return &sub, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if inv.Customer != "" {
		var sub models.Subscription
		err := models.DB.WithContext(ctx).
			Where("stripe_customer_id = ?", inv.Customer).
			First(&sub).Error
		if err == nil {
			return &sub, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, nil
}

// mapStripeStatus normalises Stripe's eight subscription
// status values into our three. `incomplete` / `unpaid` /
// `incomplete_expired` map to past_due because they all
// represent payment-failure states; `trialing` maps to active
// (entitlements during trial); `canceled` is canceled;
// everything unrecognised → active (defensive default — better
// to keep service than wrongfully terminate).
func mapStripeStatus(s string) string {
	switch s {
	case "canceled":
		return models.SubStatusCanceled
	case "past_due", "incomplete", "unpaid", "incomplete_expired":
		return models.SubStatusPastDue
	case "active", "trialing", "":
		return models.SubStatusActive
	default:
		return models.SubStatusActive
	}
}

// isDuplicateKey is the GORM-agnostic check for "unique
// constraint violation". Postgres returns SQLSTATE 23505;
// SQLite returns "UNIQUE constraint failed". We string-match
// instead of importing pgx errors because the test suite uses
// SQLite and forking the check per driver would just spread
// the same logic.
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsAny(msg,
		"duplicate key",
		"UNIQUE constraint failed",
		"SQLSTATE 23505",
	)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a tiny strings.Index replacement to avoid importing
// strings just for this check — the rest of the file doesn't use
// strings at all.
func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 || n < m {
		if m == 0 {
			return 0
		}
		return -1
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}
