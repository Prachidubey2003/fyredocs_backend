package fanout

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"notify-service/internal/encat"
	"notify-service/internal/metrics"
	"notify-service/internal/models"
)

// Metrics counters are package-level + auto-registered, so
// they persist across tests. Each test reads the BEFORE
// value, runs the operation, then asserts the delta — that
// way other tests in the package (or even other test files)
// don't contaminate the assertion.

func TestFanout_MetricNoSubscriberWhenNothingMatches(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xD0))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	before := testutil.ToFloat64(metrics.FanoutEventsTotal.WithLabelValues("job.completed", "no_subscriber"))

	disp := &recordingDispatcher{}
	if _, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed")); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	after := testutil.ToFloat64(metrics.FanoutEventsTotal.WithLabelValues("job.completed", "no_subscriber"))
	if after-before != 1 {
		t.Errorf("no_subscriber counter delta = %v, want 1", after-before)
	}
}

func TestFanout_MetricMatchedAndDeliveredOnHappyPath(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xD1))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	seedSubscription(t, db, userID, "subscription.changed", "https://hooks.a/x", "secret-aaaaaaaaaaaa")

	beforeMatched := testutil.ToFloat64(metrics.FanoutEventsTotal.WithLabelValues("subscription.changed", "matched"))
	beforeDelivered := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("subscription.changed", "delivered"))

	disp := &recordingDispatcher{} // defaults to StatusDelivered
	if _, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "subscription.changed")); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	afterMatched := testutil.ToFloat64(metrics.FanoutEventsTotal.WithLabelValues("subscription.changed", "matched"))
	afterDelivered := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("subscription.changed", "delivered"))
	if afterMatched-beforeMatched != 1 {
		t.Errorf("matched delta = %v, want 1", afterMatched-beforeMatched)
	}
	if afterDelivered-beforeDelivered != 1 {
		t.Errorf("delivered delta = %v, want 1", afterDelivered-beforeDelivered)
	}
}

func TestFanout_MetricFailedDeliveryIncrementsDeliveryCounter(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xD2))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	seedSubscription(t, db, userID, "document.created", "https://hooks.b/x", "secret-bbbbbbbbbbbb")

	beforeFailed := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("document.created", "failed"))

	disp := &recordingDispatcher{overrideStatus: models.StatusFailed}
	if _, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "document.created")); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	afterFailed := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("document.created", "failed"))
	if afterFailed-beforeFailed != 1 {
		t.Errorf("failed delta = %v, want 1", afterFailed-beforeFailed)
	}
}

func TestFanout_MetricSkippedOnDecryptFailure(t *testing.T) {
	// Seal under one KEK, then rotate before fanout — the
	// per-row decrypt fails and the metric ticks `skipped`.
	encat.SetKEKForTest(makeKEK(0xD3))
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	seedSubscription(t, db, userID, "document.updated", "https://hooks.c/x", "secret-cccccccccccc")

	encat.SetKEKForTest(makeKEK(0xD4)) // rotate — row's wrapped DEK is now unrecoverable
	defer encat.SetKEKForTest(nil)

	beforeSkipped := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("document.updated", "skipped"))

	disp := &recordingDispatcher{}
	if _, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "document.updated")); err != nil {
		t.Fatalf("Fanout: %v", err)
	}

	afterSkipped := testutil.ToFloat64(metrics.FanoutDeliveriesTotal.WithLabelValues("document.updated", "skipped"))
	if afterSkipped-beforeSkipped != 1 {
		t.Errorf("skipped delta = %v, want 1", afterSkipped-beforeSkipped)
	}
	// Dispatcher should NOT have been called for a decrypt
	// failure — defended by an earlier test, re-asserted here
	// so the metric semantics stay aligned with reality.
	if len(disp.calls) != 0 {
		t.Errorf("dispatcher should not be called when decrypt fails; got %d calls", len(disp.calls))
	}
}

func TestFanout_MetricSubscriptionsDisabledOnBreakerTrip(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xD5))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	seedSubscription(t, db, userID, "job.failed", "https://hooks.d/x", "secret-dddddddddddd")

	before := testutil.ToFloat64(metrics.FanoutSubscriptionsDisabled)

	// Trip the breaker — FailureThreshold consecutive
	// failures flips the row to disabled.
	disp := &recordingDispatcher{overrideStatus: models.StatusFailed}
	for i := 0; i < FailureThreshold; i++ {
		_, _ = Fanout(context.Background(), db, disp, sampleEvent(userID, "job.failed"))
	}

	after := testutil.ToFloat64(metrics.FanoutSubscriptionsDisabled)
	if after-before != 1 {
		t.Errorf("disabled delta = %v, want 1 (exactly one auto-disable for one subscription)", after-before)
	}

	// Confirm the breaker actually fired (defends the metric
	// assertion against silently passing when the disable
	// code path is broken).
	var sub models.WebhookSubscription
	_ = db.Where("user_id = ?", userID).First(&sub).Error
	if sub.Status != models.WebhookStatusDisabled {
		t.Errorf("subscription status = %q, want disabled", sub.Status)
	}
}
