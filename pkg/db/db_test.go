package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestDeduplicateSupersededStatesAndBatchStageIDMigration(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test_dedup.db")
	d, err := Open(tempDB)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	// 1. Manually insert duplicate movie records simulating past ID re-assignment cycles
	// Movie "Alone (2020)" with TMDB 509635 under IDs 1105078 and 1224326
	now := time.Now()
	_, _ = d.conn.Exec(`INSERT INTO movie_state (movie_id, tmdb_id, title, year, folder_name, is_activated, last_checked_at) VALUES (1105078, '509635', 'Alone', 2020, 'Alone (2020)', 1, ?)`, now)
	_, _ = d.conn.Exec(`INSERT INTO movie_state (movie_id, tmdb_id, title, year, folder_name, is_activated, last_checked_at) VALUES (1224326, '509635', 'Alone', 2020, 'Alone (2020) [tmdb-509635]', 0, ?)`, now)

	// Add a soft-deleted item matching the old folder name
	_, _ = d.SoftDeleteItem("/data/movies/Alone (2020)", "movie")

	// Verify before deduplication
	mvsBefore, _ := d.GetAllMovieStates()
	if len(mvsBefore) != 2 {
		t.Fatalf("Expected 2 movies before dedup, got %d", len(mvsBefore))
	}

	// 2. Run DeduplicateSupersededStates
	pruned, err := d.DeduplicateSupersededStates()
	if err != nil {
		t.Fatalf("DeduplicateSupersededStates failed: %v", err)
	}
	if pruned < 1 {
		t.Errorf("Expected at least 1 record pruned, got %d", pruned)
	}

	// Verify after deduplication
	mvsAfter, _ := d.GetAllMovieStates()
	if len(mvsAfter) != 1 {
		t.Fatalf("Expected exactly 1 movie after dedup, got %d", len(mvsAfter))
	}
	if mvsAfter[0].MovieID != 1224326 {
		t.Errorf("Expected retained movie to have highest ID 1224326, got %d", mvsAfter[0].MovieID)
	}
	if !mvsAfter[0].IsActivated {
		t.Errorf("Expected activation state (is_activated=1) to be transferred from old duplicate")
	}

	// Verify soft deleted items matching active movie was cleaned up
	softCount, _ := d.GetPendingSoftDeletedCount()
	if softCount != 0 {
		t.Errorf("Expected soft_deleted_items matching active movie to be pruned, got %d", softCount)
	}

	// 3. Test BatchStageMovies ID Migration when Dispatcharr issues new ID 1300000
	newBatch := []models.Movie{
		{
			ID:     1300000,
			Name:   "Alone",
			Year:   2020,
			TMDBID: "509635",
		},
	}
	if err := d.BatchStageMovies(newBatch); err != nil {
		t.Fatalf("BatchStageMovies failed: %v", err)
	}

	mvsFinal, _ := d.GetAllMovieStates()
	if len(mvsFinal) != 1 {
		t.Fatalf("Expected exactly 1 movie after BatchStageMovies ID migration, got %d", len(mvsFinal))
	}
	if mvsFinal[0].MovieID != 1300000 {
		t.Errorf("Expected movie_state to be migrated to new ID 1300000, got %d", mvsFinal[0].MovieID)
	}
	if !mvsFinal[0].IsActivated {
		t.Errorf("Expected activation state to be preserved across BatchStageMovies ID migration")
	}
}

func TestBatchStageSeriesActiveAiringEpisodeDetection(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test_airing.db")
	d, err := Open(tempDB)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	// Setup initial series state:
	// Series 1: Active, 10 episodes, updated at t1
	_ = d.UpsertInitialSeriesState(1001, "tmdb-1", "", "Active Show Timestamp", 2024, "Active Show Timestamp (2024) [tmdbid=tmdb-1]")
	_ = d.SetSeriesActivated(1001, "tmdb-1", "Active Show Timestamp")
	_ = d.MarkSeriesRefreshed(1001, 10, t1)

	// Series 2: Active, 5 episodes, updated at t1
	_ = d.UpsertInitialSeriesState(1002, "tmdb-2", "", "Active Show EpisodeCount", 2024, "Active Show EpisodeCount (2024) [tmdbid=tmdb-2]")
	_ = d.SetSeriesActivated(1002, "tmdb-2", "Active Show EpisodeCount")
	_ = d.MarkSeriesRefreshed(1002, 5, t1)

	// Series 3: Unactivated, 5 episodes, updated at t1
	_ = d.UpsertInitialSeriesState(1003, "tmdb-3", "", "Unactivated Show", 2024, "Unactivated Show (2024) [tmdbid=tmdb-3]")

	// Series 4: Active, 8 episodes, updated at t1 (will remain unchanged)
	_ = d.UpsertInitialSeriesState(1004, "tmdb-4", "", "Active Show Unchanged", 2024, "Active Show Unchanged (2024) [tmdbid=tmdb-4]")
	_ = d.SetSeriesActivated(1004, "tmdb-4", "Active Show Unchanged")
	_ = d.MarkSeriesRefreshed(1004, 8, t1)

	// Clear any residual queue items from setup
	_, _ = d.conn.Exec("DELETE FROM sync_queue")

	// Now stage a catalog batch:
	batch := []models.Series{
		{
			ID:           1001,
			Name:         "Active Show Timestamp",
			Year:         2024,
			TMDBID:       "tmdb-1",
			EpisodeCount: 10,
			UpdatedAt:    t2, // Newer timestamp
		},
		{
			ID:           1002,
			Name:         "Active Show EpisodeCount",
			Year:         2024,
			TMDBID:       "tmdb-2",
			EpisodeCount: 6,  // Increased episode count (5 -> 6)
			UpdatedAt:    t1, // Same timestamp
		},
		{
			ID:           1003,
			Name:         "Unactivated Show",
			Year:         2024,
			TMDBID:       "tmdb-3",
			EpisodeCount: 10,
			UpdatedAt:    t2, // Newer, but unactivated!
		},
		{
			ID:           1004,
			Name:         "Active Show Unchanged",
			Year:         2024,
			TMDBID:       "tmdb-4",
			EpisodeCount: 8,
			UpdatedAt:    t1, // Exactly the same
		},
	}

	if err := d.BatchStageSeries(batch); err != nil {
		t.Fatalf("BatchStageSeries failed: %v", err)
	}

	// Verify sync_queue:
	// Should have exactly 2 tasks for update_episodes: 1001 and 1002, both priority 30
	rows, err := d.conn.Query("SELECT item_id, action, priority FROM sync_queue WHERE action = 'update_episodes' ORDER BY item_id ASC")
	if err != nil {
		t.Fatalf("Failed to query sync_queue: %v", err)
	}
	defer rows.Close()

	type queueRow struct {
		id       int
		action   string
		priority int
	}
	var updateTasks []queueRow
	for rows.Next() {
		var r queueRow
		if err := rows.Scan(&r.id, &r.action, &r.priority); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		updateTasks = append(updateTasks, r)
	}

	if len(updateTasks) != 2 {
		t.Fatalf("Expected 2 update_episodes tasks, got %d: %+v", len(updateTasks), updateTasks)
	}
	if updateTasks[0].id != 1001 || updateTasks[0].priority != 30 {
		t.Errorf("Expected task for 1001 with priority 30, got %+v", updateTasks[0])
	}
	if updateTasks[1].id != 1002 || updateTasks[1].priority != 30 {
		t.Errorf("Expected task for 1002 with priority 30, got %+v", updateTasks[1])
	}

	// Verify unactivated show 1003 was NOT enqueued as update_episodes
	var unactCount int
	_ = d.conn.QueryRow("SELECT COUNT(*) FROM sync_queue WHERE item_id = 1003 AND action = 'update_episodes'").Scan(&unactCount)
	if unactCount != 0 {
		t.Errorf("Unactivated series 1003 should not have update_episodes task, got count %d", unactCount)
	}

	// Verify priority ordering in PopNextTask:
	// Add a lower priority batch task
	_ = d.EnqueueTask("series", 2000, "pass1", models.PriorityBatch) // Priority 10

	// Next task popped should be Priority 30 (1001 or 1002), not 2000
	nextTask, err := d.PopNextTask("series")
	if err != nil || nextTask == nil {
		t.Fatalf("PopNextTask failed: %v", err)
	}
	if nextTask.Priority != 30 {
		t.Errorf("Expected popped task priority 30, got %d (task %+v)", nextTask.Priority, nextTask)
	}
}

func TestGetNextSeriesEpisodes(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_db_nextep_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	d, err := Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	seriesID := 999
	// Seed episodes:
	// Season 1: Episodes 1, 2, 3
	// Season 2: Episodes 1, 2
	episodes := []struct {
		sNum, eNum int
		url        string
	}{
		{1, 1, "https://dp.example.com/s01e01.mkv"},
		{1, 2, "https://dp.example.com/s01e02.mkv"},
		{1, 3, "https://dp.example.com/s01e03.mkv"},
		{2, 1, "https://dp.example.com/s02e01.mkv"},
		{2, 2, "https://dp.example.com/s02e02.mkv"},
	}

	for _, ep := range episodes {
		if err := d.SaveStreamVariant(&StreamVariant{
			ItemID:     seriesID,
			ItemType:   "series",
			SeasonNum:  ep.sNum,
			EpisodeNum: ep.eNum,
			VariantKey: "base",
			TargetURL:  ep.url,
		}); err != nil {
			t.Fatalf("SaveStreamVariant failed: %v", err)
		}
	}

	// 1. Same season retrieval: from S01E01, count 2 -> S01E02, S01E03
	res, err := d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 2)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("Expected 2 episodes, got %d", len(res))
	}
	if res[0].SeasonNum != 1 || res[0].EpisodeNum != 2 || res[0].TargetURL != "https://dp.example.com/s01e02.mkv" {
		t.Errorf("Unexpected ep 0: %+v", res[0])
	}
	if res[1].SeasonNum != 1 || res[1].EpisodeNum != 3 || res[1].TargetURL != "https://dp.example.com/s01e03.mkv" {
		t.Errorf("Unexpected ep 1: %+v", res[1])
	}

	// 2. Season rollover: from S01E03, count 2 -> S02E01, S02E02
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 3, "base", 2)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("Expected 2 episodes on season rollover, got %d", len(res))
	}
	if res[0].SeasonNum != 2 || res[0].EpisodeNum != 1 || res[0].TargetURL != "https://dp.example.com/s02e01.mkv" {
		t.Errorf("Unexpected rollover ep 0: %+v", res[0])
	}
	if res[1].SeasonNum != 2 || res[1].EpisodeNum != 2 || res[1].TargetURL != "https://dp.example.com/s02e02.mkv" {
		t.Errorf("Unexpected rollover ep 1: %+v", res[1])
	}

	// 3. Boundary crossing: from S01E02, count 2 -> S01E03, S02E01
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 2, "base", 2)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("Expected 2 episodes crossing boundary, got %d", len(res))
	}
	if res[0].SeasonNum != 1 || res[0].EpisodeNum != 3 {
		t.Errorf("Unexpected ep 0: %+v", res[0])
	}
	if res[1].SeasonNum != 2 || res[1].EpisodeNum != 1 {
		t.Errorf("Unexpected ep 1: %+v", res[1])
	}

	// 4. Series finale / end of series: from S02E02, count 2 -> 0 episodes
	res, err = d.GetNextSeriesEpisodes(seriesID, 2, 2, "base", 2)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("Expected 0 episodes at end of series, got %d", len(res))
	}

	// 5. Count <= 0 returns nil
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 0)
	if err != nil || len(res) != 0 {
		t.Errorf("Expected nil/empty for count 0, got len %d, err %v", len(res), err)
	}

	// 6. Variant-aware retrieval with per-episode fallback:
	// Add 4K variants for S01E02 and S02E01 (S01E03 only has base)
	_ = d.SaveStreamVariant(&StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 2,
		VariantKey: "4k",
		TargetURL:  "https://dp.example.com/s01e02_4k.mkv",
	})
	_ = d.SaveStreamVariant(&StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  2,
		EpisodeNum: 1,
		VariantKey: "4k",
		TargetURL:  "https://dp.example.com/s02e01_4k.mkv",
	})

	// From S01E01 with variant "4k", count 3:
	// Ep 1 (S01E02): has 4k -> https://dp.example.com/s01e02_4k.mkv
	// Ep 2 (S01E03): only base -> https://dp.example.com/s01e03.mkv (fallback)
	// Ep 3 (S02E01): has 4k -> https://dp.example.com/s02e01_4k.mkv
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 1, "4k", 3)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes 4k failed: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("Expected 3 episodes, got %d", len(res))
	}
	if res[0].TargetURL != "https://dp.example.com/s01e02_4k.mkv" {
		t.Errorf("Ep 0 want 4k URL, got %s", res[0].TargetURL)
	}
	if res[1].TargetURL != "https://dp.example.com/s01e03.mkv" {
		t.Errorf("Ep 1 want base fallback URL, got %s", res[1].TargetURL)
	}
	if res[2].TargetURL != "https://dp.example.com/s02e01_4k.mkv" {
		t.Errorf("Ep 2 want 4k URL, got %s", res[2].TargetURL)
	}
}

func TestStreamVariantTappedTracking(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_db_tap_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	d, err := Open(filepath.Join(tempDir, "tap_test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	seriesID := 777
	if err := d.SaveStreamVariant(&StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 1,
		VariantKey: "base",
		TargetURL:  "https://dp.example.com/s01e01.mkv",
	}); err != nil {
		t.Fatalf("SaveStreamVariant failed: %v", err)
	}
	if err := d.SaveStreamVariant(&StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 2,
		VariantKey: "base",
		TargetURL:  "https://dp.example.com/s01e02.mkv",
	}); err != nil {
		t.Fatalf("SaveStreamVariant failed: %v", err)
	}

	// 1. Initial state: LastTappedAt should be nil
	res, err := d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 1)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Expected 1 episode, got %d", len(res))
	}
	if res[0].LastTappedAt != nil {
		t.Errorf("Expected LastTappedAt to be nil initially, got %v", res[0].LastTappedAt)
	}

	// 2. Mark tapped at now
	tapTime := time.Now().Truncate(time.Second)
	if err := d.MarkStreamVariantTapped(seriesID, 1, 2, "base", tapTime); err != nil {
		t.Fatalf("MarkStreamVariantTapped failed: %v", err)
	}

	// 3. Query again: LastTappedAt should be set
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 1)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if res[0].LastTappedAt == nil {
		t.Fatalf("Expected LastTappedAt to be populated, got nil")
	}
	if res[0].LastTappedAt.Unix() != tapTime.Unix() {
		t.Errorf("Expected LastTappedAt %v, got %v", tapTime, *res[0].LastTappedAt)
	}

	// Also verify direct lookup via GetSeriesEpisodeLastTappedAt
	directTapped, dErr := d.GetSeriesEpisodeLastTappedAt(seriesID, 1, 2, "base")
	if dErr != nil {
		t.Fatalf("GetSeriesEpisodeLastTappedAt failed: %v", dErr)
	}
	if directTapped == nil || directTapped.Unix() != tapTime.Unix() {
		t.Errorf("GetSeriesEpisodeLastTappedAt got %v, want %v", directTapped, tapTime)
	}

	// 4. SaveStreamVariant on the same episode should PRESERVE last_tapped_at
	if err := d.SaveStreamVariant(&StreamVariant{
		ItemID:     seriesID,
		ItemType:   "series",
		SeasonNum:  1,
		EpisodeNum: 2,
		VariantKey: "base",
		TargetURL:  "https://dp.example.com/s01e02_updated.mkv",
	}); err != nil {
		t.Fatalf("SaveStreamVariant update failed: %v", err)
	}
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 1)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if res[0].LastTappedAt == nil || res[0].LastTappedAt.Unix() != tapTime.Unix() {
		t.Errorf("SaveStreamVariant should preserve LastTappedAt, got %v", res[0].LastTappedAt)
	}

	// 5. Clear tap timestamp with zero time
	if err := d.MarkStreamVariantTapped(seriesID, 1, 2, "base", time.Time{}); err != nil {
		t.Fatalf("MarkStreamVariantTapped clear failed: %v", err)
	}
	res, err = d.GetNextSeriesEpisodes(seriesID, 1, 1, "base", 1)
	if err != nil {
		t.Fatalf("GetNextSeriesEpisodes failed: %v", err)
	}
	if res[0].LastTappedAt != nil {
		t.Errorf("Expected LastTappedAt to be nil after clearing, got %v", res[0].LastTappedAt)
	}
}

func TestBatchStageMetadataUpgrade(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_db_meta_upgrade_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	d, err := Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	// 1. Initial movie without TMDB ID (legacy folder)
	_ = d.UpsertInitialMovieState(5001, "", "", "Girl from the North Country", 2024, "Girl from the North Country (2024)")
	_, _ = d.conn.Exec("DELETE FROM sync_queue")

	// 2. Batch stage movie with TMDB ID upgrade
	upgradedBatch := []models.Movie{
		{
			ID:     5001,
			Name:   "Girl from the North Country (2024)",
			Year:   2024,
			TMDBID: "1083417",
		},
	}
	if err := d.BatchStageMovies(upgradedBatch); err != nil {
		t.Fatalf("BatchStageMovies failed: %v", err)
	}

	// Verify movie_state folder_name upgraded
	st, err := d.GetMovieState(5001)
	if err != nil || st == nil {
		t.Fatalf("GetMovieState failed: %v", err)
	}
	expectedFolder := "Girl from the North Country (2024) [tmdbid=1083417]"
	if st.FolderName != expectedFolder {
		t.Errorf("Expected upgraded folder_name %q, got %q", expectedFolder, st.FolderName)
	}
	if st.TMDBID != "1083417" {
		t.Errorf("Expected TMDB ID 1083417, got %q", st.TMDBID)
	}

	// Verify reconcile_movie task was enqueued
	var reconCount int
	_ = d.conn.QueryRow("SELECT COUNT(*) FROM sync_queue WHERE item_id = 5001 AND action = 'reconcile_movie'").Scan(&reconCount)
	if reconCount != 1 {
		t.Errorf("Expected 1 reconcile_movie task, got %d", reconCount)
	}

	// 3. Test downgrade protection: incoming batch with missing TMDB ID must NOT overwrite canonical folder
	downgradeBatch := []models.Movie{
		{
			ID:     5001,
			Name:   "Girl from the North Country",
			Year:   2024,
			TMDBID: "",
		},
	}
	if err := d.BatchStageMovies(downgradeBatch); err != nil {
		t.Fatalf("BatchStageMovies downgrade failed: %v", err)
	}
	stDowngrade, _ := d.GetMovieState(5001)
	if stDowngrade.FolderName != expectedFolder {
		t.Errorf("Downgrade protection failed! Expected folder %q, got %q", expectedFolder, stDowngrade.FolderName)
	}
}

func TestDeduplicateQueueAndGetQueuedItemIDsMap(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "dp2strm_db_dedup_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	d, err := Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer d.Close()

	// 1. Enqueue pass1 for movie 101, series 201, movie 102
	_ = d.EnqueueTask("movie", 101, "pass1", 10)
	_ = d.EnqueueTask("series", 201, "pass1", 10)
	_ = d.EnqueueTask("movie", 102, "pass1", 10)

	// 2. Enqueue reconcile_movie for movie 101, and reconcile_series for series 201
	_ = d.EnqueueTask("movie", 101, "reconcile_movie", 50)
	_ = d.EnqueueTask("series", 201, "reconcile_series", 50)

	// Verify GetQueuedItemIDsMap returns both items
	mMap, err := d.GetQueuedItemIDsMap("movie")
	if err != nil {
		t.Fatalf("GetQueuedItemIDsMap movie failed: %v", err)
	}
	if !mMap[101] || !mMap[102] || mMap[999] {
		t.Errorf("Unexpected movie queue map: %+v", mMap)
	}

	sMap, err := d.GetQueuedItemIDsMap("series")
	if err != nil {
		t.Fatalf("GetQueuedItemIDsMap series failed: %v", err)
	}
	if !sMap[201] || sMap[101] {
		t.Errorf("Unexpected series queue map: %+v", sMap)
	}

	// 3. Run DeduplicateQueue
	pruned, err := d.DeduplicateQueue()
	if err != nil {
		t.Fatalf("DeduplicateQueue failed: %v", err)
	}
	// Should have pruned pass1 for movie 101 and series 201 (total 2)
	if pruned != 2 {
		t.Errorf("Expected 2 pruned tasks, got %d", pruned)
	}

	// Movie 102 should still have pass1
	pending, _, _, err := d.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}
	if pending != 3 { // movie 102 pass1, movie 101 reconcile_movie, series 201 reconcile_series
		t.Errorf("Expected 3 pending tasks after dedup, got %d", pending)
	}
}



