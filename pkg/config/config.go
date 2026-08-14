package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dp2strm/pkg/models"
)

// DefaultConfig returns safe default configuration parameters.
func DefaultConfig() *models.Config {
	return &models.Config{
		DispatcharrURL:        "http://localhost:8000",
		APIKey:                "",
		OutputDir:             "/data/strm",
		StreamcacheURL:        "",
		FetchAheadCount:       2,
		RetentionGraceDays:    7,
		Port:                  8080,
		URL:                   "http://localhost:8080",
		SyncInterval:          0, // 0 = run once by default
		SyncCron:              "",
		TZ:                    "UTC",
		BatchSize:             0, // 0 = unlimited
		CircuitBreakerEnabled: true,
		MaxProbeConcurrency:   5,
		EmbyURL:               "",
		EmbyAPIKey:            "",
	}
}

// LoadConfig loads configuration strictly from environment variables.
func LoadConfig() *models.Config {
	cfg := DefaultConfig()

	// Environment Variable Parsing (Strict Configless Mode)
	if v := os.Getenv("DISPATCHARR_URL"); v != "" {
		cfg.DispatcharrURL = v
	}
	if v := os.Getenv("DISPATCHARR_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("OUTPUT_DIR"); v != "" {
		cfg.OutputDir = v
	}
	if v := os.Getenv("STREAMCACHE_URL"); v != "" {
		cfg.StreamcacheURL = v
	}
	if v := os.Getenv("FETCH_AHEAD_COUNT"); v != "" {
		if cnt, err := strconv.Atoi(v); err == nil && cnt >= 0 {
			cfg.FetchAheadCount = cnt
		}
	}
	if v := os.Getenv("URL"); v != "" {
		cfg.URL = v
	}
	if v := os.Getenv("EMBY_URL"); v != "" {
		cfg.EmbyURL = v
	}
	if v := os.Getenv("EMBY_API_KEY"); v != "" {
		cfg.EmbyAPIKey = v
	}
	if v := os.Getenv("SYNC_CRON"); v != "" {
		cfg.SyncCron = v
	}
	if v := os.Getenv("TZ"); v != "" {
		cfg.TZ = v
	}

	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			cfg.Port = p
		}
	}
	if v := os.Getenv("RETENTION_GRACE_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			cfg.RetentionGraceDays = d
		}
	}
	if v := os.Getenv("BATCH_SIZE"); v != "" {
		if b, err := strconv.Atoi(v); err == nil {
			cfg.BatchSize = b
		}
	}
	if v := os.Getenv("MAX_PROBE_CONCURRENCY"); v != "" {
		if c, err := strconv.Atoi(v); err == nil && c > 0 {
			cfg.MaxProbeConcurrency = c
		}
	}
	if v := os.Getenv("SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.SyncInterval = d
		}
	}
	if v := os.Getenv("CIRCUIT_BREAKER_ENABLED"); v != "" {
		cfg.CircuitBreakerEnabled = (v == "true" || v == "1")
	}

	// Clean output directory path
	if absDir, err := filepath.Abs(cfg.OutputDir); err == nil {
		cfg.OutputDir = absDir
	}

	return cfg
}
