package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// AuditEvent is one tamper-evident audit record. The chain is
// formed by `prev_hash` pointing at the SHA-256 digest of the
// previous row (or `0x00…` for the genesis row). On insert, the
// dispatcher computes:
//
//	hash = sha256(
//	    seq || ":" || actor || ":" || action || ":" || resource
//	    || ":" || metadata || ":" || prev_hash_hex
//	)
//
// A reader walks the chain in `seq` order and recomputes each
// `hash` against the row's fields + the prior row's `hash` — any
// mutation (UPDATE, DELETE, or out-of-order insert) breaks the
// chain at the first tampered row and every row after.
//
// Storage notes:
//   - `seq` is BIGSERIAL — gives us a strict insertion-order key
//     independent of `created_at` clock skew.
//   - The application code revokes UPDATE/DELETE from the role
//     the service connects with (so even a DBA-level slip can't
//     silently drop a row without leaving forensics).
//   - The hash is stored as raw bytes (BYTEA / blob); the
//     `/internal/v1/audit/verify` endpoint serialises to hex for
//     auditors.
type AuditEvent struct {
	ID         uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Seq        int64          `gorm:"primaryKey;autoIncrement;index:idx_audit_seq" json:"seq"`
	Actor      string         `gorm:"type:text;not null;index:idx_audit_actor" json:"actor"`
	Action     string         `gorm:"type:text;not null;index:idx_audit_action" json:"action"`
	Resource   string         `gorm:"type:text" json:"resource,omitempty"`
	Metadata   datatypes.JSON `gorm:"type:jsonb" json:"metadata,omitempty"`
	PrevHash   []byte         `gorm:"type:bytea;not null" json:"prevHash"`
	Hash       []byte         `gorm:"type:bytea;not null;uniqueIndex:idx_audit_hash" json:"hash"`
	OccurredAt time.Time      `gorm:"not null" json:"occurredAt"`
	CreatedAt  time.Time      `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
}

func (a *AuditEvent) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.Must(uuid.NewV7())
	}
	if a.OccurredAt.IsZero() {
		a.OccurredAt = time.Now().UTC()
	}
	return nil
}
