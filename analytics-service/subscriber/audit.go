package subscriber

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"fyredocs/shared/natsconn"
	"fyredocs/shared/queue"

	"analytics-service/internal/audit"
	"analytics-service/internal/models"
)

// subscribeAuditEvents wires the AUDIT JetStream consumer that
// appends incoming events onto the hash-chained `audit_events`
// table.
func subscribeAuditEvents(ctx context.Context) (jetstream.ConsumeContext, error) {
	consumer, err := natsconn.JS.CreateOrUpdateConsumer(ctx, "AUDIT", jetstream.ConsumerConfig{
		Durable:       "analytics-audit",
		FilterSubject: "audit.events.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, err
	}
	return consumer.Consume(func(msg jetstream.Msg) {
		handleAuditEvent(msg)
	})
}

// handleAuditEvent appends one audit row. Malformed events are
// Ack'd + dropped (poison messages won't get better on retry);
// DB / chain errors are Nak'd so JetStream redelivers.
//
// Concurrency: AppendAudit takes a transaction that SELECTs the
// previous row FOR UPDATE before INSERTing the new one. That
// serialises competing consumers — only one row can land at a
// given seq, and the prev_hash linkage is always against the
// row that won the lock.
func handleAuditEvent(msg jetstream.Msg) {
	var event queue.AuditEvent
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		slog.Warn("audit: malformed event; dropping",
			"subject", msg.Subject(), "error", err)
		_ = msg.Ack()
		return
	}
	if event.Actor == "" || event.Action == "" {
		slog.Warn("audit: missing required fields; dropping",
			"actor", event.Actor, "action", event.Action)
		_ = msg.Ack()
		return
	}
	if err := AppendAudit(models.DB, event); err != nil {
		slog.Error("audit: append failed",
			"action", event.Action, "actor", event.Actor, "error", err)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// AppendAudit inserts one audit row, computing its hash against
// the previous row's hash inside a transaction. Exported so
// in-process callers (e.g., the analytics-service's own
// admin-action handlers when they're added) can write to the
// chain without going through NATS.
//
// The SELECT … FOR UPDATE on the latest row is the
// serialisation primitive. Without it, two concurrent appends
// could both read prev_hash from the same row and both compute
// their hash against it, producing a fork in the chain that the
// verifier would later flag. The FOR UPDATE makes the loser
// wait, re-read, and chain against the winner.
func AppendAudit(db *gorm.DB, event queue.AuditEvent) error {
	metadata := []byte(event.Metadata)
	return db.Transaction(func(tx *gorm.DB) error {
		var prev models.AuditEvent
		err := tx.
			Set("gorm:query_option", "FOR UPDATE").
			Order("seq DESC").
			Limit(1).
			Find(&prev).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		prevHash := audit.GenesisPrevHash
		if prev.Seq > 0 {
			prevHash = prev.Hash
		}

		// We don't know our final `seq` until the INSERT
		// returns it (BIGSERIAL is assigned by Postgres). Insert
		// with placeholder hash bytes so we get the seq back,
		// then UPDATE with the computed hash. Both steps run
		// inside the same transaction, holding the FOR UPDATE
		// lock the whole time, so no other appender can see the
		// half-written row.
		row := models.AuditEvent{
			Actor:      event.Actor,
			Action:     event.Action,
			Resource:   event.Resource,
			Metadata:   datatypes.JSON(metadata),
			PrevHash:   prevHash,
			Hash:       []byte{0}, // placeholder; rewritten below
			OccurredAt: event.OccurredAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		row.Hash = audit.Compute(row.Seq, row.Actor, row.Action, row.Resource, metadata, prevHash)
		if err := tx.Model(&models.AuditEvent{}).
			Where("seq = ?", row.Seq).
			Update("hash", row.Hash).Error; err != nil {
			return err
		}
		return nil
	})
}
