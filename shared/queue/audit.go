package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// AuditEvent is the wire-format envelope for tamper-evident
// audit records. Any service that performs an
// account-attributable mutation (sign-in, plan change, document
// edit, key revocation, redact, share-link revoke, …) publishes
// one of these. analytics-service is the durable consumer that
// appends them to the hash-chained `audit_events` table.
//
// Why hash-chain at all: SOC2 / HIPAA / contract disputes require
// "prove this row wasn't inserted/edited/deleted after the
// fact". An append-only column + RLS REVOKE UPDATE/DELETE keeps
// honest insiders honest; the hash chain catches the dishonest
// case where someone with raw DB access rewrites history. See
// plan §3.10.
//
// Field rules:
//   - Actor: who took the action. Free-form so it can carry a
//     user UUID, an API key ID, or the literal "system" for
//     scheduled tasks. The auditor reads it as-is.
//   - Action: stable string identifier, e.g. `document.edit`,
//     `auth.login`, `apikey.revoke`, `plan.changed`. Never rename
//     these — the chain is keyed off this string forever.
//   - Resource: optional pointer at WHAT was changed
//     (`doc_abc123`, `key_xyz`, `user_def`).
//   - Metadata: free-form JSON; what's in it is action-specific
//     (e.g. for plan.changed: `{oldPlan, newPlan}`).
type AuditEvent struct {
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	OccurredAt time.Time       `json:"occurredAt"`
}

// SubjectForAudit returns the NATS subject for an audit event.
// The per-action suffix lets future fan-out (e.g., a security
// SIEM bridge subscribing to `audit.events.auth.*`) target one
// family without consuming every other.
func SubjectForAudit(action string) string {
	return "audit.events." + action
}

// PublishAuditEvent marshals + publishes an AuditEvent onto the
// AUDIT JetStream. Best-effort by convention: callers typically
// `go queue.PublishAuditEvent(...)` because a missing audit row
// shouldn't fail the user-facing operation that produced it. A
// failed publish is a SEV2 paging-worthy event for on-call (the
// audit chain has a gap) but not a 5xx for the user.
func PublishAuditEvent(ctx context.Context, js jetstream.JetStream, event AuditEvent) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = js.Publish(ctx, SubjectForAudit(event.Action), data)
	return err
}
