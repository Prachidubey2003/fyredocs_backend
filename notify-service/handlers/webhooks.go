package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/queue"
	"fyredocs/shared/response"

	"notify-service/internal/encat"
	"notify-service/internal/models"
)

// secretByteLen is the entropy size of the generated plaintext
// webhook secret BEFORE base64url encoding. 32 bytes → ~43
// base64url chars; well above the 128-bit symmetric-cipher
// floor, easily fits inside the cap of `secretPrefix` (8 chars
// visible to the user) + the bcrypt hash bound (72 bytes max
// input, which is plenty of room).
const secretByteLen = 32

// maxWebhooksPerUser caps how many subscriptions a single user
// can keep active. 100 is generous — Zapier creates one per
// Trigger instance, and most users have ≤ 20 Zaps. Catches
// a runaway integration that spawns subscriptions without
// cleaning them up.
const maxWebhooksPerUser = 100

// allowedEventTypes is the set of event types webhooks can
// subscribe to. Adding a new one is intentional + requires a
// publisher to exist for it — silently accepting `*` would
// generate dead-letter subscriptions that look healthy but
// never fire. Keep this in lockstep with the future fanout
// dispatcher.
var allowedEventTypes = map[string]bool{
	"job.completed":         true,
	"job.failed":            true,
	"document.created":      true,
	"document.updated":      true,
	"document.signed":       true,
	"subscription.created":  true,
	"subscription.changed":  true,
	"subscription.canceled": true,
}

// CreateWebhookRequest is the inbound shape for POST.
// `secret` is not accepted from the caller — we generate one
// on the server so we know the format / entropy.
type CreateWebhookRequest struct {
	EventType string `json:"eventType"`
	TargetURL string `json:"targetUrl"`
}

// CreateWebhookResponse returns the freshly-created
// subscription PLUS the plaintext secret (shown once). The
// frontend / Zapier app stores the secret; subsequent GETs
// surface only `secretPrefix`.
type CreateWebhookResponse struct {
	models.WebhookSubscription
	Secret string `json:"secret"`
}

// CreateWebhook handles POST /v1/notify/webhooks.
//
// Auth: gin-passed `X-User-ID`. The api-gateway has already
// JWT-verified the caller.
//
// Validation:
//   - eventType MUST be in allowedEventTypes (rejecting unknown
//     events early prevents the user from creating dead-letter
//     subscriptions that look healthy but never fire).
//   - targetUrl MUST be https:// — webhook deliveries carry
//     HMAC-signed payloads that can contain PII; refusing
//     plaintext http is a security boundary. localhost is
//     allowed for dev (http://localhost…) — the helper
//     distinguishes.
//
// Quota: the user has at most maxWebhooksPerUser active
// subscriptions. New attempts past that hit 429 with
// SUBSCRIPTION_QUOTA. (Future: per-plan quotas via auth-service
// — for v0 the cap is a flat number.)
func CreateWebhook(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	var req CreateWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", "Malformed request body")
		return
	}
	req.EventType = strings.TrimSpace(req.EventType)
	req.TargetURL = strings.TrimSpace(req.TargetURL)
	if !allowedEventTypes[req.EventType] {
		response.BadRequest(c, "INVALID_EVENT_TYPE",
			"Unknown event type. See API docs for the supported set.")
		return
	}
	if err := validateTargetURL(req.TargetURL); err != nil {
		response.BadRequest(c, "INVALID_TARGET_URL", err.Error())
		return
	}

	var count int64
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.WebhookSubscription{}).
		Where("user_id = ?", userID).
		Count(&count).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription count", err)
		return
	}
	if count >= maxWebhooksPerUser {
		response.Err(c, http.StatusTooManyRequests, "SUBSCRIPTION_QUOTA",
			"You've reached the webhook-subscription quota for this account.")
		return
	}

	secret, prefix, wrappedDEK, ciphertext, err := generateWebhookSecret()
	if err != nil {
		response.InternalErrorf(c, "SECRET_GEN_FAILED", "Could not generate webhook secret", err)
		return
	}
	sub := models.WebhookSubscription{
		UserID:           userID,
		EventType:        req.EventType,
		TargetURL:        req.TargetURL,
		SecretCiphertext: ciphertext,
		SecretWrappedDEK: wrappedDEK,
		SecretPrefix:     prefix,
		Status:           models.WebhookStatusActive,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&sub).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not persist subscription", err)
		return
	}

	c.JSON(http.StatusCreated, CreateWebhookResponse{
		WebhookSubscription: sub,
		Secret:              secret,
	})
}

// ListWebhooks handles GET /v1/notify/webhooks.
//
// Returns every non-deleted subscription for the caller,
// ordered by created_at DESC. Plain-text secret is NOT in the
// response (the model JSON-tags the hash as `-`).
func ListWebhooks(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	var subs []models.WebhookSubscription
	if err := models.DB.WithContext(c.Request.Context()).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&subs).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not list subscriptions", err)
		return
	}
	response.OK(c, "webhook subscriptions retrieved", gin.H{
		"subscriptions": subs,
	})
}

// DeleteWebhook handles DELETE /v1/notify/webhooks/:id.
//
// Soft-delete (gorm.DeletedAt). We keep the row so audit
// queries can still attribute past deliveries. 404 when the
// row is missing OR belongs to another user — leaks no
// information about which case occurred.
func DeleteWebhook(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	idParam := c.Param("id")
	id, err := uuid.Parse(idParam)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Subscription id must be a UUID")
		return
	}

	var sub models.WebhookSubscription
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND user_id = ?", id, userID).
		First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "SUBSCRIPTION_NOT_FOUND", "Subscription not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}
	if err := models.DB.WithContext(c.Request.Context()).Delete(&sub).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not delete subscription", err)
		return
	}
	response.OK(c, "webhook subscription deleted", gin.H{"id": id})
}

// EnableWebhook handles POST /v1/notify/webhooks/:id/enable.
//
// Resurrects a subscription the circuit breaker auto-disabled
// after the user fixed their receiver. Resets `status` to
// `active` AND zeroes `failure_count` — the breaker's
// per-event accounting starts fresh.
//
// Idempotent: re-enabling an already-active row returns 200
// with the current state (no error). Treating "already active"
// as a 409 would force clients to special-case it; idempotency
// keeps the SPA / Zapier app from needing to read-before-write.
//
// Auth + 404 semantics match DeleteWebhook — a row owned by
// another user reads as "not found" so the API leaks no info
// about which case occurred.
//
// Tombstoned (soft-deleted) rows are not resurrectable — the
// default gorm scope filters them out of the `First` lookup,
// surfacing as 404. Use POST `/v1/notify/webhooks` to create
// a fresh one if a deleted subscription should come back.
func EnableWebhook(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	idParam := c.Param("id")
	id, err := uuid.Parse(idParam)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Subscription id must be a UUID")
		return
	}

	var sub models.WebhookSubscription
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND user_id = ?", id, userID).
		First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "SUBSCRIPTION_NOT_FOUND", "Subscription not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}

	// Always reset the breaker — even when the row is already
	// active, an operator hitting this endpoint after
	// investigating a near-miss wants the counter clear so
	// the next failure starts from 0.
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.WebhookSubscription{}).
		Where("id = ?", sub.ID).
		Updates(map[string]any{
			"status":        models.WebhookStatusActive,
			"failure_count": 0,
		}).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not enable subscription", err)
		return
	}

	// Refresh for the response — the client wants the
	// canonical post-update row to confirm what happened.
	if err := models.DB.WithContext(c.Request.Context()).
		Where("id = ?", sub.ID).
		First(&sub).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not reload subscription", err)
		return
	}
	response.OK(c, "webhook subscription enabled", sub)
}

// RotateWebhookSecret handles POST /v1/notify/webhooks/:id/rotate-secret.
//
// Generates a fresh signing secret for an existing
// subscription. Returns the new plaintext exactly once (same
// disclosure contract as the create endpoint); the user
// updates their receiver's stored copy then the new secret
// signs every subsequent delivery.
//
// Use case: "we think the secret leaked, rotate it without
// recreating the subscription". Same affordance Stripe,
// GitHub, and Slack all offer for webhook signing keys.
//
// Behaviour:
//   - Atomic from the dispatcher's perspective: the row's
//     `secret_ciphertext` + `secret_wrapped_dek` +
//     `secret_prefix` change in a single UPDATE. A fanout
//     in flight at the moment of rotation might briefly use
//     the old secret on a delivery already enqueued; that's
//     acceptable — the dispatcher reads the row fresh on
//     every event, so subsequent deliveries sign with the
//     new key.
//   - No grace period / dual-key window. The old secret is
//     immediately invalid. Users who rotated by mistake can
//     rotate again; users with a real leak want the old
//     secret invalid NOW.
//   - Soft-deleted rows are NOT rotatable (404 via default
//     gorm scope) — they shouldn't be receiving deliveries
//     anyway.
//   - Same 404 semantics as DELETE / enable for cross-user
//     attempts.
func RotateWebhookSecret(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	idParam := c.Param("id")
	id, err := uuid.Parse(idParam)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Subscription id must be a UUID")
		return
	}

	var sub models.WebhookSubscription
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND user_id = ?", id, userID).
		First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "SUBSCRIPTION_NOT_FOUND", "Subscription not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}

	secret, prefix, wrappedDEK, ciphertext, err := generateWebhookSecret()
	if err != nil {
		response.InternalErrorf(c, "SECRET_GEN_FAILED", "Could not generate new secret", err)
		return
	}

	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.WebhookSubscription{}).
		Where("id = ?", sub.ID).
		Updates(map[string]any{
			"secret_ciphertext":  ciphertext,
			"secret_wrapped_dek": wrappedDEK,
			"secret_prefix":      prefix,
		}).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not persist rotated secret", err)
		return
	}

	// Refresh for the response so the SPA can display the
	// new `secretPrefix` alongside the once-shown plaintext.
	if err := models.DB.WithContext(c.Request.Context()).
		Where("id = ?", sub.ID).
		First(&sub).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not reload subscription", err)
		return
	}

	c.JSON(http.StatusOK, CreateWebhookResponse{
		WebhookSubscription: sub,
		Secret:              secret,
	})
}

// TestWebhook handles POST /v1/notify/webhooks/:id/test.
//
// Fires a synthetic `webhook.test` event at the subscription's
// target URL using the recovered per-row signing secret. Lets
// users verify their receiver works without waiting for a real
// event to fire — the same affordance Stripe, GitHub, and
// Slack offer ("send test webhook" / "redeliver" buttons in
// their dashboards).
//
// The synthetic event:
//   - eventType: `webhook.test` (NOT in `allowedEventTypes` —
//     this is a one-off, not a subscribable event; subscribers
//     can dedupe / ignore it via the eventId)
//   - userId: the caller's id
//   - data: `{"test":true,"message":"This is a Fyredocs webhook test event"}`
//
// Behaviour:
//   - Runs through `deps.Disp.DispatchWithSecret` so the
//     attempt lands in `notify_deliveries` like any real
//     dispatch — visible in the frontend's delivery history.
//   - Does NOT touch the row's failure_count / circuit
//     breaker. A test failure is a UX signal, not a "your
//     subscriber is broken" signal worth tripping the breaker
//     over.
//   - Disabled rows are tested anyway — the user is presumably
//     testing whether the receiver is fixed before flipping
//     back to active. The breaker semantics don't apply to a
//     manual test fire.
//
// Returns the full Delivery row (or its summary) so the SPA
// can render success/failure inline with the test button.
func TestWebhook(c *gin.Context) {
	if deps.Disp == nil {
		response.InternalError(c, "NOT_READY", "Dispatcher is not configured")
		return
	}
	userID, ok := requireUserID(c)
	if !ok {
		return
	}
	idParam := c.Param("id")
	id, err := uuid.Parse(idParam)
	if err != nil {
		response.BadRequest(c, "INVALID_ID", "Subscription id must be a UUID")
		return
	}

	// Owner-only — same 404 semantics as DELETE / enable to
	// keep cross-user attempts from leaking which IDs exist.
	var sub models.WebhookSubscription
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND user_id = ?", id, userID).
		First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "SUBSCRIPTION_NOT_FOUND", "Subscription not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not load subscription", err)
		return
	}

	secret, err := encat.OpenSecret(sub.SecretWrappedDEK, sub.SecretCiphertext)
	if err != nil {
		// KEK rolled back / row from a previous generation /
		// envelope tampered with — operator-visible failure
		// the user can't fix. 500 so the SPA shows a clear
		// "contact support" message.
		response.InternalErrorf(c, "DECRYPT_FAILED",
			"Could not recover signing secret for this subscription", err)
		return
	}

	// Synthetic event envelope — same shape as the real
	// fanout's DomainEvent so subscribers parsing the payload
	// don't need a special-case for tests.
	now := time.Now().UTC()
	testEvent := queue.DomainEvent{
		EventID:    "evt_test_" + uuid.Must(uuid.NewV7()).String(),
		EventType:  "webhook.test",
		UserID:     userID.String(),
		OccurredAt: now,
		Data: json.RawMessage(
			`{"test":true,"message":"This is a Fyredocs webhook test event",` +
				`"subscriptionId":"` + sub.ID.String() + `"}`),
	}
	envelope, err := json.Marshal(testEvent)
	if err != nil {
		response.InternalErrorf(c, "MARSHAL_FAILED", "Could not build test payload", err)
		return
	}

	delivery, err := deps.Disp.DispatchWithSecret(c.Request.Context(), queue.NotifyEvent{
		Channel:        queue.ChannelWebhook,
		Target:         sub.TargetURL,
		UserID:         userID.String(),
		Payload:        envelope,
		IdempotencyKey: "test:" + testEvent.EventID, // fresh per request — no dedup intended
		OccurredAt:     now,
	}, secret)
	if err != nil {
		// Persistence-level failure (DB write of the audit
		// row failed). Surface as 500 since the SPA can't
		// usefully retry.
		response.InternalErrorf(c, "DISPATCH_FAILED", "Could not dispatch test event", err)
		return
	}

	// Return the delivery row so the UI can render the
	// outcome inline (status, last_error). The dispatcher
	// already populated status + lastError based on what the
	// receiver said.
	response.OK(c, "test webhook dispatched", gin.H{
		"delivery": delivery,
	})
}

// validateTargetURL enforces the security contract for webhook
// targets. Returns nil iff the URL is parseable AND uses https://
// (with a localhost exception for dev: http://localhost or
// http://127.0.0.1 + a port). Anything else surfaces a clear
// error string.
func validateTargetURL(raw string) error {
	if raw == "" {
		return errors.New("targetUrl is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("targetUrl is not a valid URL")
	}
	if u.Scheme == "https" {
		if u.Host == "" {
			return errors.New("targetUrl must include a host")
		}
		return nil
	}
	if u.Scheme == "http" {
		// Allow only local dev — production webhooks MUST be
		// over TLS. The host check refuses public http hosts
		// even when port 80 is present.
		host := u.Hostname()
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return errors.New("targetUrl must use https:// for non-localhost hosts")
	}
	return errors.New("targetUrl must use http or https")
}

// generateWebhookSecret produces:
//
//	secret     : base64url-encoded plaintext, ~43 chars,
//	             shown ONCE to the API caller
//	prefix     : first 8 chars of the secret (user-visible,
//	             persists on the row for rotation UX)
//	wrappedDEK : the per-row DEK wrapped with the service KEK
//	             (nil when no KEK is configured — pass-through)
//	ciphertext : AES-256-GCM-sealed plaintext (== plaintext
//	             when wrappedDEK is nil)
//
// Returns each separately so the handler stores wrappedDEK +
// ciphertext on the row and returns the plaintext to the
// caller. Tests check that ciphertext != plaintext when a
// KEK is configured.
func generateWebhookSecret() (secret, prefix string, wrappedDEK, ciphertext []byte, err error) {
	buf := make([]byte, secretByteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", "", nil, nil, err
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	if len(secret) < 8 {
		// rand.Read never returns short, but if a future
		// refactor shrinks secretByteLen below the 8-char
		// floor we want to fail fast rather than skip the
		// prefix slice.
		return "", "", nil, nil, errors.New("generated secret too short to derive prefix")
	}
	prefix = secret[:8]
	wrappedDEK, ciphertext, err = encat.SealSecret([]byte(secret))
	if err != nil {
		return "", "", nil, nil, err
	}
	return secret, prefix, wrappedDEK, ciphertext, nil
}

// requireUserID is the same auth shim every notify-service
// handler uses. Lives here (vs. a shared helper file) because
// notify-service has no auth/auth.go yet and a separate file
// would dilute the existing handlers/ shape.
//
// Returns the parsed UUID + true on success; writes a 401 and
// returns false on absent / malformed `X-User-ID`.
func requireUserID(c *gin.Context) (uuid.UUID, bool) {
	raw := strings.TrimSpace(c.GetHeader("X-User-ID"))
	if raw == "" {
		response.Err(c, http.StatusUnauthorized, "UNAUTHORIZED", "Please log in.")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_USER", "Caller identity is malformed.")
		return uuid.Nil, false
	}
	return id, true
}
