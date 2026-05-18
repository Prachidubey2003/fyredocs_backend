package natsconn

// HealthStatus is the discrete readiness verdict each service's
// /readyz handler reports for its NATS dependency. Three
// values — operators care about all three:
//
//   - StatusOK         — connection is live; events flow.
//   - StatusDisconnected — natsconn.Connect ran (or was
//     attempted) but the underlying `*nats.Conn` is not
//     currently connected. Readyz should 503 — the consumers
//     are silent and events queue at publishers.
//   - StatusDisabled  — natsconn.Connect was never run, OR ran
//     and the env was empty. Service is deliberately in
//     HTTP-only mode. Readyz should 200; reporting
//     `disabled` makes the response self-documenting.
type HealthStatus string

const (
	StatusOK           HealthStatus = "ok"
	StatusDisconnected HealthStatus = "disconnected"
	StatusDisabled     HealthStatus = "disabled"
)

// IsReady reports whether `s` should pass a readiness probe.
// StatusDisabled passes — a deliberately-pruned deploy isn't
// broken. StatusDisconnected fails.
func (s HealthStatus) IsReady() bool {
	return s == StatusOK || s == StatusDisabled
}

// HealthChecker is the per-service hook handlers consult for
// the NATS readiness verdict. The production adapter
// (`DefaultHealthChecker`) reads `Conn` at request time so
// connect/reconnect during process lifetime is observed
// without a handler restart. Tests construct a `StubChecker`
// for deterministic verdicts.
//
// Stated as an interface (rather than a free function reading
// the package var) so handler tests can swap in a fake
// without crossing through a global — the same pattern used
// in notify-service/handlers/system_test.go before this
// helper was extracted.
type HealthChecker interface {
	NATSHealth() HealthStatus
}

// DefaultHealthChecker reads the package-level `Conn` at
// request time. nil means "Connect was never called or
// failed" → `disabled`; otherwise `IsConnected()` picks
// between `ok` and `disconnected`.
type DefaultHealthChecker struct{}

// NATSHealth reports the live verdict on the production
// `*nats.Conn`.
func (DefaultHealthChecker) NATSHealth() HealthStatus {
	if Conn == nil {
		return StatusDisabled
	}
	if Conn.IsConnected() {
		return StatusOK
	}
	return StatusDisconnected
}

// StubHealthChecker is the test injection point. Construct
// with `StubHealthChecker{Verdict: StatusOK}` etc; handlers
// call `.NATSHealth()` and get the fixed value back.
type StubHealthChecker struct {
	Verdict HealthStatus
}

// NATSHealth returns the configured verdict.
func (s StubHealthChecker) NATSHealth() HealthStatus { return s.Verdict }
