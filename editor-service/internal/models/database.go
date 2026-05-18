package models

import (
	"context"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"fyredocs/shared/config"
)

// DB is the package-scoped GORM handle. Each service holds its own DB
// reference per CLAUDE.md §3 (no cross-service DB access).
var DB *gorm.DB

type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefaultPoolConfig is tuned for managed Postgres pools (Neon, RDS Proxy,
// pgbouncer) that close idle TCP connections server-side. Editor reads/writes
// are bursty around active editing sessions, so we size the pool slightly
// larger than auth-service's.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 2 * time.Minute,
	}
}

func Connect(pool ...PoolConfig) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL is not set")
		os.Exit(1)
	}

	cfg := DefaultPoolConfig()
	if len(pool) > 0 {
		if pool[0].MaxOpenConns > 0 {
			cfg.MaxOpenConns = pool[0].MaxOpenConns
		}
		if pool[0].MaxIdleConns > 0 {
			cfg.MaxIdleConns = pool[0].MaxIdleConns
		}
		if pool[0].ConnMaxLifetime > 0 {
			cfg.ConnMaxLifetime = pool[0].ConnMaxLifetime
		}
		if pool[0].ConnMaxIdleTime > 0 {
			cfg.ConnMaxIdleTime = pool[0].ConnMaxIdleTime
		}
	}

	dsn = config.ApplyPostgresDSNDefaults(dsn)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		slog.Error("Failed to create database handle", "error", err)
		os.Exit(1)
	}
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		slog.Error("Database ping failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Database connection established")
}

func Migrate() {
	if err := DB.AutoMigrate(
		&Document{},
		&Revision{},
		&Comment{},
	); err != nil {
		slog.Error("Database migration failed", "error", err)
		os.Exit(1)
	}

	compositeIndexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_documents_owner_updated ON documents (owner_user_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_revisions_document_created ON revisions (document_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_comments_document_resolved ON comments (document_id, resolved)`,
	}
	for _, idx := range compositeIndexes {
		if err := DB.Exec(idx).Error; err != nil {
			slog.Warn("Failed to create composite index", "error", err, "sql", idx)
		}
	}

	// Enforce known status values so a misbehaving caller can't write
	// arbitrary strings that break downstream filters.
	checkConstraints := []string{
		`DO $$ BEGIN ALTER TABLE documents ADD CONSTRAINT chk_document_status CHECK (status IN ('ready','locked','quarantined','deleted')); EXCEPTION WHEN duplicate_object THEN NULL; END $$`,
	}
	for _, chk := range checkConstraints {
		if err := DB.Exec(chk).Error; err != nil {
			slog.Warn("Failed to add check constraint", "error", err)
		}
	}

	slog.Info("Database migration completed")
}
