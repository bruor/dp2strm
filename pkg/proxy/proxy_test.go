package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
