package handlers

import (
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"editor-service/internal/models"
)

// DownloadDocument serves the PDF bytes of a document's current
// revision. If no edits have been applied (`CurrentRevID` is nil) the
// original upload bytes are served.
//
// Response shape: `application/pdf` with `Content-Disposition:
// attachment; filename="<title>.pdf"` so browsers prompt to save
// rather than render inline (the inline-rendering case is served by
// the frontend through a separate viewer endpoint).
//
// Failure mapping:
//   - 401 if unauthenticated.
//   - 400 if `:id` is not a UUID.
//   - 404 if the document doesn't exist, isn't owned by the caller, or
//     has been soft-deleted.
//   - 500 STORAGE_FAILED if the on-disk file is missing or unreadable
//     (e.g., cleanup-worker raced ahead of an in-flight download).
func DownloadDocument(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

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

	// Resolve the storage key for the current state. When `CurrentRevID`
	// is set we serve the revision bytes; otherwise the original upload.
	// We fall back to the original upload if the revision row is
	// missing — that's a corrupt state but a graceful fallback is
	// preferable to a 500 for the user.
	key := doc.StorageKey
	if doc.CurrentRevID != nil {
		var rev models.Revision
		if err := models.DB.WithContext(c.Request.Context()).
			Where("id = ?", *doc.CurrentRevID).First(&rev).Error; err == nil && rev.PDFPatchKey != "" {
			key = rev.PDFPatchKey
		}
	}
	serveStoredPDF(c, key, downloadFilename(doc.Title))
}

// DownloadRevision serves the PDF bytes of a specific revision. The
// caller must own the parent document; the revision must belong to it.
// This is the primitive that powers "go back to version N" UX flows.
func DownloadRevision(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	revID, ok := parseUUIDParam(c, "revId")
	if !ok {
		return
	}

	// Single join-style query: confirm the document is owned by the
	// caller AND the revision belongs to it. A row count of zero is
	// indistinguishable from "doesn't exist" by design — we don't
	// want to leak whether a foreign doc owns a given revision id.
	var rev models.Revision
	err := models.DB.WithContext(c.Request.Context()).
		Joins("JOIN documents ON documents.id = revisions.document_id").
		Where("revisions.id = ? AND revisions.document_id = ?", revID, docID).
		Where("documents.owner_user_id = ? AND documents.status <> 'deleted'", uid).
		First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "REVISION_NOT_FOUND", "Revision not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_GET_FAILED", "Could not load revision", err)
		return
	}
	if rev.PDFPatchKey == "" {
		// Revision row predates the /edit-handler era (or a future op
		// that doesn't emit a PDF patch). Surface as a 404 — there's
		// nothing to download.
		response.NotFound(c, "REVISION_NO_BYTES", "Revision has no downloadable bytes")
		return
	}

	var docTitle string
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).Select("title").Where("id = ?", docID).
		Scan(&docTitle).Error; err != nil {
		docTitle = "document"
	}
	serveStoredPDF(c, rev.PDFPatchKey, downloadFilename(docTitle))
}

// serveStoredPDF resolves `storageKey` against [StorageDir] and streams
// the file to the response. Path-traversal-resistant via
// [resolveStoragePath]; the file is sent with the right Content-Type
// and a filename that the browser will use as the default save name.
func serveStoredPDF(c *gin.Context, storageKey, filename string) {
	path, err := resolveStoragePath(storageKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_KEY_INVALID",
			"Storage key is missing or unsafe", err,
			"storageKey", storageKey)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			response.Err(c, http.StatusNotFound, "STORAGE_NOT_FOUND",
				"File is missing from storage")
			return
		}
		response.InternalErrorf(c, "STORAGE_OPEN_FAILED", "Could not open file", err,
			"path", path)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		response.InternalErrorf(c, "STORAGE_STAT_FAILED", "Could not stat file", err)
		return
	}

	c.Header("Content-Type", "application/pdf")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	// http.ServeContent handles range requests + If-Modified-Since,
	// which matters for big PDFs over flaky mobile connections.
	http.ServeContent(c.Writer, c.Request, filename, stat.ModTime(), f)
}

// downloadFilename returns a safe-ish filename for the
// Content-Disposition header. We take the document title, replace
// path separators and quotes, and append .pdf if missing. The browser
// uses this as the default save name; it does not affect storage.
func downloadFilename(title string) string {
	if title == "" {
		return "document.pdf"
	}
	out := make([]byte, 0, len(title)+4)
	for i := 0; i < len(title); i++ {
		c := title[i]
		switch c {
		case '"', '/', '\\', '\r', '\n', 0:
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	name := string(out)
	if len(name) < 4 || (name[len(name)-4:] != ".pdf" && name[len(name)-4:] != ".PDF") {
		name += ".pdf"
	}
	return name
}

// Compile-time check that the uuid package is still imported (vet
// otherwise complains if we trim handler bodies during follow-ups).
var _ = uuid.Nil
