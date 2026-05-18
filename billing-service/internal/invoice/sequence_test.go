package invoice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"billing-service/internal/models"
)

func setupSeqDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Each test gets its own shared-cache in-memory DB by
	// keying the filename on t.Name(). `mode=memory` keeps
	// the bytes off disk; `cache=shared` lets multiple gorm
	// connections see the same database (the default
	// `:memory:` gives each connection its own isolated DB,
	// which breaks the concurrent test where goroutines
	// land on different pool slots). The per-test prefix
	// prevents state from leaking BETWEEN tests in the same
	// package run.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Limit to 1 connection so writes serialise — sqlite is
	// single-writer anyway, and this avoids "database is
	// locked" under the concurrent test below.
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.InvoiceSequence{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestNextNumber_StartsAtOne(t *testing.T) {
	db := setupSeqDB(t)
	got, err := NextNumber(context.Background(), db, "FYR", "2026")
	if err != nil {
		t.Fatalf("NextNumber: %v", err)
	}
	if got != "FYR-2026-0001" {
		t.Errorf("got %q, want FYR-2026-0001", got)
	}
}

func TestNextNumber_IncrementsMonotonically(t *testing.T) {
	db := setupSeqDB(t)
	want := []string{"FYR-2026-0001", "FYR-2026-0002", "FYR-2026-0003"}
	for i, w := range want {
		got, err := NextNumber(context.Background(), db, "FYR", "2026")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if got != w {
			t.Errorf("call %d: got %q, want %q", i, got, w)
		}
	}
}

func TestNextNumber_DifferentPeriodsHaveSeparateCounters(t *testing.T) {
	// `(FYR, 2025)` and `(FYR, 2026)` are independent rows
	// — pin the contract so a year-roll-over doesn't reuse
	// last year's numbers.
	db := setupSeqDB(t)
	a1, _ := NextNumber(context.Background(), db, "FYR", "2025")
	a2, _ := NextNumber(context.Background(), db, "FYR", "2025")
	b1, _ := NextNumber(context.Background(), db, "FYR", "2026")
	if a1 != "FYR-2025-0001" || a2 != "FYR-2025-0002" {
		t.Errorf("2025 sequence: %q, %q", a1, a2)
	}
	if b1 != "FYR-2026-0001" {
		t.Errorf("2026 sequence didn't restart: %q", b1)
	}
}

func TestNextNumber_DifferentPrefixesHaveSeparateCounters(t *testing.T) {
	// `(FYR, 2026)` and `(MKT, 2026)` are independent — the
	// marketplace and subscriptions can both run their own
	// numbering schemes without colliding.
	db := setupSeqDB(t)
	a, _ := NextNumber(context.Background(), db, "FYR", "2026")
	b, _ := NextNumber(context.Background(), db, "MKT", "2026")
	if a != "FYR-2026-0001" || b != "MKT-2026-0001" {
		t.Errorf("prefixes collided: %q, %q", a, b)
	}
}

func TestNextNumber_ZeroPadsToFourDigitsThenGrows(t *testing.T) {
	// Pre-seed the counter to 9998 so we can observe the
	// transition from 4-digit zero-padded to 5-digit
	// natural-width without driving 10000 sequential calls.
	db := setupSeqDB(t)
	if err := db.Create(&models.InvoiceSequence{
		Prefix:  "FYR",
		Period:  "2026",
		NextSeq: 9998,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First call after seeding: the UPSERT's UPDATE branch
	// bumps NextSeq to 9999 and returns it.
	got, _ := NextNumber(context.Background(), db, "FYR", "2026")
	if got != "FYR-2026-9999" {
		t.Errorf("got %q, want FYR-2026-9999", got)
	}
	// Next call: 10000 — still %04d-formatted but no
	// truncation; the directive widens naturally.
	got, _ = NextNumber(context.Background(), db, "FYR", "2026")
	if got != "FYR-2026-10000" {
		t.Errorf("got %q, want FYR-2026-10000", got)
	}
}

func TestNextNumber_RejectsEmptyPrefix(t *testing.T) {
	db := setupSeqDB(t)
	_, err := NextNumber(context.Background(), db, "  ", "2026")
	if !errors.Is(err, ErrEmptyPrefix) {
		t.Errorf("expected ErrEmptyPrefix; got %v", err)
	}
}

func TestNextNumber_RejectsEmptyPeriod(t *testing.T) {
	db := setupSeqDB(t)
	_, err := NextNumber(context.Background(), db, "FYR", "")
	if !errors.Is(err, ErrEmptyPeriod) {
		t.Errorf("expected ErrEmptyPeriod; got %v", err)
	}
}

func TestNextNumber_RejectsNilDB(t *testing.T) {
	_, err := NextNumber(context.Background(), nil, "FYR", "2026")
	if err == nil {
		t.Error("expected error on nil db")
	}
}

func TestNextNumber_ConcurrentCallsAllocateDistinctValues(t *testing.T) {
	// Concurrency invariant: N parallel calls produce N
	// distinct numbers — the UPSERT serialises on the row
	// lock. We're on sqlite-in-memory which has a single
	// writer; the test exercises the contract rather than
	// proving Postgres-level concurrency.
	db := setupSeqDB(t)
	const n = 50
	var wg sync.WaitGroup
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := NextNumber(context.Background(), db, "FYR", "2026")
			if err != nil {
				t.Errorf("concurrent NextNumber: %v", err)
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(results)

	seen := map[string]struct{}{}
	for r := range results {
		if _, dup := seen[r]; dup {
			t.Errorf("duplicate number issued: %q", r)
		}
		seen[r] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("expected %d distinct numbers; got %d", n, len(seen))
	}
}
