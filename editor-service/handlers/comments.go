package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"fyredocs/shared/response"

	"editor-service/internal/authclient"
	"editor-service/internal/models"
)

// authClient is the auth-service profile lookup, set by main.go.
// Nil = lookups disabled (running without auth-service configured);
// the methods on a nil pointer fall back to "no display name".
var authClient *authclient.Client

// SetAuthClient wires the auth-service client used by comment
// handlers for author display-name enrichment. Production sets
// this from main.go after constructing the client.
func SetAuthClient(c *authclient.Client) { authClient = c }

// commentResponse is the JSON shape ListComments and AddComment
// return. Embeds the Comment model so existing fields serialise
// unchanged, and adds the display name resolved from auth-service.
// Empty string when the lookup failed or the field wasn't
// populated — the frontend falls back to rendering the raw UUID.
type commentResponse struct {
	models.Comment
	AuthorDisplayName string `json:"authorDisplayName,omitempty"`
}

func enrichComments(ctx context.Context, comments []models.Comment) []commentResponse {
	out := make([]commentResponse, len(comments))
	if authClient == nil || len(comments) == 0 {
		for i, c := range comments {
			out[i] = commentResponse{Comment: c}
		}
		return out
	}
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, c.AuthorUserID.String())
	}
	names := authClient.LookupDisplayNames(ctx, ids)
	for i, c := range comments {
		out[i] = commentResponse{
			Comment:           c,
			AuthorDisplayName: names[c.AuthorUserID.String()],
		}
	}
	return out
}

// AddCommentRequest is the JSON body for POST /v1/documents/:id/comments.
type AddCommentRequest struct {
	// RevID identifies the revision the comment anchors to. Required so that
	// comments survive document edits with a stable anchor target.
	RevID string `json:"revId" binding:"required,uuid"`

	// Anchor is opaque, frontend-owned JSON. The schema is documented in
	// EDITOR_SERVICE.md (`{ type: 'span', nodeId, offsetStart, offsetEnd }`)
	// but not validated at this layer so it can evolve without migrations.
	Anchor datatypes.JSON `json:"anchor" binding:"required"`

	// Body is the comment text.
	Body string `json:"body" binding:"required,min=1,max=10000"`

	// ParentCommentID is the optional id of the comment this is a
	// reply to. When set, the new comment becomes a child of that
	// parent. v0 enforces single-depth threading — replies-to-replies
	// are rejected at the handler.
	ParentCommentID string `json:"parentCommentId,omitempty" binding:"omitempty,uuid"`
}

// AddComment handles POST /v1/documents/:id/comments.
func AddComment(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req AddCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error())
		return
	}

	// Ownership check.
	var doc models.Document
	err := models.DB.WithContext(c.Request.Context()).
		Where("id = ? AND owner_user_id = ?", docID, uid).
		First(&doc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.NotFound(c, "DOCUMENT_NOT_FOUND", "Document not found")
		return
	}
	if err != nil {
		response.InternalErrorf(c, "DB_CHECK_FAILED", "Could not verify ownership", err)
		return
	}

	revID, _ := parseUUIDValue(req.RevID)

	// Validate parentCommentId (if provided): must exist on the
	// same doc, AND must itself be top-level (single-depth
	// threading — Phase 2 follow-up; deeper threading is on the
	// roadmap but not in scope here).
	var parentRef *uuid.UUID
	if req.ParentCommentID != "" {
		parentID, _ := parseUUIDValue(req.ParentCommentID)
		var parent models.Comment
		err := models.DB.WithContext(c.Request.Context()).
			Where("id = ? AND document_id = ?", parentID, docID).
			First(&parent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.NotFound(c, "PARENT_NOT_FOUND", "Parent comment not found on this document")
			return
		}
		if err != nil {
			response.InternalErrorf(c, "DB_CHECK_FAILED", "Could not verify parent comment", err)
			return
		}
		if parent.ParentCommentID != nil {
			response.BadRequest(c, "NESTED_REPLY", "Replies cannot themselves be replied to")
			return
		}
		parentRef = &parentID
	}

	comment := models.Comment{
		DocumentID:      docID,
		RevID:           revID,
		Anchor:          req.Anchor,
		Body:            req.Body,
		AuthorUserID:    uid,
		ParentCommentID: parentRef,
	}
	if err := models.DB.WithContext(c.Request.Context()).Create(&comment).Error; err != nil {
		response.InternalErrorf(c, "DB_CREATE_FAILED", "Could not create comment", err)
		return
	}

	// Enrich with the author's display name so the frontend can
	// render "posted by X" on the freshly-inserted row without a
	// refetch. Single-comment enrichment goes through the same
	// codepath as the list to keep behaviour consistent.
	enriched := enrichComments(c.Request.Context(), []models.Comment{comment})

	// Live fan-out: peers viewing the same doc see this new
	// comment in their UI without a refetch. Fire-and-forget.
	publishCommentAdded(docID, enriched[0])

	response.Created(c, "comment created", enriched[0])
}

// ListComments handles GET /v1/documents/:id/comments.
func ListComments(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Ownership check.
	var owned int64
	if err := models.DB.WithContext(c.Request.Context()).
		Model(&models.Document{}).
		Where("id = ? AND owner_user_id = ?", docID, uid).
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

	var comments []models.Comment
	tx := models.DB.WithContext(c.Request.Context()).
		Where("document_id = ?", docID)

	// Optional ?resolved=true|false filter.
	if val := c.Query("resolved"); val != "" {
		switch val {
		case "true":
			tx = tx.Where("resolved = ?", true)
		case "false":
			tx = tx.Where("resolved = ?", false)
		default:
			response.BadRequest(c, "INVALID_QUERY", "resolved must be 'true' or 'false'")
			return
		}
	}

	if err := tx.Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&comments).Error; err != nil {
		response.InternalErrorf(c, "DB_LIST_FAILED", "Could not list comments", err)
		return
	}

	response.OKWithMeta(c, "ok",
		enrichComments(c.Request.Context(), comments),
		&response.Meta{Page: page, Limit: limit})
}

// ResolveComment handles POST /v1/documents/:id/comments/:commentId/resolve.
func ResolveComment(c *gin.Context) {
	uid, ok := requireUser(c)
	if !ok {
		return
	}
	docID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	commentID, ok := parseUUIDParam(c, "commentId")
	if !ok {
		return
	}

	res := models.DB.WithContext(c.Request.Context()).
		Model(&models.Comment{}).
		Where("id = ? AND document_id = ? AND document_id IN (SELECT id FROM documents WHERE owner_user_id = ?)", commentID, docID, uid).
		Update("resolved", true)
	if res.Error != nil {
		response.InternalErrorf(c, "DB_UPDATE_FAILED", "Could not resolve comment", res.Error)
		return
	}
	if res.RowsAffected == 0 {
		response.Err(c, http.StatusNotFound, "COMMENT_NOT_FOUND", "Comment not found")
		return
	}

	// Live fan-out so peers can flip the "resolved" badge in
	// their UI without a refetch.
	publishCommentResolved(docID, commentID)

	response.NoContent(c)
}

// parseUUIDValue is the no-Gin-context counterpart to parseUUIDParam used in
// JSON-body validation paths.
func parseUUIDValue(raw string) (uuid.UUID, error) {
	return uuid.Parse(raw)
}
