package handlers

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// TestDownloadDocument_RejectsUnauthenticated locks in the standard
// short-circuit: no auth context → 401 with no DB hit.
func TestDownloadDocument_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodGet, "/v1/documents/X/download", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
	DownloadDocument(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDownloadDocument_RejectsBadUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodGet, "/v1/documents/garbage/download", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{{Key: "id", Value: "garbage"}}
	DownloadDocument(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDownloadRevision_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodGet, "/v1/documents/X/revisions/Y/download", nil, nil)
	c.Params = gin.Params{
		{Key: "id", Value: uuid.NewString()},
		{Key: "revId", Value: uuid.NewString()},
	}
	DownloadRevision(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestDownloadRevision_RejectsBadDocUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodGet, "/v1/documents/garbage/revisions/Y/download", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{
		{Key: "id", Value: "garbage"},
		{Key: "revId", Value: uuid.NewString()},
	}
	DownloadRevision(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDownloadRevision_RejectsBadRevUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodGet, "/v1/documents/X/revisions/garbage/download", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{
		{Key: "id", Value: uuid.NewString()},
		{Key: "revId", Value: "garbage"},
	}
	DownloadRevision(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRestoreRevision_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodPost, "/v1/documents/X/revisions/Y/restore", nil, nil)
	c.Params = gin.Params{
		{Key: "id", Value: uuid.NewString()},
		{Key: "revId", Value: uuid.NewString()},
	}
	RestoreRevision(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRestoreRevision_RejectsBadDocUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodPost, "/v1/documents/garbage/revisions/Y/restore", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{
		{Key: "id", Value: "garbage"},
		{Key: "revId", Value: uuid.NewString()},
	}
	RestoreRevision(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRestoreRevision_RejectsBadRevUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodPost, "/v1/documents/X/revisions/garbage/restore", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{
		{Key: "id", Value: uuid.NewString()},
		{Key: "revId", Value: "garbage"},
	}
	RestoreRevision(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// downloadFilename is pure; can be unit-tested without DB or storage.
func TestDownloadFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "document.pdf"},
		{"contract", "contract.pdf"},
		{"contract.pdf", "contract.pdf"},
		{"Contract.PDF", "Contract.PDF"},
		{`Quarterly "Report"`, "Quarterly _Report_.pdf"},
		{"path/to/file", "path_to_file.pdf"},
		{"with\\backslash", "with_backslash.pdf"},
		{"line\nbreak", "line_break.pdf"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := downloadFilename(tc.in)
			if got != tc.want {
				t.Errorf("downloadFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
