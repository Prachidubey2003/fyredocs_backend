package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"editor-service/handlers"
	"editor-service/internal/models"
)

// SetupRouter registers all editor-service routes against the supplied gin
// engine.
//
//   - redisClient is optional today; reserved for the Phase 2 Yjs presence
//   - idempotency-key surface.
//   - authMiddleware is applied to every /v1/* route. May be nil in tests
//     that don't need auth (handler-level requireUser still rejects
//     unauthenticated callers).
func SetupRouter(r *gin.Engine, redisClient *redis.Client, authMiddleware gin.HandlerFunc) {
	// /healthz — liveness only, no dependency checks.
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// /readyz — DB + (optional) Redis readiness, with a tight timeout so the
	// handler can never block a Kubernetes probe.
	r.GET("/readyz", func(c *gin.Context) {
		checks := map[string]string{}
		status := http.StatusOK
		overall := "ready"

		if models.DB == nil {
			checks["postgres"] = "not initialized"
			status = http.StatusServiceUnavailable
			overall = "not ready"
		} else {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			sqlDB, err := models.DB.DB()
			if err != nil {
				checks["postgres"] = err.Error()
				status = http.StatusServiceUnavailable
				overall = "not ready"
			} else if err := sqlDB.PingContext(ctx); err != nil {
				checks["postgres"] = err.Error()
				status = http.StatusServiceUnavailable
				overall = "not ready"
			} else {
				checks["postgres"] = "ok"
			}
		}

		if redisClient != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			if err := redisClient.Ping(ctx).Err(); err != nil {
				checks["redis"] = err.Error()
				status = http.StatusServiceUnavailable
				overall = "not ready"
			} else {
				checks["redis"] = "ok"
			}
		}

		c.JSON(status, gin.H{"status": overall, "checks": checks})
	})

	// All editor routes live under /v1/ so we can evolve the contract via a
	// new major version without renaming individual paths. The auth
	// middleware is applied at the group level so handlers can assume an
	// authenticated context is present.
	v1 := r.Group("/v1")
	if authMiddleware != nil {
		v1.Use(authMiddleware)
	}

	// Document CRUD.
	v1.POST("/documents", handlers.CreateDocument)
	v1.GET("/documents", handlers.ListDocuments)
	v1.GET("/documents/:id", handlers.GetDocument)
	v1.DELETE("/documents/:id", handlers.DeleteDocument)

	// sPDOM edit op — v0 supports page.rotate; other ops 400 (INVALID_OP).
	v1.POST("/documents/:id/edit", handlers.EditDocument)

	// Download the document's bytes — current revision if any edits have
	// been applied, otherwise the original upload.
	v1.GET("/documents/:id/download", handlers.DownloadDocument)

	// Revisions — read-only at this layer (commits flow through /edit once
	// the sPDOM writer lands). Per-revision download lets clients fetch
	// any historical state.
	v1.GET("/documents/:id/revisions", handlers.ListRevisions)
	v1.GET("/documents/:id/revisions/:revId/download", handlers.DownloadRevision)
	// Restore — creates a new Revision whose bytes copy the target's.
	v1.POST("/documents/:id/revisions/:revId/restore", handlers.RestoreRevision)

	// sPDOM — semantic Document Object Model parsed from the stored PDF.
	// Page geometry today; Blocks/Lines/Runs fill in with the L3 layout
	// reconstruction follow-up. See internal/spdom/doc.go.
	v1.GET("/documents/:id/spdom", handlers.GetSPDOM)

	// Comments.
	v1.POST("/documents/:id/comments", handlers.AddComment)
	v1.GET("/documents/:id/comments", handlers.ListComments)
	v1.POST("/documents/:id/comments/:commentId/resolve", handlers.ResolveComment)

	// Internal-only endpoints. The gateway does NOT proxy /internal/*,
	// so these are only reachable from other in-cluster services
	// (today: collab-service for Yjs checkpoint persistence). We
	// deliberately skip the auth middleware here — trust boundary
	// is the cluster network, mirroring auth-service's
	// /internal/verify-api-key shape.
	internal := r.Group("/internal/v1")
	internal.GET("/snapshots/:docID", handlers.GetSnapshot)
	internal.PUT("/snapshots/:docID", handlers.PutSnapshot)
}
