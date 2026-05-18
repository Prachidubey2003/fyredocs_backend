package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// APIKey is a long-lived credential a user issues for programmatic
// access — used by the CLI, generated SDKs, embedded editor widgets,
// and partner integrations (Zapier / Make / n8n).
//
// Key format on the wire:
//
//	fyr_<env>_<14-char-prefix>_<32-char-secret>
//
// where `env` is `live` or `test`. The prefix is a public,
// non-secret identifier that lets us locate the row in O(1) and
// surface meaningful audit logs ("rotated key fyr_live_kJ8…") without
// ever logging the secret. The secret itself is hashed (Argon2id) at
// rest — the plaintext is shown to the user exactly once at issuance
// time. Lose the plaintext, lose the key: rotate it.
//
// Why a separate table instead of reusing UserSession:
//   - Sessions are short-lived (15-min access + 30-day refresh) and
//     created on every login; API keys are persistent credentials
//     created intentionally and revoked intentionally. Mixing them
//     would mean every UI session lookup competes with every API
//     request for cache lines.
//   - Sessions belong to a device fingerprint; API keys belong to a
//     workflow (a CLI on a CI runner, a webhook receiver). The
//     scope vocabularies differ.
//
// Scopes:
//
// Stored as a JSONB array of strings (e.g. `["documents:read",
// "documents:write"]`). Empty / null means "all scopes the issuing
// user holds". This is intentionally permissive at v0 because the
// fine-grained scope vocabulary lands with the rest of the
// developer platform (Phase 4 §4.3.1 of the blueprint).
//
// Lifecycle:
//
//   - `CreatedAt` is server-side now() — never write from the client.
//   - `LastUsedAt` is nullable and updated by a debounced goroutine
//     in the verifier (TODO: not yet wired; the column exists so the
//     write path is forward-compatible).
//   - `RevokedAt` is a soft-delete marker. We never hard-delete; the
//     audit log needs the row's continued existence to keep "which
//     key issued this action" answerable historically.
type APIKey struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	OwnerUserID uuid.UUID `gorm:"type:uuid;not null;index" json:"ownerUserId"`
	Owner       *User     `gorm:"foreignKey:OwnerUserID;references:ID" json:"-"`

	// Name is the user-facing label ("CI deploy key", "Zapier hook").
	// Required so a long list of keys stays navigable.
	Name string `gorm:"type:text;not null" json:"name"`

	// Environment splits live vs test keys so an accidental commit
	// of a test key (which CI scanners regex-detect by prefix)
	// doesn't grant production access.
	Environment string `gorm:"type:text;not null;default:'live'" json:"environment"`

	// KeyPrefix is the 14-char public identifier embedded in the
	// wire format. Indexed for O(1) lookup on every API request.
	KeyPrefix string `gorm:"type:text;unique;not null;index" json:"keyPrefix"`

	// KeyHash is the Argon2id hash of the 32-char secret portion.
	// Never returned to the client (json:"-").
	KeyHash string `gorm:"type:text;not null" json:"-"`

	// Scopes is a JSONB array of scope strings. Empty/null = inherit
	// every scope the issuing user holds.
	Scopes datatypes.JSON `gorm:"type:jsonb" json:"scopes,omitempty"`

	LastUsedAt *time.Time `gorm:"" json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `gorm:"index" json:"revokedAt,omitempty"`
	CreatedAt  time.Time  `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
}

func (k *APIKey) BeforeCreate(_ *gorm.DB) error {
	if k.ID == uuid.Nil {
		k.ID = uuid.Must(uuid.NewV7())
	}
	if k.Environment == "" {
		k.Environment = "live"
	}
	return nil
}

// Active reports whether the key can still authenticate requests —
// i.e., it hasn't been revoked. Useful in dashboard lists ("Active
// keys" tab) where we want to filter without an additional query.
func (k *APIKey) Active() bool { return k.RevokedAt == nil }
