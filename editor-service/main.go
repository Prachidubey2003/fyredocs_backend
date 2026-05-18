package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"fyredocs/shared/config"
	"fyredocs/shared/logger"
	"fyredocs/shared/metrics"
	"fyredocs/shared/natsconn"
	"fyredocs/shared/telemetry"

	"editor-service/handlers"
	"editor-service/internal/authclient"
	"editor-service/internal/authverify"
	"editor-service/internal/models"
	"editor-service/routes"
)

func main() {
	config.LoadConfig()
	logger.Init("editor-service", os.Getenv("LOG_MODE"))
	shutdownTracer := telemetry.Init("editor-service")
	defer shutdownTracer(context.Background())

	// Storage root for document / revision bytes. EDIT_STORAGE_DIR is
	// preferred; FILES_DIR is honoured as a fallback so a single env var
	// can configure the platform across api-gateway + workers + editor.
	storageDir := os.Getenv("EDIT_STORAGE_DIR")
	if storageDir == "" {
		storageDir = os.Getenv("FILES_DIR")
	}
	if storageDir == "" {
		storageDir = "/files"
	}
	handlers.SetStorageDir(storageDir)
	slog.Info("storage root configured", "dir", storageDir)

	// Auth-service profile lookup. Nil client (when AUTH_SERVICE_URL
	// is unset) is fine — comment-list responses just won't carry
	// `authorDisplayName` and the frontend falls back to rendering
	// the raw author UUID.
	if authURL := os.Getenv("AUTH_SERVICE_URL"); authURL != "" {
		handlers.SetAuthClient(authclient.New(authclient.Options{BaseURL: authURL}))
		slog.Info("auth-service profile lookup enabled", "url", authURL)
	} else {
		slog.Warn("AUTH_SERVICE_URL unset; comments will render raw author UUIDs")
	}

	models.Connect()
	models.Migrate()

	if err := natsconn.Connect(); err != nil {
		slog.Warn("NATS connection failed, edit events will be skipped", "error", err)
	} else {
		defer natsconn.Close()
		if err := natsconn.EnsureStreams(context.Background()); err != nil {
			slog.Warn("NATS stream setup failed", "error", err)
		}
	}

	// Redis is optional today for the editor-service cache (presence /
	// idempotency lands in Phase 2). The JWT denylist also rides on Redis,
	// so a missing Redis means the denylist is skipped (matching
	// job-service's behavior).
	redisClient := newRedisClient()
	if redisClient != nil {
		defer redisClient.Close()
	}

	// JWT denylist — defense-in-depth on top of the gateway's verification.
	var denylist authverify.TokenDenylist
	if redisClient != nil && config.GetEnvBool("AUTH_DENYLIST_ENABLED", true) {
		denylist = authverify.NewRedisTokenDenylist(redisClient, os.Getenv("AUTH_DENYLIST_PREFIX"))
		if denylist != nil {
			slog.Info("Token denylist enabled")
		}
	} else {
		slog.Warn("Token denylist disabled (Redis unavailable or AUTH_DENYLIST_ENABLED=false)")
	}

	r := gin.Default()
	r.Use(telemetry.GinTraceMiddleware("editor-service"))
	r.Use(metrics.GinMetricsMiddleware())
	r.Use(logger.GinRequestID())
	r.Use(logger.GinRequestLogger())
	r.GET("/metrics", metrics.MetricsHandler())
	if err := r.SetTrustedProxies(config.TrustedProxies()); err != nil {
		slog.Error("failed to set trusted proxies", "error", err)
		os.Exit(1)
	}

	authMiddleware := buildAuthMiddleware(redisClient, denylist)
	routes.SetupRouter(r, redisClient, authMiddleware)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("editor-service listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}
	slog.Info("server exited")
}

// buildAuthMiddleware constructs the Gin auth middleware. Mirrors the
// job-service / auth-service pattern: a verifier built from env (with
// optional denylist) plus a Redis guest store, then a gin handler.
//
// editor-service does not issue guest tokens (documents require accounts),
// but supporting the existing guest-cookie shape means traffic from a
// signed-in user that briefly carries a guest cookie won't error out.
func buildAuthMiddleware(redisClient *redis.Client, denylist authverify.TokenDenylist) gin.HandlerFunc {
	trustGateway := config.GetEnvBool("AUTH_TRUST_GATEWAY_HEADERS", false)

	verifier, err := authverify.NewVerifierFromEnv(denylist)
	if err != nil {
		slog.Error("auth verifier init failed", "error", err)
		os.Exit(1)
	}

	var guestStore authverify.GuestStore
	if redisClient != nil {
		guestStore = authverify.NewRedisGuestStore(redisClient, authverify.GuestStoreConfig{
			KeyPrefix: os.Getenv("AUTH_GUEST_PREFIX"),
			KeySuffix: os.Getenv("AUTH_GUEST_SUFFIX"),
		})
	}

	return authverify.GinAuthMiddleware(authverify.GinMiddlewareOptions{
		Verifier:            verifier,
		GuestStore:          guestStore,
		TrustGatewayHeaders: trustGateway,
	})
}

// newRedisClient returns a Redis client built from env vars, or nil when
// Redis is unconfigured / unreachable. The service is functional without
// Redis (presence / idempotency cache land in Phase 2).
func newRedisClient() *redis.Client {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("Redis unavailable; editor-service starting without cache", "error", err)
		_ = client.Close()
		return nil
	}
	return client
}
