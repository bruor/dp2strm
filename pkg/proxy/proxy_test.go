package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dp2strm/pkg/db"
	"dp2strm/pkg/models"
)

func TestProxyServerExtensionlessAndQueryURL(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_proxy_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "proxy_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	// Seed test data in SQLite
	movieTarget := "https://dp.example.com/movie/agy/agypassword/1058902.mkv"
	movie4kTarget := "https://dp.example.com/movie/agy/agypassword/1652156.mkv"
	seriesTarget := "https://dp.example.com/series/agy/agypassword/438365.mkv"

	database.SaveStreamVariant(&db.StreamVariant{
		ItemID: 1058902, ItemType: "movie", VariantKey: "base", TargetURL: movieTarget, StreamID: "1058902",
	})
	database.SaveStreamVariant(&db.StreamVariant{
		ItemID: 1058902, ItemType: "movie", VariantKey: "4k", TargetURL: movie4kTarget, StreamID: "1652156",
	})
	database.SaveStreamVariant(&db.StreamVariant{
		ItemID: 30998, ItemType: "series", SeasonNum: 1, EpisodeNum: 1, VariantKey: "base", TargetURL: seriesTarget, StreamID: "438365",
	})

	cfg := &models.Config{
		DispatcharrURL:  "https://dp.example.com",
		StreamcacheURL:  "http://streamcache:8080",
		Port:            8080,
		FetchAheadCount: 0,
	}

	activatedSeries := make(chan int, 1)
	onActivation := func(sid int) {
		activatedSeries <- sid
	}

	srv := NewServer(cfg, database, onActivation)

	// 1. Test Extensionless Movie Base Playback with ?url= parameter
	req := httptest.NewRequest("GET", "/play/movie/1058902", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for movie play, got %d", rec.Code)
	}
	expectedQueryMovie := url.QueryEscape(movieTarget)
	expectedLocation := "http://streamcache:8080?url=" + expectedQueryMovie
	if loc := rec.Header().Get("Location"); loc != expectedLocation {
		t.Errorf("Expected 302 Location %s, got %s", expectedLocation, loc)
	}

	// 2. Test Extensionless Movie 4K Variant Playback with ?url= parameter
	req = httptest.NewRequest("GET", "/play/movie/1058902/4k", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for 4k movie play, got %d", rec.Code)
	}
	expectedQuery4k := url.QueryEscape(movie4kTarget)
	expectedLocation4k := "http://streamcache:8080?url=" + expectedQuery4k
	if loc := rec.Header().Get("Location"); loc != expectedLocation4k {
		t.Errorf("Expected 302 Location %s, got %s", expectedLocation4k, loc)
	}

	// 3. Test Extensionless Series Playback + Async Activation Trigger
	req = httptest.NewRequest("GET", "/play/series/30998/1/1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for series play, got %d", rec.Code)
	}
	expectedQuerySeries := url.QueryEscape(seriesTarget)
	expectedSeriesLoc := "http://streamcache:8080?url=" + expectedQuerySeries
	if loc := rec.Header().Get("Location"); loc != expectedSeriesLoc {
		t.Errorf("Expected 302 Location %s, got %s", expectedSeriesLoc, loc)
	}

	select {
	case sid := <-activatedSeries:
		if sid != 30998 {
			t.Errorf("Expected activation for series 30998, got %d", sid)
		}
	case <-time.After(1 * time.Second):
		t.Errorf("Expected activation callback to be triggered asynchronously")
	}

	// 4. Test Movie Activation Callback for Uncached Movie (Unactivated)
	unactivatedMovieID := 99999
	unactivatedTarget := "https://dp.example.com/movie/agy/agypassword/99999.mkv"
	srv.SetMovieActivationCallback(func(mid int) (string, error) {
		if mid == unactivatedMovieID {
			database.SaveStreamVariant(&db.StreamVariant{
				ItemID: mid, ItemType: "movie", VariantKey: "base", TargetURL: unactivatedTarget, StreamID: "99999",
			})
			return unactivatedTarget, nil
		}
		return "", nil
	})

	req = httptest.NewRequest("GET", "/play/movie/99999", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for unactivated movie play, got %d", rec.Code)
	}
	expectedUnactLoc := "http://streamcache:8080?url=" + url.QueryEscape(unactivatedTarget)
	if loc := rec.Header().Get("Location"); loc != expectedUnactLoc {
		t.Errorf("Expected 302 Location %s, got %s", expectedUnactLoc, loc)
	}

	// 5. Test Variant Extension Stripping (/play/movie/1058902/4k.mkv)
	req = httptest.NewRequest("GET", "/play/movie/1058902/4k.mkv", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for 4k.mkv movie play, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != expectedLocation4k {
		t.Errorf("Expected 302 Location %s for stripped 4k.mkv, got %s", expectedLocation4k, loc)
	}

	// 6. Test TMDB Identifier Playback (/play/movie/tmdb:1739639)
	_ = database.UpsertInitialMovieState(1058902, "1739639", "", "Test Movie", 2026, "Test Movie (2026)")
	req = httptest.NewRequest("GET", "/play/movie/tmdb:1739639", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for tmdb movie play, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != expectedLocation {
		t.Errorf("Expected 302 Location %s for tmdb:1739639, got %s", expectedLocation, loc)
	}

	// 7. Test TMDB Identifier Series Playback (/play/series/tmdb:61381/1/1)
	_ = database.UpsertInitialSeriesState(30998, "61381", "", "Test Series", 2014, "Test Series (2014)")
	req = httptest.NewRequest("GET", "/play/series/tmdb:61381/1/1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for tmdb series play, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != expectedSeriesLoc {
		t.Errorf("Expected 302 Location %s for tmdb:61381, got %s", expectedSeriesLoc, loc)
	}
}

func TestPredictiveFetchAhead(t *testing.T) {
	var mu sync.Mutex
	var interceptedURLs []string

	mockStreamcache := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("url")
		mu.Lock()
		interceptedURLs = append(interceptedURLs, target)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock streamcache ok"))
	}))
	defer mockStreamcache.Close()

	tempDir, err := os.MkdirTemp("", "dp2strm_fetch_ahead_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "proxy_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 500
	_ = database.UpsertInitialSeriesState(seriesID, "12345", "", "Test Show", 2024, "Test Show (2024)")
	_ = database.SetSeriesActivated(seriesID, "12345", "Test Show")

	// Seed episodes:
	// S01E01, S01E02, S02E01
	episodes := []struct {
		sNum, eNum int
		target     string
	}{
		{1, 1, "https://dp.example.com/s01e01.mkv"},
		{1, 2, "https://dp.example.com/s01e02.mkv"},
		{2, 1, "https://dp.example.com/s02e01.mkv"},
	}
	for _, ep := range episodes {
		_ = database.SaveStreamVariant(&db.StreamVariant{
			ItemID:     seriesID,
			ItemType:   "series",
			SeasonNum:  ep.sNum,
			EpisodeNum: ep.eNum,
			VariantKey: "base",
			TargetURL:  ep.target,
		})
	}

	cfg := &models.Config{
		StreamcacheURL:  mockStreamcache.URL,
		FetchAheadCount: 2,
		Port:            8080,
	}

	srv := NewServer(cfg, database, nil)

	// 1. Play S01E01 with FetchAheadCount=2 -> should tap S01E02 and S02E01 (season rollover)
	req := httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found, got %d", rec.Code)
	}

	// Wait up to 500ms for async fetch-ahead goroutine to tap mockStreamcache
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(interceptedURLs)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	captured := append([]string{}, interceptedURLs...)
	interceptedURLs = nil
	mu.Unlock()

	if len(captured) != 2 {
		t.Fatalf("Expected 2 fetch-ahead taps, got %d: %v", len(captured), captured)
	}
	if captured[0] != "https://dp.example.com/s01e02.mkv" {
		t.Errorf("Tap 0 want https://dp.example.com/s01e02.mkv, got %s", captured[0])
	}
	if captured[1] != "https://dp.example.com/s02e01.mkv" {
		t.Errorf("Tap 1 want https://dp.example.com/s02e01.mkv (season rollover), got %s", captured[1])
	}

	// 2. Play S02E01 (series finale / last episode) -> should make 0 fetch-ahead taps
	req = httptest.NewRequest("GET", "/play/series/500/2/1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found, got %d", rec.Code)
	}

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	captured = append([]string{}, interceptedURLs...)
	interceptedURLs = nil
	mu.Unlock()

	if len(captured) != 0 {
		t.Errorf("Expected 0 fetch-ahead taps at end of series, got %d: %v", len(captured), captured)
	}

	// 3. Play S01E01 with StreamcacheURL empty -> 0 fetch-ahead taps
	cfgNoSC := &models.Config{
		StreamcacheURL:  "",
		FetchAheadCount: 2,
		Port:            8080,
	}
	srvNoSC := NewServer(cfgNoSC, database, nil)

	req = httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec = httptest.NewRecorder()
	srvNoSC.httpServer.Handler.ServeHTTP(rec, req)

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	captured = append([]string{}, interceptedURLs...)
	interceptedURLs = nil
	mu.Unlock()

	if len(captured) != 0 {
		t.Errorf("Expected 0 taps when StreamcacheURL is empty, got %d", len(captured))
	}

	// 4. Play S01E01 with FetchAheadCount = 0 -> 0 fetch-ahead taps
	cfgZeroCount := &models.Config{
		StreamcacheURL:  mockStreamcache.URL,
		FetchAheadCount: 0,
		Port:            8080,
	}
	srvZeroCount := NewServer(cfgZeroCount, database, nil)

	req = httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec = httptest.NewRecorder()
	srvZeroCount.httpServer.Handler.ServeHTTP(rec, req)

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	captured = append([]string{}, interceptedURLs...)
	interceptedURLs = nil
	mu.Unlock()

	if len(captured) != 0 {
		t.Errorf("Expected 0 taps when FetchAheadCount is 0, got %d", len(captured))
	}

	// 5. Play S01E01 with variant "4k" -> should tap 4K variant for S01E02 and fallback to base for S02E01
	_ = database.SaveStreamVariant(&db.StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 1,
		VariantKey: "4k",
		TargetURL:  "https://dp.example.com/s01e01_4k.mkv",
	})
	_ = database.SaveStreamVariant(&db.StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 2,
		VariantKey: "4k",
		TargetURL:  "https://dp.example.com/s01e02_4k.mkv",
	})

	srv4k := NewServer(cfg, database, nil)
	req = httptest.NewRequest("GET", "/play/series/500/1/1/4k", nil)
	rec = httptest.NewRecorder()
	srv4k.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for 4k play, got %d", rec.Code)
	}

	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(interceptedURLs)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	captured = append([]string{}, interceptedURLs...)
	interceptedURLs = nil
	mu.Unlock()

	if len(captured) != 2 {
		t.Fatalf("Expected 2 variant fetch-ahead taps, got %d: %v", len(captured), captured)
	}
	if captured[0] != "https://dp.example.com/s01e02_4k.mkv" {
		t.Errorf("Tap 0 want 4k URL https://dp.example.com/s01e02_4k.mkv, got %s", captured[0])
	}
	if captured[1] != "https://dp.example.com/s02e01.mkv" {
		t.Errorf("Tap 1 want base fallback URL https://dp.example.com/s02e01.mkv, got %s", captured[1])
	}
}

