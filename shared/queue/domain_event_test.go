package queue

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSubjectForDomainEvent(t *testing.T) {
	cases := []struct{ in, want string }{
		{"job.completed", "notify.event.job.completed"},
		{"subscription.changed", "notify.event.subscription.changed"},
		{"document.signed", "notify.event.document.signed"},
	}
	for _, tc := range cases {
		if got := SubjectForDomainEvent(tc.in); got != tc.want {
			t.Errorf("SubjectForDomainEvent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDomainEvent_JSONRoundtrip(t *testing.T) {
	in := DomainEvent{
		EventID:   "evt_01HW000000000000000000",
		EventType: "job.completed",
		UserID:    "11111111-1111-1111-1111-111111111111",
		Data:      json.RawMessage(`{"jobId":"j_1","tool":"merge"}`),
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out DomainEvent
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.EventID != in.EventID || out.EventType != in.EventType || out.UserID != in.UserID {
		t.Errorf("round-trip mismatch: got %+v want %+v", out, in)
	}
	if string(out.Data) != string(in.Data) {
		t.Errorf("data mismatch: got %s want %s", out.Data, in.Data)
	}
}

// PublishDomainEvent runs through the real JetStream client and
// can't be unit-tested without a NATS dependency. We test the
// validation + defaulting branches the function controls, NOT
// the publish itself — that's exercised by the notify-service
// integration suite.

func TestPublishDomainEvent_RejectsEmptyEventType(t *testing.T) {
	err := PublishDomainEvent(nil, nil, DomainEvent{
		EventID: "evt", UserID: "u",
	})
	if !errors.Is(err, ErrEventTypeRequired) {
		t.Errorf("expected ErrEventTypeRequired; got %v", err)
	}
}

func TestPublishDomainEvent_RejectsWhitespaceEventType(t *testing.T) {
	// `\t\n   ` — trim leaves an empty string, which should
	// fail the validation. Defends against a publisher
	// accidentally calling with an empty-but-not-quite-empty
	// type that would otherwise land on `notify.event.` (a
	// silent dead letter).
	err := PublishDomainEvent(nil, nil, DomainEvent{
		EventType: "\t\n   ",
		EventID:   "evt", UserID: "u",
	})
	if !errors.Is(err, ErrEventTypeRequired) {
		t.Errorf("expected ErrEventTypeRequired on whitespace-only type; got %v", err)
	}
}
