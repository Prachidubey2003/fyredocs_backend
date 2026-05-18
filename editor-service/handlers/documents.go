package handlers

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"editor-service/internal/editops"
	"editor-service/internal/models"
)

// CreateDocumentRequest is the JSON body accepted by POST /v1/documents.
type CreateDocumentRequest struct {
	Title      string `json:"title" binding:"required,min=1,max=512"`
	StorageKey string `json:"storageKey" binding:"required,min=1,max=2048"`
	SizeBytes  int64  `json:"sizeBytes" binding:"min=0"`
	PageCount  int    `json:"pageCount" binding:"min=0"`
}

// CreateDocument handles POST /v1/documents.
//
// Phase 1 scaffold: creates a Document row pointing at an already-uploaded
// file. In Phase 1.x the sPDOM parser will run server-side and seed the
// first Revision; for now revisions are created lazily on the first edit.
func CreateDocument(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}

	var req CreateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error())
		return
	}

	doc := models.Document{
		OwnerUserID: uid,
		Title:       strings.TrimSpace(req.Title),
		StorageKey:  strings.TrimSpace(req.StorageKey),
		SizeBytes:   req.SizeBytes,
		PageCount:   req.PageCount,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&doc).Error; err != nil {
		response.InternalErrorf(c, "DB_CREATE_FAILED", "Could not create document", err)
		return
	}

	// Fan out `document.created` to any external webhook
	// subscribers. Best-effort: a NATS hiccup does NOT fail
	// the user-facing create call. See publishDocumentLifecycleDomainEvent.
	publishDocumentLifecycleDomainEvent(c.Request.Context(), "document.created",
		uid, doc.ID, documentLifecyclePayload{Title: doc.Title})

	response.Created(c, "document created", doc)
}

// ListDocuments handles GET /v1/documents?page=&limit=.
// Returns documents owned by the authenticated user, newest first.
func ListDocuments(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}

	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	var docs []models.Document
	var total int64

	tx := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("owner_user_id = ? AND status <> 'deleted'", uid)

	if err := tx.Count(&total).Error; err != nil {
		response.InternalErrorf(c, "DB_COUNT_FAILED", "Could not count documents", err)
		return
	}
	if err := tx.Order("updated_at DESC").Limit(limit).Offset(offset).Find(&docs).Error; err != nil {
		response.InternalErrorf(c, "DB_LIST_FAILED", "Could not list documents", err)
		return
	}

	response.OKWithMeta(c, "ok", docs, &response.Meta{
		Page:  page,
		Limit: limit,
		Total: total,
	})
}

// GetDocument handles GET /v1/documents/:id.
func GetDocument(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}

	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var doc models.Document
	err := models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND owner_user_id = ?", id, uid).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_GET_FAILED", "Could not load document", err)
		return
	}

	response.OK(c, "ok", doc)
}

// DeleteDocument handles DELETE /v1/documents/:id.
// Soft-deletes by flipping status to 'deleted'. cleanup-worker (eventually
// editor-service's own cleaner) will purge the storage_key bytes from /files
// on its sweep.
func DeleteDocument(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}

	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	res := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("id = ? AND owner_user_id = ? AND status <> 'deleted'", id, uid).
		Update("status", "deleted")
	if res.Error != nil {
		response.InternalErrorf(c, "DB_DELETE_FAILED", "Could not delete document", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}

	response.NoContent(c)
}

// EditDocument handles POST /v1/documents/:id/edit — the sPDOM-op endpoint
// from plan §4.7.
//
// v0 contract:
//
//   - Request body: `{"ops":[{"type":"page.rotate","page":N,"rotation":D}], "message":"…"}`
//   - Exactly one op per request (see editops.ErrOnlyOneOpSupported).
//     Only `page.rotate` is implemented; other types return 400.
//   - The handler reads the document's current PDF bytes, applies the
//     op via the editops dispatcher (which calls into pdfedit →
//     pdfwriter), writes the new bytes to a per-revision file under
//     StorageDir, persists a new Revision row, and points the
//     Document's CurrentRevID at it.
//
// Failure mapping:
//
//   - Unknown / unimplemented op           → 400 INVALID_OP
//   - Invalid op arguments / bad PDF input → 400 INVALID_INPUT
//   - Document not found / not owned       → 404 DOCUMENT_NOT_FOUND
//   - Storage not configured / IO error    → 500 STORAGE_FAILED / DB_FAILED
//
// The DB write is *not* in a transaction with the file write because
// the file lives under /files/ which is the source of truth — if the
// DB insert fails after we wrote the file, cleanup-worker reclaims it.
// If the file write fails before the DB insert, we simply return the
// error and no revision row exists.
func EditDocument(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, "INVALID_BODY", "Could not read request body")
		return
	}
	req, err := editops.ParseRequest(body)
	if err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error())
		return
	}

	// Load the document and confirm ownership.
	var doc models.Document
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND owner_user_id = ? AND status <> 'deleted'", docID, uid).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_GET_FAILED", "Could not load document", err)
		return
	}

	// Read the current PDF bytes. v0: always from doc.StorageKey;
	// when prior revisions exist, we follow CurrentRevID instead.
	sourceKey := doc.StorageKey
	if doc.CurrentRevID != nil {
		var prev models.Revision
		if err := models.DB.WithContext(c.Request.Context()).
			Where("id = ?", *doc.CurrentRevID).First(&prev).Error; err == nil && prev.PDFPatchKey != "" {
			sourceKey = prev.PDFPatchKey
		}
	}
	sourcePath, err := resolveStoragePath(sourceKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve source path", err)
		return
	}
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not read source bytes", err)
		return
	}

	// Apply the ops. editops surfaces invalid-input failures as
	// ErrInvalidArgs / ErrUnknownOp / ErrNoOps / ErrOnlyOneOpSupported;
	// anything else is a server-side fault.
	newBytes, err := editops.Apply(req.Ops, original)
	if err != nil {
		switch {
		case errors.Is(err, editops.ErrNoOps),
			errors.Is(err, editops.ErrInvalidArgs):
			response.BadRequest(c, "INVALID_INPUT", err.Error())
		case errors.Is(err, editops.ErrUnknownOp):
			response.BadRequest(c, "INVALID_OP", err.Error())
		default:
			response.InternalErrorf(c, "APPLY_FAILED", "Could not apply ops", err)
		}
		return
	}

	// Write the new revision bytes before any DB mutation.
	revID := uuid.Must(uuid.NewV7())
	revKey := revisionStorageKey(uid, doc.ID, revID)
	revPath, err := resolveStoragePath(revKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve revision path", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(revPath), 0o755); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not create revision dir", err)
		return
	}
	if err := os.WriteFile(revPath, newBytes, 0o644); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not write revision bytes", err)
		return
	}

	// Persist the Revision and bump the Document.
	rev := models.Revision{
		ID:           revID,
		DocumentID:   doc.ID,
		ParentRevID:  doc.CurrentRevID,
		AuthorUserID: uid,
		Message:      strings.TrimSpace(req.Message),
		PDFPatchKey:  revKey,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&rev).Error; err != nil {
		// Best-effort cleanup of the orphan file; cleanup-worker would
		// catch it eventually but immediate removal keeps things tidy.
		_ = os.Remove(revPath)
		response.InternalErrorf(c, "DB_FAILED", "Could not persist revision", err)
		return
	}
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("id = ?", doc.ID).
		Updates(map[string]any{
			"current_rev_id": rev.ID,
			"size_bytes":     int64(len(newBytes)),
		}).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not update document pointer", err)
		return
	}

	// Meter the op for billing. Best-effort: a NATS hiccup does
	// not fail the user-facing edit call. See publishEditBillable
	// for the policy. Per-op-type granularity: a multi-op request
	// emits one event per distinct op type with Quantity = count.
	publishEditBillable(c.Request.Context(), uid, req.Ops)

	// Append one tamper-evident audit row for the edit. Records
	// the doc ID + op-type histogram in metadata so the auditor
	// sees WHAT changed without joining the revisions table.
	publishDocumentEditAudit(c.Request.Context(), uid, doc.ID, req.Ops)

	// Fan out `document.updated` to external webhook subscribers
	// (Zapier, customer integrations). Best-effort: failure is
	// logged, never raised to the user.
	publishDocumentLifecycleDomainEvent(c.Request.Context(), "document.updated",
		uid, doc.ID, documentLifecyclePayload{
			RevID:   rev.ID.String(),
			OpCount: len(req.Ops),
		})

	response.Created(c, "revision created", rev)
}

// RestoreRevision handles POST /v1/documents/:id/revisions/:revId/restore.
//
// Creates a new Revision whose bytes equal `revId`'s bytes (a copy on
// disk), inserts a Revision row pointing at the previous current rev,
// and updates Document.CurrentRevID to the new revision. The history
// log therefore shows: ...→ (some rev) → (restore from revX) → ...
// which is the right audit shape — every state change is its own
// revision row, no destructive overwrite.
//
// Failure mapping:
//   - 401 unauthed
//   - 400 if `:id` or `:revId` is not a UUID
//   - 404 if the document or revision isn't found / not owned / soft-deleted
//   - 500 STORAGE_FAILED / DB_FAILED for IO problems
func RestoreRevision(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	targetRevID, ok := parseUUIDParam(c, "revId")
	if !ok {
		return
	}

	// Resolve doc + verify ownership.
	var doc models.Document
	err := models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND owner_user_id = ? AND status <> 'deleted'", docID, uid).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_GET_FAILED", "Could not load document", err)
		return
	}

	// Resolve target revision — must belong to this document. The
	// ownership check is implicit via the document we just loaded;
	// requiring revisions.document_id = docID prevents leaking a
	// foreign revision's bytes.
	var target models.Revision
	err = models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND document_id = ?", targetRevID, docID).
		First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "REVISION_NOT_FOUND", "Revision not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_GET_FAILED", "Could not load revision", err)
		return
	}
	if target.PDFPatchKey == "" {
		response.NotFound(c, "REVISION_NO_BYTES", "Target revision has no bytes to restore")
		return
	}

	// Read the target's bytes.
	sourcePath, err := resolveStoragePath(target.PDFPatchKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve source path", err)
		return
	}
	bytes, err := os.ReadFile(sourcePath)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not read source bytes", err)
		return
	}

	// Write a copy at a fresh revision path. We DON'T re-use the
	// target's path: restoring is a new history entry, not a fork
	// pointer. Same bytes on disk twice is acceptable — disks are
	// cheap, audit clarity is not.
	newRevID := uuid.Must(uuid.NewV7())
	newRevKey := revisionStorageKey(uid, doc.ID, newRevID)
	newRevPath, err := resolveStoragePath(newRevKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not resolve restore path", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(newRevPath), 0o755); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not create restore dir", err)
		return
	}
	if err := os.WriteFile(newRevPath, bytes, 0o644); err != nil {
		response.InternalErrorf(c, "STORAGE_FAILED", "Could not write restore bytes", err)
		return
	}

	// Persist the new Revision + point Document at it.
	rev := models.Revision{
		ID:           newRevID,
		DocumentID:   doc.ID,
		ParentRevID:  doc.CurrentRevID,
		AuthorUserID: uid,
		Message:      "Restored from revision " + targetRevID.String(),
		PDFPatchKey:  newRevKey,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&rev).Error; err != nil {
		_ = os.Remove(newRevPath)
		response.InternalErrorf(c, "DB_FAILED", "Could not persist restore revision", err)
		return
	}
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("id = ?", doc.ID).
		Updates(map[string]any{
			"current_rev_id": rev.ID,
			"size_bytes":     int64(len(bytes)),
		}).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not update document pointer", err)
		return
	}

	// Restoring a revision IS a state change — fan out
	// `document.updated` so subscribers re-sync. RevID is the
	// new revision, not the source one; subscribers care about
	// "what's current".
	publishDocumentLifecycleDomainEvent(c.Request.Context(), "document.updated",
		uid, doc.ID, documentLifecyclePayload{RevID: rev.ID.String()})

	response.Created(c, "revision restored", rev)
}

// ListRevisions handles GET /v1/documents/:id/revisions.
func ListRevisions(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}

	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Ownership check: only return revisions for documents the caller owns.
	var owned int64
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("id = ? AND owner_user_id = ?", id, uid).
		Count(&owned).Error; err != nil {
		response.InternalErrorf(c, "DB_CHECK_FAILED", "Could not verify ownership", err)
		return
	}
	if owned == 0 {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}

	page, limit := parsePagination(c)
	offset := (page - 1) * limit

	var revs []models.Revision
	if err := models.DB.WithContext(c.Request.Context()).
		Where("document_id = ?", id).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&revs).Error; err != nil {
		response.InternalErrorf(c, "DB_LIST_FAILED", "Could not list revisions", err)
		return
	}

	response.OKWithMeta(c, "ok", revs, &response.Meta{Page: page, Limit: limit})
}

// parsePagination returns the page (>=1) and limit (>=1, <=100) from the
// query string with safe defaults.
func parsePagination(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "25"))
	if limit < 1 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

// parseUUIDParam reads a UUID path param and writes a 400 envelope on
// failure. Returns the UUID and a "ok" boolean to make handler control flow
// linear: `id, ok := parseUUIDParam(c, "id"); if !ok { return }`.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	raw := c.Param(name)
	id, err := uuid.Parse(raw)
	if err != nil {
		response.BadRequest(c, "INVALID_PARAM", "Path parameter '"+name+"' must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}
