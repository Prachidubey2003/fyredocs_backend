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

// DB is the shared GORM handle. Initialised by Connect; handlers
// and tests use it directly (sqlite swap in test setup).
var DB *gorm.DB

type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    10,
		MaxIdleConns:    5,
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
	}
	dsn = config.ApplyPostgresDSNDefaults(dsn)

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		slog.Error("billing-service: connect failed", "error", err)
		os.Exit(1)
	}
	sqlDB, err := DB.DB()
	if err != nil {
		slog.Error("billing-service: handle failed", "error", err)
		os.Exit(1)
	}
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		slog.Error("billing-service: ping failed", "error", err)
		os.Exit(1)
	}
	slog.Info("billing-service: DB connected")
}

func Migrate() {
	if err := DB.AutoMigrate(&Subscription{}, &ProcessedStripeEvent{}, &RevshareEntry{}, &InvoiceSequence{}); err != nil {
		slog.Error("billing-service: migrate failed", "error", err)
		os.Exit(1)
	}
	slog.Info("billing-service: migration done")
}
