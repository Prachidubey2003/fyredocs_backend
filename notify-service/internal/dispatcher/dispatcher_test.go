package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"notify-service/internal/channels"
	"notify-service/internal/models"
)

// stubChannel is a Channel impl that records calls and optionally
// returns an error. Avoids spinning up an httptest server for the
// dispatcher's own tests — those concerns belong in webhook_test.
type stubChannel struct {
	calls []channels.SendRequest
	err   error
}

func (s *stubChannel) Send(ctx context.Context, req channels.SendRequest) error {
	s.calls = append(s.calls, req)
	return s.err
}

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Delivery{}); err != nil {
		t.Fatalf("migrate Delivery: %v", err)
	}
	return db
}

func TestDispatch_PersistsDeliveredOnSuccess(t *testing.T) {
	db := setupDB(t)
	d := New(db)
	stub := &stubChannel{}
	d.Register(queue.ChannelWebhook, stub)

	row, err := d.Dispatch(context.Background(), queue.NotifyEvent{
		Channel: queue.ChannelWebhook,
		Target:  "https://example.com/hook",
		UserID:  uuid.New().String(),
		Payload: json.RawMessage(`{"event":"x"}`),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if row.Status != models.StatusDelivered {
		t.Errorf("Status = %q, want %q", row.Status, models.StatusDelivered)
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", row.Attempts)
	}
	if len(stub.calls) != 1 {
		t.Errorf("stub.calls = %d, want 1", len(stub.calls))
	}
	// Row should be re-readable from the DB with the same status.
	var reloaded models.Delivery
	if err := db.First(&reloaded, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != models.StatusDelivered {
		t.Errorf("DB row Status = %q, want %q", reloaded.Status, models.StatusDelivered)
	}
}

func TestDispatch_PersistsFailedOnChannelError(t *testing.T) {
	db := setupDB(t)
	d := New(db)
	stub := &stubChannel{err: errors.New("upstream 503")}
	d.Register(queue.ChannelWebhook, stub)

	row, err := d.Dispatch(context.Background(), queue.NotifyEvent{
		Channel: queue.ChannelWebhook,
		Target:  "https://example.com",
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if row.Status != models.StatusFailed {
		t.Errorf("Status = %q, want %q", row.Status, models.StatusFailed)
	}
	if row.LastError != "upstream 503" {
		t.Errorf("LastError = %q, want \"upstream 503\"", row.LastError)
	}
}

func TestDispatch_SkipsUnsupportedChannel(t *testing.T) {
	db := setupDB(t)
	d := New(db)
	// No channels registered.

	row, err := d.Dispatch(context.Background(), queue.NotifyEvent{
		Channel: queue.ChannelPush,
		Target:  "device-token",
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if row.Status != models.StatusSkipped {
		t.Errorf("Status = %q, want %q", row.Status, models.StatusSkipped)
	}
	if row.Attempts != 0 {
		t.Errorf("Attempts = %d, want 0 (channel never ran)", row.Attempts)
	}
}

func TestDispatch_IdempotencyKeyCollapsesDuplicates(t *testing.T) {
	db := setupDB(t)
	d := New(db)
	stub := &stubChannel{}
	d.Register(queue.ChannelEmail, stub)

	const key = "verify-email-2026-05-16-user-abc"
	event := queue.NotifyEvent{
		Channel:        queue.ChannelEmail,
		Target:         "user@example.com",
		Payload:        json.RawMessage(`{"subject":"hi","text":"hi"}`),
		IdempotencyKey: key,
	}

	first, err := d.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}

	// Replay same event. Should return the same row (by ID) and
	// NOT invoke the channel again.
	beforeCalls := len(stub.calls)
	second, err := d.Dispatch(context.Background(), event)
	if err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("idempotency collapse should return the original row; got new ID")
	}
	if len(stub.calls) != beforeCalls {
		t.Errorf("channel was re-invoked on idempotent replay; calls=%d want=%d",
			len(stub.calls), beforeCalls)
	}
}

func TestDispatch_PersistsUserIDWhenValid(t *testing.T) {
	db := setupDB(t)
	d := New(db)
	d.Register(queue.ChannelWebhook, &stubChannel{})

	u := uuid.New()
	row, err := d.Dispatch(context.Background(), queue.NotifyEvent{
		Channel: queue.ChannelWebhook,
		Target:  "https://example.com",
		UserID:  u.String(),
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if row.UserID == nil || *row.UserID != u {
		t.Errorf("UserID = %v, want %v", row.UserID, u)
	}
}

func TestDispatch_IgnoresMalformedUserID(t *testing.T) {
	// A non-UUID userId on the wire shouldn't cause a 500; the
	// dispatcher just leaves the column nil and records the event.
	db := setupDB(t)
	d := New(db)
	d.Register(queue.ChannelWebhook, &stubChannel{})

	row, err := d.Dispatch(context.Background(), queue.NotifyEvent{
		Channel: queue.ChannelWebhook,
		Target:  "https://example.com",
		UserID:  "not-a-uuid",
		Payload: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if row.UserID != nil {
		t.Errorf("UserID = %v, want nil for invalid wire input", row.UserID)
	}
}
