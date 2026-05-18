// Package eventbridge translates job-service's internal job
// lifecycle events into public DomainEvents on the NOTIFY
// stream. The fanout consumer in notify-service then expands
// each public event into one webhook delivery per matching
// WebhookSubscription.
//
// Why a translator in job-service (not "let each worker
// publish a DomainEvent itself"):
//
//   - Single translation point. Adding a new event type or
//     evolving the public payload shape is one file, not four.
//   - Worker services stay decoupled from the
//     webhook-subscription concept entirely — they continue
//     emitting JobEvents to `jobs.events.<jobID>.<type>` for
//     SSE delivery, unaware of fanout.
//   - The bridge owns the user_id fallback. Workers emit
//     JobEvents without UserID populated; the bridge fetches
//     it from `processing_jobs` when missing, so a webhook
//     subscription on `job.completed` works even for legacy
//     jobs.
//
// What this package does NOT do:
//   - Subscribe to per-job SSE streams. That's the existing
//     SSE handler's concern.
//   - Publish on every JobEvent. Only the terminal events
//     `JobCompleted` and `JobFailed` translate to fanout
//     domain events. `JobProgress` is intentionally NOT a
//     domain event — subscribers want completion, not chatter.
package eventbridge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"gorm.io/gorm"

	"fyredocs/shared/queue"

	"job-service/internal/models"
)

// Bridge owns the JetStream consumer. Stop must be called on
// shutdown to drain the goroutine.
type Bridge struct {
	cc jetstream.ConsumeContext
}

// Start opens a durable consumer on `jobs.events.>` and
// translates terminal events into DomainEvents on
// `notify.event.job.*`. The consumer is named
// `job-events-fanout-bridge` — distinct from the SSE
// per-connection consumers so it's durable across restarts.
func Start(ctx context.Context, js jetstream.JetStream, db *gorm.DB) (*Bridge, error) {
	consumer, err := js.CreateOrUpdateConsumer(ctx, "JOBS_EVENTS", jetstream.ConsumerConfig{
		Durable:       "job-events-fanout-bridge",
		FilterSubject: "jobs.events.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, err
	}
	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		handle(ctx, js, db, msg)
	})
	if err != nil {
		return nil, err
	}
	return &Bridge{cc: cc}, nil
}

// Stop halts the consumer goroutine. Safe on a nil receiver
// and idempotent.
func (b *Bridge) Stop() {
	if b == nil || b.cc == nil {
		return
	}
	b.cc.Stop()
	b.cc = nil
}

// handle decodes one JobEvent and (if terminal) re-publishes
// it as a DomainEvent. Non-terminal events Ack-and-drop.
// Malformed events Ack-and-drop (poison message; redelivery
// won't help). DB-lookup failures Nak so JetStream retries —
// without the user_id we can't fan out, and the fanout
// dispatcher would just skip the row anyway.
func handle(ctx context.Context, js jetstream.JetStream, db *gorm.DB, msg jetstream.Msg) {
	var job queue.JobEvent
	if err := json.Unmarshal(msg.Data(), &job); err != nil {
		slog.Warn("eventbridge: malformed JobEvent; dropping",
			"subject", msg.Subject(), "error", err)
		_ = msg.Ack()
		return
	}

	domainType, ok := mapEventType(job.EventType)
	if !ok {
		// Non-terminal (JobProgress, JobQueued, …) — no
		// fanout side. Ack to advance the consumer cursor.
		_ = msg.Ack()
		return
	}

	userID, err := resolveUserID(ctx, db, job)
	if err != nil {
		// Transient DB error — Nak so JetStream retries.
		// (A truly absent processing_jobs row returns nil
		// from resolveUserID, not an error — those events
		// Ack-and-skip below.)
		slog.Error("eventbridge: user lookup failed; will retry",
			"job_id", job.JobID, "error", err)
		_ = msg.Nak()
		return
	}
	if userID == "" {
		// No anchor for fanout — skip. Logged so an operator
		// notices a misconfigured worker emitting events
		// without populating UserID and without a
		// processing_jobs row matching the JobID.
		slog.Info("eventbridge: skipping JobEvent with no resolvable userId",
			"job_id", job.JobID, "event_type", job.EventType)
		_ = msg.Ack()
		return
	}

	// Build the list of domain events to emit for this
	// JobEvent. Every terminal job emits the generic `job.*`
	// (`job.completed` / `job.failed`); tool-specific events
	// (today: `document.signed` for sign-pdf) are emitted IN
	// ADDITION so subscribers picking the specific event get
	// it AND subscribers picking the generic one still fire.
	domainTypes := []string{domainType}
	if specific, ok := toolSpecificEventType(job.ToolType, job.EventType); ok {
		domainTypes = append(domainTypes, specific)
	}

	for _, eventType := range domainTypes {
		data, err := buildEventPayload(eventType, job, userID)
		if err != nil {
			// Marshal failures are programming bugs (every
			// field is JSON-safe) — log + skip rather than
			// Nak forever.
			slog.Error("eventbridge: payload marshal failed; dropping",
				"job_id", job.JobID, "event_type", eventType, "error", err)
			continue
		}
		event := queue.DomainEvent{
			EventType:  eventType,
			UserID:     userID,
			OccurredAt: job.Timestamp,
			Data:       data,
		}
		if err := queue.PublishDomainEvent(ctx, js, event); err != nil {
			// Publish failure — Nak so JetStream retries. The
			// fanout-subscriber will dedupe via the per-event
			// uuid that PublishDomainEvent assigns, so a
			// late-arriving retry doesn't double-deliver.
			//
			// Note: a partial failure (first event published,
			// second failed) results in a re-publish of BOTH on
			// retry. The fanout's per-subscription idempotency
			// key (`fanout:<eventId>:<subId>`) prevents
			// double-POST for the event that already landed
			// because the eventId is re-assigned per
			// PublishDomainEvent call — so we DO get duplicate
			// fanout deliveries for the successful one on Nak.
			// Acceptable v0 cost; subscribers MUST dedupe on
			// eventId (documented).
			slog.Error("eventbridge: PublishDomainEvent failed; will retry",
				"job_id", job.JobID, "event_type", eventType, "error", err)
			_ = msg.Nak()
			return
		}
	}
	_ = msg.Ack()
}

// toolSpecificEventType returns an additional, tool-specific
// public event type to emit alongside the generic `job.*`.
// Returns `("", false)` when the (tool, jobEventType) pair has
// no specific mapping — the caller emits only the generic event
// in that case.
//
// Today the only specific mapping is `sign-pdf` + JobCompleted
// → `document.signed`. Adding more (e.g., `compress-pdf` →
// `document.compressed`) requires (a) a new entry here AND
// (b) adding the new event type to `allowedEventTypes` in
// notify-service/handlers/webhooks.go — otherwise the
// subscription registry refuses to accept subscriptions to it.
func toolSpecificEventType(tool, jobEventType string) (string, bool) {
	if jobEventType != "JobCompleted" {
		// Tool-specific events are success-only today. A
		// `sign-pdf` JobFailed surfaces as `job.failed` —
		// "the document was NOT signed" doesn't make sense
		// as `document.signed`.
		return "", false
	}
	switch tool {
	case "sign-pdf":
		return "document.signed", true
	default:
		return "", false
	}
}

// mapEventType translates worker-emitted job-lifecycle event
// types into the public fanout event types. Returns
// `("", false)` for non-terminal events so the caller can
// skip without translating.
func mapEventType(t string) (string, bool) {
	switch t {
	case "JobCompleted":
		return "job.completed", true
	case "JobFailed":
		return "job.failed", true
	default:
		return "", false
	}
}

// resolveUserID returns the user_id to anchor the fanout on.
// Prefers `event.UserID` if the worker populated it; falls
// back to a DB lookup against `processing_jobs.user_id`.
// Returns `("", nil)` when there's no row to match — the
// caller Acks + skips (vs. Nak, which would retry forever).
func resolveUserID(ctx context.Context, db *gorm.DB, event queue.JobEvent) (string, error) {
	if event.UserID != "" {
		return event.UserID, nil
	}
	if db == nil || event.JobID == "" {
		return "", nil
	}
	jobID, err := uuid.Parse(event.JobID)
	if err != nil {
		return "", nil
	}
	var row models.ProcessingJob
	err = db.WithContext(ctx).
		Select("user_id").
		Where("id = ?", jobID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.UserID == nil {
		return "", nil
	}
	return row.UserID.String(), nil
}

// jobEventData is the public payload shape every generic
// job-related DomainEvent (`job.completed`, `job.failed`)
// carries. Tighter than the internal JobEvent — subscribers
// don't get Attempts / CorrelationID / Options (which can
// contain internal-only flags).
//
// JSON-tagged with omitempty so the failure-only fields don't
// pollute job.completed events and the success-only fields
// don't pollute job.failed events.
type jobEventData struct {
	JobID         string `json:"jobId"`
	Tool          string `json:"tool"`
	OutputPath    string `json:"outputPath,omitempty"`
	FileSize      int64  `json:"fileSize,omitempty"`
	FailureReason string `json:"failureReason,omitempty"`
}

// signedEventData is the public payload for `document.signed`.
// Extends the base shape with sign-specific fields that
// webhook subscribers (e.g., compliance pipelines, audit
// loggers) need to act on without re-fetching the doc.
//
// Field rationale:
//
//   - signerId — the user who applied the signature. In v0 this
//     mirrors the submitter (job owner); the field is reserved
//     so future delegated-signing flows (admin signs on behalf
//     of an employee) can populate a different uuid without a
//     schema break.
//   - signMode — flags which signing mode produced the output.
//     v0 emits "image" (visual-stamp signature via pdfcpu —
//     not cryptographic). Future cryptographic PAdES support
//     will populate "pades-b-b" / "pades-b-t" / "pades-b-lt"
//     / "pades-b-lta" so receivers can gate on assurance level.
//     Required field — subscribers that only accept verifiable
//     signatures need a way to filter out image stamps without
//     parsing the output PDF.
type signedEventData struct {
	JobID      string `json:"jobId"`
	Tool       string `json:"tool"`
	OutputPath string `json:"outputPath,omitempty"`
	FileSize   int64  `json:"fileSize,omitempty"`
	SignerID   string `json:"signerId"`
	SignMode   string `json:"signMode"`
}

// signModeForTool returns the assurance label for an output
// signed by `tool`. Today only `sign-pdf` exists and emits
// image-stamped signatures via pdfcpu; when cryptographic
// PAdES lands as a separate tool (or as an option on
// sign-pdf), extend this switch.
//
// "image" is the explicit, documented default. We deliberately
// do NOT return an empty string so subscribers always see a
// value — an unset field would be ambiguous (legacy event vs.
// unknown mode).
func signModeForTool(tool string) string {
	switch tool {
	case "sign-pdf":
		return "image"
	default:
		return "image"
	}
}

// buildEventPayload returns the JSON bytes for the public
// payload of `eventType`. Centralises per-event-type shape so
// the handle() loop stays type-agnostic and adding a new event
// type means adding a case here (not threading new fields
// through the loop).
//
// Errors are reserved for json.Marshal failures (shouldn't
// happen — every field is JSON-safe). Caller logs + skips
// rather than Naks the message; redelivery wouldn't help.
func buildEventPayload(eventType string, job queue.JobEvent, userID string) ([]byte, error) {
	switch eventType {
	case "document.signed":
		return json.Marshal(signedEventData{
			JobID:      job.JobID,
			Tool:       job.ToolType,
			OutputPath: job.OutputPath,
			FileSize:   job.FileSize,
			SignerID:   userID,
			SignMode:   signModeForTool(job.ToolType),
		})
	default:
		return json.Marshal(jobEventData{
			JobID:         job.JobID,
			Tool:          job.ToolType,
			OutputPath:    job.OutputPath,
			FileSize:      job.FileSize,
			FailureReason: job.FailureReason,
		})
	}
}
