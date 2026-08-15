package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dp2strm/pkg/models"
)

// DefaultConfig returns safe default configuration parameters.
func DefaultConfig() *models.Config {
	return &models.Config{
		DispatcharrURL:        "http://localhost:8000",
		APIKey:                "",
		XCUser:                "username",
		XCPassword:            "password",
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
		PageSize:              100,
		PageWorkers:           5,
		EmbyURL:               "",
		EmbyAPIKey:            "",
		EmbyMediaPath:         "",
		Ordering:              "-created_at",
		EnableNFO:             false,
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
	if v := os.Getenv("DISPATCHARR_XC_USER"); v != "" {
		cfg.XCUser = v
	}
	if v := os.Getenv("DISPATCHARR_XC_PASSWORD"); v != "" {
		cfg.XCPassword = v
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
	urlEnvSet := false
	if v := os.Getenv("URL"); v != "" {
		cfg.URL = v
		urlEnvSet = true
	}
	if v := os.Getenv("EMBY_URL"); v != "" {
		cfg.EmbyURL = v
	}
	if v := os.Getenv("EMBY_API_KEY"); v != "" {
		cfg.EmbyAPIKey = v
	}
	if v := os.Getenv("EMBY_MEDIA_PATH"); v != "" {
		cfg.EmbyMediaPath = v
	}
	if v := os.Getenv("SYNC_CRON"); v != "" {
		cfg.SyncCron = v
	}
	if v := os.Getenv("TZ"); v != "" {
		cfg.TZ = v
	}
	if v := os.Getenv("DISPATCHARR_ORDERING"); v != "" {
		cfg.Ordering = v
	}
	if v := os.Getenv("SKIP_STARTUP_SYNC"); v != "" {
		cfg.SkipStartupSync = (v == "true" || v == "1")
	}

	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			cfg.Port = p
			if !urlEnvSet {
				cfg.URL = fmt.Sprintf("http://localhost:%d", p)
			}
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
	if v := os.Getenv("PAGE_SIZE"); v != "" {
		if ps, err := strconv.Atoi(v); err == nil && ps > 0 {
			cfg.PageSize = ps
		}
	}
	if v := os.Getenv("PAGE_WORKERS"); v != "" {
		if pw, err := strconv.Atoi(v); err == nil && pw > 0 {
			cfg.PageWorkers = pw
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
	if v := os.Getenv("ENABLE_NFO"); v != "" {
		cfg.EnableNFO = (v == "true" || v == "1")
	} else if v := os.Getenv("GENERATE_NFO"); v != "" {
		cfg.EnableNFO = (v == "true" || v == "1")
	}

	// Clean output directory path
	if absDir, err := filepath.Abs(cfg.OutputDir); err == nil {
		cfg.OutputDir = absDir
	}

	// Normalize URL: Ensure port is appended if missing and non-standard (e.g. "http://dp2strm" -> "http://dp2strm:8080")
	cfg.URL = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if parsed, err := url.Parse(cfg.URL); err == nil && parsed.Host != "" {
		if parsed.Port() == "" && cfg.Port != 80 && cfg.Port != 443 {
			if !strings.Contains(parsed.Host, ":") {
				parsed.Host = fmt.Sprintf("%s:%d", parsed.Host, cfg.Port)
				cfg.URL = strings.TrimRight(parsed.String(), "/")
			}
		}
	}

	return cfg
}
