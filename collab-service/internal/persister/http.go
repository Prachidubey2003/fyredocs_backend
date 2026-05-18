package persister

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HTTP is a Persister that calls editor-service's
// `/internal/v1/snapshots/{docID}` endpoints. The wire format is
// the length-prefixed framing defined in framing.go.
//
// Best-effort semantics:
//   - Save logs and swallows errors. Losing one snapshot is
//     recoverable on the next room close.
//   - Load returns nil on any non-2xx response or transport
//     error. A room that fails to load just starts empty; live
//     editing continues to work.
//
// Timeout: per-request, configurable via NewHTTP. Defaults to
// 5s for Load (cold-start latency tolerance) and 10s for Save
// (allowing for big snapshots). The room run-loop is blocked
// during these calls, so the timeouts MUST be finite — that's
// what keeps a slow editor-service from holding up Join events.
type HTTP struct {
	baseURL     string
	client      *http.Client
	loadTimeout time.Duration
	saveTimeout time.Duration
	logger      *slog.Logger
}

// HTTPOptions configures an HTTP persister. Defaults are fine for
// production; tests override the client to use httptest.Server.
type HTTPOptions struct {
	// BaseURL is the editor-service base, e.g. `http://editor-service:8090`.
	// Required.
	BaseURL string
	// Client overrides the *http.Client used for outbound calls.
	// nil means a default client with sensible transport settings.
	Client *http.Client
	// LoadTimeout / SaveTimeout default to 5s / 10s.
	LoadTimeout time.Duration
	SaveTimeout time.Duration
	// Logger overrides the slog logger. nil means slog.Default().
	Logger *slog.Logger
}

// NewHTTP constructs an HTTP persister. Returns an error only on
// obviously-broken configuration (missing BaseURL); transport
// problems surface at Load/Save time.
func NewHTTP(opts HTTPOptions) (*HTTP, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("persister/http: BaseURL required")
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 0} // per-request context handles timeout
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	loadTO := opts.LoadTimeout
	if loadTO == 0 {
		loadTO = 5 * time.Second
	}
	saveTO := opts.SaveTimeout
	if saveTO == 0 {
		saveTO = 10 * time.Second
	}
	return &HTTP{
		baseURL:     base,
		client:      client,
		loadTimeout: loadTO,
		saveTimeout: saveTO,
		logger:      logger,
	}, nil
}

// Load fetches the latest snapshot for docID and decodes it.
// Returns nil on 404 (no snapshot yet), nil + logged warning on
// any other error. Never returns a partial frame slice — Decode's
// truncated-body partial result is discarded because a half-applied
// log is harder to reason about than starting fresh.
func (h *HTTP) Load(docID string) [][]byte {
	if h == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.loadTimeout)
	defer cancel()

	url := h.baseURL + "/internal/v1/snapshots/" + docID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.logger.Warn("persister load: build request failed", "doc", docID, "error", err)
		return nil
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.logger.Warn("persister load: request failed", "doc", docID, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No snapshot for this doc yet — silent.
		return nil
	}
	if resp.StatusCode/100 != 2 {
		h.logger.Warn("persister load: non-2xx", "doc", docID, "status", resp.StatusCode)
		return nil
	}

	frames, err := Decode(resp.Body)
	if err != nil {
		h.logger.Warn("persister load: decode failed", "doc", docID, "error", err)
		return nil
	}
	return frames
}

// Save uploads the encoded snapshot via PUT. Empty inputs are
// dropped — there's nothing useful for editor-service to record,
// and the previous snapshot (if any) remains the latest.
func (h *HTTP) Save(docID string, frames [][]byte) {
	if h == nil || len(frames) == 0 {
		return
	}
	body := Encode(frames)
	ctx, cancel := context.WithTimeout(context.Background(), h.saveTimeout)
	defer cancel()

	url := h.baseURL + "/internal/v1/snapshots/" + docID
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		h.logger.Warn("persister save: build request failed", "doc", docID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(body))

	resp, err := h.client.Do(req)
	if err != nil {
		h.logger.Warn("persister save: request failed", "doc", docID, "error", err)
		return
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused (keep-alive).
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		h.logger.Warn("persister save: non-2xx", "doc", docID, "status", resp.StatusCode)
		return
	}
}
