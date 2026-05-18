package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"fyredocs/shared/natsconn"
	"fyredocs/shared/queue"

	"editor-service/internal/editops"
)

// CommentEventsSubjectPrefix is the NATS subject prefix used for
// real-time comment notifications. Subscribers (today:
// collab-service) listen on `editor.comments.>`. Per-doc routing
// is encoded in the subject: `editor.comments.<docID>`.
//
// We use core NATS pub/sub (not JetStream) because these events
// are ephemeral. A subscriber that's offline at publish time has
// nothing to catch up on — the next ListComments refresh will
// surface the change. Persistence is overkill and would force
// every collab-service replica to drain a stream on startup.
const CommentEventsSubjectPrefix = "editor.comments"

// commentEventKind tags each event in the JSON envelope. The
// frontend matches on this and updates the local comments state.
type commentEventKind string

const (
	commentAddedEvent    commentEventKind = "comment.added"
	commentResolvedEvent commentEventKind = "comment.resolved"
)

// commentEvent is the JSON wire shape for comment notifications.
// Comment is the enriched response shape (display name resolved)
// so subscribers don't need a second auth-service hop to render.
type commentEvent struct {
	Kind    commentEventKind `json:"kind"`
	DocID   string           `json:"docId"`
	Comment *commentResponse `json:"comment,omitempty"`   // set for comment.added
	ID      string           `json:"id,omitempty"`        // set for comment.resolved
}

// publishCommentAdded broadcasts a freshly-created comment to any
// collab-service replicas listening on the doc's subject. The
// comment carries the enriched display-name so receivers can
// render it without a second auth-service lookup.
//
// Best-effort: if NATS is down, logged and dropped. The comment
// row is already persisted; the user sees their own write
// immediately (local state), and any peer sees it on the next
// ListComments refresh.
func publishCommentAdded(docID uuid.UUID, comment commentResponse) {
	if natsconn.Conn == nil {
		return
	}
	ev := commentEvent{
		Kind:    commentAddedEvent,
		DocID:   docID.String(),
		Comment: &comment,
	}
	publishCommentEvent(docID, ev)
}

// publishCommentResolved fans a resolve event so peers can flip
// the "resolved" badge in their UI without a refetch.
func publishCommentResolved(docID, commentID uuid.UUID) {
	if natsconn.Conn == nil {
		return
	}
	ev := commentEvent{
		Kind:  commentResolvedEvent,
		DocID: docID.String(),
		ID:    commentID.String(),
	}
	publishCommentEvent(docID, ev)
}

// publishEditBillable emits one billable event PER OP TYPE in a
// successful /v1/documents/:id/edit call.
//
// Why per op type rather than one `op.edit` row: the rollup
// surface in /v1/usage/me already aggregates by event type, so
// granular naming (`op.edit.text.replace`, `op.edit.annotation.add`)
// lets the billing UI show users what they're actually doing
// without a second join. It also lets billing-service price
// distinct op kinds differently down the road (e.g., a text
// replace might count for more than a page rotate).
//
// Quantity is the COUNT of that op type within the request — a
// /edit batch with three annotation.add ops produces one
// BillableEvent with Quantity=3, not three events. Cheaper to
// publish + easier to aggregate.
//
// Best-effort: NATS unavailable or publish failure is logged at
// Warn and dropped. The revision is already committed; missing a
// usage row is preferable to a 5xx on the user's edit call.
func publishEditBillable(ctx context.Context, userID uuid.UUID, ops []editops.Op) {
	if natsconn.JS == nil {
		return
	}
	for _, event := range editBillableEvents(userID, ops) {
		if err := queue.PublishBillableEvent(ctx, natsconn.JS, event); err != nil {
			slog.Warn("publishEditBillable: publish failed",
				"userId", userID, "eventType", event.EventType, "error", err)
		}
	}
}

// publishDocumentLifecycleDomainEvent emits a public DomainEvent
// on `notify.event.document.<verb>` so notify-service's fanout
// consumer can deliver one webhook per matching
// WebhookSubscription. Distinct from publishDocumentEditAudit
// (audit log, internal) — these are the events external
// integrations subscribe to.
//
// Best-effort: NATS unavailable / publish failure is logged at
// Warn and dropped. The document write already succeeded; the
// caller's UX is unaffected. notify-service consumers dedupe
// on the event's eventId (assigned by PublishDomainEvent), so
// a late retry from the publisher does not double-fire on
// subscribers.
//
// `extra` is the event-type-specific payload merged into the
// public envelope's `data` field. For `document.updated` this
// includes the new rev id; callers leave it nil for
// `document.created` (the doc id + title are enough).
func publishDocumentLifecycleDomainEvent(ctx context.Context, eventType string, userID, docID uuid.UUID, doc documentLifecyclePayload) {
	if natsconn.JS == nil {
		return
	}
	doc.DocID = docID.String()
	data, err := json.Marshal(doc)
	if err != nil {
		slog.Warn("publishDocumentLifecycleDomainEvent: marshal failed",
			"userId", userID, "docId", docID, "eventType", eventType, "error", err)
		return
	}
	event := queue.DomainEvent{
		EventType:  eventType,
		UserID:     userID.String(),
		OccurredAt: time.Now().UTC(),
		Data:       data,
	}
	if err := queue.PublishDomainEvent(ctx, natsconn.JS, event); err != nil {
		slog.Warn("publishDocumentLifecycleDomainEvent: publish failed",
			"userId", userID, "docId", docID, "eventType", eventType, "error", err)
	}
}

// documentLifecyclePayload is the public payload shape every
// `document.*` DomainEvent carries. Tight by design — internal
// fields (storageKey, currentRevId, etc.) DO NOT leak to
// subscribers. omitempty keeps `document.created` events from
// carrying revision fields and vice versa.
type documentLifecyclePayload struct {
	DocID   string `json:"docId"`
	Title   string `json:"title,omitempty"`
	RevID   string `json:"revId,omitempty"`
	OpCount int    `json:"opCount,omitempty"`
}

// publishDocumentEditAudit appends one audit row per successful
// edit, capturing the document ID + the op-type histogram in
// `metadata` so the auditor sees what was changed without
// joining against the revisions table.
//
// Best-effort: a NATS hiccup doesn't fail the user-facing edit.
// The PR-mandated REVOKE on the audit table + the verifier are
// the longer-term integrity guarantees; a single missing row is
// a SEV2 paging concern but not a 5xx for the user.
func publishDocumentEditAudit(ctx context.Context, userID, docID uuid.UUID, ops []editops.Op) {
	if natsconn.JS == nil {
		return
	}
	histogram := make(map[editops.OpType]int64)
	for _, op := range ops {
		if op.Type != "" {
			histogram[op.Type]++
		}
	}
	meta, _ := json.Marshal(map[string]any{
		"docId": docID.String(),
		"ops":   histogram,
	})
	event := queue.AuditEvent{
		Actor:      userID.String(),
		Action:     "document.edit",
		Resource:   docID.String(),
		Metadata:   meta,
		OccurredAt: time.Now().UTC(),
	}
	if err := queue.PublishAuditEvent(ctx, natsconn.JS, event); err != nil {
		slog.Warn("publishDocumentEditAudit: publish failed",
			"userId", userID, "docId", docID, "error", err)
	}
}

// editBillableEvents is the pure function the publisher wraps —
// pulled out so tests can drive it without a live NATS. Returns
// one BillableEvent per distinct op.Type in `ops`, with Quantity
// = count of that type. Order is deterministic by op type so
// snapshot-style tests stay stable.
//
// The event-type taxonomy is `op.edit.<opType>` — `<opType>` is
// the wire-format op type from editops verbatim. This mirrors
// the wire-format identifiers the OpenAPI spec documents, so
// billing-service / dashboards don't need a translation table.
func editBillableEvents(userID uuid.UUID, ops []editops.Op) []queue.BillableEvent {
	counts := make(map[editops.OpType]int64)
	for _, op := range ops {
		if op.Type == "" {
			continue
		}
		counts[op.Type]++
	}
	if len(counts) == 0 {
		return nil
	}
	keys := make([]editops.OpType, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	now := time.Now().UTC()
	out := make([]queue.BillableEvent, 0, len(keys))
	for _, k := range keys {
		out = append(out, queue.BillableEvent{
			UserID:     userID.String(),
			EventType:  "op.edit." + string(k),
			Quantity:   counts[k],
			Unit:       "ops",
			OccurredAt: now,
		})
	}
	return out
}

func publishCommentEvent(docID uuid.UUID, ev commentEvent) {
	subject := CommentEventsSubjectPrefix + "." + docID.String()
	body, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("publishCommentEvent: marshal failed",
			"doc", docID, "kind", ev.Kind, "error", err)
		return
	}
	if err := natsconn.Conn.Publish(subject, body); err != nil {
		// Logged at Warn rather than Error: a transient NATS
		// outage doesn't compromise correctness, just delays
		// the live-update UX until peers refetch.
		slog.Warn("publishCommentEvent: NATS publish failed",
			"doc", docID, "kind", ev.Kind, "error", err)
		return
	}
}

