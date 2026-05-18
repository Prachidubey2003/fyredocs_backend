// Command notify-service is the outbound notifications fan-out
// service. It consumes the NOTIFY JetStream subjects
// `notify.send.{email,webhook,push,slack}` and dispatches each
// event to the matching channel implementation, persisting one
// `notify_deliveries` row per attempt for the audit log.
//
// v0 ships email, webhook, Slack, and Expo Push channels with
// full DB persistence. Direct APNs/FCM transports (without the
// Expo intermediary) are a follow-up for tenants that want
// Expo out of their trust chain.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/smtp"
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
	"fyredocs/shared/queue"
	"fyredocs/shared/telemetry"

	"notify-service/handlers"
	"notify-service/internal/channels"
	"notify-service/internal/dispatcher"
	"notify-service/internal/models"
	"notify-service/routes"
	"notify-service/subscriber"
)

func main() {
	config.LoadConfig()
	logger.Init("notify-service", os.Getenv("LOG_MODE"))
	shutdownTracer := telemetry.Init("notify-service")
	defer shutdownTracer(context.Background())

	models.Connect()
	models.Migrate()

	disp := buildDispatcher()
	handlers.SetDeps(handlers.Deps{Disp: disp})

	// NATS is optional in dev — when unreachable, the HTTP
	// `/internal/v1/notify/send` path still works.
	var subs *subscriber.Subscribers
	if err := natsconn.Connect(); err != nil {
		slog.Warn("notify-service: NATS unavailable; HTTP-only mode", "error", err)
	} else if err := natsconn.EnsureStreams(context.Background()); err != nil {
		slog.Warn("notify-service: stream ensure failed; HTTP-only mode", "error", err)
	} else {
		s, err := subscriber.Start(context.Background(), disp, models.DB)
		if err != nil {
			slog.Warn("notify-service: subscriber start failed; HTTP-only mode", "error", err)
		} else {
			subs = s
		}
	}
	defer natsconn.Close()

	r := gin.Default()
	r.Use(telemetry.GinTraceMiddleware("notify-service"))
	r.Use(metrics.GinMetricsMiddleware())
	r.Use(logger.GinRequestID())
	r.Use(logger.GinRequestLogger())
	r.GET("/metrics", metrics.MetricsHandler())
	if err := r.SetTrustedProxies(config.TrustedProxies()); err != nil {
		slog.Error("failed to set trusted proxies", "error", err)
		os.Exit(1)
	}

	routes.SetupRouter(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8098"
	}
	srv := &http.Server{Addr: ":" + port, Handler: r}

	go func() {
		slog.Info("notify-service listening", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down notify-service...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
	}
	subs.Stop()
	slog.Info("notify-service exited")
}

// buildDispatcher constructs the production dispatcher with the
// channels we ship in v0.
//
//   - email:   uses SMTP env vars when set; falls back to log-only
//     for dev / preview environments.
//   - webhook: HMAC-signed POST when NOTIFY_WEBHOOK_SECRET is set;
//     unsigned when empty (dev convenience — subscribers verifying
//     signatures will reject).
//   - slack:   POSTs the payload to req.Target (a Slack incoming-
//     webhook URL). The URL itself is the auth token — callers
//     store it as a per-workspace secret. No HMAC needed.
//   - push:    POSTs the payload to Expo Push
//     (https://exp.host/--/api/v2/push/send) with req.Target as
//     the device's ExponentPushToken. EXPO_ACCESS_TOKEN, when
//     set, enables enhanced-security mode. Mobile apps that opt
//     out of Expo (direct APNs/FCM) are a tracked follow-up.
func buildDispatcher() *dispatcher.Dispatcher {
	d := dispatcher.New(models.DB)

	d.Register(queue.ChannelEmail, &channels.Email{
		SMTPHost:    strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPAuth:    buildSMTPAuth(),
		FromAddress: orDefault(os.Getenv("EMAIL_FROM"), "no-reply@fyredocs.com"),
	})

	d.Register(queue.ChannelWebhook,
		channels.NewWebhook([]byte(strings.TrimSpace(os.Getenv("NOTIFY_WEBHOOK_SECRET")))),
	)

	d.Register(queue.ChannelSlack, channels.NewSlack())

	d.Register(queue.ChannelPush,
		channels.NewPush(strings.TrimSpace(os.Getenv("EXPO_ACCESS_TOKEN"))),
	)

	return d
}

func buildSMTPAuth() smtp.Auth {
	user := strings.TrimSpace(os.Getenv("SMTP_USER"))
	pass := os.Getenv("SMTP_PASSWORD")
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if user == "" || pass == "" || host == "" {
		return nil
	}
	// SMTP host may include port; PlainAuth wants the hostname
	// alone for the realm.
	hostname := host
	if i := strings.LastIndex(host, ":"); i > 0 {
		hostname = host[:i]
	}
	return smtp.PlainAuth("", user, pass, hostname)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
