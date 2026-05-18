package handlers

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"fyredocs/shared/response"

	"analytics-service/internal/audit"
	"analytics-service/internal/models"
)

// auditRow is the wire shape — same as the model but with `[]byte`
// digests serialised as lowercase hex so the API doesn't paint
// auditors into having to base64-decode every row.
type auditRow struct {
	Seq        int64           `json:"seq"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	PrevHash   string          `json:"prevHash"`
	Hash       string          `json:"hash"`
	OccurredAt string          `json:"occurredAt"`
}

func toAuditRow(m models.AuditEvent) auditRow {
	return auditRow{
		Seq:        m.Seq,
		Actor:      m.Actor,
		Action:     m.Action,
		Resource:   m.Resource,
		Metadata:   json.RawMessage(m.Metadata),
		PrevHash:   hex.EncodeToString(m.PrevHash),
		Hash:       hex.EncodeToString(m.Hash),
		OccurredAt: m.OccurredAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// AuditMe returns the calling user's audit history, newest first.
// Caller identity comes from the `X-User-ID` header set by
// api-gateway after JWT verification.
//
//	GET /v1/audit/me?action=&limit=
func AuditMe(c *gin.Context) {
	rawUser := strings.TrimSpace(c.GetHeader("X-User-ID"))
	if rawUser == "" {
		response.Err(c, http.StatusUnauthorized, "UNAUTHORIZED", "Please log in to view audit history.")
		return
	}
	q := models.DB.WithContext(c.Request.Context()).
		Where("actor = ?", rawUser)
	if a := strings.TrimSpace(c.Query("action")); a != "" {
		q = q.Where("action = ?", a)
	}
	limit := queryInt(c, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	if limit < 1 {
		limit = 50
	}

	var rows []models.AuditEvent
	if err := q.Order("seq DESC").Limit(limit).Find(&rows).Error; err != nil {
		response.InternalErrorf(c, "DB_FAILED", "Could not list audit events", err)
		return
	}
	out := make([]auditRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAuditRow(r))
	}
	response.OK(c, "audit history retrieved", gin.H{"items": out})
}

// AuditVerify re-walks the chain in seq order and reports the
// first broken link (or "ok" if the chain is intact end-to-end).
//
//	GET /internal/v1/audit/verify
//
// Internal-only endpoint — assumed mesh-private. The on-call
// runbook calls it after every restore-from-backup and on a
// daily cron to catch silent corruption.
//
// Walks in batches of `walkBatch` so a multi-million-row chain
// doesn't pin a large slice into memory.
func AuditVerify(c *gin.Context) {
	const walkBatch = 10_000

	prevHash := audit.GenesisPrevHash
	var count int64
	var lastSeq int64

	for {
		var batch []models.AuditEvent
		err := models.DB.WithContext(c.Request.Context()).
			Where("seq > ?", lastSeq).
			Order("seq ASC").
			Limit(walkBatch).
			Find(&batch).Error
		if err != nil {
			response.InternalErrorf(c, "DB_FAILED", "Could not read audit chain", err)
			return
		}
		if len(batch) == 0 {
			break
		}

		// Build the audit.Row slice with the rolling prevHash
		// from the previous batch's tail. audit.Verify is pure
		// and validates the chain math; we provide the linkage
		// continuity here.
		rows := make([]audit.Row, len(batch))
		for i, b := range batch {
			rows[i] = audit.Row{
				Seq: b.Seq, Actor: b.Actor, Action: b.Action,
				Resource: b.Resource, Metadata: []byte(b.Metadata),
				PrevHash: b.PrevHash, Hash: b.Hash,
			}
		}
		// Override the first row's prevHash so the batch picks
		// up where the previous one left off.
		if count > 0 {
			rows[0].PrevHash = prevHash
		}
		res := audit.Verify(rows)
		if !res.OK {
			response.OK(c, "audit chain verified", gin.H{
				"ok":          false,
				"brokenAtSeq": res.BrokenAtSeq,
				"reason":      res.Reason,
				"verified":    count + res.Count,
			})
			return
		}
		count += res.Count
		lastSeq = batch[len(batch)-1].Seq
		prevHash = batch[len(batch)-1].Hash
		if len(batch) < walkBatch {
			break
		}
	}
	response.OK(c, "audit chain verified", gin.H{
		"ok":       true,
		"verified": count,
	})
}
