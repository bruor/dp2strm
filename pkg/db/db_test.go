package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteStateEngine(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_db_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "dp2strm_test.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	// 1. Test Series Activation & Metadata
	seriesID := 30998
	st, err := d.GetSeriesState(seriesID)
	if err != nil {
		t.Fatalf("Error querying unactivated series: %v", err)
	}
	if st != nil {
		t.Fatalf("Expected nil state for unactivated series, got: %+v", st)
	}

	if err := d.SetSeriesActivated(seriesID, "61381", "black-ish"); err != nil {
		t.Fatalf("Failed to set series activated: %v", err)
	}

	st, err = d.GetSeriesState(seriesID)
	if err != nil || st == nil {
		t.Fatalf("Expected series state after activation, got: %+v, err: %v", st, err)
	}
	if !st.IsActivated {
		t.Errorf("Expected IsActivated=true, got false")
	}

	// Test Movie Activation State
	movieID := 1058902
	mst, err := d.GetMovieState(movieID)
	if err != nil {
		t.Fatalf("Error querying unactivated movie: %v", err)
	}
	if mst != nil {
		t.Fatalf("Expected nil state for unactivated movie, got: %+v", mst)
	}

	if err := d.SetMovieActivated(movieID, "19995", "Avatar"); err != nil {
		t.Fatalf("Failed to set movie activated: %v", err)
	}

	mst, err = d.GetMovieState(movieID)
	if err != nil || mst == nil {
		t.Fatalf("Expected movie state after activation, got: %+v, err: %v", mst, err)
	}
	if !mst.IsActivated {
		t.Errorf("Expected movie IsActivated=true, got false")
	}

	// Update metadata to Continuing
	if err := d.UpdateSeriesMetadata(seriesID, "Continuing", 24, "2026-08-20", 1786616158); err != nil {
		t.Fatalf("Failed to update series metadata: %v", err)
	}

	// Query active series to sync for currentDate = "2026-08-25"
	activeIDs, err := d.GetActiveSeriesToSync("2026-08-25")
	if err != nil {
		t.Fatalf("Failed to query active series: %v", err)
	}
	if len(activeIDs) != 1 || activeIDs[0] != seriesID {
		t.Errorf("Expected active series ID %d, got: %v", seriesID, activeIDs)
	}

	// Query active series for currentDate = "2026-08-15" (before next_air_date)
	activeIDs, err = d.GetActiveSeriesToSync("2026-08-15")
	if err != nil {
		t.Fatalf("Failed to query active series: %v", err)
	}
	if len(activeIDs) != 0 {
		t.Errorf("Expected 0 active series (skipped before air date), got: %v", activeIDs)
	}

	// Mark series as Ended
	if err := d.UpdateSeriesMetadata(seriesID, "Ended", 24, "", 1786616158); err != nil {
		t.Fatalf("Failed to update series status to Ended: %v", err)
	}

	activeIDs, err = d.GetActiveSeriesToSync("2026-08-30")
	if err != nil {
		t.Fatalf("Failed to query active series: %v", err)
	}
	if len(activeIDs) != 0 {
		t.Errorf("Expected 0 active series for Ended show, got: %v", activeIDs)
	}

	// 2. Test Stream Variants
	movieVariant := &StreamVariant{
		ItemID:             1058902,
		ItemType:           "movie",
		VariantKey:         "base",
		TargetURL:          "https://dp.example.com/movie/agy/agypassword/1058902.mkv",
		Quality:            "1080p",
		StreamID:           "1058902",
		ContainerExtension: "mkv",
	}
	if err := d.SaveStreamVariant(movieVariant); err != nil {
		t.Fatalf("Failed to save movie stream variant: %v", err)
	}

	targetURL, err := d.GetMovieTargetURL(1058902, "base")
	if err != nil {
		t.Fatalf("Failed to get movie target URL: %v", err)
	}
	if targetURL != movieVariant.TargetURL {
		t.Errorf("Expected target URL %s, got %s", movieVariant.TargetURL, targetURL)
	}

	seriesEpVariant := &StreamVariant{
		ItemID:             seriesID,
		ItemType:           "series",
		SeasonNum:          1,
		EpisodeNum:         1,
		VariantKey:         "4k",
		TargetURL:          "https://dp.example.com/series/agy/agypassword/438365_4k.mkv",
		Quality:            "4K",
		StreamID:           "438365_4k",
		ContainerExtension: "mkv",
	}
	if err := d.SaveStreamVariant(seriesEpVariant); err != nil {
		t.Fatalf("Failed to save series episode stream variant: %v", err)
	}

	targetURL, err = d.GetSeriesTargetURL(seriesID, 1, 1, "4k")
	if err != nil {
		t.Fatalf("Failed to get series target URL: %v", err)
	}
	if targetURL != seriesEpVariant.TargetURL {
		t.Errorf("Expected series 4k target URL %s, got %s", seriesEpVariant.TargetURL, targetURL)
	}

	// 3. Test Soft Delete Tracking
	itemPath := "/data/strm/Movies/Avatar (2009) [tmdbid=19995]"
	if err := d.SoftDeleteItem(itemPath, "movie"); err != nil {
		t.Fatalf("Failed to soft delete item: %v", err)
	}

	expired, err := d.GetExpiredSoftDeletedItems(0) // 0 days retention cutoff
	if err != nil {
		t.Fatalf("Failed to query expired soft deleted items: %v", err)
	}
	if len(expired) != 1 || expired[0] != itemPath {
		t.Errorf("Expected expired item path %s, got %v", itemPath, expired)
	}

	if err := d.RemoveSoftDeletedItem(itemPath); err != nil {
		t.Fatalf("Failed to remove soft deleted item: %v", err)
	}
}
