package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Document is the editor-service top-level entity: a PDF (or other supported
// format) opened for editing. Schema follows plan §4.6 with `tenant_id`
// elided until multi-tenancy lands (auth-service has no tenant model today —
// owner_user_id is the access-control anchor).
type Document struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	OwnerUserID  uuid.UUID  `gorm:"type:uuid;not null;index" json:"ownerUserId"`
	Title        string     `gorm:"type:text;not null" json:"title"`
	CurrentRevID *uuid.UUID `gorm:"type:uuid" json:"currentRevId,omitempty"`
	SizeBytes    int64      `gorm:"not null;default:0" json:"sizeBytes"`
	PageCount    int        `gorm:"not null;default:0" json:"pageCount"`
	StorageKey   string     `gorm:"type:text;not null" json:"-"`
	Status       string     `gorm:"type:text;not null;default:'ready'" json:"status"`
	CreatedAt    time.Time  `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
	UpdatedAt    time.Time  `gorm:"default:CURRENT_TIMESTAMP" json:"updatedAt"`
}

// BeforeCreate assigns a v7 UUID (time-ordered) when the caller hasn't.
// Matches job-service convention.
func (d *Document) BeforeCreate(_ *gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.Must(uuid.NewV7())
	}
	if d.Status == "" {
		d.Status = "ready"
	}
	return nil
}

// Revision is one committed edit to a Document. The Yjs CRDT update bytes
// live on disk (pdf_patch_key); only metadata + the optional commit-style
// message live in Postgres. See plan §4.6.
type Revision struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	DocumentID   uuid.UUID  `gorm:"type:uuid;not null;index;constraint:OnDelete:CASCADE" json:"documentId"`
	ParentRevID  *uuid.UUID `gorm:"type:uuid" json:"parentRevId,omitempty"`
	AuthorUserID uuid.UUID  `gorm:"type:uuid;not null" json:"authorUserId"`
	Message      string     `gorm:"type:text" json:"message,omitempty"`
	YjsUpdateKey string     `gorm:"type:text" json:"-"` // path under /files/ to the Yjs binary update
	PDFPatchKey  string     `gorm:"type:text" json:"-"` // path to incremental PDF patch bytes
	// WrappedDEK is the AES-256-GCM-wrapped Data Encryption Key for the
	// snapshot bytes at YjsUpdateKey, exactly `keystore.WrappedDEKSize`
	// (60) bytes long. Nil/empty means the file is stored as plaintext
	// — the pre-keystore default. When set the file at YjsUpdateKey is
	// the keystore.SealWithDEK envelope (nonce || ciphertext || tag),
	// and snapshots are read via keystore.OpenWithDEK after unwrapping
	// this column with the master KEK.
	WrappedDEK []byte    `gorm:"type:bytea" json:"-"`
	CreatedAt  time.Time `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
}

func (r *Revision) BeforeCreate(_ *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.Must(uuid.NewV7())
	}
	return nil
}

// Comment is a span-anchored or page-anchored comment on a revision.
// `Anchor` is opaque JSON owned by the frontend — the schema is documented in
// docs/developer/services/EDITOR_SERVICE.md and validated at the handler
// layer, NOT at the DB layer (so the comment shape can evolve without a
// migration).
type Comment struct {
	ID           uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	DocumentID   uuid.UUID      `gorm:"type:uuid;not null;index;constraint:OnDelete:CASCADE" json:"documentId"`
	RevID        uuid.UUID      `gorm:"type:uuid;not null" json:"revId"`
	Anchor       datatypes.JSON `gorm:"type:jsonb;not null" json:"anchor"`
	Body         string         `gorm:"type:text;not null" json:"body"`
	AuthorUserID uuid.UUID      `gorm:"type:uuid;not null" json:"authorUserId"`
	// ParentCommentID points to the comment this is a reply to. Nil
	// for top-level comments. The handler enforces single-depth
	// threading (the parent must itself have ParentCommentID == nil) —
	// the schema doesn't enforce that because it would complicate
	// migrations if we ever loosen the rule.
	ParentCommentID *uuid.UUID `gorm:"type:uuid;index" json:"parentCommentId,omitempty"`
	Resolved        bool       `gorm:"not null;default:false" json:"resolved"`
	CreatedAt       time.Time  `gorm:"default:CURRENT_TIMESTAMP" json:"createdAt"`
}

func (c *Comment) BeforeCreate(_ *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.Must(uuid.NewV7())
	}
	return nil
}
