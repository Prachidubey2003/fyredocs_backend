package fanout

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"notify-service/internal/encat"
	"notify-service/internal/models"
)

// recordingDispatcher captures every DispatchWithSecret call
// + lets the test inject an error per call OR override the
// Delivery status (so circuit-breaker tests can simulate
// channel-level failures). The fanout layer is pure with
// respect to the dispatcher; we don't need to spin up the
// real persist-then-channel pipeline for these unit tests.
type recordingDispatcher struct {
	calls         []dispatchCall
	err           error  // returned by EVERY call when non-nil (persistence failure)
	overrideStatus string // when non-empty, set on the returned Delivery instead of StatusDelivered
}

type dispatchCall struct {
	Event  queue.NotifyEvent
	Secret []byte
}

func (r *recordingDispatcher) DispatchWithSecret(_ context.Context, event queue.NotifyEvent, secret []byte) (*models.Delivery, error) {
	r.calls = append(r.calls, dispatchCall{Event: event, Secret: append([]byte(nil), secret...)})
	if r.err != nil {
		return nil, r.err
	}
	status := models.StatusDelivered
	if r.overrideStatus != "" {
		status = r.overrideStatus
	}
	return &models.Delivery{
		ID:      uuid.Must(uuid.NewV7()),
		Channel: event.Channel,
		Target:  event.Target,
		Status:  status,
	}, nil
}

// setupDB opens an in-memory sqlite + migrates the webhook
// subscription table. Returns the DB handle.
func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.WebhookSubscription{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedSubscription builds + persists one active subscription
// + returns the matching plaintext secret. The fanout test
// then asserts that this secret was forwarded to the
// dispatcher unmodified.
func seedSubscription(t *testing.T, db *gorm.DB, userID uuid.UUID, eventType, target, plaintext string) uuid.UUID {
	t.Helper()
	wrappedDEK, ciphertext, err := encat.SealSecret([]byte(plaintext))
	if err != nil {
		t.Fatalf("encat.SealSecret: %v", err)
	}
	sub := models.WebhookSubscription{
		UserID:           userID,
		EventType:        eventType,
		TargetURL:        target,
		SecretCiphertext: ciphertext,
		SecretWrappedDEK: wrappedDEK,
		SecretPrefix:     plaintext[:8],
		Status:           models.WebhookStatusActive,
	}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return sub.ID
}

func sampleEvent(userID uuid.UUID, eventType string) queue.DomainEvent {
	return queue.DomainEvent{
		EventID:    "evt_test_001",
		EventType:  eventType,
		UserID:     userID.String(),
		OccurredAt: time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC),
		Data:       json.RawMessage(`{"thing":42}`),
	}
}

// makeKEK returns a deterministic 32-byte KEK so tests can
// seal + open without depending on global env state.
func makeKEK(seed byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed
	}
	return k
}

// ---- happy path ----

func TestFanout_DispatchesOnePerMatchingSubscription(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xAA))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	subA := seedSubscription(t, db, userID, "job.completed", "https://hooks.a/x", "secret-a-aaaaaaaaaaaaa")
	subB := seedSubscription(t, db, userID, "job.completed", "https://hooks.b/x", "secret-b-bbbbbbbbbbbbb")
	// non-matching: same user but different event type.
	_ = seedSubscription(t, db, userID, "subscription.changed", "https://hooks.c/x", "secret-c-ccccccccccccc")
	// non-matching: different user.
	_ = seedSubscription(t, db, uuid.Must(uuid.NewV7()), "job.completed", "https://hooks.d/x", "secret-d-ddddddddddddd")

	disp := &recordingDispatcher{}
	results, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results; got %d", len(results))
	}
	if len(disp.calls) != 2 {
		t.Fatalf("expected 2 dispatcher calls; got %d", len(disp.calls))
	}

	// Each call must (a) target the right URL, (b) carry the
	// matching subscription's plaintext secret, (c) include an
	// idempotency key tying event+subscription, (d) wrap the
	// public envelope as payload.
	gotByTarget := map[string]dispatchCall{}
	for _, call := range disp.calls {
		gotByTarget[call.Event.Target] = call
	}
	for _, want := range []struct {
		Target string
		Sub    uuid.UUID
		Secret string
	}{
		{"https://hooks.a/x", subA, "secret-a-aaaaaaaaaaaaa"},
		{"https://hooks.b/x", subB, "secret-b-bbbbbbbbbbbbb"},
	} {
		call, ok := gotByTarget[want.Target]
		if !ok {
			t.Errorf("no dispatch call for target %s", want.Target)
			continue
		}
		if string(call.Secret) != want.Secret {
			t.Errorf("%s: secret = %q, want %q", want.Target, call.Secret, want.Secret)
		}
		wantKey := "fanout:evt_test_001:" + want.Sub.String()
		if call.Event.IdempotencyKey != wantKey {
			t.Errorf("%s: idempotency key = %q, want %q", want.Target, call.Event.IdempotencyKey, wantKey)
		}
		if call.Event.Channel != queue.ChannelWebhook {
			t.Errorf("%s: channel = %q, want webhook", want.Target, call.Event.Channel)
		}
		// Payload must be the DomainEvent envelope (round-trip
		// it through json.Decode for assertion stability).
		var decoded queue.DomainEvent
		if err := json.Unmarshal(call.Event.Payload, &decoded); err != nil {
			t.Errorf("%s: payload not valid DomainEvent JSON: %v", want.Target, err)
			continue
		}
		if decoded.EventID != "evt_test_001" || decoded.EventType != "job.completed" {
			t.Errorf("%s: payload event mismatch: %+v", want.Target, decoded)
		}
	}
}

func TestFanout_ReturnsEmptyWhenNoSubscriptionsMatch(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xAB))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	disp := &recordingDispatcher{}
	results, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results when no subscriptions match; got %d", len(results))
	}
	if len(disp.calls) != 0 {
		t.Errorf("dispatcher should not be called when no subscriptions match; got %d", len(disp.calls))
	}
}

func TestFanout_SkipsDisabledSubscriptions(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xAC))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	// One active, one disabled — only the active should fire.
	seedSubscription(t, db, userID, "job.completed", "https://hooks.active/x", "active-secret-aaaaaaa")
	disabledID := seedSubscription(t, db, userID, "job.completed", "https://hooks.disabled/x", "disabled-secret-aaa")
	if err := db.Model(&models.WebhookSubscription{}).
		Where("id = ?", disabledID).
		Update("status", models.WebhookStatusDisabled).Error; err != nil {
		t.Fatalf("disable subscription: %v", err)
	}

	disp := &recordingDispatcher{}
	results, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (active only); got %d", len(results))
	}
	if disp.calls[0].Event.Target != "https://hooks.active/x" {
		t.Errorf("dispatched to wrong target: %s", disp.calls[0].Event.Target)
	}
}

func TestFanout_SkipsSoftDeletedSubscriptions(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xAD))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	seedSubscription(t, db, userID, "job.completed", "https://hooks.keep/x", "keep-secret-aaaaaaaaa")
	deleteID := seedSubscription(t, db, userID, "job.completed", "https://hooks.gone/x", "gone-secret-aaaaaaaaa")
	// Soft-delete via gorm scope.
	if err := db.Delete(&models.WebhookSubscription{}, "id = ?", deleteID).Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	disp := &recordingDispatcher{}
	results, _ := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if len(results) != 1 {
		t.Fatalf("expected 1 result (deleted should be filtered out); got %d", len(results))
	}
}

// ---- per-subscription failure isolation ----

func TestFanout_PerSubscriptionDispatchFailureDoesNotAbortRest(t *testing.T) {
	// The recording dispatcher returns the same error for
	// EVERY call. The fanout must still call it for every
	// matching subscription — each one's failure is recorded
	// in the per-result Err, not bubbled as a top-level
	// abort.
	encat.SetKEKForTest(makeKEK(0xAE))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	seedSubscription(t, db, userID, "job.completed", "https://hooks.a/x", "secret-aaaaaaaaaaaa")
	seedSubscription(t, db, userID, "job.completed", "https://hooks.b/x", "secret-bbbbbbbbbbbb")

	disp := &recordingDispatcher{err: errors.New("simulated dispatch failure")}
	results, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("top-level Fanout error should be nil; got %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results; got %d", len(results))
	}
	if len(disp.calls) != 2 {
		t.Errorf("dispatcher should be called once per subscription; got %d", len(disp.calls))
	}
	for _, r := range results {
		if r.Err == nil {
			t.Errorf("expected per-result Err for subscription %s", r.SubscriptionID)
		}
	}
}

// ---- malformed event ----

func TestFanout_RejectsEventWithMissingFields(t *testing.T) {
	db := setupDB(t)
	disp := &recordingDispatcher{}
	cases := []queue.DomainEvent{
		{EventType: "job.completed", UserID: uuid.Must(uuid.NewV7()).String()}, // missing eventId
		{EventID: "evt", UserID: uuid.Must(uuid.NewV7()).String()},              // missing eventType
		{EventID: "evt", EventType: "job.completed"},                            // missing userId
	}
	for i, evt := range cases {
		_, err := Fanout(context.Background(), db, disp, evt)
		if err == nil {
			t.Errorf("case %d: expected error on malformed event", i)
		}
	}
	if len(disp.calls) != 0 {
		t.Errorf("dispatcher must not be called for malformed events; got %d", len(disp.calls))
	}
}

func TestFanout_RejectsMalformedUserID(t *testing.T) {
	db := setupDB(t)
	disp := &recordingDispatcher{}
	evt := queue.DomainEvent{
		EventID:   "evt",
		EventType: "job.completed",
		UserID:    "not-a-uuid",
	}
	_, err := Fanout(context.Background(), db, disp, evt)
	if err == nil {
		t.Error("expected error on non-UUID userId")
	}
}

func TestFanout_RejectsNilDispatcher(t *testing.T) {
	db := setupDB(t)
	_, err := Fanout(context.Background(), db, nil, sampleEvent(uuid.Must(uuid.NewV7()), "job.completed"))
	if err == nil {
		t.Error("expected error on nil dispatcher")
	}
}

// ---- decryption failure path ----

func TestFanout_RecordsDecryptFailureWithoutAbortingFanout(t *testing.T) {
	// Seal with one KEK, then swap it for a different one
	// before running fanout. The decrypt fails per
	// subscription; the per-result Err is set and the
	// dispatcher is never called for that subscription —
	// but the fanout doesn't abort.
	encat.SetKEKForTest(makeKEK(0xAF))
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())

	seedSubscription(t, db, userID, "job.completed", "https://hooks.bad-kek/x", "soon-undecryptable")

	// Rotate to a different KEK — the row's wrapped DEK is
	// now unrecoverable.
	encat.SetKEKForTest(makeKEK(0xB0))
	defer encat.SetKEKForTest(nil)

	disp := &recordingDispatcher{}
	results, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("top-level Fanout error should be nil; got %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result; got %d", len(results))
	}
	if results[0].Err == nil {
		t.Error("expected per-result Err when decrypt fails")
	}
	if len(disp.calls) != 0 {
		t.Errorf("dispatcher must not be called when decrypt fails; got %d", len(disp.calls))
	}
}

// ---- circuit breaker -------------------------------------------------

// loadSub fetches the subscription row by id, including
// soft-deleted rows. Tests use it to assert on the
// post-fanout bookkeeping state.
func loadSub(t *testing.T, db *gorm.DB, subID uuid.UUID) models.WebhookSubscription {
	t.Helper()
	var sub models.WebhookSubscription
	if err := db.Unscoped().Where("id = ?", subID).First(&sub).Error; err != nil {
		t.Fatalf("loadSub: %v", err)
	}
	return sub
}

func TestFanout_SuccessfulDeliveryResetsFailureCount(t *testing.T) {
	// Pre-seed a subscription with failure_count > 0. After a
	// successful fanout the counter resets to 0 — proves the
	// breaker isn't sticky and a transient outage doesn't
	// disable a subscriber that comes back.
	encat.SetKEKForTest(makeKEK(0xC0))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	subID := seedSubscription(t, db, userID, "job.completed", "https://hooks.a/x", "secret-aaaaaaaaaaaa")
	if err := db.Model(&models.WebhookSubscription{}).
		Where("id = ?", subID).
		Update("failure_count", 3).Error; err != nil {
		t.Fatalf("preset failure_count: %v", err)
	}

	disp := &recordingDispatcher{} // default → StatusDelivered
	_, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	got := loadSub(t, db, subID)
	if got.FailureCount != 0 {
		t.Errorf("failure_count = %d, want 0 after success", got.FailureCount)
	}
	if got.LastDeliveryAt == nil {
		t.Error("last_delivery_at must be set after a delivery")
	}
}

func TestFanout_FailedDeliveryIncrementsFailureCount(t *testing.T) {
	encat.SetKEKForTest(makeKEK(0xC1))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	subID := seedSubscription(t, db, userID, "job.completed", "https://hooks.b/x", "secret-bbbbbbbbbbbb")

	disp := &recordingDispatcher{overrideStatus: models.StatusFailed}
	_, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	got := loadSub(t, db, subID)
	if got.FailureCount != 1 {
		t.Errorf("failure_count = %d, want 1 after first failure", got.FailureCount)
	}
	if got.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active (single failure shouldn't disable)", got.Status)
	}
}

func TestFanout_AutoDisablesAfterThresholdConsecutiveFailures(t *testing.T) {
	// FailureThreshold consecutive failures must flip the row
	// to disabled. We drive FailureThreshold fanouts back-to-
	// back (no successes in between) — the breaker should
	// trip on the threshold-th call, NOT before.
	encat.SetKEKForTest(makeKEK(0xC2))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	subID := seedSubscription(t, db, userID, "job.completed", "https://hooks.c/x", "secret-cccccccccccc")

	disp := &recordingDispatcher{overrideStatus: models.StatusFailed}
	for i := 0; i < FailureThreshold-1; i++ {
		_, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
		if err != nil {
			t.Fatalf("Fanout #%d: %v", i, err)
		}
	}
	// One short of the threshold — still active.
	got := loadSub(t, db, subID)
	if got.Status != models.WebhookStatusActive {
		t.Errorf("after %d failures: status = %q, want active",
			FailureThreshold-1, got.Status)
	}
	if got.FailureCount != FailureThreshold-1 {
		t.Errorf("failure_count = %d, want %d", got.FailureCount, FailureThreshold-1)
	}

	// One more — should trip.
	_, err := Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("threshold fanout: %v", err)
	}
	got = loadSub(t, db, subID)
	if got.Status != models.WebhookStatusDisabled {
		t.Errorf("after %d failures: status = %q, want disabled",
			FailureThreshold, got.Status)
	}
	if got.FailureCount != FailureThreshold {
		t.Errorf("failure_count = %d, want %d", got.FailureCount, FailureThreshold)
	}
}

func TestFanout_DisabledSubscriptionDoesNotReceiveFurtherEvents(t *testing.T) {
	// Once auto-disabled by the breaker, the subscription
	// must be filtered out by the next Fanout's lookup —
	// dispatcher should not be called for it.
	encat.SetKEKForTest(makeKEK(0xC3))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	seedSubscription(t, db, userID, "job.completed", "https://hooks.d/x", "secret-dddddddddddd")

	// Trip the breaker.
	failDisp := &recordingDispatcher{overrideStatus: models.StatusFailed}
	for i := 0; i < FailureThreshold; i++ {
		_, _ = Fanout(context.Background(), db, failDisp, sampleEvent(userID, "job.completed"))
	}

	// Send another event — the disabled row must NOT receive it.
	freshDisp := &recordingDispatcher{}
	results, err := Fanout(context.Background(), db, freshDisp, sampleEvent(userID, "job.completed"))
	if err != nil {
		t.Fatalf("Fanout: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after auto-disable; got %d", len(results))
	}
	if len(freshDisp.calls) != 0 {
		t.Errorf("dispatcher must not be called for disabled subscription; got %d calls", len(freshDisp.calls))
	}
}

func TestFanout_PersistenceFailureDoesNotTouchFailureCount(t *testing.T) {
	// A DB-level dispatch failure (vs. channel-level) MUST
	// NOT charge the subscriber's failure_count — the
	// subscriber didn't do anything wrong; OUR DB hiccuped.
	// JetStream will retry the whole event.
	encat.SetKEKForTest(makeKEK(0xC4))
	defer encat.SetKEKForTest(nil)
	db := setupDB(t)
	userID := uuid.Must(uuid.NewV7())
	subID := seedSubscription(t, db, userID, "job.completed", "https://hooks.e/x", "secret-eeeeeeeeeeee")

	disp := &recordingDispatcher{err: errors.New("simulated DB outage")}
	_, _ = Fanout(context.Background(), db, disp, sampleEvent(userID, "job.completed"))

	got := loadSub(t, db, subID)
	if got.FailureCount != 0 {
		t.Errorf("failure_count = %d, want 0 (persistence failure shouldn't charge the subscriber)", got.FailureCount)
	}
	if got.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active (persistence failure shouldn't disable)", got.Status)
	}
}
