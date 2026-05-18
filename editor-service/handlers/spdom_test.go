package handlers

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestResolveStoragePath_StorageDirUnset(t *testing.T) {
	// When StorageDir is empty (legacy GetSPDOM behavior used in tests),
	// resolveStoragePath returns the cleaned relative key.
	prev := StorageDir
	SetStorageDir("")
	t.Cleanup(func() { SetStorageDir(prev) })

	cases := []struct {
		name     string
		key      string
		wantPath string
		wantErr  bool
	}{
		{"simple relative", "users/abc/jobs/123/output/doc.pdf", "users/abc/jobs/123/output/doc.pdf", false},
		{"empty", "", "", true},
		{"whitespace only", "   ", "", true},
		{"absolute", "/etc/passwd", "", true},
		{"absolute under files", "/files/users/abc/doc.pdf", "", true},
		{"parent escape", "../etc/passwd", "", true},
		{"parent escape after clean", "users/../../etc/passwd", "", true},
		{"trims whitespace", "  doc.pdf  ", "doc.pdf", false},
		{"nested dots cleaned", "users/abc/./jobs/doc.pdf", "users/abc/jobs/doc.pdf", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotErr := resolveStoragePath(tc.key)
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v (path=%q)", gotErr, tc.wantErr, gotPath)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}

func TestResolveStoragePath_StorageDirSetJoinsAbsolute(t *testing.T) {
	prev := StorageDir
	SetStorageDir("/tmp/fyredocs-test")
	t.Cleanup(func() { SetStorageDir(prev) })

	got, err := resolveStoragePath("users/abc/doc.pdf")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := "/tmp/fyredocs-test/users/abc/doc.pdf"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveStoragePath_StorageDirSetRefusesEscape(t *testing.T) {
	prev := StorageDir
	SetStorageDir("/tmp/fyredocs-test")
	t.Cleanup(func() { SetStorageDir(prev) })

	if _, err := resolveStoragePath("../etc/passwd"); err == nil {
		t.Error("expected error for path traversal under configured StorageDir")
	}
}

// TestGetSPDOM_RejectsUnauthenticated guarantees the handler short-circuits
// before any storage / DB lookups when there's no auth context.
func TestGetSPDOM_RejectsUnauthenticated(t *testing.T) {
	c, rec := newCtx(http.MethodGet, "/v1/documents/X/spdom", nil, nil)
	c.Params = gin.Params{{Key: "id", Value: uuid.NewString()}}
	GetSPDOM(c)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestGetSPDOM_RejectsBadUUID guarantees the handler rejects a non-UUID
// path parameter before any DB lookup. This protects the DB from a buggy
// gateway forwarding garbage.
func TestGetSPDOM_RejectsBadUUID(t *testing.T) {
	uid := uuid.Must(uuid.NewV7())
	c, rec := newCtx(http.MethodGet, "/v1/documents/garbage/spdom", nil, map[string]string{
		"X-User-ID": uid.String(),
	})
	c.Params = gin.Params{{Key: "id", Value: "garbage"}}
	GetSPDOM(c)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
