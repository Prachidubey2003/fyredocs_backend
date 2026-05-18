package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/natsconn"

	"notify-service/internal/models"
)

// natsCheck is the package-level handle that ReadyCheck
// consults for NATS reachability. Defaults to the production
// adapter which reads natsconn.Conn at request time; tests
// swap via SetNATSCheckForTest.
var natsCheck natsconn.HealthChecker = natsconn.DefaultHealthChecker{}

// SetNATSCheckForTest installs a custom checker (or nil to
// fall back to the production adapter). Production code MUST
// NOT call this.
func SetNATSCheckForTest(c natsconn.HealthChecker) {
	if c == nil {
		natsCheck = natsconn.DefaultHealthChecker{}
		return
	}
	natsCheck = c
}

func HealthCheck(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

// ReadyCheck reports whether notify-service has its
// dependencies wired and reachable. Both checks must pass
// for a 200:
//
//   - Postgres reachability via `SELECT 1`. Without the DB
//     the deliveries audit table writes fail and the
//     subscriber Naks every event.
//   - NATS JetStream reachability via the shared
//     `natsconn.HealthChecker`. Without NATS the fanout
//     consumer + the legacy notify.send.> consumer are both
//     silent — events queue at publishers but nothing
//     dispatches. Documented as a 503 so the deploy can
//     roll back instead of the pod looking healthy while
//     deliveries silently back up.
//
// When NATS was never configured (HTTP-only mode allowed by
// main.go), the check reports `disabled` instead of failing.
// Operators can still tell from the response shape that NATS
// isn't configured, but they don't get false negatives on a
// deliberately-pruned deploy.
func ReadyCheck(c *gin.Context) {
	checks := gin.H{}
	ready := true

	if err := models.DB.Exec("SELECT 1").Error; err != nil {
		checks["postgres"] = err.Error()
		ready = false
	} else {
		checks["postgres"] = "ok"
	}

	natsStatus := natsCheck.NATSHealth()
	checks["nats"] = string(natsStatus)
	if !natsStatus.IsReady() {
		ready = false
	}

	if !ready {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "checks": checks})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready", "checks": checks})
}
