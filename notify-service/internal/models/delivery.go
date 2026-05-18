package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Delivery is one persisted attempt to deliver a notification.
// Every NotifyEvent the subscriber consumes produces exactly one
// Delivery row, irrespective of success — this is the audit trail
// the dev console renders and the on-call dashboard scans for
// stuck queues.
//
// Status semantics:
//   - `pending`   : row created, channel hasn't run yet (rare —
//                   exists for the moment between INSERT and the
//                   channel's Send call).
//   - `delivered` : channel returned no error.
//   - `failed`    : channel returned a permanent error; will NOT
//                   be retried. The retry policy lives in the
//                   subscriber's Nak/Ack decision, not here.
//   - `skipped`   : channel was disabled or the event was filtered
//                   out (e.g., idempotency-key duplicate).
//
// idempotency_key is uniquely indexed (when non-empty) so an
// upstream service retrying the same Publish call collapses into
// one row.
type Delivery struct {
	ID             uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	UserID         *uuid.UUID     `gorm:"type:uuid;index:idx_delivery_user" json:"userId,omitempty"`
	Channel        string         `gorm:"type:text;not null;index:idx_delivery_channel" json:"channel"`
	Target         string         `gorm:"type:text;not null" json:"target"`
	Status         string         `gorm:"type:text;not null;default:'pending';index:idx_delivery_status" json:"status"`
	Attempts       int            `gorm:"not null;default:0" json:"attempts"`
	Payload        datatypes.JSON `gorm:"type:jsonb" json:"payload,omitempty"`
	LastError      string         `gorm:"type:text" json:"lastError,omitempty"`
	IdempotencyKey *string        `gorm:"type:text;uniqueIndex:idx_delivery_idem,where:idempotency_key IS NOT NULL" json:"idempotencyKey,omitempty"`
	CreatedAt      time.Time      `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
	UpdatedAt      time.Time      `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

func (d *Delivery) BeforeCreate(tx *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.Must(uuid.NewV7())
	}
	return nil
}

// Status constants. Kept stable; rolling them through the DB
// would be an unnecessary migration when they're just enums.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)
