// Package routes registers billing-service HTTP routes.
package routes

import (
	"github.com/gin-gonic/gin"

	"billing-service/handlers"
)

// SetupRouter wires every billing-service HTTP route onto the
// supplied gin.Engine. Mirror of the analytics-service /
// editor-service patterns: health probes at root, public /v1
// for user-facing, /internal/v1 reserved for future
// service-to-service endpoints (e.g., billing pulls subscription
// state into invoice generation when it lands).
func SetupRouter(r *gin.Engine) {
	r.GET("/healthz", handlers.HealthCheck)
	r.GET("/readyz", handlers.ReadyCheck)

	// User-facing. Caller identity comes from the `X-User-ID`
	// header set by api-gateway after JWT verification.
	v1 := r.Group("/v1/billing")
	{
		// Public — pricing pages render the plan list without
		// authentication so unauthed visitors can shop tiers.
		v1.GET("/plans", handlers.ListPlans)
		v1.GET("/me", handlers.Me)
		v1.POST("/me/subscribe", handlers.Subscribe)
		v1.GET("/me/marketplace-earnings", handlers.MarketplaceEarnings)

		// Outbound Stripe — creates a Checkout Session and
		// returns the redirect URL the SPA opens. JWT-gated
		// at the gateway like the rest of /me/*.
		v1.POST("/checkout/session", handlers.CheckoutSession)

		// Stripe webhook receiver. The HMAC signature in the
		// `Stripe-Signature` header is the auth — api-gateway
		// MUST proxy this route WITHOUT JWT verification (a
		// JWT-protected webhook is unreachable for Stripe).
		// Verification + dispatch live in handlers/stripe.go.
		v1.POST("/stripe/webhook", handlers.StripeWebhook)
	}
}
