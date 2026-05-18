// Package routes registers notify-service HTTP routes.
package routes

import (
	"github.com/gin-gonic/gin"

	"notify-service/handlers"
)

// SetupRouter wires routes onto the gin engine. Same shape as
// every other Fyredocs gin service: health probes at root, /v1
// for user-facing reads, /internal/v1 for service-to-service
// writes.
func SetupRouter(r *gin.Engine) {
	r.GET("/healthz", handlers.HealthCheck)
	r.GET("/readyz", handlers.ReadyCheck)

	v1 := r.Group("/v1/notify")
	{
		// Authenticated reads — caller's deliveries only.
		v1.GET("/deliveries", handlers.ListMyDeliveries)

		// Webhook subscriptions — third-party integrations
		// (Zapier, customer scripts) register here. Auth is
		// the regular `X-User-ID` JWT path.
		v1.POST("/webhooks", handlers.CreateWebhook)
		v1.GET("/webhooks", handlers.ListWebhooks)
		v1.DELETE("/webhooks/:id", handlers.DeleteWebhook)
		// Resurrect a subscription the circuit breaker
		// auto-disabled. Idempotent — re-enabling an active
		// row is allowed (and resets failure_count so the
		// breaker starts fresh).
		v1.POST("/webhooks/:id/enable", handlers.EnableWebhook)

		// Fire a synthetic `webhook.test` event at the
		// subscription's target URL — lets users verify
		// their receiver works before relying on a real
		// event. Does NOT touch the circuit-breaker state.
		v1.POST("/webhooks/:id/test", handlers.TestWebhook)

		// Generate a fresh signing secret for an existing
		// subscription. Returns the new plaintext once. Old
		// secret becomes invalid immediately (no grace
		// window).
		v1.POST("/webhooks/:id/rotate-secret", handlers.RotateWebhookSecret)
	}

	internal := r.Group("/internal/v1/notify")
	{
		// Service-to-service synchronous send. The NATS path
		// (notify.send.>) is the preferred async route; this
		// HTTP endpoint exists for flows that need an immediate
		// success/failure return value.
		internal.POST("/send", handlers.Send)
	}
}
