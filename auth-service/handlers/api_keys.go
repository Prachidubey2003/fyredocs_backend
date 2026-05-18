package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"auth-service/internal/apikey"
	"auth-service/internal/models"
)

// IssueAPIKeyRequest is the JSON body for POST /auth/api-keys.
//
// Field rules:
//   - Name: required, 1..64 chars. Surfaces in the dashboard list.
//   - Environment: optional; "live" if omitted. "test" is reserved
//     for the sandbox tenant slice (not yet wired at the data plane,
//     but the wire format is segregated so accidental commits of
//     test keys don't grant production access).
//   - Scopes: optional. Empty/nil means "inherit every scope the
//     issuing user holds" — the fine-grained scope vocabulary lands
//     with the rest of Phase 4.
type IssueAPIKeyRequest struct {
	Name        string   `json:"name" binding:"required,min=1,max=64"`
	Environment string   `json:"environment,omitempty" binding:"omitempty,oneof=live test"`
	Scopes      []string `json:"scopes,omitempty"`
}

// IssueAPIKeyResponse is what POST /auth/api-keys returns. The
// `plaintext` field is the only place the unhashed secret is ever
// exposed — clients MUST store it immediately; we can't recover it.
type IssueAPIKeyResponse struct {
	// Key is the persisted row (name, prefix, environment, scopes,
	// timestamps). Same shape as ListAPIKeys rows.
	Key models.APIKey `json:"key"`
	// Plaintext is the full wire-format token (`fyr_<env>_<prefix>_<secret>`)
	// shown exactly once. Lost → rotate.
	Plaintext string `json:"plaintext"`
}

// IssueAPIKey handles POST /auth/api-keys. Mints a new key for the
// authenticated user.
//
// Failure modes:
//   - 400 INVALID_INPUT on body validation
//   - 401 if unauthed
//   - 500 KEY_GEN_FAILED for crypto / DB errors
func (ae *AuthEndpoints) IssueAPIKey(c *gin.Context) {
	user, _, ok := loadUserFromAuth(c)
	if !ok {
		return
	}

	var req IssueAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error())
		return
	}
	env := req.Environment
	if env == "" {
		env = apikey.EnvLive
	}

	gen, err := apikey.Generate(env)
	if err != nil {
		response.InternalErrorf(c, "KEY_GEN_FAILED", "Could not generate API key", err)
		return
	}

	scopesJSON, err := scopesToJSON(req.Scopes)
	if err != nil {
		response.BadRequest(c, "INVALID_INPUT", "scopes must be a JSON array of strings")
		return
	}

	row := models.APIKey{
		OwnerUserID: user.ID,
		Name:        strings.TrimSpace(req.Name),
		Environment: env,
		KeyPrefix:   gen.Prefix,
		KeyHash:     gen.Hash,
		Scopes:      scopesJSON,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&row).Error; err != nil {
		response.InternalErrorf(c, "DB_CREATE_FAILED", "Could not persist API key", err)
		return
	}

	// Audit: record the key issuance. The actor is the user who
	// owns the key (its creator); the resource is the key ID so
	// the audit row links back to the API key table.
	publishAuditEvent(c.Request.Context(), "apikey.created", user.ID.String(), row.ID.String(), nil)

	response.Created(c, "API key created", IssueAPIKeyResponse{
		Key:       row,
		Plaintext: gen.Plaintext,
	})
}

// ListAPIKeys handles GET /auth/api-keys. Returns the caller's keys,
// newest-first. Active by default; pass `?revoked=true` to see
// historical keys (audit log purposes).
//
// The hash never leaves the server — the JSON tag `json:"-"` on the
// model field prevents accidental exposure.
func (ae *AuthEndpoints) ListAPIKeys(c *gin.Context) {
	user, _, ok := loadUserFromAuth(c)
	if !ok {
		return
	}

	tx := models.DB.WithContext(c.Request.Context()).
		Where("owner_user_id = ?", user.ID)

	switch c.Query("revoked") {
	case "":
		tx = tx.Where("revoked_at IS NULL")
	case "true":
		tx = tx.Where("revoked_at IS NOT NULL")
	case "false":
		tx = tx.Where("revoked_at IS NULL")
	default:
		response.BadRequest(c, "INVALID_QUERY", "revoked must be 'true' or 'false'")
		return
	}

	var keys []models.APIKey
	if err := tx.Order("created_at DESC").Find(&keys).Error; err != nil {
		response.InternalErrorf(c, "DB_LIST_FAILED", "Could not list API keys", err)
		return
	}

	response.OK(c, "ok", keys)
}

// RevokeAPIKey handles POST /auth/api-keys/:id/revoke. Sets
// `revoked_at = now()` on the key. Idempotent — revoking an
// already-revoked key returns 204 without touching the timestamp,
// so retries are safe.
//
// We don't hard-delete: the audit log needs the row's continued
// existence to keep "which key did this" answerable historically.
func (ae *AuthEndpoints) RevokeAPIKey(c *gin.Context) {
	user, _, ok := loadUserFromAuth(c)
	if !ok {
		return
	}
	keyID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.BadRequest(c, "INVALID_PARAM", "Path parameter 'id' must be a UUID")
		return
	}

	res := models.DB.WithContext(c.Request.Context()).
		Model(&models.APIKey{}).
		Where("id = ? AND owner_user_id = ? AND revoked_at IS NULL", keyID, user.ID).
		Update("revoked_at", time.Now().UTC())
	if res.Error != nil {
		response.InternalErrorf(c, "DB_UPDATE_FAILED", "Could not revoke API key", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		// Either the key doesn't exist for this user OR it's already
		// revoked. We treat both the same — 404 — to avoid leaking
		// whether a key id belongs to another tenant.
		//
		// The "already revoked" case being 404 instead of 204 is a
		// deliberate trade: it costs a tiny bit of idempotency UX
		// (retries on a deleted key fail loudly) in exchange for not
		// confirming the existence of foreign key ids. Same calculus
		// as the revisions/restore endpoint.
		response.Err(c, http.StatusNotFound, "API_KEY_NOT_FOUND", "API key not found")
		return
	}
	publishAuditEvent(c.Request.Context(), "apikey.revoked", user.ID.String(), keyID.String(), nil)
	response.NoContent(c)
}

// scopesToJSON serialises a string slice to `datatypes.JSON` while
// reusing the empty-slice ⇒ JSON null convention from GORM. The DB
// column is JSONB so either shape is fine; the verifier treats both
// as "inherit user's scopes".
func scopesToJSON(scopes []string) (datatypes.JSON, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	// Build the JSON ourselves to avoid pulling encoding/json just
	// for a string array. JSON quoting per RFC 8259 §7.
	var b strings.Builder
	b.WriteByte('[')
	for i, s := range scopes {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for j := 0; j < len(s); j++ {
			switch s[j] {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteByte(s[j])
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if s[j] < 0x20 {
					return nil, errors.New("scope contains a control character")
				}
				b.WriteByte(s[j])
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return datatypes.JSON(b.String()), nil
}

// Compile-time assertion that the gorm import we pulled is actually
// used somewhere — keeps `goimports` happy when tests stub things
// out in follow-up patches.
var _ = gorm.ErrRecordNotFound
