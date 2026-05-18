package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fyredocs/shared/queue"
	"fyredocs/shared/response"

	"notify-service/internal/dispatcher"
	"notify-service/internal/models"
)

// Deps holds the dispatcher injected from main.go. Package-level
// var avoids threading it through every handler signature.
type Deps struct {
	Disp *dispatcher.Dispatcher
}

var deps Deps

// SetDeps wires the dispatcher. Must be called before SetupRouter.
func SetDeps(d Deps) { deps = d }

// SendRequest is the body of POST /internal/v1/notify/send. The
// shape mirrors queue.NotifyEvent so callers that already have
// the wire-format struct can JSON-encode it directly.
type SendRequest struct {
	Channel        string          `json:"channel"`
	Target         string          `json:"target"`
	UserID         string          `json:"userId,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
}

// Send is the synchronous HTTP entrypoint. Internal-only — for
// service-to-service calls that need an immediate
// success/failure (e.g., the auth-service password-reset flow,
// which wants to refuse the API call if the email can't be
// dispatched).
//
//	POST /internal/v1/notify/send
func Send(c *gin.Context) {
	if deps.Disp == nil {
		response.InternalError(c, "NOT_READY", "Dispatcher is not configured")
		return
	}
	var req SendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "INVALID_INPUT", err.Error())
		return
	}
	if strings.TrimSpace(req.Channel) == "" || strings.TrimSpace(req.Target) == "" {
		response.BadRequest(c, "INVALID_INPUT", "channel and target are required")
		return
	}

	delivery, err := deps.Disp.Dispatch(c.Request.Context(), queue.NotifyEvent{
		Channel:        req.Channel,
		Target:         req.Target,
		UserID:         req.UserID,
		Payload:        req.Payload,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		response.InternalErrorf(c, "DISPATCH_FAILED", "Could not dispatch notification", err)
		return
	}
	response.OK(c, "notification dispatched", delivery)
}

// ListMyDeliveries returns the calling user's recent deliveries,
// newest first. Auth via `X-User-ID` header (set by api-gateway).
//
//	GET /v1/notify/deliveries?channel=&limit=
func ListMyDeliveries(c *gin.Context) {
	rawUser := strings.TrimSpace(c.GetHeader("X-User-ID"))
	if rawUser == "" {
		response.Err(c, http.StatusUnauthorized, "UNAUTHORIZED", "Please log in.")
		return
	}
	userID, err := uuid.Parse(rawUser)
	if err != nil {
		response.BadRequest(c, "INVALID_USER", "Caller identity is malformed.")
		return
	}

	q := models.DB.WithContext(c.Request.Context()).
		Where("user_id = ?", userID)
	if ch := strings.TrimSpace(c.Query("channel")); ch != "" {
		q = q.Where("channel = ?", ch)
	}
	limit := parseLimit(c.Query("limit"), 50, 200)

	var rows []models.Delivery
	if err := q.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not list deliveries", err)
		return
	}
	response.OK(c, "deliveries retrieved", gin.H{"items": rows})
}

// parseLimit parses the ?limit= query param. Out-of-range or
// non-numeric values fall back to `defaultLimit`; values above
// `maxLimit` clamp.
func parseLimit(raw string, defaultLimit, maxLimit int) int {
	if raw == "" {
		return defaultLimit
	}
	var n int
	for _, c := range raw {
		if c < '0' || c > '9' {
			return defaultLimit
		}
		n = n*10 + int(c-'0')
		if n > maxLimit {
			return maxLimit
		}
	}
	if n == 0 {
		return defaultLimit
	}
	return n
}
