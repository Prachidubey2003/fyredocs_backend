package handlers

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"editor-service/internal/encat"
	"editor-service/internal/models"
)

// snapshotMaxBytes caps an inbound PUT to keep a malicious or
// runaway collab-service from filling the disk with one request.
// 16 MiB is well above the per-room replay buffer cap (8 MiB
// today) — gives headroom for the wire-format framing overhead
// and any near-term ceiling growth.
const snapshotMaxBytes = 16 * 1024 * 1024

// snapshotMessage tags the Revision row so a future inspector can
// distinguish editor-driven revisions (which carry an empty or
// user-supplied message) from collab-service checkpoints.
const snapshotMessage = "yjs-checkpoint"

// snapshotStorageKey is the relative path under StorageDir where
// a checkpoint blob lives. Mirrors revisionStorageKey but lives
// under snapshots/ so a future cleanup-worker can target Yjs
// blobs without scanning the edits/ tree.
func snapshotStorageKey(ownerUserID, docID, revID uuid.UUID) string {
	return filepath.Join(
		"users", ownerUserID.String(),
		"docs", docID.String(),
		"snapshots", revID.String()+".yjs",
	)
}

// GetSnapshot handles GET /internal/v1/snapshots/:docID.
//
// Internal-only endpoint: the gateway does not proxy /internal/*,
// so external traffic cannot reach this handler. Callers are
// assumed to be other in-cluster services (today: collab-service).
//
// Returns the most recent Revision with yjs_update_key set,
// streaming the file at that path. 404 if no snapshot exists
// (either no rows or the file is missing).
func GetSnapshot(c *gin.Context) {
	docID, ok := parseUUIDParam(c, "docID")
	if !ok {
		return
	}

	var rev models.Revision
	err := models.DB.WithContext(c.Request.Context()).
		Where("document_id = ? AND yjs_update_key <> ''", docID).
		Order("created_at DESC").
		First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not look up snapshot", err)
		return
	}

	path, err := resolveStoragePath(rev.YjsUpdateKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve snapshot path", err)
		return
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		// File missing — treat as "no snapshot". A row without
		// bytes is a bug somewhere, but the safe response is the
		// same one a never-saved doc gets, and the caller's
		// fallback (start empty) is correct in both cases.
		c.Status(http.StatusNotFound)
		return
	}
	// Bypass the envelope when the row was never sealed (legacy
	// or KEK-off deploys) — encat.OpenSnapshot returns sealed
	// bytes verbatim when WrappedDEK is empty.
	plain, err := encat.OpenSnapshot(rev.WrappedDEK, sealed)
	if err != nil {
		response.InternalErrorf(c, "DECRYPT_FAILED", "Could not decrypt snapshot", err)
		return
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(plain)
}

// PutSnapshot handles PUT /internal/v1/snapshots/:docID.
//
// Writes the request body to a new file under
// `users/{owner}/docs/{docID}/snapshots/{revID}.yjs` and persists
// a Revision row with `yjs_update_key` set to that path. Each
// PUT creates a new revision — older snapshots remain on disk
// until cleanup-worker reaps them, so we never lose history to
// a failed write of the next snapshot.
//
// Auth: none. This is an internal-only endpoint; trust boundary
// is the cluster network. See routes.go for the registration
// (NOT inside the v1 auth group).
func PutSnapshot(c *gin.Context) {
	docID, ok := parseUUIDParam(c, "docID")
	if !ok {
		return
	}

	// Look up the document to get the owner — the storage path
	// is owner-scoped to match the rest of the platform's layout.
	// Also confirms the document exists; a snapshot for a
	// non-existent doc is meaningless.
	var doc models.Document
	err := models.DB.WithContext(c.Request.Context()).
		Where("id = ?", docID).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not look up document", err)
		return
	}

	// Read the body with a hard cap.
	limited := io.LimitReader(c.Request.Body, int64(snapshotMaxBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		response.InternalErrorf(c, "READ_FAILED", "Could not read snapshot body", err)
		return
	}
	if len(body) > snapshotMaxBytes {
		response.BadRequest(c, "SNAPSHOT_TOO_LARGE", "Snapshot exceeds maximum size")
		return
	}

	revID := uuid.Must(uuid.NewV7())
	revKey := snapshotStorageKey(doc.OwnerUserID, doc.ID, revID)
	revPath, err := resolveStoragePath(revKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve snapshot path", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(revPath), 0o755); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not create snapshot dir", err)
		return
	}
	// Seal-on-write when a KEK is configured; otherwise the
	// helper passes plain through and `wrappedDEK` stays nil
	// so the Revision row records the legacy plaintext shape.
	wrappedDEK, sealed, err := encat.SealSnapshot(body)
	if err != nil {
		response.InternalErrorf(c, "ENCRYPT_FAILED", "Could not seal snapshot bytes", err)
		return
	}
	if err := os.WriteFile(revPath, sealed, 0o644); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not write snapshot bytes", err)
		return
	}

	rev := models.Revision{
		ID:           revID,
		DocumentID:   doc.ID,
		ParentRevID:  doc.CurrentRevID,
		AuthorUserID: doc.OwnerUserID,
		Message:      snapshotMessage,
		YjsUpdateKey: revKey,
		WrappedDEK:   wrappedDEK,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&rev).Error; err != nil {
		// Orphan file — cleanup-worker would catch it eventually,
		// but immediate removal keeps the snapshots/ tree tidy.
		_ = os.Remove(revPath)
		response.InternalErrorf(c, "DB_FAILED", "Could not persist snapshot revision", err)
		return
	}

	c.Header("Content-Type", "application/json")
	c.JSON(http.StatusCreated, gin.H{
		"revId":  rev.ID.String(),
		"docId":  doc.ID.String(),
		"bytes":  len(body),
		"key":    revKey,
		"kind":   strings.TrimSpace(snapshotMessage),
	})
}
