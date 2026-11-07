package proxy

import (
	"context"
	"fmt"
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
	onActivation := func(sid int) error {
		activatedSeries <- sid
		return nil
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
	expectedLocation := "http://streamcache:8080/stream?url=" + expectedQueryMovie
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
	expectedLocation4k := "http://streamcache:8080/stream?url=" + expectedQuery4k
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
	expectedSeriesLoc := "http://streamcache:8080/stream?url=" + expectedQuerySeries
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
	expectedUnactLoc := "http://streamcache:8080/stream?url=" + url.QueryEscape(unactivatedTarget)
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

	// 1. Play S01E01 with FetchAheadCount=2 -> should append S01E02 and S02E01 (season rollover) via &next_url=
	req := httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}

	loc := rec.Header().Get("Location")
	parsedLoc, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Failed to parse redirect location: %v", err)
	}
	if parsedLoc.Query().Get("url") != "https://dp.example.com/s01e01.mkv" {
		t.Errorf("url want https://dp.example.com/s01e01.mkv, got %s", parsedLoc.Query().Get("url"))
	}
	nextURLs := parsedLoc.Query()["next_url"]
	if len(nextURLs) != 2 {
		t.Fatalf("Expected 2 next_url parameters, got %d: %v", len(nextURLs), nextURLs)
	}
	if nextURLs[0] != "https://dp.example.com/s01e02.mkv" {
		t.Errorf("next_url 0 want https://dp.example.com/s01e02.mkv, got %s", nextURLs[0])
	}
	if nextURLs[1] != "https://dp.example.com/s02e01.mkv" {
		t.Errorf("next_url 1 want https://dp.example.com/s02e01.mkv (season rollover), got %s", nextURLs[1])
	}

	// 2. Play S02E01 (series finale / last episode) -> should have 0 next_url parameters
	req = httptest.NewRequest("GET", "/play/series/500/2/1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}
	parsedLoc, _ = url.Parse(rec.Header().Get("Location"))
	if len(parsedLoc.Query()["next_url"]) != 0 {
		t.Errorf("Expected 0 next_urls at end of series, got %v", parsedLoc.Query()["next_url"])
	}

	// 3. Play S01E01 with StreamcacheURL empty -> raw redirect without &next_url=
	cfgNoSC := &models.Config{
		StreamcacheURL:  "",
		FetchAheadCount: 2,
		Port:            8080,
	}
	srvNoSC := NewServer(cfgNoSC, database, nil)

	req = httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec = httptest.NewRecorder()
	srvNoSC.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "https://dp.example.com/s01e01.mkv" {
		t.Errorf("Expected direct target URL when StreamcacheURL is empty, got %s", rec.Header().Get("Location"))
	}

	// 4. Play S01E01 with FetchAheadCount = 0 -> 0 next_url parameters
	cfgZeroCount := &models.Config{
		StreamcacheURL:  mockStreamcache.URL,
		FetchAheadCount: 0,
		Port:            8080,
	}
	srvZeroCount := NewServer(cfgZeroCount, database, nil)

	req = httptest.NewRequest("GET", "/play/series/500/1/1", nil)
	rec = httptest.NewRecorder()
	srvZeroCount.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}
	parsedLoc, _ = url.Parse(rec.Header().Get("Location"))
	if len(parsedLoc.Query()["next_url"]) != 0 {
		t.Errorf("Expected 0 next_urls when FetchAheadCount is 0, got %v", parsedLoc.Query()["next_url"])
	}

	// 5. Play S01E01 with variant "4k" -> should resolve 4K variant for S01E02 and fallback to base for S02E01
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
		t.Fatalf("Expected 302 Found for 4k play, got %d", rec.Code)
	}
	parsedLoc, _ = url.Parse(rec.Header().Get("Location"))
	nextURLs = parsedLoc.Query()["next_url"]
	if len(nextURLs) != 2 {
		t.Fatalf("Expected 2 variant next_urls, got %d: %v", len(nextURLs), nextURLs)
	}
	if nextURLs[0] != "https://dp.example.com/s01e02_4k.mkv" {
		t.Errorf("next_url[0] want 4k URL https://dp.example.com/s01e02_4k.mkv, got %s", nextURLs[0])
	}
	if nextURLs[1] != "https://dp.example.com/s02e01.mkv" {
		t.Errorf("next_url[1] want base fallback URL https://dp.example.com/s02e01.mkv, got %s", nextURLs[1])
	}
}

func TestStreamcacheDispatcharrURLOverride(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_url_override_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "proxy_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	// Seed movie with internal docker URL
	movieTarget := "http://dispatcharr:9191/movie/dp2strm/mockpassword/1288105.mkv"
	database.SaveStreamVariant(&db.StreamVariant{
		ItemID:     1288105,
		ItemType:   "movie",
		VariantKey: "base",
		TargetURL:  movieTarget,
	})

	// Seed series episode with internal docker URL
	seriesTarget := "http://dispatcharr:9191/series/dp2strm/mockpassword/438365.mkv"
	database.SaveStreamVariant(&db.StreamVariant{
		ItemID:     30998,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 1,
		VariantKey: "base",
		TargetURL:  seriesTarget,
	})

	cfg := &models.Config{
		DispatcharrURL:            "http://dispatcharr:9191",
		StreamcacheURL:            "http://streamcache:8080",
		StreamcacheDispatcharrURL: "https://dp.example.com",
		Port:                      8080,
	}

	srv := NewServer(cfg, database, nil)

	// 1. Test Movie Redirect: http://dispatcharr:9191 should be replaced with https://dp.example.com
	req := httptest.NewRequest("GET", "/play/movie/1288105", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for movie play, got %d", rec.Code)
	}
	expectedRewrittenMovie := "https://dp.example.com/movie/dp2strm/mockpassword/1288105.mkv"
	expectedMovieLoc := "http://streamcache:8080/stream?url=" + url.QueryEscape(expectedRewrittenMovie)
	if loc := rec.Header().Get("Location"); loc != expectedMovieLoc {
		t.Errorf("Expected movie location:\n%s\ngot:\n%s", expectedMovieLoc, loc)
	}

	// 2. Test Series Redirect: http://dispatcharr:9191 should be replaced with https://dp.example.com
	req = httptest.NewRequest("GET", "/play/series/30998/1/1", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for series play, got %d", rec.Code)
	}
	expectedRewrittenSeries := "https://dp.example.com/series/dp2strm/mockpassword/438365.mkv"
	expectedSeriesLoc := "http://streamcache:8080/stream?url=" + url.QueryEscape(expectedRewrittenSeries)
	if loc := rec.Header().Get("Location"); loc != expectedSeriesLoc {
		t.Errorf("Expected series location:\n%s\ngot:\n%s", expectedSeriesLoc, loc)
	}

	// 3. Test without StreamcacheURL: should redirect directly to https://dp.example.com
	cfgDirect := &models.Config{
		DispatcharrURL:            "http://dispatcharr:9191",
		StreamcacheURL:            "",
		StreamcacheDispatcharrURL: "https://dp.example.com",
		Port:                      8080,
	}
	srvDirect := NewServer(cfgDirect, database, nil)

	req = httptest.NewRequest("GET", "/play/movie/1288105", nil)
	rec = httptest.NewRecorder()
	srvDirect.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("Expected 302 Found for direct movie play, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != expectedRewrittenMovie {
		t.Errorf("Expected direct location %s, got %s", expectedRewrittenMovie, loc)
	}
}

func TestConcurrentSeriesActivationWaits(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_concurrent_act_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	database, err := db.Open(filepath.Join(tempDir, "proxy_concurrent.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 77777
	targetStream := "https://dp.example.com/episodes/real_stream.mkv"

	// Mock onActivation that takes 50ms and saves stream variant
	onActivation := func(sid int) error {
		time.Sleep(50 * time.Millisecond)
		_ = database.SaveStreamVariant(&db.StreamVariant{
			ItemID:     sid,
			ItemType:   "series",
			SeasonNum:  1,
			EpisodeNum: 1,
			VariantKey: "base",
			TargetURL:  targetStream,
		})
		return nil
	}

	cfg := &models.Config{
		DispatcharrURL: "http://dispatcharr:9191",
		Port:           8080,
	}

	srv := NewServer(cfg, database, onActivation)

	// Send two requests concurrently
	var wg sync.WaitGroup
	results := make([]int, 2)
	locations := make([]string, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", fmt.Sprintf("/play/series/%d/1/1", seriesID), nil)
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)
			results[idx] = rec.Code
			locations[idx] = rec.Header().Get("Location")
		}(i)
	}

	wg.Wait()

	for idx, code := range results {
		if code != http.StatusFound {
			t.Errorf("Request %d expected 302 Found, got %d (bumper served instead of waiting!)", idx, code)
		}
		if locations[idx] != targetStream {
			t.Errorf("Request %d expected location %s, got %s", idx, targetStream, locations[idx])
		}
	}
}

func TestProxyActivationErrorFailsPlaybackNoBumper(t *testing.T) {
	tempDir := t.TempDir()
	database, err := db.Open(filepath.Join(tempDir, "proxy_error_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 88888
	cfg := &models.Config{
		DispatcharrURL: "http://dispatcharr:9191",
		Port:           8080,
	}

	// Mock onActivation that fails
	onActivation := func(sid int) error {
		return fmt.Errorf("series %d has 0 episodes in Dispatcharr", sid)
	}

	srv := NewServer(cfg, database, onActivation)

	req := httptest.NewRequest("GET", fmt.Sprintf("/play/series/%d/1/1", seriesID), nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	// Must fail with 502 Bad Gateway and NOT return MP4 bumper
	if rec.Code != http.StatusBadGateway {
		t.Errorf("Expected 502 Bad Gateway on activation error, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") == "video/mp4" {
		t.Errorf("Expected NO video/mp4 bumper when activation fails!")
	}
}

func TestProxyActivationSuccessNoS01E01StreamsBumper(t *testing.T) {
	tempDir := t.TempDir()
	database, err := db.Open(filepath.Join(tempDir, "proxy_bumper_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 99999
	cfg := &models.Config{
		DispatcharrURL: "http://dispatcharr:9191",
		Port:           8080,
	}

	// Mock onActivation that activates series but only has Season 2+
	onActivation := func(sid int) error {
		_ = database.SetSeriesActivated(sid, "99999", "Season 2 Only Show")
		_ = database.SaveStreamVariant(&db.StreamVariant{
			ItemID:     sid,
			ItemType:   "series",
			SeasonNum:  2,
			EpisodeNum: 1,
			VariantKey: "base",
			TargetURL:  "https://dp.example.com/s02e01.mkv",
		})
		return nil
	}

	srv := NewServer(cfg, database, onActivation)

	// User played S01E01 placeholder
	req := httptest.NewRequest("GET", fmt.Sprintf("/play/series/%d/1/1", seriesID), nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	// Because series IS activated but S01E01 does not exist, serve 5-second bumper MP4
	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for bumper video, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("Expected video/mp4 Content-Type for bumper, got %s", rec.Header().Get("Content-Type"))
	}
}

func TestPredictiveFetchAheadQueueFIFOAndDeduplication(t *testing.T) {
	tempDir := t.TempDir()
	database, err := db.Open(filepath.Join(tempDir, "fetch_ahead_queue.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 555
	_ = database.UpsertInitialSeriesState(seriesID, "555", "", "Test Show", 2024, "Test Show (2024)")
	_ = database.SetSeriesActivated(seriesID, "555", "Test Show")

	for i := 1; i <= 5; i++ {
		_ = database.SaveStreamVariant(&db.StreamVariant{
			ItemID:     seriesID,
			ItemType:   "series",
			SeasonNum:  1,
			EpisodeNum: i,
			VariantKey: "base",
			TargetURL:  fmt.Sprintf("http://dispatcharr:9191/proxy/vod/episode/ep%d", i),
		})
	}

	cfg := &models.Config{
		StreamcacheURL:  "http://streamcache:8080",
		FetchAheadCount: 2,
		Port:            8080,
	}
	srv := NewServer(cfg, database, nil)
	defer func() { _ = srv.Stop(context.Background()) }()

	// Play ep1 with FetchAheadCount=2 -> should return next_urls for ep2 and ep3
	req := httptest.NewRequest("GET", "/play/series/555/1/1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}

	loc := rec.Header().Get("Location")
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Failed to parse redirect Location: %v", err)
	}
	if parsed.Query().Get("url") != "http://dispatcharr:9191/proxy/vod/episode/ep1" {
		t.Errorf("url want ep1, got %s", parsed.Query().Get("url"))
	}
	nextURLs := parsed.Query()["next_url"]
	if len(nextURLs) != 2 {
		t.Fatalf("Expected 2 next_urls, got %d: %v", len(nextURLs), nextURLs)
	}
	if nextURLs[0] != "http://dispatcharr:9191/proxy/vod/episode/ep2" {
		t.Errorf("next_url[0] want ep2, got %s", nextURLs[0])
	}
	if nextURLs[1] != "http://dispatcharr:9191/proxy/vod/episode/ep3" {
		t.Errorf("next_url[1] want ep3, got %s", nextURLs[1])
	}
}

func TestFormat302TargetWithNext(t *testing.T) {
	rawURL := "http://dispatcharr:9191/proxy/vod/episode/ep1"
	nextURLs := []string{
		"http://dispatcharr:9191/proxy/vod/episode/ep2",
		"http://dispatcharr:9191/proxy/vod/episode/ep3",
	}

	// 1. Without StreamcacheURL -> returns rawURL
	got := format302TargetWithNext(rawURL, nextURLs, "", "", "")
	if got != rawURL {
		t.Errorf("format302TargetWithNext without streamcache = %q; want %q", got, rawURL)
	}

	// 2. With StreamcacheURL -> returns stream URL with encoded url and next_url params
	gotSC := format302TargetWithNext(rawURL, nextURLs, "http://streamcache:8080", "", "")
	parsed, err := url.Parse(gotSC)
	if err != nil {
		t.Fatalf("Failed to parse URL: %v", err)
	}
	if parsed.Path != "/stream" {
		t.Errorf("Path = %q; want /stream", parsed.Path)
	}
	if parsed.Query().Get("url") != rawURL {
		t.Errorf("Query 'url' = %q; want %q", parsed.Query().Get("url"), rawURL)
	}
	gotNexts := parsed.Query()["next_url"]
	if len(gotNexts) != 2 {
		t.Fatalf("Query 'next_url' len = %d; want 2", len(gotNexts))
	}
	if gotNexts[0] != nextURLs[0] || gotNexts[1] != nextURLs[1] {
		t.Errorf("Query 'next_url' = %v; want %v", gotNexts, nextURLs)
	}

	// 3. With StreamcacheDispatcharrURL rewrite
	gotRewrite := format302TargetWithNext(
		rawURL,
		nextURLs,
		"http://streamcache:8080",
		"http://dispatcharr-internal:9191",
		"http://dispatcharr:9191",
	)
	parsedRewrite, err := url.Parse(gotRewrite)
	if err != nil {
		t.Fatalf("Failed to parse URL: %v", err)
	}
	if parsedRewrite.Query().Get("url") != "http://dispatcharr-internal:9191/proxy/vod/episode/ep1" {
		t.Errorf("Rewritten url = %q", parsedRewrite.Query().Get("url"))
	}
	rewrittenNexts := parsedRewrite.Query()["next_url"]
	if len(rewrittenNexts) != 2 {
		t.Fatalf("Expected 2 rewritten next_urls, got %d", len(rewrittenNexts))
	}
	if rewrittenNexts[0] != "http://dispatcharr-internal:9191/proxy/vod/episode/ep2" {
		t.Errorf("Rewritten next_url[0] = %q", rewrittenNexts[0])
	}
}

func TestSeriesPlayWithNextURLSequentialOrdering(t *testing.T) {
	tempDir := t.TempDir()
	database, err := db.Open(filepath.Join(tempDir, "sequential_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer database.Close()

	seriesID := 777
	_ = database.UpsertInitialSeriesState(seriesID, "777", "", "Multi-Season Show", 2024, "Multi-Season Show (2024)")
	_ = database.SetSeriesActivated(seriesID, "777", "Multi-Season Show")

	// S1: E1, E2
	// S2: E1, E2
	for s := 1; s <= 2; s++ {
		for e := 1; e <= 2; e++ {
			_ = database.SaveStreamVariant(&db.StreamVariant{
				ItemID:     seriesID,
				ItemType:   "series",
				SeasonNum:  s,
				EpisodeNum: e,
				VariantKey: "base",
				TargetURL:  fmt.Sprintf("http://dispatcharr:9191/s%02de%02d.mkv", s, e),
			})
		}
	}

	cfg := &models.Config{
		StreamcacheURL:  "http://streamcache:8080",
		FetchAheadCount: 3,
		Port:            8080,
	}
	srv := NewServer(cfg, database, nil)
	defer func() { _ = srv.Stop(context.Background()) }()

	// Play S01E01 with FetchAheadCount=3 -> next_urls should be S01E02, S02E01, S02E02
	req := httptest.NewRequest("GET", "/play/series/777/1/1", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("Expected 302 Found, got %d", rec.Code)
	}
	parsed, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Failed to parse Location: %v", err)
	}
	nextURLs := parsed.Query()["next_url"]
	if len(nextURLs) != 3 {
		t.Fatalf("Expected 3 next_urls across seasons, got %d: %v", len(nextURLs), nextURLs)
	}
	expected := []string{
		"http://dispatcharr:9191/s01e02.mkv",
		"http://dispatcharr:9191/s02e01.mkv",
		"http://dispatcharr:9191/s02e02.mkv",
	}
	for i, exp := range expected {
		if nextURLs[i] != exp {
			t.Errorf("next_url[%d] = %q; want %q", i, nextURLs[i], exp)
		}
	}
}





