// Package metrics exposes the collab-specific Prometheus surface.
// Generic HTTP-request metrics (latency histograms) ride on
// `fyredocs/shared/metrics`; this package owns the room/connection
// gauges that are specific to a multiplayer service.
//
// Registration is process-wide via promauto. The metrics expose a
// `Bind(hub)` function so the rooms-total gauge can scrape live
// state from the hub without the metrics package importing the
// `room` package directly (which would create a circular import
// once main.go imports both).
package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RoomCounter is the interface the rooms-total gauge calls on
// every scrape. The Hub satisfies it via Hub.RoomCount(). We use
// an interface instead of importing room directly so the package
// stays acyclic.
type RoomCounter interface {
	RoomCount() int
}

// hubRef holds the registered Hub via atomic.Pointer so the
// GaugeFunc closure can read it without a mutex. Set once at
// startup by Bind, never re-assigned in production.
var hubRef atomic.Pointer[RoomCounter]

// Connections is the live connection-count gauge. The websocket
// handler calls Inc on Join and Dec on close, so the value
// reflects the size of the global connection fleet across rooms.
var Connections = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "collab_connections_total",
	Help: "Number of active websocket connections served by collab-service.",
})

// BroadcastBytes counts the total bytes relayed to peers. Inbound
// bytes (from a sender) become outbound bytes to N-1 peers, so
// this counter is per-peer-delivery — useful for capacity
// planning. We observe this once per Send call, not once per
// inbound message.
var BroadcastBytes = promauto.NewCounter(prometheus.CounterOpts{
	Name: "collab_broadcast_bytes_total",
	Help: "Total bytes broadcast to peer connections (per-recipient).",
})

// roomsTotal is the gauge that reports active rooms. Initialised
// at package-load via NewGaugeFunc — the function captures the
// (currently nil) hubRef and re-reads on every scrape, so
// Bind-after-init works correctly.
//
// Underscore-assigned so `go vet` doesn't flag it as unused: the
// side effect (promauto registration) is what we want, and the
// var itself isn't called from Go code.
var _ = promauto.NewGaugeFunc(prometheus.GaugeOpts{
	Name: "collab_rooms_total",
	Help: "Number of active multiplayer rooms (one per document with at least one connection).",
}, func() float64 {
	h := hubRef.Load()
	if h == nil {
		return 0
	}
	return float64((*h).RoomCount())
})

// Bind registers the Hub as the source of truth for the rooms
// gauge. Call from main.go after constructing the Hub. Calling
// multiple times replaces the binding (last write wins) — useful
// only in tests; production calls Bind once.
func Bind(hub RoomCounter) {
	hubRef.Store(&hub)
}
