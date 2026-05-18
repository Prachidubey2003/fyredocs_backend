package models

import (
	"testing"

	"github.com/google/uuid"
)

func TestDocument_BeforeCreate_AssignsUUID(t *testing.T) {
	d := &Document{Title: "test", OwnerUserID: uuid.Must(uuid.NewV7()), StorageKey: "x"}
	if err := d.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if d.ID == uuid.Nil {
		t.Fatal("ID was not assigned")
	}
	if d.Status != "ready" {
		t.Errorf("Status: got %q, want %q", d.Status, "ready")
	}
}

func TestDocument_BeforeCreate_PreservesExistingValues(t *testing.T) {
	existingID := uuid.Must(uuid.NewV7())
	d := &Document{ID: existingID, Title: "x", Status: "locked"}
	if err := d.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if d.ID != existingID {
		t.Errorf("ID mutated: got %v, want %v", d.ID, existingID)
	}
	if d.Status != "locked" {
		t.Errorf("Status mutated: got %q, want %q", d.Status, "locked")
	}
}

func TestRevision_BeforeCreate_AssignsUUID(t *testing.T) {
	r := &Revision{DocumentID: uuid.Must(uuid.NewV7()), AuthorUserID: uuid.Must(uuid.NewV7())}
	if err := r.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if r.ID == uuid.Nil {
		t.Fatal("ID was not assigned")
	}
}

func TestComment_BeforeCreate_AssignsUUID(t *testing.T) {
	c := &Comment{
		DocumentID:   uuid.Must(uuid.NewV7()),
		RevID:        uuid.Must(uuid.NewV7()),
		AuthorUserID: uuid.Must(uuid.NewV7()),
		Body:         "test",
	}
	if err := c.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if c.ID == uuid.Nil {
		t.Fatal("ID was not assigned")
	}
}

func TestDefaultPoolConfig(t *testing.T) {
	c := DefaultPoolConfig()
	if c.MaxOpenConns < c.MaxIdleConns {
		t.Errorf("MaxOpenConns (%d) must be >= MaxIdleConns (%d)", c.MaxOpenConns, c.MaxIdleConns)
	}
	if c.ConnMaxLifetime <= 0 || c.ConnMaxIdleTime <= 0 {
		t.Error("lifetimes must be positive")
	}
}
