package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dp2strm/pkg/client"
	"dp2strm/pkg/db"
	"dp2strm/pkg/models"
)

func TestSyncerPass1AndActivation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	// Mock Dispatcharr Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/vod/movies/"):
			if r.URL.Path == "/api/vod/movies/" {
				_, _ = w.Write([]byte(`{"count": 1, "results": [{"id": 1058902, "name": "Avatar (2009)", "year": 2009, "tmdb_id": "19995"}]}`))
			} else if strings.Contains(r.URL.Path, "/providers/") {
				_, _ = w.Write([]byte(`[{"id": 1, "stream_id": "1058902", "container_extension": "mkv", "category": {"id": 1, "name": "Action"}}]`))
			} else {
				_, _ = w.Write([]byte(`{"id": 1058902, "name": "Avatar (2009)", "year": 2009, "tmdb_id": "19995"}`))
			}
		case strings.HasPrefix(r.URL.Path, "/api/vod/series/"):
			if r.URL.Path == "/api/vod/series/" {
				_, _ = w.Write([]byte(`{"count": 1, "results": [{"id": 30998, "name": "Breaking Bad (2008)", "year": 2008, "tmdb_id": "1396"}]}`))
			} else if strings.Contains(r.URL.Path, "/episodes/") {
				_, _ = w.Write([]byte(`[{"id": 438365, "name": "Pilot", "season_number": 1, "episode_number": 1, "container_extension": "mkv"}]`))
			} else {
				_, _ = w.Write([]byte(`{"id": 30998, "name": "Breaking Bad (2008)", "year": 2008, "tmdb_id": "1396"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	cfg := &models.Config{
		DispatcharrURL:        ts.URL,
		APIKey:                "test-api-key",
		XCUser:                "testuser",
		XCPassword:            "testpass",
		OutputDir:             tempDir,
		URL:                   "http://localhost:8080",
		PageSize:              100,
		PageWorkers:           2,
		MaxProbeConcurrency:   2,
		CircuitBreakerEnabled: true,
	}

	c := client.NewClient(ts.URL, "test-api-key")
	s := NewSyncer(cfg, c, database)

	// 1. Run Movie Sync Pass 1
	if err := s.SyncMovies(context.Background(), false); err != nil {
		t.Fatalf("SyncMovies failed: %v", err)
	}

	movieFolder := filepath.Join(tempDir, "Movies", "Avatar (2009) [tmdbid=19995]")
	movieSTRM := filepath.Join(movieFolder, "Avatar (2009).strm")
	if _, err := os.Stat(movieSTRM); os.IsNotExist(err) {
		t.Errorf("Expected movie STRM file to exist at %s", movieSTRM)
	}

	// With EnableNFO=false: movie.nfo and tvshow.nfo should not exist
	movieNFO := filepath.Join(movieFolder, "Avatar (2009).nfo")
	if _, err := os.Stat(movieNFO); !os.IsNotExist(err) {
		t.Errorf("Expected movie NFO to not exist when EnableNFO=false")
	}

	// 2. Run Series Sync Pass 1
	if err := s.SyncSeries(context.Background(), false); err != nil {
		t.Fatalf("SyncSeries failed: %v", err)
	}

	seriesFolder := filepath.Join(tempDir, "TV Shows", "Breaking Bad (2008) [tmdbid=1396]")
	tvshowNFO := filepath.Join(seriesFolder, "tvshow.nfo")
	if _, err := os.Stat(tvshowNFO); !os.IsNotExist(err) {
		t.Errorf("Expected tvshow.nfo to not exist when EnableNFO=false")
	}

	activationSTRM := filepath.Join(seriesFolder, "Season 01", "Breaking Bad (2008) - S01E01.strm")
	activationNFO := filepath.Join(seriesFolder, "Season 01", "Breaking Bad (2008) - S01E01.nfo")

	if _, err := os.Stat(activationSTRM); os.IsNotExist(err) {
		t.Errorf("Expected Season 01 S01E01 STRM file to exist at %s", activationSTRM)
	}
	if _, err := os.Stat(activationNFO); os.IsNotExist(err) {
		t.Errorf("Expected Season 01 S01E01 NFO file to exist at %s (even when EnableNFO=false)", activationNFO)
	}

	// 3. Test Series Activation with empty directory string (should auto-resolve from SQLite state)
	_, _, err = s.PopulateActivatedSeries(context.Background(), 30998, "", "")
	if err != nil {
		t.Fatalf("PopulateActivatedSeries failed: %v", err)
	}

	episode1STRM := filepath.Join(seriesFolder, "Season 01", "Breaking Bad (2008) - S01E01.strm")
	// S01E01 STRM must exist after activation
	if _, err := os.Stat(episode1STRM); os.IsNotExist(err) {
		t.Errorf("Expected real S01E01 STRM file to exist on disk after activation")
	}

	// S01E01 activation placeholder NFO must be deleted upon activation when EnableNFO=false!
	if _, err := os.Stat(activationNFO); !os.IsNotExist(err) {
		t.Errorf("Expected Season 01 S01E01 activation placeholder NFO to be deleted upon activation when EnableNFO=false")
	}

	// 4. Test Movie Activation with empty directory string (should auto-resolve from SQLite state)
	_, _, baseTarget, err := s.PopulateActivatedMovie(context.Background(), 1058902, "", "", 0, "")
	if err != nil {
		t.Fatalf("PopulateActivatedMovie failed: %v", err)
	}
	if baseTarget == "" {
		t.Errorf("Expected valid baseTargetURL from movie activation")
	}

	// 5. Test EnableNFO=true behavior
	cfg.EnableNFO = true
	sNFO := NewSyncer(cfg, c, database)
	if err := sNFO.SyncMovies(context.Background(), false); err != nil {
		t.Fatalf("SyncMovies with EnableNFO=true failed: %v", err)
	}
	if _, err := os.Stat(movieNFO); os.IsNotExist(err) {
		t.Errorf("Expected movie NFO to exist when EnableNFO=true")
	}

	if err := sNFO.SyncSeries(context.Background(), false); err != nil {
		t.Fatalf("SyncSeries with EnableNFO=true failed: %v", err)
	}
	if _, err := os.Stat(tvshowNFO); os.IsNotExist(err) {
		t.Errorf("Expected tvshow.nfo to exist when EnableNFO=true")
	}
}

func TestSyncerCircuitBreaker(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	// Mock Server returning 429 Too Many Requests
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	cfg := &models.Config{
		DispatcharrURL:        ts.URL,
		APIKey:                "test-api-key",
		OutputDir:             tempDir,
		CircuitBreakerEnabled: true,
		PageSize:              100,
	}

	c := client.NewClient(ts.URL, "test-api-key")
	s := NewSyncer(cfg, c, database)

	_ = s.SyncMovies(context.Background(), false)
	if !s.isCircuitTripped() {
		t.Errorf("Expected circuit breaker to be tripped on 429 rate limit")
	}
}

func TestSyncSharedPassRollover(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	// Mock Server with 1 Movie and 5 Series
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if strings.HasPrefix(r.URL.Path, "/api/vod/movies/") {
			_, _ = w.Write([]byte(`{"count": 1, "results": [
				{"id": 1, "name": "Movie 1", "year": 2024, "tmdb_id": "1001"}
			]}`))
		} else if strings.HasPrefix(r.URL.Path, "/api/vod/series/") {
			_, _ = w.Write([]byte(`{"count": 5, "results": [
				{"id": 1, "name": "Series 1", "year": 2024, "tmdb_id": "2001"},
				{"id": 2, "name": "Series 2", "year": 2024, "tmdb_id": "2002"},
				{"id": 3, "name": "Series 3", "year": 2024, "tmdb_id": "2003"},
				{"id": 4, "name": "Series 4", "year": 2024, "tmdb_id": "2004"},
				{"id": 5, "name": "Series 5", "year": 2024, "tmdb_id": "2005"}
			]}`))
		}
	}))
	defer ts.Close()

	cfg := &models.Config{
		DispatcharrURL: ts.URL,
		APIKey:         "test-api-key",
		OutputDir:      tempDir,
		URL:            "http://localhost:8080",
		PageSize:       10,
		PageWorkers:    2,
		BatchSize:      4, // Total shared budget = 4 (Initial: 2 Movies, 2 Series)
	}

	c := client.NewClient(ts.URL, "test-api-key")
	s := NewSyncer(cfg, c, database)

	// Execute Shared Sync Pass:
	// Movie only has 1 available (quota is 2), so 1 unused budget rolls over to Series (Series budget = 2 + 1 = 3).
	// Total created should be 1 movie + 3 series = 4.
	if err := s.SyncSharedPass(context.Background(), false); err != nil {
		t.Fatalf("SyncSharedPass failed: %v", err)
	}

	// Verify Movie 1 was created
	movie1STRM := filepath.Join(tempDir, "Movies", "Movie 1 (2024) [tmdbid=1001]", "Movie 1 (2024).strm")
	if _, err := os.Stat(movie1STRM); os.IsNotExist(err) {
		t.Errorf("Expected Movie 1 to exist at %s", movie1STRM)
	}

	// Verify Series 1, 2, 3 were created (3 series total created due to rollover)
	for i := 1; i <= 3; i++ {
		sDir := filepath.Join(tempDir, "TV Shows", fmt.Sprintf("Series %d (2024) [tmdbid=200%d]", i, i))
		if _, err := os.Stat(sDir); os.IsNotExist(err) {
			t.Errorf("Expected Series %d to exist at %s", i, sDir)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m 30s"},
		{2 * time.Minute, "2m"},
		{12*time.Minute + 5*time.Second, "12m 05s"},
		{1*time.Hour + 23*time.Minute + 10*time.Second, "1h 23m"},
		{2 * time.Hour, "2h"},
	}

	for _, tt := range tests {
		actual := formatDuration(tt.d)
		if actual != tt.expected {
			t.Errorf("formatDuration(%v) = %q, expected %q", tt.d, actual, tt.expected)
		}
	}
}

func TestQueueProgressSummaryAndEstimation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	cfg := &models.Config{
		EmbyBatchSize: 30,
	}
	s := NewSyncer(cfg, nil, database)

	// 1. Initial empty state
	summary := s.GetQueueProgressSummary()
	if summary.CompletedTasks != 0 || summary.RemainingTasks != 0 || summary.TotalTasks != 0 {
		t.Errorf("Unexpected initial summary: %+v", summary)
	}
	if summary.RemainingString() != "0 tasks left" {
		t.Errorf("Expected '0 tasks left', got %q", summary.RemainingString())
	}
	if summary.EstimateString() != "Sync complete" {
		t.Errorf("Expected 'Sync complete', got %q", summary.EstimateString())
	}

	// 2. Enqueue 90 tasks (pass1 = Adds)
	for i := 1; i <= 90; i++ {
		_ = database.EnqueueTask("movie", i, "pass1", models.PriorityBatch)
	}

	summary = s.GetQueueProgressSummary()
	if summary.PendingTasks != 90 || summary.RemainingTasks != 90 || summary.TotalTasks != 90 {
		t.Errorf("Expected 90 pending tasks, got %+v", summary)
	}
	if summary.MovieAdds != 90 {
		t.Errorf("Expected 90 MovieAdds, got %d", summary.MovieAdds)
	}
	if summary.TotalMovies() != 90 {
		t.Errorf("Expected 90 TotalMovies, got %d", summary.TotalMovies())
	}
	if summary.RemainingBatches != 3 {
		t.Errorf("Expected 3 remaining batches (90 / 30), got %d", summary.RemainingBatches)
	}
	if summary.HasScanHistory {
		t.Errorf("Expected HasScanHistory to be false before any scan")
	}
	if summary.ProgressString() != "0/90 (0.0%)" {
		t.Errorf("Expected '0/90 (0.0%%%%)', got %q", summary.ProgressString())
	}
	if summary.RemainingString() != "90 task(s) remaining [90 Movies: 90 Adds] (~3 batches)" {
		t.Errorf("Expected '90 task(s) remaining [90 Movies: 90 Adds] (~3 batches)', got %q", summary.RemainingString())
	}
	if summary.EstimateString() != "Est. remaining: calculating after first scan..." {
		t.Errorf("Expected 'Est. remaining: calculating after first scan...', got %q", summary.EstimateString())
	}

	// 3. Record 1st Emby scan (30s) and 30 completed tasks
	s.recordEmbyScanDuration(30 * time.Second)
	s.progressMu.Lock()
	s.completedTasks = 30
	s.progressMu.Unlock()

	// Simulate 30 tasks popped and completed (60 remaining in DB)
	for i := 1; i <= 30; i++ {
		_ = database.MarkTaskComplete(i)
	}

	summary = s.GetQueueProgressSummary()
	if summary.CompletedTasks != 30 || summary.RemainingTasks != 60 || summary.TotalTasks != 90 {
		t.Errorf("Expected 30 completed, 60 remaining, 90 total; got %+v", summary)
	}
	if summary.ProgressPct < 33.3 || summary.ProgressPct > 33.4 {
		t.Errorf("Expected ~33.3%% progress, got %.2f%%", summary.ProgressPct)
	}
	if summary.RemainingBatches != 2 {
		t.Errorf("Expected 2 remaining batches, got %d", summary.RemainingBatches)
	}
	if !summary.HasScanHistory {
		t.Errorf("Expected HasScanHistory to be true")
	}
	if summary.AvgScanDuration != 30*time.Second {
		t.Errorf("Expected AvgScanDuration 30s, got %v", summary.AvgScanDuration)
	}
	// 2 remaining batches * 30s avg = 60s (1m)
	if summary.EstimatedDuration != 60*time.Second {
		t.Errorf("Expected EstimatedDuration 60s, got %v", summary.EstimatedDuration)
	}
	if summary.EstimateString() != "Est. remaining: ~1m (avg 30s/batch)" {
		t.Errorf("Expected 'Est. remaining: ~1m (avg 30s/batch)', got %q", summary.EstimateString())
	}

	// 4. Record 2nd Emby scan (40s) and 30 more completed tasks
	s.recordEmbyScanDuration(40 * time.Second)
	s.progressMu.Lock()
	s.completedTasks = 60
	s.progressMu.Unlock()

	for i := 31; i <= 60; i++ {
		_ = database.MarkTaskComplete(i)
	}

	summary = s.GetQueueProgressSummary()
	if summary.CompletedTasks != 60 || summary.RemainingTasks != 30 || summary.TotalTasks != 90 {
		t.Errorf("Expected 60 completed, 30 remaining, 90 total; got %+v", summary)
	}
	// Avg scan: (30s + 40s)/2 = 35s
	if summary.AvgScanDuration != 35*time.Second {
		t.Errorf("Expected AvgScanDuration 35s, got %v", summary.AvgScanDuration)
	}
	// 1 remaining batch * 35s avg = 35s
	if summary.EstimatedDuration != 35*time.Second {
		t.Errorf("Expected EstimatedDuration 35s, got %v", summary.EstimatedDuration)
	}
	if summary.EstimateString() != "Est. remaining: ~35s (avg 35s/batch)" {
		t.Errorf("Expected 'Est. remaining: ~35s (avg 35s/batch)', got %q", summary.EstimateString())
	}

	// 5. Test resetCompletedTasks (retains scan duration history for future passes)
	s.resetCompletedTasks()
	summary = s.GetQueueProgressSummary()
	if summary.CompletedTasks != 0 {
		t.Errorf("Expected CompletedTasks reset to 0, got %d", summary.CompletedTasks)
	}
	if !summary.HasScanHistory || summary.AvgScanDuration != 35*time.Second {
		t.Errorf("Expected scan history preserved after task count reset, got avg %v", summary.AvgScanDuration)
	}
}

func TestVIPActivationPauseCoordination(t *testing.T) {
	cfg := &models.Config{
		EmbyBatchSize: 30,
	}
	s := NewSyncer(cfg, nil, nil)

	if s.IsVIPActivationInProgress() {
		t.Errorf("Expected IsVIPActivationInProgress to be false initially")
	}

	ctx := context.Background()

	// 1. Acquire VIP lock for Show A
	unlockA := s.BeginVIPActivation(ctx, 1001)
	if !s.IsVIPActivationInProgress() {
		t.Errorf("Expected IsVIPActivationInProgress to be true after Show A lock")
	}

	// 2. Acquire VIP lock for Show B (concurrent activation)
	unlockB := s.BeginVIPActivation(ctx, 1002)
	if !s.IsVIPActivationInProgress() {
		t.Errorf("Expected IsVIPActivationInProgress to be true with 2 locks")
	}

	// 3. Worker trying to wait should block while activations are active
	workerUnblocked := make(chan struct{})
	go func() {
		s.WaitForVIPActivations(ctx)
		close(workerUnblocked)
	}()

	// Verify worker is still blocked
	select {
	case <-workerUnblocked:
		t.Fatalf("Worker unblocked prematurely while Show A and B are active!")
	case <-time.After(50 * time.Millisecond):
		// Expected to remain blocked
	}

	// 4. Show A finishes (Show B still in progress)
	unlockA()
	if !s.IsVIPActivationInProgress() {
		t.Errorf("Expected IsVIPActivationInProgress to be true while Show B is still active")
	}

	// Verify worker is STILL blocked
	select {
	case <-workerUnblocked:
		t.Fatalf("Worker unblocked prematurely while Show B is still active!")
	case <-time.After(50 * time.Millisecond):
		// Expected to remain blocked
	}

	// 5. Show B finishes (all activations complete)
	unlockB()
	if s.IsVIPActivationInProgress() {
		t.Errorf("Expected IsVIPActivationInProgress to be false after all locks released")
	}

	// Verify worker unblocks cleanly
	select {
	case <-workerUnblocked:
		// Success!
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Worker failed to unblock after all VIP activation locks were released")
	}
}

func TestReconcileDiskStateNoRedundantTasks(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "reconcile_test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	cfg := &models.Config{
		OutputDir: tempDir,
		URL:       "http://localhost:9199",
	}
	s := NewSyncer(cfg, nil, database)

	// 1. Setup Movie in DB with legacy folder name (no TMDB ID in FolderName)
	movieID := 5001
	_ = database.UpsertInitialMovieState(movieID, "27205", "", "Inception", 2010, "Inception (2010)")

	// Create canonical directory and valid STRM on disk
	canonicalMovieFolder := filepath.Join(tempDir, "Movies", "Inception (2010) [tmdbid=27205]")
	if err := os.MkdirAll(canonicalMovieFolder, 0755); err != nil {
		t.Fatalf("Failed to create movie dir: %v", err)
	}
	movieSTRM := filepath.Join(canonicalMovieFolder, "Inception (2010).strm")
	movieURL := "http://localhost:9199/play/movie/tmdb:27205"
	if err := os.WriteFile(movieSTRM, []byte(movieURL), 0644); err != nil {
		t.Fatalf("Failed to write movie strm: %v", err)
	}

	// Create legacy duplicate movie folder without ID on disk and mark in soft delete
	legacyMovieFolder := filepath.Join(tempDir, "Movies", "Inception (2010)")
	if err := os.MkdirAll(legacyMovieFolder, 0755); err != nil {
		t.Fatalf("Failed to create legacy movie dir: %v", err)
	}
	_, _ = database.SoftDeleteItem(legacyMovieFolder, "movie")

	// 2. Setup Series in DB with legacy folder name
	seriesID := 9001
	_ = database.UpsertInitialSeriesState(seriesID, "4607", "", "Lost", 2004, "Lost (2004)")

	// Create canonical directory, Season 01, S01E01.strm, and S01E01.nfo on disk
	canonicalSeriesFolder := filepath.Join(tempDir, "TV Shows", "Lost (2004) [tmdbid=4607]")
	season1Dir := filepath.Join(canonicalSeriesFolder, "Season 01")
	if err := os.MkdirAll(season1Dir, 0755); err != nil {
		t.Fatalf("Failed to create series season1 dir: %v", err)
	}
	seriesSTRM := filepath.Join(season1Dir, "Lost - S01E01.strm")
	seriesURL := "http://localhost:9199/play/series/tmdb:4607/1/1"
	if err := os.WriteFile(seriesSTRM, []byte(seriesURL), 0644); err != nil {
		t.Fatalf("Failed to write series strm: %v", err)
	}
	seriesNFO := filepath.Join(season1Dir, "Lost - S01E01.nfo")
	nfoContent := `<episodedetails><title>⚡ Play to Activate Series</title><season>1</season><episode>1</episode><runtime>5</runtime><watched>false</watched></episodedetails>`
	if err := os.WriteFile(seriesNFO, []byte(nfoContent), 0644); err != nil {
		t.Fatalf("Failed to write series nfo: %v", err)
	}

	// Create legacy duplicate series folder without ID on disk
	legacySeriesFolder := filepath.Join(tempDir, "TV Shows", "Lost (2004)")
	if err := os.MkdirAll(legacySeriesFolder, 0755); err != nil {
		t.Fatalf("Failed to create legacy series dir: %v", err)
	}

	// 3. First Run of ReconcileDiskState:
	// Should recognize that canonical files already exist, update SQLite folder names,
	// prune legacy duplicate directories immediately from disk, and enqueue ZERO tasks!
	if err := s.ReconcileDiskState(context.Background()); err != nil {
		t.Fatalf("ReconcileDiskState failed: %v", err)
	}

	stats, err := database.GetDetailedQueueStats()
	if err != nil {
		t.Fatalf("GetDetailedQueueStats failed: %v", err)
	}
	if stats.Pending != 0 {
		t.Errorf("Expected 0 pending tasks for already valid items, got %d", stats.Pending)
	}

	// Verify database records were upgraded to canonical folder names
	mst, _ := database.GetMovieState(movieID)
	if mst.FolderName != "Inception (2010) [tmdbid=27205]" {
		t.Errorf("Expected movie folder_name to be upgraded to canonical, got %q", mst.FolderName)
	}
	sst, _ := database.GetSeriesState(seriesID)
	if sst.FolderName != "Lost (2004) [tmdbid=4607]" {
		t.Errorf("Expected series folder_name to be upgraded to canonical, got %q", sst.FolderName)
	}

	// Verify legacy duplicate folders were purged from disk immediately
	if _, err := os.Stat(legacyMovieFolder); !os.IsNotExist(err) {
		t.Errorf("Expected legacy movie folder %q to be purged from disk", legacyMovieFolder)
	}
	if _, err := os.Stat(legacySeriesFolder); !os.IsNotExist(err) {
		t.Errorf("Expected legacy series folder %q to be purged from disk", legacySeriesFolder)
	}

	// Verify soft deleted items count is 0
	pendingSoftDeletes, _ := database.GetPendingSoftDeletedCount()
	if pendingSoftDeletes != 0 {
		t.Errorf("Expected 0 pending soft deleted items, got %d", pendingSoftDeletes)
	}

	// 4. Second Run of ReconcileDiskState:
	// Should remain 0 tasks enqueued
	if err := s.ReconcileDiskState(context.Background()); err != nil {
		t.Fatalf("Second ReconcileDiskState failed: %v", err)
	}
	stats, _ = database.GetDetailedQueueStats()
	if stats.Pending != 0 {
		t.Errorf("Expected 0 pending tasks on second run, got %d", stats.Pending)
	}

	// 5. Test genuine missing file detection: Delete movie STRM and verify exactly 1 task is enqueued
	if err := os.Remove(movieSTRM); err != nil {
		t.Fatalf("Failed to remove movie STRM: %v", err)
	}
	if err := s.ReconcileDiskState(context.Background()); err != nil {
		t.Fatalf("Third ReconcileDiskState failed: %v", err)
	}
	stats, _ = database.GetDetailedQueueStats()
	if stats.Pending != 1 || stats.MovieReconciles != 1 {
		t.Errorf("Expected 1 pending MovieReconcile task after deleting STRM, got %+v", stats)
	}
}

func TestStageCatalogDeltaSafetyAndSoftDeleteLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}
	defer database.Close()

	// Seed 1 active movie in database
	movieID := 8888
	_ = database.UpsertInitialMovieState(movieID, "550", "", "Fight Club", 1999, "Fight Club (1999) [tmdbid=550]")

	// Mock server returning 502 Bad Gateway error for movies
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
	}))
	defer ts.Close()

	restClient := client.NewClient(ts.URL, "test-key")
	cfg := &models.Config{
		OutputDir:          tempDir,
		RetentionGraceDays: 7,
		URL:                "http://localhost:9199",
	}
	s := NewSyncer(cfg, restClient, database)

	// 1. Run StageCatalogDelta when upstream returns 502 error
	// Staging must fail safely: it must NOT mark existing movie 8888 as disappeared!
	_ = s.StageCatalogDelta(context.Background())

	softDeletedCount, _ := database.GetPendingSoftDeletedCount()
	if softDeletedCount != 0 {
		t.Fatalf("Expected 0 soft deleted items after failed fetch (safety guardrail), got %d", softDeletedCount)
	}

	// 2. Test Soft-Delete Lifecycle: Manually mark movie folder as soft deleted
	movieFolder := filepath.Join(tempDir, "Movies", "Fight Club (1999) [tmdbid=550]")
	inserted, err := database.SoftDeleteItem(movieFolder, "movie")
	if err != nil || !inserted {
		t.Fatalf("Expected movie to be newly soft-deleted: inserted=%v, err=%v", inserted, err)
	}

	// Verify ReconcileDiskState skips soft-deleted movie even if its folder/STRM does not exist on disk
	_ = s.ReconcileDiskState(context.Background())
	stats, _ := database.GetDetailedQueueStats()
	if stats.MovieReconciles != 0 {
		t.Errorf("Expected 0 MovieReconciles (soft-deleted movie must not be resurrected), got %d", stats.MovieReconciles)
	}

	// 3. Test Reappearance: When movie is re-staged by Dispatcharr, it must be removed from soft_deleted_items
	restagedMovies := []models.Movie{
		{ID: movieID, TMDBID: "550", Name: "Fight Club", Year: 1999},
	}
	if err := database.BatchStageMovies(restagedMovies); err != nil {
		t.Fatalf("BatchStageMovies failed: %v", err)
	}

	softDeletedAfterReappearance, _ := database.GetPendingSoftDeletedCount()
	if softDeletedAfterReappearance != 0 {
		t.Errorf("Expected 0 soft deleted items after movie reappeared in Dispatcharr, got %d", softDeletedAfterReappearance)
	}
}

func TestWorkerProcessUpdateEpisodesTask(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_update_ep.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	seriesID := 5555
	// Seed active series state
	_ = database.UpsertInitialSeriesState(seriesID, "1396", "", "Breaking Bad", 2008, "Breaking Bad (2008) [tmdbid=1396]")
	_ = database.SetSeriesActivated(seriesID, "1396", "Breaking Bad")

	// Mock server returning multiple episodes for series 5555
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "/episodes/") {
			_, _ = w.Write([]byte(`[
				{"id": 1001, "name": "Pilot", "season_number": 1, "episode_number": 1, "container_extension": "mkv"},
				{"id": 1002, "name": "Cat's in the Bag...", "season_number": 1, "episode_number": 2, "container_extension": "mkv"}
			]`))
		} else {
			_, _ = w.Write([]byte(`{"id": 5555, "name": "Breaking Bad (2008)", "year": 2008, "tmdb_id": "1396"}`))
		}
	}))
	defer ts.Close()

	restClient := client.NewClient(ts.URL, "test-key")
	cfg := &models.Config{
		OutputDir: tempDir,
		URL:       "http://localhost:8080",
		XCUser:    "user",
		XCPassword: "pass",
	}
	s := NewSyncer(cfg, restClient, database)

	task := &models.QueueItem{
		ID:       1,
		ItemType: "series",
		ItemID:   seriesID,
		Action:   "update_episodes",
		Priority: models.PriorityAiring,
		Status:   "pending",
	}

	pErr, summary := s.processQueueTask(context.Background(), task)
	if pErr != nil {
		t.Fatalf("processQueueTask returned error: %v", pErr)
	}
	if summary == nil || !summary.STRMCreated {
		t.Errorf("Expected summary.STRMCreated=true for newly written episodes")
	}

	// Verify episodes exist on disk
	ep1Path := filepath.Join(tempDir, "TV Shows", "Breaking Bad (2008) [tmdbid=1396]", "Season 01", "Breaking Bad - S01E01.strm")
	ep2Path := filepath.Join(tempDir, "TV Shows", "Breaking Bad (2008) [tmdbid=1396]", "Season 01", "Breaking Bad - S01E02.strm")

	if _, err := os.Stat(ep1Path); err != nil {
		t.Errorf("Expected S01E01.strm to exist, stat err: %v", err)
	}
	if _, err := os.Stat(ep2Path); err != nil {
		t.Errorf("Expected S01E02.strm to exist, stat err: %v", err)
	}

	// Verify database was updated with refresh timestamp and count
	st, err := database.GetSeriesState(seriesID)
	if err != nil || st == nil {
		t.Fatalf("GetSeriesState failed: %v", err)
	}
	if st.LastEpisodeCount != 2 {
		t.Errorf("Expected LastEpisodeCount=2, got %d", st.LastEpisodeCount)
	}
	if st.LastRefreshedAt == nil {
		t.Errorf("Expected LastRefreshedAt to be set after update_episodes execution")
	}
}

func TestPopulateActivatedSeriesZeroEpisodesPreservesPlaceholder(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	// Mock Dispatcharr server that returns [] for /episodes/
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "/episodes/") {
			_, _ = w.Write([]byte(`[]`))
		} else {
			_, _ = w.Write([]byte(`{"id": 99999, "name": "Empty Show (2024)", "year": 2024, "tmdb_id": "99999", "episode_count": 0}`))
		}
	}))
	defer ts.Close()

	cfg := &models.Config{
		DispatcharrURL: ts.URL,
		APIKey:         "test-api-key",
		OutputDir:      tempDir,
		URL:            "http://localhost:8080",
	}

	c := client.NewClient(ts.URL, "test-api-key")
	s := NewSyncer(cfg, c, database)

	// Pre-create series state and on-disk Season 01 placeholder
	seriesID := 99999
	cleanTitle := "Empty Show"
	folderName := "Empty Show (2024) [tmdbid=99999]"
	seriesFolder := filepath.Join(tempDir, "TV Shows", folderName)
	season1Dir := filepath.Join(seriesFolder, "Season 01")
	if err := os.MkdirAll(season1Dir, 0755); err != nil {
		t.Fatalf("Failed to create season1Dir: %v", err)
	}
	placeholderSTRM := filepath.Join(season1Dir, "Empty Show - S01E01.strm")
	if err := os.WriteFile(placeholderSTRM, []byte("http://dummy"), 0644); err != nil {
		t.Fatalf("Failed to write placeholder STRM: %v", err)
	}

	_ = database.UpsertInitialSeriesState(seriesID, "99999", "", cleanTitle, 2024, folderName)

	// 1. Attempt to activate the series with 0 episodes
	_, _, err = s.PopulateActivatedSeries(context.Background(), seriesID, cleanTitle, seriesFolder)
	if err == nil {
		t.Fatalf("Expected PopulateActivatedSeries to return error for 0 episodes series")
	}

	// 2. Verify placeholder STRM still exists on disk! (Must NOT be erased)
	if _, err := os.Stat(placeholderSTRM); os.IsNotExist(err) {
		t.Errorf("Expected placeholder STRM to be preserved on disk when series has 0 episodes!")
	}
	if _, err := os.Stat(season1Dir); os.IsNotExist(err) {
		t.Errorf("Expected Season 01 directory to be preserved on disk!")
	}

	// 3. Verify SQLite is_activated is 0
	st, err := database.GetSeriesState(seriesID)
	if err != nil || st == nil {
		t.Fatalf("GetSeriesState failed: %v", err)
	}
	if st.IsActivated {
		t.Errorf("Expected series to remain unactivated in SQLite when 0 episodes upstream")
	}

	// 4. Test Self-Healing in ReconcileDiskState:
	// Simulate an empty activated series (e.g. wiped disk + is_activated=1)
	_ = database.SetSeriesActivated(seriesID, "99999", cleanTitle)
	_ = os.RemoveAll(season1Dir) // wipe season1Dir

	if err := s.ReconcileDiskState(context.Background()); err != nil {
		t.Fatalf("ReconcileDiskState failed: %v", err)
	}

	// Verify ReconcileDiskState reset is_activated to 0
	healedSt, _ := database.GetSeriesState(seriesID)
	if healedSt.IsActivated {
		t.Errorf("Expected ReconcileDiskState to reset is_activated to false for empty series")
	}

	// Execute pending reconciliation task
	task, _ := database.PopNextTask("")
	if task == nil {
		t.Fatalf("Expected reconcile_series task to be queued for empty series")
	}
	pErr, summary := s.processQueueTask(context.Background(), task)
	if pErr != nil {
		t.Fatalf("processQueueTask failed: %v", pErr)
	}
	if summary == nil || !summary.STRMCreated {
		t.Errorf("Expected summary.STRMCreated=true after healing placeholder")
	}

	// Verify placeholder was restored on disk!
	if _, err := os.Stat(placeholderSTRM); os.IsNotExist(err) {
		t.Errorf("Expected placeholder STRM to be restored on disk after reconciliation healing!")
	}
}

func TestResolveSeriesStreamVariantsFast(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_resolve.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open DB failed: %v", err)
	}
	defer database.Close()

	// Mock Dispatcharr server that returns episodes for /episodes/
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "/episodes/") {
			_, _ = w.Write([]byte(`[
				{"id": 101, "name": "Episode One", "season_number": 1, "episode_number": 1, "container_extension": "mkv"},
				{"id": 102, "name": "Episode Two", "season_number": 1, "episode_number": 2, "container_extension": "mkv"}
			]`))
		} else {
			_, _ = w.Write([]byte(`{"id": 88888, "name": "Fast Show", "year": 2024, "tmdb_id": "88888", "episode_count": 2}`))
		}
	}))
	defer ts.Close()

	cfg := &models.Config{
		DispatcharrURL: ts.URL,
		APIKey:         "test-api-key",
		OutputDir:      tempDir,
		URL:            "http://localhost:8080",
		XCUser:         "testuser",
		XCPassword:     "testpass",
		EnableNFO:      false,
	}

	c := client.NewClient(ts.URL, "test-api-key")
	s := NewSyncer(cfg, c, database)

	seriesID := 88888
	cleanTitle := "Fast Show"
	folderName := "Fast Show (2024) [tmdbid=88888]"
	seriesFolder := filepath.Join(tempDir, "TV Shows", folderName)
	_ = database.UpsertInitialSeriesState(seriesID, "88888", "", cleanTitle, 2024, folderName)

	// 1. Run ResolveSeriesStreamVariants
	episodes, title, dir, err := s.ResolveSeriesStreamVariants(context.Background(), seriesID)
	if err != nil {
		t.Fatalf("ResolveSeriesStreamVariants failed: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("Expected 2 episodes, got %d", len(episodes))
	}
	if title != cleanTitle {
		t.Errorf("Expected title %q, got %q", cleanTitle, title)
	}
	if dir != seriesFolder {
		t.Errorf("Expected dir %q, got %q", seriesFolder, dir)
	}

	// 2. Verify stream variants are already in SQLite for immediate 302 redirect
	targetURL1, err := database.GetSeriesTargetURL(seriesID, 1, 1, "base")
	if err != nil || targetURL1 == "" {
		t.Errorf("Expected targetURL for S01E01 to be immediately available in DB, got %q (err: %v)", targetURL1, err)
	}
	targetURL2, err := database.GetSeriesTargetURL(seriesID, 1, 2, "base")
	if err != nil || targetURL2 == "" {
		t.Errorf("Expected targetURL for S01E02 to be immediately available in DB, got %q (err: %v)", targetURL2, err)
	}

	// 3. Verify series is marked as activated in DB
	st, err := database.GetSeriesState(seriesID)
	if err != nil || st == nil || !st.IsActivated {
		t.Errorf("Expected series to be marked as activated in DB after ResolveSeriesStreamVariants")
	}

	// 4. Verify disk files are written when PopulateActivatedSeriesDisk is called
	created, preserved, err := s.PopulateActivatedSeriesDisk(context.Background(), seriesID, title, dir, episodes)
	if err != nil {
		t.Fatalf("PopulateActivatedSeriesDisk failed: %v", err)
	}
	if created == 0 && preserved == 0 {
		t.Errorf("Expected created or preserved > 0 from PopulateActivatedSeriesDisk")
	}

	ep1STRM := filepath.Join(seriesFolder, "Season 01", "Fast Show - S01E01.strm")
	if _, err := os.Stat(ep1STRM); os.IsNotExist(err) {
		t.Errorf("Expected S01E01 STRM to exist on disk after PopulateActivatedSeriesDisk")
	}
	ep2STRM := filepath.Join(seriesFolder, "Season 01", "Fast Show - S01E02.strm")
	if _, err := os.Stat(ep2STRM); os.IsNotExist(err) {
		t.Errorf("Expected S01E02 STRM to exist on disk after PopulateActivatedSeriesDisk")
	}
}



