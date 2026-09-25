package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfigFromEnvironment(t *testing.T) {
	// Set environment variables
	os.Setenv("DISPATCHARR_URL", "https://dp.example.com")
	os.Setenv("DISPATCHARR_API_KEY", "test-api-key-123")
	os.Setenv("OUTPUT_DIR", "/custom/data/strm")
	os.Setenv("PORT", "9090")
	os.Setenv("SYNC_INTERVAL", "2h")
	os.Setenv("EMBY_URL", "http://emby.example.com")
	os.Setenv("EMBY_API_KEY", "emby-token-456")
	os.Setenv("STREAMCACHE_URL", "http://streamcache.local:8080")
	os.Setenv("STREAMCACHE_DISPATCHARR_URL", "https://dp.example.com")

	defer func() {
		os.Unsetenv("DISPATCHARR_URL")
		os.Unsetenv("DISPATCHARR_API_KEY")
		os.Unsetenv("OUTPUT_DIR")
		os.Unsetenv("PORT")
		os.Unsetenv("SYNC_INTERVAL")
		os.Unsetenv("EMBY_URL")
		os.Unsetenv("EMBY_API_KEY")
		os.Unsetenv("STREAMCACHE_URL")
		os.Unsetenv("STREAMCACHE_DISPATCHARR_URL")
	}()

	// Load config strictly from environment variables
	cfg := LoadConfig()

	if cfg.DispatcharrURL != "https://dp.example.com" {
		t.Errorf("DispatcharrURL = %q; want %q", cfg.DispatcharrURL, "https://dp.example.com")
	}
	if cfg.APIKey != "test-api-key-123" {
		t.Errorf("APIKey = %q; want %q", cfg.APIKey, "test-api-key-123")
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d; want 9090", cfg.Port)
	}
	if cfg.SyncInterval != 2*time.Hour {
		t.Errorf("SyncInterval = %v; want %v", cfg.SyncInterval, 2*time.Hour)
	}
	if cfg.EmbyURL != "http://emby.example.com" {
		t.Errorf("EmbyURL = %q; want %q", cfg.EmbyURL, "http://emby.example.com")
	}
	if cfg.StreamcacheURL != "http://streamcache.local:8080" {
		t.Errorf("StreamcacheURL = %q; want %q", cfg.StreamcacheURL, "http://streamcache.local:8080")
	}
	if cfg.StreamcacheDispatcharrURL != "https://dp.example.com" {
		t.Errorf("StreamcacheDispatcharrURL = %q; want %q", cfg.StreamcacheDispatcharrURL, "https://dp.example.com")
	}
	if cfg.GetDispatcharrStreamURL() != "https://dp.example.com" {
		t.Errorf("GetDispatcharrStreamURL() = %q; want %q", cfg.GetDispatcharrStreamURL(), "https://dp.example.com")
	}
}

func TestURLPortNormalization(t *testing.T) {
	// Case 1: Hostname without port and non-standard PORT
	os.Setenv("URL", "http://dp2strm")
	os.Setenv("PORT", "9199")
	cfg := LoadConfig()
	want := "http://dp2strm:9199"
	if cfg.URL != want {
		t.Errorf("URL = %q; want %q", cfg.URL, want)
	}

	// Case 2: URL with trailing slash and custom PORT
	os.Setenv("URL", "http://dp2strm/")
	os.Setenv("PORT", "9199")
	cfg = LoadConfig()
	if cfg.URL != want {
		t.Errorf("URL with trailing slash = %q; want %q", cfg.URL, want)
	}

	// Case 3: PORT set without URL env var
	os.Unsetenv("URL")
	os.Setenv("PORT", "9199")
	cfg = LoadConfig()
	wantPortOnly := "http://localhost:9199"
	if cfg.URL != wantPortOnly {
		t.Errorf("URL without URL env = %q; want %q", cfg.URL, wantPortOnly)
	}

	os.Unsetenv("PORT")
}

func TestPrefetchDelayConfig(t *testing.T) {
	// 1. Default should be 5 minutes
	cfgDefault := DefaultConfig()
	if cfgDefault.PrefetchDelay != 5*time.Minute {
		t.Errorf("Default PrefetchDelay = %v, want 5m", cfgDefault.PrefetchDelay)
	}

	// 2. Custom duration string (e.g. 2m30s)
	os.Setenv("PREFETCH_DELAY", "2m30s")
	cfgParsed := LoadConfig()
	if cfgParsed.PrefetchDelay != 150*time.Second {
		t.Errorf("PrefetchDelay = %v, want 2m30s", cfgParsed.PrefetchDelay)
	}

	// 3. Integer seconds (e.g. 60)
	os.Setenv("PREFETCH_DELAY", "60")
	cfgSec := LoadConfig()
	if cfgSec.PrefetchDelay != 60*time.Second {
		t.Errorf("PrefetchDelay from integer = %v, want 1m0s", cfgSec.PrefetchDelay)
	}

	os.Unsetenv("PREFETCH_DELAY")
}
