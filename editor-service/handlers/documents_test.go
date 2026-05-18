package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newCtx(method, path string, body []byte, headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	return c, rec
}

func TestAuthUserID_Missing(t *testing.T) {
	c, _ := newCtx(http.MethodGet, "/v1/documents", nil, nil)
	if got := authUserID(c); got != nil {
		t.Errorf("expected nil for missing header, got %v", got)
	}
}

func TestAuthUserID_Invalid(t *testing.T) {
	c, _ := newCtx(http.MethodGet, "/v1/documents", nil, map[string]string{
		"X-User-ID": "not-a-uuid",
	})
	if got := authUserID(c); got != nil {
		t.Errorf("expected nil for invalid uuid, got %v", got)
	}
}

func TestAuthUserID_Valid(t *testing.T) {
	want := uuid.Must(uuid.NewV7())
	c, _ := newCtx(http.MethodGet, "/v1/documents", nil, map[string]string{
		"X-User-ID": want.String(),
	})
	got := authUserID(c)
	if got == nil || *got != want {
		t.Errorf("authUserID = %v, want %v", got, want)
	}
}

func TestRequireUser_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodGet, "/v1/documents", nil, nil)
	_, ok := requireUser(c)
	if ok {
		t.Fatal("requireUser should reject missing X-User-ID")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}

	var envelope struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Success {
		t.Error("envelope.success should be false")
	}
	if envelope.Error.Code != "UNAUTHENTICATED" {
		t.Errorf("error.code = %q, want UNAUTHENTICATED", envelope.Error.Code)
	}
}

func TestParsePagination_Defaults(t *testing.T) {
	c, _ := newCtx(http.MethodGet, "/v1/documents", nil, nil)
	page, limit := parsePagination(c)
	if page != 1 || limit != 25 {
		t.Errorf("defaults: got page=%d limit=%d, want 1/25", page, limit)
	}
}

func TestParsePagination_Clamps(t *testing.T) {
	cases := []struct {
		path                string
		wantPage, wantLimit int
	}{
		{"/v1/documents?page=0&limit=0", 1, 25},
		{"/v1/documents?page=-5&limit=-10", 1, 25},
		{"/v1/documents?page=3&limit=50", 3, 50},
		{"/v1/documents?page=3&limit=1000", 3, 100}, // capped at 100
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			c, _ := newCtx(http.MethodGet, tc.path, nil, nil)
			page, limit := parsePagination(c)
			if page != tc.wantPage || limit != tc.wantLimit {
				t.Errorf("got page=%d limit=%d, want %d/%d",
					page, limit, tc.wantPage, tc.wantLimit)
			}
		})
	}
}

func TestParseUUIDParam_Valid(t *testing.T) {
	want := uuid.Must(uuid.NewV7())
	c, _ := newCtx(http.MethodGet, "/v1/documents/"+want.String(), nil, nil)
	c.Params = gin.Params{{Key: "id", Value: want.String()}}
	got, ok := parseUUIDParam(c, "id")
	if !ok {
		t.Fatal("parseUUIDParam should accept valid uuid")
	}
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseUUIDParam_Invalid(t *testing.T) {
	c, rec := newCtx(http.MethodGet, "/v1/documents/garbage", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: "garbage"}}
	_, ok := parseUUIDParam(c, "id")
	if ok {
		t.Fatal("parseUUIDParam should reject non-uuid")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestEditDocument_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodPost, "/v1/documents/X/edit", []byte("{}"), nil)
	EditDocument(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestEditDocument_RejectsBadUUIDInPath(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	body := []byte(`{"ops":[{"type":"page.rotate","page":1,"rotation":90}]}`)
	c, rec := newCtx(http.MethodPost, "/v1/documents/garbage/edit", body, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{{Key: "id", Value: "garbage"}}
	EditDocument(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestEditDocument_RejectsMalformedJSON(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodPost, "/v1/documents/"+docID.String()+"/edit",
		[]byte("{not json"), map[string]string{
			"X-User-ID": uid.String(),
		})
	c.Params = gin.Params{{Key: "id", Value: docID.String()}}
	EditDocument(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed JSON", rec.Code)
	}
}
