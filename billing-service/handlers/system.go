package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/natsconn"

	"billing-service/internal/models"
)

// natsCheck is the package-level handle that ReadyCheck
// consults for NATS reachability. Defaults to the production
// adapter; tests swap via SetNATSCheckForTest.
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

// HealthCheck is the liveness probe — always 200 if the process
// is up. Kept dependency-free so a degraded DB doesn't flip the
// probe (use /readyz for DB-aware readiness).
func HealthCheck(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

// ReadyCheck reports 200 only when Postgres AND NATS (when
// configured) are reachable. billing-service publishes audit
// events + `subscription.*` fanout events on NATS; an
// unreachable NATS means those publish calls log + drop,
// leaving an audit-trail gap. Surface as 503 so K8s rolls
// back instead of the pod looking healthy while audit rows
// go missing.
//
// NATS not configured at all reports `disabled` and DOES
// NOT fail readyz — billing-service can run HTTP-only when
// the audit pipeline is deliberately pruned.
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
