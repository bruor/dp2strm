package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	// 2. Run Series Sync Pass 1
	if err := s.SyncSeries(context.Background(), false); err != nil {
		t.Fatalf("SyncSeries failed: %v", err)
	}

	seriesFolder := filepath.Join(tempDir, "TV Shows", "Breaking Bad (2008) [tmdbid=1396]")
	placeholderSTRM := filepath.Join(seriesFolder, "Season 01", "Breaking Bad (2008) - S01E01 - Play to Activate Series.strm")
	if _, err := os.Stat(placeholderSTRM); os.IsNotExist(err) {
		t.Errorf("Expected placeholder STRM file to exist at %s", placeholderSTRM)
	}

	// 3. Test Series Activation with empty directory string (should auto-resolve from SQLite state)
	_, _, err = s.PopulateActivatedSeries(context.Background(), 30998, "", "")
	if err != nil {
		t.Fatalf("PopulateActivatedSeries failed: %v", err)
	}

	// Placeholder must be deleted!
	if _, err := os.Stat(placeholderSTRM); !os.IsNotExist(err) {
		t.Errorf("Expected placeholder STRM file to be deleted after activation")
	}

	// Real episode S01E01 must exist!
	realEpisodeSTRM := filepath.Join(seriesFolder, "Season 01", "Breaking Bad (2008) - S01E01 - Pilot.strm")
	if _, err := os.Stat(realEpisodeSTRM); os.IsNotExist(err) {
		t.Errorf("Expected real episode STRM file to exist at %s", realEpisodeSTRM)
	}

	// 4. Test Movie Activation with empty directory string (should auto-resolve from SQLite state)
	_, _, baseTarget, err := s.PopulateActivatedMovie(context.Background(), 1058902, "", "", 0, "")
	if err != nil {
		t.Fatalf("PopulateActivatedMovie failed: %v", err)
	}
	if baseTarget == "" {
		t.Errorf("Expected valid baseTargetURL from movie activation")
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
