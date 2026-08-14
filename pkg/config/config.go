package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dp2strm/pkg/models"
	"gopkg.in/yaml.v3"
)

// DefaultConfig returns safe default configuration parameters.
func DefaultConfig() *models.Config {
	return &models.Config{
		DispatcharrURL:        "http://localhost:8000",
		APIKey:                "",
		OutputDir:             "/data/strm",
		RetentionGraceDays:    7,
		ProxyPort:             8080,
		ProxyPublicURL:        "http://localhost:8080",
		SyncInterval:          0, // 0 = run once by default
		BatchSize:             0, // 0 = unlimited
		CircuitBreakerEnabled: true,
		MaxProbeConcurrency:   5,
		EmbyURL:               "",
		EmbyAPIKey:            "",
	}
}

// LoadConfig reads configuration from a YAML file (if present) and applies environment variable overrides.
func LoadConfig(path string) (*models.Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			_ = yaml.Unmarshal(data, cfg)
		}
	}

	// Environment Variable Parsing (Cloud-Native Configless Mode)
	if v := os.Getenv("DISPATCHARR_URL"); v != "" {
		cfg.DispatcharrURL = v
	}
	if v := os.Getenv("DISPATCHARR_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("OUTPUT_DIR"); v != "" {
		cfg.OutputDir = v
	}
	if v := os.Getenv("PROXY_PUBLIC_URL"); v != "" {
		cfg.ProxyPublicURL = v
	}
	if v := os.Getenv("EMBY_URL"); v != "" {
		cfg.EmbyURL = v
	}
	if v := os.Getenv("EMBY_API_KEY"); v != "" {
		cfg.EmbyAPIKey = v
	}

	if v := os.Getenv("PROXY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			cfg.ProxyPort = p
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

	// Ensure output directory path is cleaned
	if absDir, err := filepath.Abs(cfg.OutputDir); err == nil {
		cfg.OutputDir = absDir
	}

	return cfg, nil
}
