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
		StreamcacheURL:            "",
		StreamcacheDispatcharrURL: "",
		FetchAheadCount:           0,
		PrefetchDelay:             5 * time.Minute,
		RetentionGraceDays:        7,
		Port:                  8080,
		URL:                   "http://localhost:8080",
		SyncInterval:          4 * time.Hour,
		SyncCron:              "",
		ReconcileInterval:     24 * time.Hour,
		ReconcileCron:         "",
		TZ:                    "UTC",
		BatchSize:             0, // 0 = unlimited
		CircuitBreakerEnabled: true,
		MaxProbeConcurrency:   5,
		PageSize:              100,
		PageWorkers:           1,
		DispatcharrWorkers:    1,
		EmbyWorkers:           1,
		EmbyPaceDelay:         0,
		EmbyBatchSize:         25,
		EmbyBatchIdleDelay:    10 * time.Second,
		EmbyURL:               "",
		EmbyAPIKey:            "",
		EmbyMediaPath:         "",
		EmbyTVLibrary:         "",
		EmbyMoviesLibrary:     "",
		Ordering:               "-created_at",
		EnableNFO:              false,
		ActivationRefreshDelay: 10 * time.Second,
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
	if v := os.Getenv("STREAMCACHE_DISPATCHARR_URL"); v != "" {
		cfg.StreamcacheDispatcharrURL = v
	} else if v := os.Getenv("DISPATCHARR_STREAM_URL"); v != "" {
		cfg.StreamcacheDispatcharrURL = v
	}
	if v := os.Getenv("FETCH_AHEAD_COUNT"); v != "" {
		if cnt, err := strconv.Atoi(v); err == nil && cnt >= 0 {
			cfg.FetchAheadCount = cnt
		}
	}
	if v := os.Getenv("PREFETCH_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.PrefetchDelay = d
		} else if sec, err := strconv.Atoi(v); err == nil && sec >= 0 {
			cfg.PrefetchDelay = time.Duration(sec) * time.Second
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
	if v := os.Getenv("EMBY_TV_LIBRARY"); v != "" {
		cfg.EmbyTVLibrary = v
	}
	if v := os.Getenv("EMBY_MOVIES_LIBRARY"); v != "" {
		cfg.EmbyMoviesLibrary = v
	}
	if v := os.Getenv("SYNC_CRON"); v != "" {
		cfg.SyncCron = v
	}
	if v := os.Getenv("RECONCILE_CRON"); v != "" {
		cfg.ReconcileCron = v
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
	if v := os.Getenv("DISPATCHARR_WORKERS"); v != "" {
		if dw, err := strconv.Atoi(v); err == nil && dw > 0 {
			cfg.DispatcharrWorkers = dw
			cfg.PageWorkers = dw
		}
	} else if v := os.Getenv("PAGE_WORKERS"); v != "" {
		if pw, err := strconv.Atoi(v); err == nil && pw > 0 {
			cfg.PageWorkers = pw
			cfg.DispatcharrWorkers = pw
		}
	}
	if v := os.Getenv("EMBY_WORKERS"); v != "" {
		if ew, err := strconv.Atoi(v); err == nil && ew > 0 {
			cfg.EmbyWorkers = ew
		}
	} else if v := os.Getenv("EMBY_CONCURRENCY"); v != "" {
		if ew, err := strconv.Atoi(v); err == nil && ew > 0 {
			cfg.EmbyWorkers = ew
		}
	}
	if v := os.Getenv("EMBY_PACE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.EmbyPaceDelay = d
		}
	}
	if v := os.Getenv("EMBY_BATCH_SIZE"); v != "" {
		if bs, err := strconv.Atoi(v); err == nil && bs > 0 {
			cfg.EmbyBatchSize = bs
		}
	}
	if v := os.Getenv("EMBY_BATCH_IDLE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.EmbyBatchIdleDelay = d
		}
	}
	if v := os.Getenv("SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.SyncInterval = d
		}
	}
	if v := os.Getenv("RECONCILE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ReconcileInterval = d
		}
	} else if v := os.Getenv("RECONCILER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ReconcileInterval = d
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
	if v := os.Getenv("ACTIVATION_REFRESH_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ActivationRefreshDelay = d
		} else if sec, err := strconv.Atoi(v); err == nil && sec >= 0 {
			cfg.ActivationRefreshDelay = time.Duration(sec) * time.Second
		}
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
