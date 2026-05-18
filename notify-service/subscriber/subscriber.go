// Package subscriber wires the NOTIFY JetStream to the
// dispatcher + fanout. Two durable consumers:
//
//   - `notify-service` filters on `notify.send.>` (pre-routed
//     delivery requests) and hands each message to
//     dispatcher.Dispatch.
//   - `notify-service-fanout` filters on `notify.event.>`
//     (raw domain events) and hands each message to
//     fanout.Fanout, which expands one event into one
//     dispatch per matching subscription.
//
// Both consumers run as goroutines in the same process; the
// dispatcher writes the same notify_deliveries audit table
// from either path, so the dev console doesn't have to know
// which path produced a row.
package subscriber

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"

	"fyredocs/shared/natsconn"
	"fyredocs/shared/queue"

	"notify-service/internal/dispatcher"
	"notify-service/internal/fanout"
)

// Subscribers owns the JetStream ConsumeContexts. Stop must
// be called on shutdown to drain both goroutines.
type Subscribers struct {
	notify jetstream.ConsumeContext
	fanout jetstream.ConsumeContext
}

// Start opens the durable consumers and registers the
// per-subject handlers. `db` is required for the fanout path
// (subscription lookup); pass-through for the dispatcher
// path.
func Start(ctx context.Context, disp *dispatcher.Dispatcher, db *gorm.DB) (*Subscribers, error) {
	notifyConsumer, err := natsconn.JS.CreateOrUpdateConsumer(ctx, "NOTIFY", jetstream.ConsumerConfig{
		Durable:       "notify-service",
		FilterSubject: "notify.send.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, err
	}
	notifyCC, err := notifyConsumer.Consume(func(msg jetstream.Msg) {
		handleNotifyEvent(ctx, disp, msg)
	})
	if err != nil {
		return nil, err
	}

	fanoutConsumer, err := natsconn.JS.CreateOrUpdateConsumer(ctx, "NOTIFY", jetstream.ConsumerConfig{
		Durable:       "notify-service-fanout",
		FilterSubject: "notify.event.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		notifyCC.Stop()
		return nil, err
	}
	fanoutCC, err := fanoutConsumer.Consume(func(msg jetstream.Msg) {
		handleDomainEvent(ctx, db, disp, msg)
	})
	if err != nil {
		notifyCC.Stop()
		return nil, err
	}

	return &Subscribers{notify: notifyCC, fanout: fanoutCC}, nil
}

// Stop halts both consumer goroutines. Safe on a nil receiver
// and idempotent.
func (s *Subscribers) Stop() {
	if s == nil {
		return
	}
	if s.notify != nil {
		s.notify.Stop()
		s.notify = nil
	}
	if s.fanout != nil {
		s.fanout.Stop()
		s.fanout = nil
	}
}

// handleNotifyEvent decodes one NotifyEvent and dispatches it.
// Decoding failures Ack-and-drop (poison message) since
// re-delivering won't help. DB-persistence failures Nak so
// JetStream retries — but channel-send failures are NOT Naked
// (they're recorded as `failed` in the audit row and the
// upstream service is expected to look at the row, not the queue).
func handleNotifyEvent(ctx context.Context, disp *dispatcher.Dispatcher, msg jetstream.Msg) {
	var event queue.NotifyEvent
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		slog.Warn("notify-service: malformed event; dropping",
			"subject", msg.Subject(), "error", err)
		_ = msg.Ack()
		return
	}
	if _, err := disp.Dispatch(ctx, event); err != nil {
		// Persistence failure — Nak so JetStream redelivers.
		slog.Error("notify-service: dispatch failed (persistence)",
			"channel", event.Channel, "target", event.Target, "error", err)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// handleDomainEvent decodes a DomainEvent + runs fanout. Same
// Ack policy as the dispatcher path:
//
//   - Malformed JSON / missing required fields: Ack-and-drop
//     (poison message; redelivery won't help).
//   - DB lookup failure: Nak so JetStream retries.
//   - Per-subscription failures (decrypt, dispatch): logged
//     by fanout but DO NOT trigger Nak — the dispatcher
//     already recorded a `failed` row that the dev console
//     surfaces. Nakking on per-sub failure would cause every
//     OTHER subscription on the same event to be re-fanned
//     out, double-POSTing them.
func handleDomainEvent(ctx context.Context, db *gorm.DB, disp *dispatcher.Dispatcher, msg jetstream.Msg) {
	var event queue.DomainEvent
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		slog.Warn("notify-service: malformed domain event; dropping",
			"subject", msg.Subject(), "error", err)
		_ = msg.Ack()
		return
	}
	if _, err := fanout.Fanout(ctx, db, disp, event); err != nil {
		slog.Error("notify-service: fanout failed (lookup)",
			"event_id", event.EventID, "event_type", event.EventType, "error", err)
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}
