// Package metrics defines notify-service's Prometheus
// counters for the webhook-fanout pipeline. Operators chart
// these to answer questions the audit log can't:
//
//   - How many of THIS event type fired in the last hour?
//   - Which event types have the most failures right now?
//   - How often is the circuit breaker tripping?
//
// All counters are auto-registered on the default Prometheus
// registry via `promauto.NewCounterVec`. The `/metrics`
// endpoint in main.go scrapes the same registry.
//
// Service-specific (not promoted to shared/metrics) because
// the labels are webhook-specific and other services don't
// need them. shared/metrics carries cross-service primitives
// like request duration; this carries pipeline-specific
// instrumentation.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// FanoutEventsTotal counts every DomainEvent the fanout
// processes, with the outcome.
//
// Labels:
//   - `event_type`: the DomainEvent.EventType (job.completed,
//     subscription.changed, etc.)
//   - `result`:
//       - `matched`     — at least one subscription matched
//                         the user_id + event_type, fanout ran
//       - `no_subscriber` — no subscription matched; event Ack'd
//                         without dispatch (the common case
//                         for users without webhooks)
//       - `error`       — DB lookup / marshal failed; JetStream
//                         redelivered (also incremented on each
//                         redelivery)
var FanoutEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "notify_fanout_events_total",
	Help: "Domain events processed by the webhook fanout.",
}, []string{"event_type", "result"})

// FanoutDeliveriesTotal counts every PER-SUBSCRIPTION
// delivery attempt the fanout makes. The cardinality is
// `event_types × dispatch_outcomes` (currently 8 × 3 = 24
// series) which is well within Prometheus comfort range.
//
// Labels:
//   - `event_type`: the DomainEvent.EventType
//   - `status`:
//       - `delivered` — channel send returned nil
//       - `failed`    — channel send returned an error
//                       (HTTP non-2xx, transport failure, etc.)
//       - `skipped`   — pre-dispatch failure (decrypt of the
//                       row's signing secret failed; the
//                       fanout records but does not try the
//                       receiver)
var FanoutDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "notify_fanout_deliveries_total",
	Help: "Per-subscription webhook deliveries the fanout attempted.",
}, []string{"event_type", "status"})

// FanoutSubscriptionsDisabled counts every time the circuit
// breaker flips a subscription's status to `disabled` past
// `fanout.FailureThreshold` consecutive failures. Used by the
// alert "we auto-disabled N subscriptions in the last 24h"
// — a sudden jump means a subscriber is broken or under
// attack.
var FanoutSubscriptionsDisabled = promauto.NewCounter(prometheus.CounterOpts{
	Name: "notify_fanout_subscriptions_disabled_total",
	Help: "Subscriptions auto-disabled by the circuit breaker.",
})
