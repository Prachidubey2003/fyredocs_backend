// Package fanout expands one domain event into per-subscriber
// webhook deliveries. Owned by notify-service; consumed by the
// NATS subscriber that listens on `notify.event.>`.
//
// Flow per event:
//  1. Look up every active WebhookSubscription matching
//     (user_id + event_type). Soft-deleted and `disabled`
//     rows are filtered out by the WHERE clause.
//  2. For each match: recover the plaintext signing secret
//     via encat.OpenSecret + build the public envelope (the
//     same Stripe/GitHub-style shape every subscriber sees) +
//     dispatch a NotifyEvent through the existing webhook
//     channel, passing the per-subscription secret so the
//     HMAC is unique to this subscriber.
//  3. The dispatcher persists one notify_deliveries row per
//     subscription — preserves the existing audit trail.
//
// Why this lives in a package, not inline in the subscriber:
//   - The lookup + sign + dispatch sequence is pure-function
//     friendly (db handle + dispatcher in, slice of Deliveries
//     out) and tests against an in-memory sqlite easily.
//   - Future fanout targets (slack-style per-channel, email
//     templates) plug in here without touching the NATS path.
//
// What this package does NOT do:
//   - Read from NATS. The subscriber wires NATS → fanout.
//   - Retry on per-subscriber failure. The dispatcher records
//     the failure; JetStream-level retry happens at the
//     NATS-message layer, NOT per-subscriber. Re-delivering
//     the domain event re-fans-out to every subscription,
//     including the ones that previously succeeded — the
//     idempotency key on each NotifyEvent prevents a double
//     POST to the ones that already received.
//
// Circuit breaker: every successful delivery resets the row's
// `failure_count` to 0; every failed delivery increments it.
// Past `failureThreshold` consecutive failures the bridge
// flips the row's `status` to `disabled` — the subscription
// stays in the table (so an operator can inspect WHY it
// failed) but the fanout filter excludes it from future
// matches. Re-enabling is a manual operator action (or a
// future "test webhook" endpoint that does the dance for the
// user).
package fanout

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"notify-service/internal/encat"
	"notify-service/internal/metrics"
	"notify-service/internal/models"
)

// FailureThreshold is the number of consecutive failed
// deliveries that flips a subscription's `status` to
// `disabled`. Conservative default — 10 lets a transient
// outage (subscriber's host bouncing, brief DNS hiccup) ride
// through, while a genuinely-broken endpoint (404'd URL,
// permanent 401, wrong domain) gets cut off within an hour
// or so at typical event volumes.
//
// Re-enabling is a manual operator action today. A future
// "test webhook" endpoint will let the owning user un-disable
// after fixing the receiver — tracked separately.
const FailureThreshold = 10

// Dispatcher is the slice of dispatcher.Dispatcher fanout
// actually uses. Stated as an interface so the unit tests can
// drive fanout against a fake recorder without spinning up
// the real persist-then-channel pipeline.
type Dispatcher interface {
	DispatchWithSecret(ctx context.Context, event queue.NotifyEvent, secret []byte) (*models.Delivery, error)
}

// Result is the per-subscription outcome of fanning out one
// event. Returned in slice order matching the matched-
// subscriptions order. Callers use it for tests and for
// audit-line logging.
type Result struct {
	SubscriptionID uuid.UUID
	Delivery       *models.Delivery
	Err            error
}

// Fanout runs the lookup + dispatch sequence for one event.
// Returns a slice with one entry per matched subscription
// (empty when no subscription matched — never an error).
//
// Errors:
//   - Returns a non-nil top-level error only when the DB
//     lookup itself fails. Per-subscription failures (decrypt,
//     channel send) land in the per-result Err and DO NOT
//     abort the rest of the fanout.
//   - When no subscriptions match returns `(nil, nil)`. That's
//     not an error — most domain events have zero
//     subscribers, especially early on.
func Fanout(ctx context.Context, db *gorm.DB, disp Dispatcher, event queue.DomainEvent) ([]Result, error) {
	if disp == nil {
		return nil, errors.New("fanout: dispatcher is required")
	}
	if event.EventID == "" || event.EventType == "" || event.UserID == "" {
		// We don't fail the upstream JetStream message on a
		// malformed event — but we don't silently fan out
		// either. The caller logs + Acks so a bad publisher
		// doesn't block the queue.
		return nil, errors.New("fanout: event missing eventId/eventType/userId")
	}

	userID, err := uuid.Parse(event.UserID)
	if err != nil {
		// Malformed user id is a publisher bug. Return an
		// error so the subscriber logs it; don't poison the
		// queue.
		return nil, errors.New("fanout: event.UserID is not a UUID")
	}

	var subs []models.WebhookSubscription
	err = db.WithContext(ctx).
		Where("user_id = ? AND event_type = ? AND status = ?",
			userID, event.EventType, models.WebhookStatusActive).
		Find(&subs).Error
	if err != nil {
		metrics.FanoutEventsTotal.WithLabelValues(event.EventType, "error").Inc()
		return nil, err
	}
	if len(subs) == 0 {
		// Common case: most users have no webhooks. Counted
		// separately from `matched` so an operator can see
		// the proportion of events with any subscribers.
		metrics.FanoutEventsTotal.WithLabelValues(event.EventType, "no_subscriber").Inc()
		return nil, nil
	}

	envelope, err := json.Marshal(event)
	if err != nil {
		// Shouldn't happen — DomainEvent marshals cleanly.
		// Surfaces here as a DB-error-equivalent so the
		// subscriber Naks.
		metrics.FanoutEventsTotal.WithLabelValues(event.EventType, "error").Inc()
		return nil, err
	}
	metrics.FanoutEventsTotal.WithLabelValues(event.EventType, "matched").Inc()

	out := make([]Result, 0, len(subs))
	for _, sub := range subs {
		result := Result{SubscriptionID: sub.ID}
		secret, openErr := encat.OpenSecret(sub.SecretWrappedDEK, sub.SecretCiphertext)
		if openErr != nil {
			// Likely cause: operator rolled back a KEK
			// without setting the env, OR the row is from a
			// previous KEK generation. Log + skip; never
			// crash the fanout for the other subscriptions.
			slog.Error("fanout: could not recover subscription secret",
				"subscription_id", sub.ID, "user_id", sub.UserID,
				"event_type", event.EventType, "error", openErr)
			metrics.FanoutDeliveriesTotal.WithLabelValues(event.EventType, "skipped").Inc()
			result.Err = openErr
			out = append(out, result)
			continue
		}

		// Idempotency: same domain event id + subscription id
		// collapses to one delivery row. JetStream will
		// re-deliver on Nak; we don't want to double-POST.
		idemKey := "fanout:" + event.EventID + ":" + sub.ID.String()

		delivery, dispErr := disp.DispatchWithSecret(ctx, queue.NotifyEvent{
			Channel:        queue.ChannelWebhook,
			Target:         sub.TargetURL,
			UserID:         event.UserID,
			Payload:        json.RawMessage(envelope),
			IdempotencyKey: idemKey,
			OccurredAt:     event.OccurredAt,
		}, secret)
		result.Delivery = delivery
		result.Err = dispErr
		if dispErr != nil {
			// Persistence failure (e.g., DB write of the
			// audit row failed). NOT a subscriber-side
			// failure — don't touch the circuit-breaker
			// counter. The handler will Nak and JetStream
			// will retry the whole event.
			slog.Error("fanout: dispatch failed",
				"subscription_id", sub.ID, "error", dispErr)
			out = append(out, result)
			continue
		}
		if delivery != nil {
			metrics.FanoutDeliveriesTotal.WithLabelValues(event.EventType, delivery.Status).Inc()
			updateCircuitBreaker(ctx, db, sub.ID, delivery.Status)
		}
		out = append(out, result)
	}
	return out, nil
}

// updateCircuitBreaker reconciles the subscription row's
// `failure_count` / `status` / `last_delivery_at` based on
// the just-persisted Delivery status.
//
//   - `delivered` → reset failure_count to 0, bump last_delivery_at.
//   - `failed`    → increment failure_count, bump last_delivery_at.
//                   If the result is at or past FailureThreshold,
//                   flip status to `disabled` so the next fanout
//                   excludes this row.
//   - Any other status (`pending`, `skipped`) — leave the row
//     alone. `pending` shouldn't reach us (the dispatcher's
//     persistStatus runs before return); `skipped` only fires
//     for unsupported channels which can't happen on the
//     fanout's hard-coded webhook path.
//
// Best-effort: a DB update failure here is logged and
// ignored. The Delivery row is the source of truth for
// what happened; the circuit-breaker counter is a derived
// view that converges on the next event.
func updateCircuitBreaker(ctx context.Context, db *gorm.DB, subID uuid.UUID, status string) {
	if db == nil {
		return
	}
	now := time.Now().UTC()
	switch status {
	case models.StatusDelivered:
		err := db.WithContext(ctx).
			Model(&models.WebhookSubscription{}).
			Where("id = ?", subID).
			Updates(map[string]any{
				"failure_count":    0,
				"last_delivery_at": now,
				"updated_at":       now,
			}).Error
		if err != nil {
			slog.Warn("fanout: reset failure_count failed",
				"subscription_id", subID, "error", err)
		}
	case models.StatusFailed:
		// Atomic increment via gorm.Expr so two concurrent
		// fanouts on the same subscription don't lose an
		// increment between read-modify-write.
		err := db.WithContext(ctx).
			Model(&models.WebhookSubscription{}).
			Where("id = ?", subID).
			Updates(map[string]any{
				"failure_count":    gorm.Expr("failure_count + 1"),
				"last_delivery_at": now,
				"updated_at":       now,
			}).Error
		if err != nil {
			slog.Warn("fanout: increment failure_count failed",
				"subscription_id", subID, "error", err)
			return
		}
		// Re-read to see whether we tripped the breaker. We
		// could fold this into the UPDATE with a CASE
		// expression, but the two-query path is portable
		// across drivers (the test suite uses sqlite, prod
		// uses Postgres) and the cost is negligible.
		var current models.WebhookSubscription
		if err := db.WithContext(ctx).
			Select("failure_count", "status").
			Where("id = ?", subID).
			First(&current).Error; err != nil {
			slog.Warn("fanout: reread for breaker check failed",
				"subscription_id", subID, "error", err)
			return
		}
		if current.FailureCount >= FailureThreshold && current.Status != models.WebhookStatusDisabled {
			if err := db.WithContext(ctx).
				Model(&models.WebhookSubscription{}).
				Where("id = ?", subID).
				Updates(map[string]any{
					"status":     models.WebhookStatusDisabled,
					"updated_at": now,
				}).Error; err != nil {
				slog.Warn("fanout: auto-disable failed",
					"subscription_id", subID, "failure_count", current.FailureCount, "error", err)
				return
			}
			metrics.FanoutSubscriptionsDisabled.Inc()
			slog.Info("fanout: subscription auto-disabled by circuit breaker",
				"subscription_id", subID, "failure_count", current.FailureCount,
				"threshold", FailureThreshold)
		}
	}
}
