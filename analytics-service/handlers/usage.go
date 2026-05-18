package handlers

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"analytics-service/internal/models"
	"fyredocs/shared/response"
)

// billingPeriodRe matches the canonical YYYY-MM period key.
// Anchored so a stray suffix or path traversal in the query
// parameter (`2026-05/etc`) gets rejected at the boundary.
var billingPeriodRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// UsageRollupRow is one (event_type, unit) bucket of a user's
// usage within a single billing period. Returned by the rollup
// endpoints as part of UsageRollupResponse.Items.
type UsageRollupRow struct {
	EventType     string `json:"eventType"`
	Unit          string `json:"unit"`
	TotalQuantity int64  `json:"totalQuantity"`
	EventCount    int64  `json:"eventCount"`
}

// UsageRollupResponse is the standard envelope for the rollup
// endpoints. UserID + Period are echoed back so callers can
// confirm they queried the right slice when batching requests.
type UsageRollupResponse struct {
	UserID string           `json:"userId"`
	Period string           `json:"period"`
	Items  []UsageRollupRow `json:"items"`
}

// UsageMe returns the calling user's usage rollup for the
// requested billing period (defaults to the current month in
// UTC). The caller is identified by the `X-User-ID` header set
// by api-gateway after JWT verification — same pattern as the
// admin endpoints above.
//
// Path: GET /v1/usage/me?period=YYYY-MM
//
// Used by the in-app "Usage" tab. For the inverse direction
// (billing-service reading a target user's usage to produce an
// invoice), use the internal endpoint below.
func UsageMe(c *gin.Context) {
	rawUser := strings.TrimSpace(c.GetHeader("X-User-ID"))
	if rawUser == "" {
		response.Err(c, http.StatusUnauthorized, "UNAUTHORIZED", "Please log in to view usage.")
		return
	}
	userID, err := uuid.Parse(rawUser)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_USER", "Caller identity is malformed.")
		return
	}
	period, err := resolvePeriod(c.Query("period"))
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_PERIOD", err.Error())
		return
	}
	resp, err := queryRollup(userID, period)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "INTERNAL", "Failed to compute usage rollup.")
		return
	}
	response.OK(c, "usage rollup retrieved", resp)
}

// UsageByUser is the internal endpoint billing-service calls to
// pull a specific user's rollup. Lives under /internal/v1/usage
// so the public /v1/usage namespace stays focused on the calling
// user's own data. Trust boundary: this endpoint assumes the
// service mesh is private (same posture as
// /internal/v1/snapshots in editor-service); api-gateway must
// NOT proxy /internal/* to the public network.
//
// Path: GET /internal/v1/usage/:userID?period=YYYY-MM
func UsageByUser(c *gin.Context) {
	rawUser := c.Param("userID")
	userID, err := uuid.Parse(rawUser)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_USER", "userID path parameter must be a UUID.")
		return
	}
	period, err := resolvePeriod(c.Query("period"))
	if err != nil {
		response.Err(c, http.StatusBadRequest, "INVALID_PERIOD", err.Error())
		return
	}
	resp, err := queryRollup(userID, period)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "INTERNAL", "Failed to compute usage rollup.")
		return
	}
	response.OK(c, "usage rollup retrieved", resp)
}

// resolvePeriod normalises the `period` query parameter. Empty
// → current UTC month; otherwise must match YYYY-MM with a valid
// calendar month. Returns the canonical string used as the
// `billing_period` column value, so callers compare strings only.
func resolvePeriod(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return models.FormatBillingPeriod(time.Now()), nil
	}
	if !billingPeriodRe.MatchString(raw) {
		return "", errInvalidPeriod
	}
	return raw, nil
}

// errInvalidPeriod is a value error rather than a sentinel —
// usage rollup is a single endpoint, no need for errors.Is
// matching upstream.
var errInvalidPeriod = errInvalidPeriodValue{}

type errInvalidPeriodValue struct{}

func (errInvalidPeriodValue) Error() string {
	return "period must be YYYY-MM (e.g., 2026-05)"
}

// queryRollup runs the GROUP BY (event_type, unit) aggregation
// for one user in one billing period. The composite index
// `idx_usage_user_period` (user_id, billing_period) makes this
// an index range scan even at 10M+ events per user.
//
// EventCount is included alongside TotalQuantity so the UI can
// distinguish "one big op" from "many small ops" — e.g.,
// 100 pages over 50 ops vs over 1 op tells different stories.
func queryRollup(userID uuid.UUID, period string) (*UsageRollupResponse, error) {
	type row struct {
		EventType     string
		Unit          string
		TotalQuantity int64
		EventCount    int64
	}
	var rows []row
	err := models.DB.
		Model(&models.UsageEvent{}).
		Select("event_type, unit, SUM(quantity) AS total_quantity, COUNT(*) AS event_count").
		Where("user_id = ? AND billing_period = ?", userID, period).
		Group("event_type, unit").
		Order("event_type ASC, unit ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	items := make([]UsageRollupRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, UsageRollupRow{
			EventType:     r.EventType,
			Unit:          r.Unit,
			TotalQuantity: r.TotalQuantity,
			EventCount:    r.EventCount,
		})
	}
	return &UsageRollupResponse{
		UserID: userID.String(),
		Period: period,
		Items:  items,
	}, nil
}
