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

	defer func() {
		os.Unsetenv("DISPATCHARR_URL")
		os.Unsetenv("DISPATCHARR_API_KEY")
		os.Unsetenv("OUTPUT_DIR")
		os.Unsetenv("PORT")
		os.Unsetenv("SYNC_INTERVAL")
		os.Unsetenv("EMBY_URL")
		os.Unsetenv("EMBY_API_KEY")
		os.Unsetenv("STREAMCACHE_URL")
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
}

func TestURLPortNormalization(t *testing.T) {
	os.Setenv("URL", "http://dp2strm")
	os.Setenv("PORT", "9199")
	defer func() {
		os.Unsetenv("URL")
		os.Unsetenv("PORT")
	}()

	cfg := LoadConfig()
	want := "http://dp2strm:9199"
	if cfg.URL != want {
		t.Errorf("URL = %q; want %q", cfg.URL, want)
	}
}
