package handlers

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSnapshotStorageKey_Layout(t *testing.T) {
	owner := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	doc := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	rev := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	got := snapshotStorageKey(owner, doc, rev)
	wantParts := []string{
		"users", owner.String(),
		"docs", doc.String(),
		"snapshots", rev.String() + ".yjs",
	}
	for _, p := range wantParts {
		if !strings.Contains(got, p) {
			t.Errorf("snapshotStorageKey missing %q in result %q", p, got)
		}
	}
	if !strings.HasSuffix(got, ".yjs") {
		t.Errorf("snapshotStorageKey = %q, want trailing .yjs extension", got)
	}
}

func TestSnapshotStorageKey_KeepsOwnerScope(t *testing.T) {
	// The owner-prefix is what isolates one tenant's snapshots
	// from another. Different owners must produce different
	// top-level directories even when docID + revID match (a
	// theoretical UUIDv7 collision).
	docA, _ := uuid.NewV7()
	revA, _ := uuid.NewV7()
	o1, _ := uuid.NewV7()
	o2, _ := uuid.NewV7()
	k1 := snapshotStorageKey(o1, docA, revA)
	k2 := snapshotStorageKey(o2, docA, revA)
	if k1 == k2 {
		t.Errorf("different owners produced same key: %q", k1)
	}
}
