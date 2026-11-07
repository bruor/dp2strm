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
	os.Setenv("STREAMCACHE_DISPATCHARR_URL", "https://dp-public.example.com")

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
	if cfg.StreamcacheDispatcharrURL != "https://dp-public.example.com" {
		t.Errorf("StreamcacheDispatcharrURL = %q; want %q", cfg.StreamcacheDispatcharrURL, "https://dp-public.example.com")
	}
	if cfg.GetDispatcharrStreamURL() != "https://dp-public.example.com" {
		t.Errorf("GetDispatcharrStreamURL() = %q; want %q", cfg.GetDispatcharrStreamURL(), "https://dp-public.example.com")
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

func TestActiveSeriesRefreshIntervalConfig(t *testing.T) {
	// 1. Default should be 4 hours
	cfgDefault := DefaultConfig()
	if cfgDefault.ActiveSeriesRefreshInterval != 4*time.Hour {
		t.Errorf("Default ActiveSeriesRefreshInterval = %v, want 4h", cfgDefault.ActiveSeriesRefreshInterval)
	}

	// 2. Custom duration string (e.g. 6h)
	os.Setenv("ACTIVE_SERIES_REFRESH_INTERVAL", "6h")
	cfgParsed := LoadConfig()
	if cfgParsed.ActiveSeriesRefreshInterval != 6*time.Hour {
		t.Errorf("ActiveSeriesRefreshInterval = %v, want 6h", cfgParsed.ActiveSeriesRefreshInterval)
	}

	// 3. Integer seconds (e.g. 7200)
	os.Setenv("ACTIVE_SERIES_REFRESH_INTERVAL", "7200")
	cfgSec := LoadConfig()
	if cfgSec.ActiveSeriesRefreshInterval != 2*time.Hour {
		t.Errorf("ActiveSeriesRefreshInterval from integer = %v, want 2h", cfgSec.ActiveSeriesRefreshInterval)
	}

	os.Unsetenv("ACTIVE_SERIES_REFRESH_INTERVAL")
}

func TestAdultContentConfig(t *testing.T) {
	// 1. Default should be "disabled" and have default filter terms
	cfgDefault := DefaultConfig()
	if cfgDefault.AdultContent != "disabled" {
		t.Errorf("Default AdultContent = %q, want 'disabled'", cfgDefault.AdultContent)
	}
	if !cfgDefault.IsAdultContentDisabled() {
		t.Errorf("Default IsAdultContentDisabled() = false, want true")
	}
	expectedFilter := []string{"18+", "adult", "porn", "xxx", "nutflix"}
	if len(cfgDefault.AdultGroupFilter) != len(expectedFilter) {
		t.Fatalf("Default AdultGroupFilter len = %d, want %d", len(cfgDefault.AdultGroupFilter), len(expectedFilter))
	}
	for i, f := range expectedFilter {
		if cfgDefault.AdultGroupFilter[i] != f {
			t.Errorf("Default AdultGroupFilter[%d] = %q, want %q", i, cfgDefault.AdultGroupFilter[i], f)
		}
	}

	// 2. ADULT_CONTENT=enabled
	os.Setenv("ADULT_CONTENT", "enabled")
	cfgEnabled := LoadConfig()
	if cfgEnabled.AdultContent != "enabled" {
		t.Errorf("AdultContent = %q, want 'enabled'", cfgEnabled.AdultContent)
	}
	if cfgEnabled.IsAdultContentDisabled() {
		t.Errorf("IsAdultContentDisabled() = true for enabled, want false")
	}

	// 3. ADULT_CONTENT=true synonym
	os.Setenv("ADULT_CONTENT", "true")
	cfgTrue := LoadConfig()
	if cfgTrue.IsAdultContentDisabled() {
		t.Errorf("IsAdultContentDisabled() = true for 'true', want false")
	}

	// 4. ADULT_CONTENT=disabled / false synonym
	os.Setenv("ADULT_CONTENT", "disabled")
	cfgDisabled := LoadConfig()
	if !cfgDisabled.IsAdultContentDisabled() {
		t.Errorf("IsAdultContentDisabled() = false for 'disabled', want true")
	}

	os.Setenv("ADULT_CONTENT", "false")
	cfgFalse := LoadConfig()
	if !cfgFalse.IsAdultContentDisabled() {
		t.Errorf("IsAdultContentDisabled() = false for 'false', want true")
	}

	// 5. Custom ADULT_GROUP_FILTER
	os.Setenv("ADULT_GROUP_FILTER", "18+, Mature , Erotica, XXX ")
	cfgFilter := LoadConfig()
	expectedCustom := []string{"18+", "mature", "erotica", "xxx"}
	if len(cfgFilter.AdultGroupFilter) != len(expectedCustom) {
		t.Fatalf("Custom AdultGroupFilter len = %d, want %d", len(cfgFilter.AdultGroupFilter), len(expectedCustom))
	}
	for i, f := range expectedCustom {
		if cfgFilter.AdultGroupFilter[i] != f {
			t.Errorf("Custom AdultGroupFilter[%d] = %q, want %q", i, cfgFilter.AdultGroupFilter[i], f)
		}
	}

	os.Unsetenv("ADULT_CONTENT")
	os.Unsetenv("ADULT_GROUP_FILTER")
}

func TestPrefetchCountConfiguration(t *testing.T) {
	// 1. Default with StreamcacheURL set -> 1
	os.Setenv("STREAMCACHE_URL", "http://streamcache:8080")
	cfg := LoadConfig()
	if cfg.FetchAheadCount != 1 {
		t.Errorf("FetchAheadCount with StreamcacheURL = %d, want 1", cfg.FetchAheadCount)
	}

	// 2. Explicit PREFETCH_COUNT overrides default
	os.Setenv("PREFETCH_COUNT", "3")
	cfg2 := LoadConfig()
	if cfg2.FetchAheadCount != 3 {
		t.Errorf("FetchAheadCount with PREFETCH_COUNT=3 = %d, want 3", cfg2.FetchAheadCount)
	}

	// 3. Explicit PREFETCH_COUNT=0 disables fetch ahead
	os.Setenv("PREFETCH_COUNT", "0")
	cfg3 := LoadConfig()
	if cfg3.FetchAheadCount != 0 {
		t.Errorf("FetchAheadCount with PREFETCH_COUNT=0 = %d, want 0", cfg3.FetchAheadCount)
	}

	// 4. Fallback to FETCH_AHEAD_COUNT if PREFETCH_COUNT not set
	os.Unsetenv("PREFETCH_COUNT")
	os.Setenv("FETCH_AHEAD_COUNT", "2")
	cfg4 := LoadConfig()
	if cfg4.FetchAheadCount != 2 {
		t.Errorf("FetchAheadCount with FETCH_AHEAD_COUNT=2 = %d, want 2", cfg4.FetchAheadCount)
	}

	os.Unsetenv("STREAMCACHE_URL")
	os.Unsetenv("FETCH_AHEAD_COUNT")
}


