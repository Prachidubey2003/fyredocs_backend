package natsconn

import "testing"

func TestHealthStatus_IsReady(t *testing.T) {
	cases := []struct {
		in   HealthStatus
		want bool
	}{
		{StatusOK, true},
		{StatusDisabled, true}, // deliberately-pruned deploy passes
		{StatusDisconnected, false},
		{HealthStatus("some-future-value"), false}, // defensive default
	}
	for _, tc := range cases {
		if got := tc.in.IsReady(); got != tc.want {
			t.Errorf("HealthStatus(%q).IsReady() = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDefaultHealthChecker_DisabledWhenConnIsNil(t *testing.T) {
	// Test binary doesn't call Connect — Conn is nil — adapter
	// reports `disabled`. Pin this so a future refactor that
	// returns `disconnected` on nil (treating nil-conn as a
	// disconnected conn) doesn't regress to false 503s on
	// HTTP-only deploys.
	prev := Conn
	Conn = nil
	defer func() { Conn = prev }()

	if got := (DefaultHealthChecker{}).NATSHealth(); got != StatusDisabled {
		t.Errorf("got %q, want %q", got, StatusDisabled)
	}
}

func TestStubHealthChecker_ReturnsConfiguredVerdict(t *testing.T) {
	for _, v := range []HealthStatus{StatusOK, StatusDisconnected, StatusDisabled} {
		if got := (StubHealthChecker{Verdict: v}).NATSHealth(); got != v {
			t.Errorf("StubHealthChecker.NATSHealth() = %q, want %q", got, v)
		}
	}
}
