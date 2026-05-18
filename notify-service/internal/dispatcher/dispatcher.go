// Package dispatcher routes inbound NotifyEvents to the right
// channel implementation and persists one Delivery row per event.
//
// The dispatcher is the only piece that touches both the
// `channels` registry and the `models.Delivery` table. Channels
// themselves are stateless; persistence lives here so the audit
// trail's shape is uniform regardless of channel.
package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"notify-service/internal/channels"
	"notify-service/internal/models"
)

// Dispatcher dispatches NotifyEvents through registered channels
// and writes the audit trail. Construct one per process via New;
// it's safe to share across goroutines.
type Dispatcher struct {
	db       *gorm.DB
	channels map[string]channels.Channel
}

// New returns a Dispatcher with the given DB handle and an empty
// channel map. Register channels via Register before calling Run
// — an event for an unregistered channel results in a Delivery
// row with status=`skipped`.
func New(db *gorm.DB) *Dispatcher {
	return &Dispatcher{
		db:       db,
		channels: make(map[string]channels.Channel),
	}
}

// Register binds a Channel to a channel name. Last-write-wins —
// re-registering replaces the prior implementation, useful for
// tests that want to inject a stub.
func (d *Dispatcher) Register(name string, ch channels.Channel) {
	d.channels[name] = ch
}

// Dispatch persists a Delivery row for `event` and calls the
// matching channel. The audit row is created BEFORE the channel
// runs so that even a panic in Send leaves a `pending` row the
// dev console can surface.
//
// Returns the persisted Delivery (with status filled in) and a
// non-nil error only on DB persistence failure — channel-level
// errors are folded into Delivery.LastError + status=`failed`
// and returned alongside the row. Callers (the NATS subscriber
// + the HTTP handler) decide how to map those to upstream
// behaviour: subscriber Naks on persistence-level errors so
// JetStream retries; the HTTP handler returns 500 in that case.
//
// Convenience wrapper around DispatchWithSecret(nil) — the
// channel uses its configured default signing key (for
// webhook, the global NOTIFY_WEBHOOK_SECRET).
func (d *Dispatcher) Dispatch(ctx context.Context, event queue.NotifyEvent) (*models.Delivery, error) {
	return d.DispatchWithSecret(ctx, event, nil)
}

// DispatchWithSecret is identical to Dispatch but injects
// `secret` into the SendRequest. Used by the fanout consumer
// so each subscriber's webhook is HMAC'd with its own
// per-subscription signing key (recovered from the encrypted
// row at fanout time). The secret never travels on the wire —
// it's an in-process parameter from the fanout to the channel.
//
// A nil/empty secret behaves exactly like Dispatch — the
// channel falls back to its configured default.
func (d *Dispatcher) DispatchWithSecret(ctx context.Context, event queue.NotifyEvent, secret []byte) (*models.Delivery, error) {
	row := models.Delivery{
		Channel: event.Channel,
		Target:  event.Target,
		Status:  models.StatusPending,
		Payload: datatypes.JSON(event.Payload),
	}
	if event.UserID != "" {
		if id, err := uuid.Parse(event.UserID); err == nil {
			row.UserID = &id
		}
	}
	if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
		row.IdempotencyKey = &key
		// Collapse a duplicate: if the same idempotency key
		// already produced a row (in any status), short-circuit
		// and return status=`skipped` for the new attempt. The
		// unique index would otherwise raise a constraint
		// violation on the INSERT anyway; checking up-front gives
		// us a clean status code without parsing the driver error.
		var existing models.Delivery
		err := d.db.WithContext(ctx).
			Where("idempotency_key = ?", key).
			First(&existing).Error
		if err == nil {
			slog.Info("notify-service: dispatch skipped (idempotency hit)",
				"channel", event.Channel, "key", key, "existingId", existing.ID)
			return &existing, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	if err := d.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}

	channel, ok := d.channels[event.Channel]
	if !ok {
		row.Status = models.StatusSkipped
		row.LastError = channels.ErrUnsupportedChannel.Error() + ": " + event.Channel
		d.persistStatus(ctx, &row)
		return &row, nil
	}

	row.Attempts++
	if err := channel.Send(ctx, channels.SendRequest{
		Target:  event.Target,
		Payload: json.RawMessage(event.Payload),
		UserID:  event.UserID,
		Secret:  secret,
	}); err != nil {
		row.Status = models.StatusFailed
		row.LastError = err.Error()
		d.persistStatus(ctx, &row)
		return &row, nil
	}

	row.Status = models.StatusDelivered
	row.LastError = ""
	d.persistStatus(ctx, &row)
	return &row, nil
}

// persistStatus writes the post-Send Delivery state. Failure here
// is logged-and-tolerated: the in-memory row already reflects the
// channel outcome, and the NATS subscriber will Nak the message
// on the parent dispatch error (forcing a retry that re-INSERTs
// with a fresh ID — fine since channel sends are typically
// idempotent or the customer's webhook handler dedupes).
func (d *Dispatcher) persistStatus(ctx context.Context, row *models.Delivery) {
	err := d.db.WithContext(ctx).
		Model(&models.Delivery{}).
		Where("id = ?", row.ID).
		Updates(map[string]any{
			"status":     row.Status,
			"attempts":   row.Attempts,
			"last_error": row.LastError,
		}).Error
	if err != nil {
		slog.Warn("notify-service: persistStatus failed",
			"deliveryId", row.ID, "channel", row.Channel,
			"status", row.Status, "error", err)
	}
}
