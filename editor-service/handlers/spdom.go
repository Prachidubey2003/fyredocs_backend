package handlers

import (
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"editor-service/internal/models"
	"editor-service/internal/spdom"
)

// GetSPDOM handles GET /v1/documents/:id/spdom.
//
// It loads the Document row, opens the file referenced by storage_key on
// the local FS (see STORAGE.md §4.4.3), and runs the sPDOM parser. Today
// the parser returns Pages with empty Blocks — L3 layout reconstruction
// is a tracked Phase 1 follow-up. The data model and stable IDs are
// final, so frontend integrations can build against the contract now.
func GetSPDOM(c *gin.Context) {
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

	path, err := resolveStoragePath(doc.StorageKey)
	if err != nil {
		response.InternalErrorf(c, "STORAGE_KEY_INVALID",
			"Document storage key is missing or unsafe", err,
			"docId", doc.ID, "storageKey", doc.StorageKey)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			response.Err(c, http.StatusNotFound, "STORAGE_NOT_FOUND",
				"Document file is missing from storage")
			return
		}
		response.InternalErrorf(c, "STORAGE_OPEN_FAILED", "Could not open document file", err,
			"docId", doc.ID, "path", path)
		return
	}
	defer f.Close()

	tree, err := spdom.Parse(doc.ID.String(), f)
	if err != nil {
		response.InternalErrorf(c, "SPDOM_PARSE_FAILED",
			"Could not parse document into sPDOM", err,
			"docId", doc.ID)
		return
	}

	response.OK(c, "ok", tree)
}
