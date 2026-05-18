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
	if len(pool) > 0 && pool[0].MaxOpenConns > 0 {
		cfg.MaxOpenConns = pool[0].MaxOpenConns
	}
	dsn = config.ApplyPostgresDSNDefaults(dsn)
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		slog.Error("notify-service: connect failed", "error", err)
		os.Exit(1)
	}
	sqlDB, err := DB.DB()
	if err != nil {
		slog.Error("notify-service: handle failed", "error", err)
		os.Exit(1)
	}
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		slog.Error("notify-service: ping failed", "error", err)
		os.Exit(1)
	}
	slog.Info("notify-service: DB connected")
}

func Migrate() {
	if err := DB.AutoMigrate(&Delivery{}, &WebhookSubscription{}); err != nil {
		slog.Error("notify-service: migrate failed", "error", err)
		os.Exit(1)
	}
	slog.Info("notify-service: migration done")
}
