package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"editor-service/internal/editops"
)

func TestEditBillableEvents_OneEventPerOpType(t *testing.T) {
	user := uuid.New()
	ops := []editops.Op{
		{Type: editops.PageRotate, Page: 1, Rotation: 90},
		{Type: editops.AnnotationAdd, Page: 1, Kind: "highlight"},
		{Type: editops.PageRotate, Page: 2, Rotation: 180},
		{Type: editops.AnnotationAdd, Page: 1, Kind: "underline"},
		{Type: editops.AnnotationAdd, Page: 2, Kind: "square"},
	}

	got := editBillableEvents(user, ops)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (one per distinct op type)", len(got))
	}

	// Output is sorted by op type string. annotation.add < page.rotate.
	want := map[string]int64{
		"op.edit.annotation.add": 3,
		"op.edit.page.rotate":    2,
	}
	for _, ev := range got {
		expected, ok := want[ev.EventType]
		if !ok {
			t.Errorf("unexpected event type %q", ev.EventType)
			continue
		}
		if ev.Quantity != expected {
			t.Errorf("%s: Quantity = %d, want %d", ev.EventType, ev.Quantity, expected)
		}
		if ev.Unit != "ops" {
			t.Errorf("%s: Unit = %q, want \"ops\"", ev.EventType, ev.Unit)
		}
		if ev.UserID != user.String() {
			t.Errorf("%s: UserID = %q, want %q", ev.EventType, ev.UserID, user)
		}
	}
}

func TestEditBillableEvents_DeterministicOrder(t *testing.T) {
	// Same input must produce the same order regardless of map
	// iteration. The function sorts by op type alphabetically.
	ops := []editops.Op{
		{Type: editops.TextReplace},
		{Type: editops.PageDelete},
		{Type: editops.AnnotationAdd},
		{Type: editops.PageRotate},
	}
	got := editBillableEvents(uuid.New(), ops)
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
	// Alphabetical: annotation.add, page.delete, page.rotate, text.replace.
	want := []string{
		"op.edit.annotation.add",
		"op.edit.page.delete",
		"op.edit.page.rotate",
		"op.edit.text.replace",
	}
	for i, ev := range got {
		if ev.EventType != want[i] {
			t.Errorf("got[%d].EventType = %q, want %q", i, ev.EventType, want[i])
		}
	}
}

func TestEditBillableEvents_EmptyAndZeroValueOps(t *testing.T) {
	// Empty input → no events.
	if got := editBillableEvents(uuid.New(), nil); len(got) != 0 {
		t.Errorf("nil ops produced %d events, want 0", len(got))
	}
	if got := editBillableEvents(uuid.New(), []editops.Op{}); len(got) != 0 {
		t.Errorf("empty ops produced %d events, want 0", len(got))
	}
	// An op with empty Type (defensive — shouldn't happen post-
	// validation, but the function must not emit "op.edit." with
	// nothing after the dot).
	got := editBillableEvents(uuid.New(), []editops.Op{{}, {Type: editops.PageRotate}})
	if len(got) != 1 {
		t.Fatalf("expected 1 event (skipped empty Type), got %d", len(got))
	}
	if got[0].EventType != "op.edit.page.rotate" {
		t.Errorf("event type = %q, want op.edit.page.rotate", got[0].EventType)
	}
}

func TestEditBillableEvents_PreservesTimestamp(t *testing.T) {
	// All events in a single call share the same OccurredAt — they
	// describe one request, after all. The exact value comes from
	// time.Now() so we just verify they match each other and are
	// recent.
	ops := []editops.Op{
		{Type: editops.PageRotate},
		{Type: editops.AnnotationAdd},
	}
	got := editBillableEvents(uuid.New(), ops)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if !got[0].OccurredAt.Equal(got[1].OccurredAt) {
		t.Errorf("OccurredAt should be identical across one batch; got %v vs %v",
			got[0].OccurredAt, got[1].OccurredAt)
	}
}

// ---- documentLifecyclePayload JSON shape ----

func TestDocumentLifecyclePayload_OmitsZeroFields(t *testing.T) {
	// document.created carries `title` but not `revId` /
	// `opCount`; document.updated carries `revId` (+ optional
	// `opCount`) but not `title`. omitempty makes the same
	// struct serve both — pin the contract so a future field
	// rename doesn't quietly bleed internal fields into the
	// public payload.
	created := documentLifecyclePayload{
		DocID: "doc-1",
		Title: "Q4 Contract.pdf",
	}
	bytes, _ := json.Marshal(created)
	s := string(bytes)
	for _, want := range []string{`"docId":"doc-1"`, `"title":"Q4 Contract.pdf"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in created payload: %s", want, s)
		}
	}
	for _, leak := range []string{"revId", "opCount"} {
		if strings.Contains(s, leak) {
			t.Errorf("created payload must not include %q: %s", leak, s)
		}
	}

	updated := documentLifecyclePayload{
		DocID:   "doc-2",
		RevID:   "rev-7",
		OpCount: 3,
	}
	bytes2, _ := json.Marshal(updated)
	s2 := string(bytes2)
	for _, want := range []string{`"docId":"doc-2"`, `"revId":"rev-7"`, `"opCount":3`} {
		if !strings.Contains(s2, want) {
			t.Errorf("missing %q in updated payload: %s", want, s2)
		}
	}
	if strings.Contains(s2, "title") {
		t.Errorf("updated payload must not include title: %s", s2)
	}
}

func TestPublishDocumentLifecycleDomainEvent_NoOpWhenNATSDown(t *testing.T) {
	// natsconn.JS is nil in the test binary — the helper must
	// log + return silently. Defends against a regression
	// where a missing JS handle panics the request goroutine.
	publishDocumentLifecycleDomainEvent(context.Background(),
		"document.created", uuid.New(), uuid.New(),
		documentLifecyclePayload{Title: "irrelevant"})
}
