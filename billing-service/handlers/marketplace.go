package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"fyredocs/shared/response"

	"billing-service/internal/models"
)

// MarketplaceEarning is the curated public shape of a
// [models.RevshareEntry] returned by the developer-facing
// earnings endpoint. Tighter than the DB row — internal
// fields the marketplace partner doesn't need to see DO NOT
// leak:
//
//   - `platform_share_cents` — that's our take; developer
//     doesn't need to see it (and Stripe would say it
//     shouldn't see it).
//   - `stripe_fee_cents` — handling concern of our payout
//     pipeline; developer cares about THEIR cut.
//   - `source` / `source_ref` — internal dedup metadata;
//     would just confuse the developer.
//   - `developer_user_id` — the caller knows their own id.
type MarketplaceEarning struct {
	ID                  uuid.UUID `json:"id"`
	TransactionID       string    `json:"transactionId"`
	PluginID            string    `json:"pluginId"`
	GrossCents          int64     `json:"grossCents"`
	DeveloperShareCents int64     `json:"developerShareCents"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	RecordedAt          time.Time `json:"recordedAt"`
}

// MarketplaceEarningsResponse is the envelope's `data` field.
// `totalEarnedCents` is the sum of `developerShareCents`
// across THE RETURNED PAGE (not lifetime) — gives the
// developer an at-a-glance "what's in this view" without a
// second query. The `lifetime` aggregate ships in a follow-up
// once the payout pipeline materialises it.
type MarketplaceEarningsResponse struct {
	Items            []MarketplaceEarning `json:"items"`
	TotalEarnedCents int64                `json:"totalEarnedCents"`
}

// marketplaceEarningsLimit defaults / cap.
const (
	marketplaceEarningsDefaultLimit = 50
	marketplaceEarningsMaxLimit     = 500
)

// MarketplaceEarnings handles GET
// `/v1/billing/me/marketplace-earnings`.
//
// Returns the caller's revshare entries — what they've
// earned through marketplace plugin sales. The caller IS the
// developer; entries where `developer_user_id` matches the
// authenticated user are visible.
//
// Filters:
//   - `?status=pending|payable|paid|reversed` — narrow by
//     lifecycle position. Unrecognised values produce a
//     400 (refuse to silently ignore — a developer
//     filtering by an invalid status thinks they're
//     looking at a payout total when they're actually
//     looking at everything).
//   - `?limit=N` — cap the page. Defaults to 50, max 500.
//
// Sort order: newest first by `recorded_at`. Pagination via
// cursor (`?before=<iso8601>`) is a follow-up — v0 returns
// the top N, which is enough for the dashboard view.
func MarketplaceEarnings(c *gin.Context) {
	userID, ok := requireUserID(c)
	if !ok {
		return
	}

	q := models.DB.WithContext(c.Request.Context()).
		Where("developer_user_id = ?", userID)

	if status := strings.TrimSpace(c.Query("status")); status != "" {
		if !isValidMarketplaceStatus(status) {
			response.BadRequest(c, "INVALID_STATUS",
				"Status must be one of: pending, payable, paid, reversed")
			return
		}
		q = q.Where("status = ?", status)
	}

	limit := marketplaceEarningsDefaultLimit
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > marketplaceEarningsMaxLimit {
		limit = marketplaceEarningsMaxLimit
	}

	var rows []models.RevshareEntry
	if err := q.Order("recorded_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not list marketplace earnings", err)
		return
	}

	items := make([]MarketplaceEarning, 0, len(rows))
	var pageTotal int64
	for _, r := range rows {
		items = append(items, MarketplaceEarning{
			ID:                  r.ID,
			TransactionID:       r.TransactionID,
			PluginID:            r.PluginID,
			GrossCents:          r.GrossCents,
			DeveloperShareCents: r.DeveloperShareCents,
			Currency:            r.Currency,
			Status:              r.Status,
			RecordedAt:          r.RecordedAt,
		})
		// Page-scoped total — the payout pipeline emits a
		// separate lifetime aggregate when it lands.
		pageTotal += r.DeveloperShareCents
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "marketplace earnings retrieved",
		"data": MarketplaceEarningsResponse{
			Items:            items,
			TotalEarnedCents: pageTotal,
		},
	})
}

// isValidMarketplaceStatus mirrors [revshare.Status] without
// importing it (the handler doesn't need the lifecycle
// behaviour, just the enum). Keep in sync with
// internal/revshare/revshare.go's Status constants.
func isValidMarketplaceStatus(s string) bool {
	switch s {
	case "pending", "payable", "paid", "reversed":
		return true
	default:
		return false
	}
}
