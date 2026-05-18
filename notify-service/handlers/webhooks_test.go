package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"notify-service/internal/encat"
	"notify-service/internal/models"
)

// testKEK is the deterministic master key the webhook tests
// install for the sealed-secret round-trip checks. Used by the
// happy-path tests; the pass-through tests leave it unset to
// exercise the no-KEK branch.
func testKEK(seed byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed
	}
	return k
}

// newWebhookRouter spins up a router with only the webhook
// routes wired. setupTest takes care of the DB; we extend the
// returned engine with the webhook routes.
func newWebhookRouter(t *testing.T) (*gin.Engine, func()) {
	r, _, restore := setupTest(t)
	r.POST("/v1/notify/webhooks", CreateWebhook)
	r.GET("/v1/notify/webhooks", ListWebhooks)
	r.DELETE("/v1/notify/webhooks/:id", DeleteWebhook)
	r.POST("/v1/notify/webhooks/:id/enable", EnableWebhook)
	r.POST("/v1/notify/webhooks/:id/test", TestWebhook)
	r.POST("/v1/notify/webhooks/:id/rotate-secret", RotateWebhookSecret)
	return r, restore
}

// validCreateBody returns the JSON body for a happy-path create.
func validCreateBody(t *testing.T, eventType, target string) []byte {
	t.Helper()
	b, _ := json.Marshal(CreateWebhookRequest{EventType: eventType, TargetURL: target})
	return b
}

// ---- happy path ----

func TestCreateWebhook_PersistsAndReturnsPlaintextSecret(t *testing.T) {
	encat.SetKEKForTest(testKEK(0x11))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())

	body := validCreateBody(t, "job.completed", "https://hooks.zapier.com/x")
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks", bytes.NewReader(body))
	req.Header.Set("X-User-ID", userID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var resp CreateWebhookResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, w.Body.String())
	}
	if resp.ID == uuid.Nil {
		t.Error("response missing id")
	}
	if resp.Secret == "" {
		t.Error("response missing plaintext secret — must be shown once at creation")
	}
	if resp.SecretPrefix == "" || !strings.HasPrefix(resp.Secret, resp.SecretPrefix) {
		t.Errorf("secretPrefix must be the first 8 chars of secret; got prefix=%q secret=%q",
			resp.SecretPrefix, resp.Secret)
	}
	if resp.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active", resp.Status)
	}

	// Stored secret is envelope-encrypted — round-trip via
	// encat.OpenSecret to verify the row carries usable
	// ciphertext (the fanout dispatcher will use the same call
	// to recover plaintext for HMAC signing).
	var stored models.WebhookSubscription
	if err := models.DB.Where("id = ?", resp.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored row: %v", err)
	}
	if len(stored.SecretCiphertext) == 0 {
		t.Fatal("stored row missing SecretCiphertext")
	}
	if len(stored.SecretWrappedDEK) == 0 {
		t.Fatal("stored row missing SecretWrappedDEK (KEK was set — must seal)")
	}
	plain, err := encat.OpenSecret(stored.SecretWrappedDEK, stored.SecretCiphertext)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if string(plain) != resp.Secret {
		t.Errorf("decrypted secret does not match returned plaintext")
	}
	// Encryption actually happened — ciphertext must not
	// equal plaintext (defends against a regression where
	// SealSecret silently fell back to pass-through).
	if string(stored.SecretCiphertext) == resp.Secret {
		t.Error("ciphertext equals plaintext — encryption was a no-op")
	}
}

func TestCreateWebhook_AllowsLocalhostHTTP(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())

	req := httptest.NewRequest(
		http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, "job.completed", "http://localhost:3000/hook")),
	)
	req.Header.Set("X-User-ID", userID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (localhost http allowed for dev); body=%s", w.Code, w.Body.String())
	}
}

// ---- validation rejections ----

func TestCreateWebhook_RejectsUnauthenticated(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, "job.completed", "https://hooks.example.com/x")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestCreateWebhook_RejectsUnknownEventType(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, "frobnicate.completed", "https://hooks.example.com/x")))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_EVENT_TYPE") {
		t.Errorf("expected INVALID_EVENT_TYPE; got %s", w.Body.String())
	}
}

func TestCreateWebhook_RejectsPlaintextHTTPTarget(t *testing.T) {
	// http:// to a non-localhost host must be rejected — webhook
	// payloads carry PII and the HMAC isn't useful if the wire is
	// plaintext.
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, "job.completed", "http://hooks.evil.example/x")))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "INVALID_TARGET_URL") {
		t.Errorf("expected INVALID_TARGET_URL; got %s", w.Body.String())
	}
}

func TestCreateWebhook_RejectsMalformedURL(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, "job.completed", "not a url at all")))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateWebhook_RejectsMalformedJSON(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		strings.NewReader(`{not json`))
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ---- list ----

func TestListWebhooks_OnlyReturnsCallerSubscriptions(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	userA := uuid.Must(uuid.NewV7())
	userB := uuid.Must(uuid.NewV7())

	// userA creates two; userB creates one.
	for _, ev := range []string{"job.completed", "subscription.changed"} {
		mustCreate(t, r, userA, ev, "https://hooks.example.com/a")
	}
	mustCreate(t, r, userB, "job.completed", "https://hooks.example.com/b")

	req := httptest.NewRequest(http.MethodGet, "/v1/notify/webhooks", nil)
	req.Header.Set("X-User-ID", userA.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var envelope struct {
		Data struct {
			Subscriptions []models.WebhookSubscription `json:"subscriptions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, w.Body.String())
	}
	if len(envelope.Data.Subscriptions) != 2 {
		t.Errorf("expected 2 subscriptions for userA; got %d", len(envelope.Data.Subscriptions))
	}
	for _, s := range envelope.Data.Subscriptions {
		if s.UserID != userA {
			t.Errorf("list leaked another user's row: %v", s.UserID)
		}
	}
}

func TestListWebhooks_DoesNotLeakSecretsInResponseBody(t *testing.T) {
	encat.SetKEKForTest(testKEK(0x33))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")

	// Load the stored ciphertext + wrapped DEK from the DB —
	// the create response itself MUST NOT carry them
	// (json:"-"), so we can't read them off the resp struct.
	var stored models.WebhookSubscription
	if err := models.DB.Where("id = ?", resp.ID).First(&stored).Error; err != nil {
		t.Fatalf("load stored row: %v", err)
	}
	if len(stored.SecretCiphertext) == 0 {
		t.Fatal("stored row has empty SecretCiphertext — handler did not persist the sealed secret")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/notify/webhooks", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	body := w.Body.String()
	// Neither the sealed ciphertext (as base64-ish text) nor
	// the plaintext secret may appear in the response. The
	// ciphertext check uses a non-trivial slice from the
	// middle of the bytes so we don't false-trigger on common
	// short sequences.
	if len(stored.SecretCiphertext) > 16 {
		mid := stored.SecretCiphertext[8 : 8+8] // 8 bytes from the middle
		if strings.Contains(body, string(mid)) {
			t.Error("list response leaked SecretCiphertext bytes")
		}
	}
	if resp.Secret == "" {
		t.Fatal("create response had empty plaintext secret — fixture failed")
	}
	if strings.Contains(body, resp.Secret) {
		t.Error("list response leaked plaintext secret")
	}
}

// ---- delete ----

func TestDeleteWebhook_SoftDeletesCallerRow(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")

	req := httptest.NewRequest(http.MethodDelete, "/v1/notify/webhooks/"+resp.ID.String(), nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Soft-deleted: a plain Find() doesn't see it.
	var rows []models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).Find(&rows).Error
	if len(rows) != 0 {
		t.Errorf("expected row to be hidden by default scope; got %d", len(rows))
	}
	// But Unscoped() still does — soft-delete preserves audit.
	var unscoped []models.WebhookSubscription
	_ = models.DB.Unscoped().Where("id = ?", resp.ID).Find(&unscoped).Error
	if len(unscoped) != 1 {
		t.Errorf("expected row to remain in DB for audit; got %d", len(unscoped))
	}
}

func TestDeleteWebhook_RefusesAnotherUsersRow(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	owner := uuid.Must(uuid.NewV7())
	attacker := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, owner, "job.completed", "https://hooks.example.com/x")

	req := httptest.NewRequest(http.MethodDelete, "/v1/notify/webhooks/"+resp.ID.String(), nil)
	req.Header.Set("X-User-ID", attacker.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no info leak); body=%s", w.Code, w.Body.String())
	}

	// Original row untouched.
	var rows []models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).Find(&rows).Error
	if len(rows) != 1 {
		t.Errorf("owner's row should still exist after cross-user delete attempt; got %d", len(rows))
	}
}

func TestDeleteWebhook_RejectsMalformedID(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodDelete, "/v1/notify/webhooks/not-a-uuid", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// ---- validateTargetURL unit tests ----

func TestValidateTargetURL_Variants(t *testing.T) {
	cases := []struct {
		raw     string
		wantErr bool
	}{
		{"https://hooks.zapier.com/abc", false},
		{"https://x.example.com:8443/path?q=1", false},
		{"http://localhost:3000/dev", false},
		{"http://127.0.0.1:3000/dev", false},
		{"http://[::1]:3000/dev", false},
		{"http://hooks.evil.example/x", true},
		{"ftp://files.example/x", true},
		{"https://", true},
		{"", true},
		{"not a url", true},
	}
	for _, tc := range cases {
		err := validateTargetURL(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateTargetURL(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
		}
	}
}

// ---- generateWebhookSecret unit tests ----

func TestGenerateWebhookSecret_SealsAndRoundTrips(t *testing.T) {
	encat.SetKEKForTest(testKEK(0x22))
	defer encat.SetKEKForTest(nil)

	secret, prefix, wrappedDEK, ciphertext, err := generateWebhookSecret()
	if err != nil {
		t.Fatalf("generateWebhookSecret: %v", err)
	}
	if !strings.HasPrefix(secret, prefix) {
		t.Errorf("prefix %q is not a prefix of secret %q", prefix, secret)
	}
	if len(prefix) != 8 {
		t.Errorf("prefix len = %d, want 8", len(prefix))
	}
	if len(wrappedDEK) == 0 || len(ciphertext) == 0 {
		t.Fatal("encrypted-mode call returned empty wrappedDEK/ciphertext")
	}
	plain, err := encat.OpenSecret(wrappedDEK, ciphertext)
	if err != nil {
		t.Fatalf("OpenSecret round-trip: %v", err)
	}
	if string(plain) != secret {
		t.Errorf("round-trip mismatch: got %q, want %q", plain, secret)
	}
	if string(ciphertext) == secret {
		t.Error("ciphertext equals plaintext — encryption was a no-op")
	}
}

func TestGenerateWebhookSecret_PassThroughWhenKEKUnset(t *testing.T) {
	// No KEK configured → wrappedDEK is nil and ciphertext IS
	// the plaintext. The dev/staging path stays usable without
	// a KEK env, but operators rolling to prod must set one
	// (tracked via readyz when the readiness check lands).
	encat.SetKEKForTest(nil)
	t.Setenv("NOTIFY_SECRET_KEK_HEX", "")

	secret, _, wrappedDEK, ciphertext, err := generateWebhookSecret()
	if err != nil {
		t.Fatalf("generateWebhookSecret: %v", err)
	}
	if wrappedDEK != nil {
		t.Errorf("expected wrappedDEK to be nil in pass-through; got %d bytes", len(wrappedDEK))
	}
	if string(ciphertext) != secret {
		t.Error("pass-through ciphertext must equal plaintext")
	}
}

func TestGenerateWebhookSecret_ProducesDistinctValues(t *testing.T) {
	// Two calls must produce different secrets (entropy +
	// no global state corruption). 32 bytes of crypto/rand
	// collision is astronomically rare.
	a, _, _, _, _ := generateWebhookSecret()
	b, _, _, _, _ := generateWebhookSecret()
	if a == b {
		t.Errorf("two consecutive secrets identical (rand reuse?): %q", a)
	}
}

// ---- helpers ----

// mustCreate fires a CreateWebhook request and returns the
// parsed response. Fails the test on any non-201 result.
func mustCreate(t *testing.T, r *gin.Engine, userID uuid.UUID, eventType, target string) CreateWebhookResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/notify/webhooks",
		bytes.NewReader(validCreateBody(t, eventType, target)))
	req.Header.Set("X-User-ID", userID.String())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("mustCreate failed: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp CreateWebhookResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mustCreate parse: %v", err)
	}
	return resp
}

// ---- enable ----

// markDisabled flips a subscription to disabled + sets a
// non-zero failure_count, simulating what the circuit breaker
// would have done after FailureThreshold consecutive failures.
func markDisabled(t *testing.T, subID uuid.UUID, failures int) {
	t.Helper()
	if err := models.DB.Model(&models.WebhookSubscription{}).
		Where("id = ?", subID).
		Updates(map[string]any{
			"status":        models.WebhookStatusDisabled,
			"failure_count": failures,
		}).Error; err != nil {
		t.Fatalf("markDisabled: %v", err)
	}
}

func TestEnableWebhook_RestoresDisabledRowAndResetsFailureCount(t *testing.T) {
	encat.SetKEKForTest(testKEK(0xE0))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())

	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")
	markDisabled(t, resp.ID, 15)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/enable", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Row is now active + failure_count is 0.
	var stored models.WebhookSubscription
	if err := models.DB.Where("id = ?", resp.ID).First(&stored).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if stored.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active", stored.Status)
	}
	if stored.FailureCount != 0 {
		t.Errorf("failure_count = %d, want 0", stored.FailureCount)
	}
}

func TestEnableWebhook_IdempotentOnAlreadyActiveRow(t *testing.T) {
	// Hitting the endpoint on a healthy subscription must NOT
	// error — operators sometimes hit it preemptively after
	// investigating a near-miss; the contract is "ensure
	// active + reset counter".
	encat.SetKEKForTest(testKEK(0xE1))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())

	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")
	// Set a partial failure_count (didn't trip the breaker yet);
	// the endpoint should still reset it.
	if err := models.DB.Model(&models.WebhookSubscription{}).
		Where("id = ?", resp.ID).
		Update("failure_count", 5).Error; err != nil {
		t.Fatalf("preset failure_count: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/enable", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent)", w.Code)
	}

	var stored models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&stored).Error
	if stored.FailureCount != 0 {
		t.Errorf("failure_count = %d, want 0", stored.FailureCount)
	}
	if stored.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active", stored.Status)
	}
}

func TestEnableWebhook_RefusesAnotherUsersRow(t *testing.T) {
	encat.SetKEKForTest(testKEK(0xE2))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	owner := uuid.Must(uuid.NewV7())
	attacker := uuid.Must(uuid.NewV7())

	resp := mustCreate(t, r, owner, "job.completed", "https://hooks.example.com/x")
	markDisabled(t, resp.ID, 15)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/enable", nil)
	req.Header.Set("X-User-ID", attacker.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no info leak); body=%s", w.Code, w.Body.String())
	}

	// Row must remain disabled — attacker had no business
	// resurrecting it.
	var stored models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&stored).Error
	if stored.Status != models.WebhookStatusDisabled {
		t.Errorf("status = %q, want disabled (unchanged by cross-user attempt)", stored.Status)
	}
}

func TestEnableWebhook_RejectsMalformedID(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/not-a-uuid/enable", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestEnableWebhook_RejectsUnauthenticated(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+uuid.Must(uuid.NewV7()).String()+"/enable", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestEnableWebhook_TombstonedRowStaysDeleted(t *testing.T) {
	// A soft-deleted subscription must NOT be resurrectable
	// via /enable. The default gorm scope filters it out;
	// the user has to create a fresh subscription instead.
	encat.SetKEKForTest(testKEK(0xE3))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())

	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")
	// Soft-delete the row.
	if err := models.DB.Delete(&models.WebhookSubscription{}, "id = ?", resp.ID).Error; err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/enable", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (deleted rows are unrecoverable); body=%s", w.Code, w.Body.String())
	}
}

// ---- test (fire synthetic event) ----

func TestTestWebhook_DispatchesSyntheticEvent(t *testing.T) {
	// Wires the dispatcher to an okChannel so the
	// `webhook.test` event lands cleanly. The test asserts
	// (a) 200 status, (b) the dispatcher was called.
	encat.SetKEKForTest(testKEK(0xF0))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/test", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"delivery"`) {
		t.Errorf("expected delivery object in body; got %s", w.Body.String())
	}

	// One Delivery row landed — the test fire is auditable
	// alongside real fanout deliveries.
	var count int64
	models.DB.Model(&models.Delivery{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 delivery row after test fire; got %d", count)
	}
}

func TestTestWebhook_DoesNotMutateFailureCount(t *testing.T) {
	// A test fire's failure must NOT charge the circuit
	// breaker — users are EXPECTED to hit /test when their
	// receiver is broken to figure out what's wrong. Pinning
	// this defends against a future refactor that
	// accidentally folds the test path into the breaker.
	encat.SetKEKForTest(testKEK(0xF1))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/test", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var stored models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&stored).Error
	if stored.FailureCount != 0 {
		t.Errorf("failure_count = %d after test fire; want 0 (breaker should not engage)",
			stored.FailureCount)
	}
}

func TestTestWebhook_DispatchesEvenWhenDisabled(t *testing.T) {
	// The user disabled their subscription (or the breaker
	// did) — they may be testing whether the receiver is
	// fixed before flipping back to active. The test path
	// dispatches regardless of status.
	encat.SetKEKForTest(testKEK(0xF2))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")
	markDisabled(t, resp.ID, 15)

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/test", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 even when disabled; body=%s", w.Code, w.Body.String())
	}
}

func TestTestWebhook_RefusesAnotherUsersRow(t *testing.T) {
	encat.SetKEKForTest(testKEK(0xF3))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	owner := uuid.Must(uuid.NewV7())
	attacker := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, owner, "job.completed", "https://hooks.example.com/x")

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/test", nil)
	req.Header.Set("X-User-ID", attacker.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no info leak); body=%s", w.Code, w.Body.String())
	}
	// Dispatcher must not have been called for an attacker's
	// request — a delivery row for another user's URL would
	// be a real security bug.
	var count int64
	models.DB.Model(&models.Delivery{}).Count(&count)
	if count != 0 {
		t.Errorf("attacker triggered a delivery; got %d rows", count)
	}
}

func TestTestWebhook_RejectsMalformedID(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/not-a-uuid/test", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestTestWebhook_RejectsUnauthenticated(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+uuid.Must(uuid.NewV7()).String()+"/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ---- rotate-secret ----

func TestRotateWebhookSecret_ReturnsNewPlaintextAndUpdatesRow(t *testing.T) {
	encat.SetKEKForTest(testKEK(0xF8))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "job.completed", "https://hooks.example.com/x")

	var before models.WebhookSubscription
	if err := models.DB.Where("id = ?", resp.ID).First(&before).Error; err != nil {
		t.Fatalf("load before: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/rotate-secret", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var rotated CreateWebhookResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, w.Body.String())
	}
	if rotated.Secret == "" {
		t.Error("response missing plaintext secret after rotation")
	}
	if rotated.Secret == resp.Secret {
		t.Error("rotated secret equals the original — rotation did nothing")
	}
	if !strings.HasPrefix(rotated.Secret, rotated.SecretPrefix) {
		t.Errorf("secretPrefix must be prefix of new secret; prefix=%q secret=%q",
			rotated.SecretPrefix, rotated.Secret)
	}

	// Row's stored ciphertext + prefix changed; row id is the
	// same (rotation MUST be in-place — never deletes + recreates).
	var after models.WebhookSubscription
	if err := models.DB.Where("id = ?", resp.ID).First(&after).Error; err != nil {
		t.Fatalf("load after: %v", err)
	}
	if after.ID != resp.ID {
		t.Errorf("subscription id changed during rotation: %v → %v", resp.ID, after.ID)
	}
	if string(after.SecretCiphertext) == string(before.SecretCiphertext) {
		t.Error("SecretCiphertext unchanged — rotation didn't persist")
	}
	if after.SecretPrefix == before.SecretPrefix {
		t.Error("SecretPrefix unchanged — rotation didn't persist")
	}

	// The new sealed bytes round-trip back to the returned
	// plaintext — the dispatcher will sign with this key on
	// the next fanout.
	plain, err := encat.OpenSecret(after.SecretWrappedDEK, after.SecretCiphertext)
	if err != nil {
		t.Fatalf("decrypt rotated secret: %v", err)
	}
	if string(plain) != rotated.Secret {
		t.Error("decrypted stored secret does not match returned plaintext")
	}
}

func TestRotateWebhookSecret_PreservesOtherFields(t *testing.T) {
	// Rotation MUST NOT reset the row's status, failure_count,
	// event_type, or target_url. A user rotating after a
	// suspected leak still wants their subscription's
	// behaviour intact.
	encat.SetKEKForTest(testKEK(0xF9))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	userID := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, userID, "document.signed", "https://hooks.example.com/x")

	// Set a non-zero failure_count so we can prove rotation
	// doesn't reset it (unlike the /enable path).
	if err := models.DB.Model(&models.WebhookSubscription{}).
		Where("id = ?", resp.ID).
		Update("failure_count", 4).Error; err != nil {
		t.Fatalf("preset failure_count: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/rotate-secret", nil)
	req.Header.Set("X-User-ID", userID.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var stored models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&stored).Error
	if stored.EventType != "document.signed" {
		t.Errorf("event_type clobbered: %q", stored.EventType)
	}
	if stored.TargetURL != "https://hooks.example.com/x" {
		t.Errorf("target_url clobbered: %q", stored.TargetURL)
	}
	if stored.FailureCount != 4 {
		t.Errorf("failure_count = %d, want 4 (rotation must not reset)", stored.FailureCount)
	}
	if stored.Status != models.WebhookStatusActive {
		t.Errorf("status = %q, want active (rotation must not change)", stored.Status)
	}
}

func TestRotateWebhookSecret_RefusesAnotherUsersRow(t *testing.T) {
	encat.SetKEKForTest(testKEK(0xFA))
	defer encat.SetKEKForTest(nil)
	r, restore := newWebhookRouter(t)
	defer restore()
	owner := uuid.Must(uuid.NewV7())
	attacker := uuid.Must(uuid.NewV7())
	resp := mustCreate(t, r, owner, "job.completed", "https://hooks.example.com/x")

	var before models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&before).Error

	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+resp.ID.String()+"/rotate-secret", nil)
	req.Header.Set("X-User-ID", attacker.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no info leak); body=%s", w.Code, w.Body.String())
	}

	// Owner's secret is untouched — an attacker invalidating
	// another user's webhook signing key would be a real
	// security bug (denial of service against legitimate
	// integrations).
	var after models.WebhookSubscription
	_ = models.DB.Where("id = ?", resp.ID).First(&after).Error
	if string(after.SecretCiphertext) != string(before.SecretCiphertext) {
		t.Error("attacker mutated another user's secret_ciphertext")
	}
	if after.SecretPrefix != before.SecretPrefix {
		t.Error("attacker mutated another user's secret_prefix")
	}
}

func TestRotateWebhookSecret_RejectsMalformedID(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/not-a-uuid/rotate-secret", nil)
	req.Header.Set("X-User-ID", uuid.Must(uuid.NewV7()).String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRotateWebhookSecret_RejectsUnauthenticated(t *testing.T) {
	r, restore := newWebhookRouter(t)
	defer restore()
	req := httptest.NewRequest(http.MethodPost,
		"/v1/notify/webhooks/"+uuid.Must(uuid.NewV7()).String()+"/rotate-secret", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
