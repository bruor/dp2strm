package db

import (
	"os"
	"path/filepath"
	"testing"

	"dp2strm/pkg/models"
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

	// Test UpsertInitialSeriesState
	if err := d.UpsertInitialSeriesState(seriesID, "61381", "tt3487356", "black-ish", 2014, "black-ish (2014) [tmdbid=61381]"); err != nil {
		t.Fatalf("Failed to upsert initial series state: %v", err)
	}
	st, err = d.GetSeriesState(seriesID)
	if err != nil || st == nil {
		t.Fatalf("Expected series state after upsert: %v", err)
	}
	if st.IsActivated {
		t.Errorf("Expected IsActivated=false before activation")
	}
	if st.IMDBID != "tt3487356" {
		t.Errorf("Expected IMDBID %q, got %q", "tt3487356", st.IMDBID)
	}
	if st.FolderName != "black-ish (2014) [tmdbid=61381]" {
		t.Errorf("Expected FolderName %q, got %q", "black-ish (2014) [tmdbid=61381]", st.FolderName)
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
	if st.FolderName != "black-ish (2014) [tmdbid=61381]" {
		t.Errorf("Expected FolderName preserved after activation, got %q", st.FolderName)
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

	if err := d.UpsertInitialMovieState(movieID, "19995", "tt0499549", "Avatar", 2009, "Avatar (2009) [tmdbid=19995]"); err != nil {
		t.Fatalf("Failed to upsert initial movie state: %v", err)
	}
	mst, err = d.GetMovieState(movieID)
	if err != nil || mst == nil {
		t.Fatalf("Expected movie state after initial upsert: %v", err)
	}
	if mst.IsActivated {
		t.Errorf("Expected movie IsActivated=false before activation")
	}
	if mst.IMDBID != "tt0499549" {
		t.Errorf("Expected movie IMDBID %q, got %q", "tt0499549", mst.IMDBID)
	}
	if mst.FolderName != "Avatar (2009) [tmdbid=19995]" {
		t.Errorf("Expected movie FolderName %q, got %q", "Avatar (2009) [tmdbid=19995]", mst.FolderName)
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
	if mst.FolderName != "Avatar (2009) [tmdbid=19995]" {
		t.Errorf("Expected movie FolderName preserved after activation, got %q", mst.FolderName)
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
	dummyPath := "/data/strm/Movies/Dummy Expired (2020) [tmdbid=99999]"
	inserted, err := d.SoftDeleteItem(dummyPath, "movie")
	if err != nil || !inserted {
		t.Fatalf("Failed to soft delete item (inserted=%v): %v", inserted, err)
	}

	// Re-inserting the same item path should NOT re-insert (ON CONFLICT DO NOTHING)
	insertedAgain, err := d.SoftDeleteItem(dummyPath, "movie")
	if err != nil || insertedAgain {
		t.Fatalf("Expected insertedAgain=false for existing soft-deleted item, got %v (err: %v)", insertedAgain, err)
	}

	// Test GetSoftDeletedPathsMap
	sdMap, err := d.GetSoftDeletedPathsMap()
	if err != nil || !sdMap[dummyPath] || !sdMap["Dummy Expired (2020) [tmdbid=99999]"] {
		t.Fatalf("GetSoftDeletedPathsMap missing path or folder, got: %v", sdMap)
	}

	expired, err := d.GetExpiredSoftDeletedItems(0) // 0 days retention cutoff
	if err != nil {
		t.Fatalf("Failed to query expired soft deleted items: %v", err)
	}
	if len(expired) != 1 || expired[0] != dummyPath {
		t.Errorf("Expected expired item path %s, got %v", dummyPath, expired)
	}

	if err := d.PurgeExpiredSoftDeletedItem(dummyPath); err != nil {
		t.Fatalf("Failed to purge expired soft deleted item: %v", err)
	}

	// Verify it was removed
	sdMapAfter, _ := d.GetSoftDeletedPathsMap()
	if sdMapAfter[dummyPath] {
		t.Errorf("Expected dummyPath to be removed from soft delete map")
	}

	// 4. Test Sync Queue Operations
	// Enqueue batch tasks (Priority 10)
	if err := d.EnqueueTask("movie", 1001, "pass1", models.PriorityBatch); err != nil {
		t.Fatalf("Failed to enqueue movie batch task: %v", err)
	}
	if err := d.EnqueueTask("series", 2001, "pass1", models.PriorityBatch); err != nil {
		t.Fatalf("Failed to enqueue series batch task: %v", err)
	}
	if err := d.EnqueueTask("movie", 1002, "pass1", models.PriorityBatch); err != nil {
		t.Fatalf("Failed to enqueue second movie batch task: %v", err)
	}

	pending, inProgress, failed, err := d.GetQueueStats()
	if err != nil || pending != 3 || inProgress != 0 || failed != 0 {
		t.Fatalf("Expected 3 pending tasks, got pending=%d, inProgress=%d, failed=%d, err=%v", pending, inProgress, failed, err)
	}

	// Pop with preferredType "series" (round-robin)
	task1, err := d.PopNextTask("series")
	if err != nil || task1 == nil || task1.ItemType != "series" || task1.ItemID != 2001 {
		t.Fatalf("Expected task 2001 (series), got %+v, err: %v", task1, err)
	}

	// Enqueue VIP On-Demand Activation (Priority 100) while worker is processing
	if err := d.EnqueueTask("series", 3001, "activate", models.PriorityVIP); err != nil {
		t.Fatalf("Failed to enqueue VIP activation: %v", err)
	}

	// Pop next task: VIP item (3001) MUST jump the queue ahead of remaining movie tasks!
	task2, err := d.PopNextTask("movie")
	if err != nil || task2 == nil || task2.Priority != models.PriorityVIP || task2.ItemID != 3001 {
		t.Fatalf("Expected VIP task 3001 to jump queue, got %+v, err: %v", task2, err)
	}

	// Mark VIP task complete
	if err := d.MarkTaskComplete(task2.ID); err != nil {
		t.Fatalf("Failed to mark task complete: %v", err)
	}

	// Mark task1 failed
	if err := d.MarkTaskFailed(task1.ID, "Emby timeout"); err != nil {
		t.Fatalf("Failed to mark task failed: %v", err)
	}

	pending, inProgress, failed, _ = d.GetQueueStats()
	if pending != 2 || failed != 1 {
		t.Errorf("Expected pending=2, failed=1, got pending=%d, failed=%d", pending, failed)
	}

	// Test ResetInProgressTasks
	task3, _ := d.PopNextTask("")
	if task3 == nil {
		t.Fatalf("Expected task3, got nil")
	}
	if err := d.ResetInProgressTasks(); err != nil {
		t.Fatalf("Failed to reset in-progress tasks: %v", err)
	}
	pending, inProgress, _, _ = d.GetQueueStats()
	if pending != 2 || inProgress != 0 {
		t.Errorf("Expected pending=2, inProgress=0 after reset, got pending=%d, inProgress=%d", pending, inProgress)
	}

	// 5. Test GetDetailedQueueStats and Batch Delta Staging
	_ = d.EnqueueTask("series", 5001, "update_episodes", models.PriorityAiring)
	_ = d.EnqueueTask("movie", 6001, "reconcile_movie", models.PriorityReconcile)

	detailedStats, err := d.GetDetailedQueueStats()
	if err != nil {
		t.Fatalf("GetDetailedQueueStats failed: %v", err)
	}
	if detailedStats.MovieAdds != 2 || detailedStats.SeriesUpdates != 1 || detailedStats.MovieReconciles != 1 {
		t.Errorf("Unexpected detailed stats: %+v", detailedStats)
	}
	if detailedStats.TotalMovies() != 3 || detailedStats.TotalSeries() != 1 {
		t.Errorf("Expected TotalMovies=3, TotalSeries=1; got %d, %d", detailedStats.TotalMovies(), detailedStats.TotalSeries())
	}

	// Test Delta Staging: First batch stages new movies and enqueues pass1 tasks
	newMovies := []models.Movie{
		{ID: 7001, TMDBID: "100", Name: "New Movie A", Year: 2024},
		{ID: 7002, TMDBID: "200", Name: "New Movie B", Year: 2024},
	}
	if err := d.BatchStageMovies(newMovies); err != nil {
		t.Fatalf("BatchStageMovies failed: %v", err)
	}

	// Verify movies in movie_state (1 from earlier test + 2 new)
	allMovies, err := d.GetAllMovieStates()
	if err != nil || len(allMovies) != 3 {
		t.Errorf("Expected 3 movies in GetAllMovieStates, got %d, err: %v", len(allMovies), err)
	}

	statsAfterFirstStage, _ := d.GetDetailedQueueStats()
	if statsAfterFirstStage.MovieAdds != 4 { // 2 original + 2 new
		t.Errorf("Expected 4 MovieAdds in queue, got %d", statsAfterFirstStage.MovieAdds)
	}

	// Second staging pass with same movies (should update state timestamp but NOT re-enqueue)
	if err := d.BatchStageMovies(newMovies); err != nil {
		t.Fatalf("BatchStageMovies second pass failed: %v", err)
	}

	statsAfterSecondStage, _ := d.GetDetailedQueueStats()
	if statsAfterSecondStage.MovieAdds != 4 {
		t.Errorf("Expected MovieAdds count to remain 4 (no duplicate enqueue), got %d", statsAfterSecondStage.MovieAdds)
	}

	// 10. Test Identifier Resolution and Self-Healing Fingerprints
	// Test TMDB identifier resolution
	resolvedMovieID, tmdbVal, err := d.GetMovieIDByIdentifier("tmdb:100")
	if err != nil || resolvedMovieID != 7001 || tmdbVal != "100" {
		t.Errorf("GetMovieIDByIdentifier tmdb:100 got (%d, %q, %v); want (7001, 100, nil)", resolvedMovieID, tmdbVal, err)
	}

	// Test numeric identifier resolution
	resolvedNumID, _, err := d.GetMovieIDByIdentifier("7002")
	if err != nil || resolvedNumID != 7002 {
		t.Errorf("GetMovieIDByIdentifier numeric got (%d, %v); want (7002, nil)", resolvedNumID, err)
	}

	// Test series identifier resolution
	resolvedSeriesID, seriesTMDB, err := d.GetSeriesIDByIdentifier("tmdb:61381")
	if err != nil || resolvedSeriesID != seriesID || seriesTMDB != "61381" {
		t.Errorf("GetSeriesIDByIdentifier tmdb:61381 got (%d, %q, %v); want (%d, 61381, nil)", resolvedSeriesID, seriesTMDB, err, seriesID)
	}

	// Test Self-Healing Fingerprint when Dispatcharr ID changes (7001 -> 8001 with same TMDB 100)
	if err := d.UpsertInitialMovieState(8001, "100", "", "New Movie A", 2024, "New Movie A (2024) [tmdbid=100]"); err != nil {
		t.Fatalf("Failed to upsert new movie state: %v", err)
	}
	healedMovieID, err := d.FindMovieByFingerprint(7001, "New Movie A", 2024)
	if err != nil || healedMovieID != 8001 {
		t.Errorf("FindMovieByFingerprint got (%d, %v); want (8001, nil)", healedMovieID, err)
	}

	// Test FindMovieSupersededState
	oldState, err := d.FindMovieSupersededState(8001, "100", "New Movie A", 2024)
	if err != nil || oldState == nil || oldState.MovieID != 7001 {
		t.Errorf("FindMovieSupersededState got (%+v, %v); want MovieID 7001", oldState, err)
	}
	if err := d.DeleteMovieState(7001); err != nil {
		t.Fatalf("DeleteMovieState failed: %v", err)
	}
	if st, err := d.GetMovieState(7001); err != nil || st != nil {
		t.Errorf("Expected movie 7001 to be deleted from database, got %+v (err: %v)", st, err)
	}
}
