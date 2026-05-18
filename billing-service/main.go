// Command billing-service is the subscription + plan registry
// service.
//
// v0 surface:
//   - GET  /v1/billing/plans            — public plan tier list
//   - GET  /v1/billing/me               — caller's plan + period usage
//   - POST /v1/billing/me/subscribe     — switch self-serve plans
//   - GET  /healthz / /readyz / /metrics — standard infra probes
//
// Subscriptions live in this service's own Postgres schema; plans
// live in code (see internal/plans). Period-usage data comes from
// analytics-service via the internal usage endpoint (HTTP client
// in internal/usageclient).
//
// Out of scope for v0 (tracked follow-ups):
//   - Stripe integration (PaymentIntent, customer portal, webhooks)
//   - Invoice generation + line-item rendering
//   - Past-due / dunning state machine
//   - Per-seat seat management (team admin → assign seats)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/config"
	"fyredocs/shared/logger"
	"fyredocs/shared/metrics"
	"fyredocs/shared/natsconn"
	"fyredocs/shared/telemetry"

	"billing-service/handlers"
	"billing-service/internal/feereconcile"
	"billing-service/internal/models"
	"billing-service/internal/stripeclient"
	"billing-service/internal/usageclient"
	"billing-service/routes"
)

func main() {
	config.LoadConfig()
	logger.Init("billing-service", os.Getenv("LOG_MODE"))
	shutdownTracer := telemetry.Init("billing-service")
	defer shutdownTracer(context.Background())

	models.Connect()
	models.Migrate()

	// Best-effort NATS — when unreachable, audit emits become
	// no-ops (publishAudit guards on nil JS). Mirrors the
	// notify-service posture: the user-facing HTTP path doesn't
	// depend on NATS being up.
	if err := natsconn.Connect(); err != nil {
		slog.Warn("billing-service: NATS unavailable; audit publishing disabled", "error", err)
	} else if err := natsconn.EnsureStreams(context.Background()); err != nil {
		slog.Warn("billing-service: stream ensure failed; audit publishing disabled", "error", err)
	}
	defer natsconn.Close()

	// Wire the analytics-service usage client if reachable. A
	// missing/invalid URL is non-fatal — the /v1/billing/me
	// handler renders without the usage section in that case.
	handlers.SetDeps(handlers.Deps{
		Usage: buildUsageClient(),
	})

	r := gin.Default()
	r.Use(telemetry.GinTraceMiddleware("billing-service"))
	r.Use(metrics.GinMetricsMiddleware())
	r.Use(logger.GinRequestID())
	r.Use(logger.GinRequestLogger())
	r.GET("/metrics", metrics.MetricsHandler())
	if err := r.SetTrustedProxies(config.TrustedProxies()); err != nil {
		slog.Error("failed to set trusted proxies", "error", err)
		os.Exit(1)
	}

	routes.SetupRouter(r)

	// Periodic fee-reconciliation pass. Gated by env so dev
	// + local-stripe deploys don't blast Stripe with calls
	// every 10 minutes for no reason. Production turns this
	// on once STRIPE_API_KEY is wired.
	reconcileCtx, cancelReconcile := context.WithCancel(context.Background())
	defer cancelReconcile()
	var reconcileRunner *feereconcile.Runner
	if feeReconcileEnabled() {
		reconcileRunner = &feereconcile.Runner{}
		reconcileRunner.Start(reconcileCtx, models.DB, defaultStripeClientFactory, feereconcile.RunnerOptions{}, nil)
		slog.Info("billing-service: fee-reconciliation runner started")
		defer reconcileRunner.Stop()
	} else {
		slog.Info("billing-service: BILLING_FEE_RECONCILE_ENABLED unset; fee-reconciliation runner not started")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8099"
	}
	srv := &http.Server{Addr: ":" + port, Handler: r}

	go func() {
		slog.Info("billing-service listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down billing-service...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}
	slog.Info("billing-service exited")
}

// feeReconcileEnabled reports whether the periodic Stripe
// fee back-fill loop is on. Off by default — production
// turns it on once STRIPE_API_KEY is wired and the
// reconciliation pass should run. Mirrors the DLP_ENABLED
// posture in job-service: feature-gate the env-driven side
// effect so the same binary safely runs in dev / staging
// without burning Stripe quota.
func feeReconcileEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("BILLING_FEE_RECONCILE_ENABLED")))
	return v == "1" || v == "true" || v == "yes"
}

// defaultStripeClientFactory mirrors handlers.defaultStripeClient
// — reads STRIPE_API_KEY at call time so a config rotation
// mid-run picks up the new key on the next reconciliation
// pass. Lives in main.go (rather than re-exported from the
// handlers package) so the feereconcile loop has no
// dependency on the user-facing handler scaffolding.
func defaultStripeClientFactory() (*stripeclient.Client, error) {
	key := strings.TrimSpace(os.Getenv("STRIPE_API_KEY"))
	if key == "" {
		return nil, errStripeAPIKeyUnset
	}
	return &stripeclient.Client{SecretKey: key}, nil
}

var errStripeAPIKeyUnset = errStr("STRIPE_API_KEY is not set")

type errStr string

func (e errStr) Error() string { return string(e) }

// buildUsageClient returns the analytics-service HTTP client, or
// nil if ANALYTICS_SERVICE_URL is unset/invalid. Returning nil
// (rather than failing the process) keeps billing-service
// operable in dev / single-service deployments — the usage
// section just doesn't render.
func buildUsageClient() handlers.UsageFetcher {
	url := strings.TrimSpace(os.Getenv("ANALYTICS_SERVICE_URL"))
	if url == "" {
		slog.Warn("billing-service: ANALYTICS_SERVICE_URL unset; /v1/billing/me will render without usage")
		return nil
	}
	c, err := usageclient.New(usageclient.Options{BaseURL: url})
	if err != nil {
		slog.Warn("billing-service: usageclient init failed", "url", url, "error", err)
		return nil
	}
	slog.Info("billing-service: usage client wired", "analytics", url)
	return c
}
