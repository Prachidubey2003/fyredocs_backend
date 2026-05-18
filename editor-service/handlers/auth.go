package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fyredocs/shared/response"

	"editor-service/internal/authverify"
)

// authUserID extracts the owning user from the request.
//
// Defense in depth: editor-service runs its own JWT verifier via the
// authverify middleware (wired in main.go). On the request path the
// middleware populates the auth context with a parsed-and-validated user
// id. We read that here.
//
// Fallback: if the auth context is missing (e.g. routes_test.go that
// bypasses the middleware), we honour the `X-User-ID` header as a test
// affordance. Production requests always go through the middleware.
//
// Returns nil for unauthenticated callers; the caller decides whether to
// allow guest access on a per-route basis.
func authUserID(c *gin.Context) *uuid.UUID {
	if c == nil || c.Request == nil {
		return nil
	}
	if authCtx, ok := authverify.GetGinAuth(c); ok && authCtx.UserID != "" {
		id, err := uuid.Parse(authCtx.UserID)
		if err == nil {
			return &id
		}
	}
	// Test affordance only; the production request path always populates the
	// auth context above. Anonymous "guest" requests bypass requireUser.
	raw := strings.TrimSpace(c.Request.Header.Get("X-User-ID"))
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	return &id
}

// requireUser is the helper for routes that MUST be authenticated. It writes
// a 401 envelope and returns false when no user is present.
func requireUser(c *gin.Context) (uuid.UUID, bool) {
	uid := authUserID(c)
	if uid == nil {
		response.Err(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required")
		return uuid.Nil, false
	}
	return *uid, true
}
