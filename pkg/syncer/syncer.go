package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dp2strm/pkg/artwork"
	"dp2strm/pkg/client"
	"dp2strm/pkg/db"
	"dp2strm/pkg/models"
	"dp2strm/pkg/nfo"
	"dp2strm/pkg/sanitizer"
	"dp2strm/pkg/strm"
	"dp2strm/pkg/versioning"
)

type Syncer struct {
	cfg            *models.Config
	client         *client.Client
	database       *db.DB
	circuitTripped bool
	mu             sync.Mutex
}

func NewSyncer(cfg *models.Config, client *client.Client, database *db.DB) *Syncer {
	return &Syncer{
		cfg:      cfg,
		client:   client,
		database: database,
	}
}

func (s *Syncer) isCircuitTripped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.circuitTripped
}

func (s *Syncer) tripCircuitBreaker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.circuitTripped {
		s.circuitTripped = true
		log.Println("[Syncer] ⚠️ Circuit breaker tripped due to HTTP 429 Rate Limit from Dispatcharr! Halting current sync pass.")
	}
}

func (s *Syncer) resetCircuitBreaker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.circuitTripped = false
}

// SyncMovies executes a full movie sync pass from Dispatcharr REST API to disk.
func (s *Syncer) SyncMovies(ctx context.Context, dryRun bool) error {
	created, err := s.SyncMoviesWithLimit(ctx, s.cfg.BatchSize, dryRun)
	if err == nil && created > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}
	return err
}

// SyncMoviesWithLimit executes a movie sync pass, creating up to maxNewItems new .strm files on disk.
func (s *Syncer) SyncMoviesWithLimit(ctx context.Context, maxNewItems int, dryRun bool) (int, error) {
	log.Printf("[Syncer] Starting Parallel Movie Sync Pass (Max New Items: %d)...", maxNewItems)
	start := time.Now()
	s.resetCircuitBreaker()

	pageSize := s.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	concurrency := s.cfg.MaxProbeConcurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	// Fetch Page 1 to determine total count & pages
	log.Printf("[Syncer] Fetching movie page 1 (size %d)...", pageSize)
	apiStart := time.Now()
	firstResp, err := s.client.FetchMoviePage(ctx, 1, pageSize)
	apiDuration := time.Since(apiStart)
	if err != nil {
		if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
			s.tripCircuitBreaker()
		}
		return 0, fmt.Errorf("failed to fetch movie page 1: %w", err)
	}
	log.Printf("[Syncer] Movie Page 1 API Fetch took %v", apiDuration)

	if len(firstResp.Results) == 0 {
		log.Println("[Syncer] Movie Sync Complete: 0 movies found.")
		return 0, nil
	}

	totalCount := firstResp.Count
	if totalCount <= 0 {
		totalCount = len(firstResp.Results)
	}
	actualPageSize := len(firstResp.Results)
	if actualPageSize <= 0 {
		actualPageSize = pageSize
	}
	totalPages := (totalCount + actualPageSize - 1) / actualPageSize

	log.Printf("[Syncer] Found %d total movies across %d pages.", totalCount, totalPages)

	var totalProcessed int64
	var totalCreated int64
	var totalPreserved int64
	var newStrmCreated int64

	processPage := func(pageNum int, movies []models.Movie) {
		if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
			return
		}
		if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
			return
		}
		atomic.AddInt64(&totalProcessed, int64(len(movies)))
		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)

		var pCreated, pPreserved int64
		var pArtDur, pDiskDur time.Duration
		var mu sync.Mutex

		pStart := time.Now()
		for _, m := range movies {
			if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
				break
			}
			if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(movie models.Movie) {
				defer func() {
					<-sem
					wg.Done()
				}()
				if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
					return
				}
				if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
					return
				}
				c, p, isNewStrm, aDur, dDur, err := s.processSingleMovieProfiled(ctx, movie, dryRun)
				if err != nil {
					if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
						s.tripCircuitBreaker()
					}
					log.Printf("[Syncer] Warning: Error processing movie '%s' (ID %d): %v", movie.Name, movie.ID, err)
					return
				}
				if isNewStrm {
					atomic.AddInt64(&newStrmCreated, 1)
				}
				mu.Lock()
				pCreated += int64(c)
				pPreserved += int64(p)
				pArtDur += aDur
				pDiskDur += dDur
				mu.Unlock()
			}(m)
		}
		wg.Wait()
		pDuration := time.Since(pStart)

		atomic.AddInt64(&totalCreated, pCreated)
		atomic.AddInt64(&totalPreserved, pPreserved)

		log.Printf("[Syncer] Movie Page %d/%d Complete (%d items) in %v | Processing: %v (Artwork: %v, Disk: %v)",
			pageNum, totalPages, len(movies), pDuration, pDuration, pArtDur, pDiskDur)
	}

	// Process Page 1 immediately
	processPage(1, firstResp.Results)

	// Fetch & process remaining pages using parallel page workers
	if totalPages > 1 && (!s.cfg.CircuitBreakerEnabled || !s.isCircuitTripped()) {
		if maxNewItems <= 0 || atomic.LoadInt64(&newStrmCreated) < int64(maxNewItems) {
			pageWorkers := s.cfg.PageWorkers
			if pageWorkers <= 0 {
				pageWorkers = 5
			}
			pageChan := make(chan int, totalPages)
			for p := 2; p <= totalPages; p++ {
				pageChan <- p
			}
			close(pageChan)

			var pageWg sync.WaitGroup
			for w := 0; w < pageWorkers; w++ {
				pageWg.Add(1)
				go func() {
					defer pageWg.Done()
					for pageNum := range pageChan {
						if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
							return
						}
						if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
							return
						}
						fStart := time.Now()
						resp, err := s.client.FetchMoviePage(ctx, pageNum, pageSize)
						fDuration := time.Since(fStart)
						if err != nil {
							if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
								s.tripCircuitBreaker()
							}
							log.Printf("[Syncer] Warning: Error fetching movie page %d: %v", pageNum, err)
							continue
						}
						if len(resp.Results) > 0 {
							processPage(pageNum, resp.Results)
							log.Printf("[Syncer] Movie Page %d API Fetch took %v", pageNum, fDuration)
						}
					}
				}()
			}
			pageWg.Wait()
		}
	}

	duration := time.Since(start)
	createdCount := int(atomic.LoadInt64(&newStrmCreated))
	log.Printf("[Syncer] Movie Sync Complete in %v: %d movies processed, %d new movies created (%d total files created/updated, %d preserved).",
		duration, totalProcessed, createdCount, totalCreated, totalPreserved)

	return createdCount, nil
}

// SyncSeries Pass 1: Generates initial lightweight TV series tree with S01E01 Activation placeholder.
func (s *Syncer) SyncSeries(ctx context.Context, dryRun bool) error {
	created, err := s.SyncSeriesWithLimit(ctx, s.cfg.BatchSize, dryRun)
	if err == nil && created > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}
	return err
}

// SyncSeriesWithLimit executes Pass 1 series sync, creating up to maxNewItems new .strm files on disk.
func (s *Syncer) SyncSeriesWithLimit(ctx context.Context, maxNewItems int, dryRun bool) (int, error) {
	log.Printf("[Syncer] Starting Pass 1 Parallel TV Series Sync Pass (Max New Items: %d)...", maxNewItems)
	start := time.Now()
	s.resetCircuitBreaker()

	pageSize := s.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	concurrency := s.cfg.MaxProbeConcurrency
	if concurrency <= 0 {
		concurrency = 5
	}

	// Fetch Page 1 to determine total count & pages
	log.Printf("[Syncer] Fetching series page 1 (size %d)...", pageSize)
	apiStart := time.Now()
	firstResp, err := s.client.FetchSeriesPage(ctx, 1, pageSize)
	apiDuration := time.Since(apiStart)
	if err != nil {
		if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
			s.tripCircuitBreaker()
		}
		return 0, fmt.Errorf("failed to fetch series page 1: %w", err)
	}
	log.Printf("[Syncer] Series Page 1 API Fetch took %v", apiDuration)

	if len(firstResp.Results) == 0 {
		log.Println("[Syncer] Pass 1 Series Sync Complete: 0 series found.")
		return 0, nil
	}

	totalCount := firstResp.Count
	if totalCount <= 0 {
		totalCount = len(firstResp.Results)
	}
	actualPageSize := len(firstResp.Results)
	if actualPageSize <= 0 {
		actualPageSize = pageSize
	}
	totalPages := (totalCount + actualPageSize - 1) / actualPageSize

	log.Printf("[Syncer] Found %d total series across %d pages.", totalCount, totalPages)

	var totalProcessed int64
	var totalCreated int64
	var totalPreserved int64
	var newStrmCreated int64

	processPage := func(pageNum int, seriesList []models.Series) {
		if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
			return
		}
		if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
			return
		}
		atomic.AddInt64(&totalProcessed, int64(len(seriesList)))
		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)

		var pCreated, pPreserved int64
		var pArtDur, pDiskDur time.Duration
		var mu sync.Mutex

		pStart := time.Now()
		for _, srs := range seriesList {
			if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
				break
			}
			if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(series models.Series) {
				defer func() {
					<-sem
					wg.Done()
				}()
				if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
					return
				}
				if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
					return
				}
				c, p, isNewStrm, aDur, dDur, err := s.processSingleSeriesPass1Profiled(ctx, series, dryRun)
				if err != nil {
					if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
						s.tripCircuitBreaker()
					}
					log.Printf("[Syncer] Warning: Error processing series '%s' (ID %d): %v", series.Name, series.ID, err)
					return
				}
				if isNewStrm {
					atomic.AddInt64(&newStrmCreated, 1)
				}
				mu.Lock()
				pCreated += int64(c)
				pPreserved += int64(p)
				pArtDur += aDur
				pDiskDur += dDur
				mu.Unlock()
			}(srs)
		}
		wg.Wait()
		pDuration := time.Since(pStart)

		atomic.AddInt64(&totalCreated, pCreated)
		atomic.AddInt64(&totalPreserved, pPreserved)

		log.Printf("[Syncer] Series Page %d/%d Complete (%d items) in %v | Processing: %v (Artwork: %v, Disk: %v)",
			pageNum, totalPages, len(seriesList), pDuration, pDuration, pArtDur, pDiskDur)
	}

	// Process Page 1 immediately
	processPage(1, firstResp.Results)

	// Fetch & process remaining pages using parallel page workers
	if totalPages > 1 && (!s.cfg.CircuitBreakerEnabled || !s.isCircuitTripped()) {
		if maxNewItems <= 0 || atomic.LoadInt64(&newStrmCreated) < int64(maxNewItems) {
			pageWorkers := s.cfg.PageWorkers
			if pageWorkers <= 0 {
				pageWorkers = 5
			}
			pageChan := make(chan int, totalPages)
			for p := 2; p <= totalPages; p++ {
				pageChan <- p
			}
			close(pageChan)

			var pageWg sync.WaitGroup
			for w := 0; w < pageWorkers; w++ {
				pageWg.Add(1)
				go func() {
					defer pageWg.Done()
					for pageNum := range pageChan {
						if s.cfg.CircuitBreakerEnabled && s.isCircuitTripped() {
							return
						}
						if maxNewItems > 0 && atomic.LoadInt64(&newStrmCreated) >= int64(maxNewItems) {
							return
						}
						fStart := time.Now()
						resp, err := s.client.FetchSeriesPage(ctx, pageNum, pageSize)
						fDuration := time.Since(fStart)
						if err != nil {
							if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
								s.tripCircuitBreaker()
							}
							log.Printf("[Syncer] Warning: Error fetching series page %d: %v", pageNum, err)
							continue
						}
						if len(resp.Results) > 0 {
							processPage(pageNum, resp.Results)
							log.Printf("[Syncer] Series Page %d API Fetch took %v", pageNum, fDuration)
						}
					}
				}()
			}
			pageWg.Wait()
		}
	}

	duration := time.Since(start)
	createdCount := int(atomic.LoadInt64(&newStrmCreated))
	log.Printf("[Syncer] Pass 1 Series Sync Complete in %v: %d series processed, %d new series created (%d total files created/updated, %d preserved).",
		duration, totalProcessed, createdCount, totalCreated, totalPreserved)

	return createdCount, nil
}

// SyncSharedPass coordinates a combined Movies and TV Series sync with shared BATCH_SIZE allocation,
// dynamic quota rollover, and a single unified Emby library refresh.
func (s *Syncer) SyncSharedPass(ctx context.Context, dryRun bool) error {
	totalBudget := s.cfg.BatchSize
	log.Printf("[Syncer] === Starting Shared Sync Pass (Shared Budget: %d) ===", totalBudget)

	if totalBudget <= 0 {
		// Unlimited mode: sync all movies and all series
		var totalNew int
		mCreated, err := s.SyncMoviesWithLimit(ctx, 0, dryRun)
		if err != nil {
			log.Printf("[Syncer] Error during movie sync: %v", err)
		}
		totalNew += mCreated

		sCreated, err := s.SyncSeriesWithLimit(ctx, 0, dryRun)
		if err != nil {
			log.Printf("[Syncer] Error during series sync: %v", err)
		}
		totalNew += sCreated

		if totalNew > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
			s.triggerEmbyRefresh(ctx)
		}
		return nil
	}

	// 1. Initial Quota Split (50% Movies, 50% Series)
	movieQuota := totalBudget / 2
	if movieQuota <= 0 {
		movieQuota = 1
	}
	seriesQuota := totalBudget - movieQuota

	// 2. Phase 1: Sync Movies with initial quota
	log.Printf("[Syncer] Phase 1: Syncing Movies (Target Quota: %d)...", movieQuota)
	moviesCreated, err := s.SyncMoviesWithLimit(ctx, movieQuota, dryRun)
	if err != nil {
		log.Printf("[Syncer] Warning: Movie sync encountered error: %v", err)
	}

	// 3. Phase 2: Sync Series with SeriesQuota + unused Movie budget
	unusedMovieBudget := movieQuota - moviesCreated
	if unusedMovieBudget < 0 {
		unusedMovieBudget = 0
	}
	seriesBudget := seriesQuota + unusedMovieBudget
	log.Printf("[Syncer] Phase 2: Syncing Series (Target Quota: %d, includes %d rollover)...", seriesBudget, unusedMovieBudget)
	seriesCreated, sErr := s.SyncSeriesWithLimit(ctx, seriesBudget, dryRun)
	if sErr != nil {
		log.Printf("[Syncer] Warning: Series sync encountered error: %v", sErr)
	}

	// 4. Phase 3: Movie Top-Off (if Series had leftover budget)
	totalCreated := moviesCreated + seriesCreated
	if totalCreated < totalBudget && moviesCreated >= movieQuota {
		remainingBudget := totalBudget - totalCreated
		log.Printf("[Syncer] Phase 3: Top-Off Movies with remaining budget (%d)...", remainingBudget)
		topOffCreated, _ := s.SyncMoviesWithLimit(ctx, remainingBudget, dryRun)
		moviesCreated += topOffCreated
		totalCreated = moviesCreated + seriesCreated
	}

	log.Printf("[Syncer] === Shared Sync Pass Complete: %d new movies, %d new series (%d / %d total budget used) ===",
		moviesCreated, seriesCreated, totalCreated, totalBudget)

	// 5. Unified Emby Library Refresh
	if totalCreated > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}

	return nil
}

func (s *Syncer) processSingleSeriesPass1(ctx context.Context, series models.Series, dryRun bool) (created, preserved int, err error) {
	c, p, _, _, _, err := s.processSingleSeriesPass1Profiled(ctx, series, dryRun)
	return c, p, err
}

func (s *Syncer) processSingleSeriesPass1Profiled(ctx context.Context, series models.Series, dryRun bool) (created, preserved int, strmCreated bool, artDur, diskDur time.Duration, err error) {
	cleanTitle := sanitizer.CleanTitle(series.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, series.Year, series.TMDBID, series.IMDBID)
	seriesDir := filepath.Join(s.cfg.OutputDir, "TV Shows", folderName)

	if s.database != nil {
		_ = s.database.UpsertInitialSeriesState(series.ID, series.TMDBID, series.IMDBID, cleanTitle, series.Year, folderName)
	}

	if dryRun {
		log.Printf("[DRY-RUN] Pass 1 Series '%s' (TMDB %s) -> Folder: %s", cleanTitle, series.TMDBID, folderName)
		return 1, 0, true, 0, 0, nil
	}

	diskStart := time.Now()
	// 1. Write or purge tvshow.nfo based on EnableNFO
	tvshowNFOPath := filepath.Join(seriesDir, "tvshow.nfo")
	if s.cfg.EnableNFO {
		nfoUpdated, err := nfo.WriteTVShowNFO(tvshowNFOPath, series)
		if err == nil {
			if nfoUpdated {
				created++
			} else {
				preserved++
			}
		}
	} else {
		if _, statErr := os.Stat(tvshowNFOPath); statErr == nil {
			_ = os.Remove(tvshowNFOPath)
		}
	}

	// Check if this series has already been activated by user; if so, keep all episodes up-to-date
	if s.database != nil {
		st, _ := s.database.GetSeriesState(series.ID)
		if st != nil && st.IsActivated {
			c, p, err := s.PopulateActivatedSeries(ctx, series.ID, cleanTitle, seriesDir)
			diskDur = time.Since(diskStart)
			return c, p, c > 0, 0, diskDur, err
		}
	}

	// 2. Write Pass 1 Season 01 S01E01 Activation placeholder (always generated in Pass 1 to display activation message)
	season1Dir := filepath.Join(seriesDir, "Season 01")

	// TODO: Legacy migration cleanup - auto-cleans any old "... - S01E01 - Play to Activate Series.*" placeholder files from early pre-v1.0 builds. Can be safely removed in a future release.
	if entries, readErr := os.ReadDir(season1Dir); readErr == nil {
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "Play to Activate Series") {
				_ = os.Remove(filepath.Join(season1Dir, entry.Name()))
			}
		}
	}

	actStrmFilename := fmt.Sprintf("%s - S01E01.strm", sanitizer.SanitizeFileName(cleanTitle))
	actStrmPath := filepath.Join(season1Dir, actStrmFilename)

	actNFOFilename := fmt.Sprintf("%s - S01E01.nfo", sanitizer.SanitizeFileName(cleanTitle))
	actNFOPath := filepath.Join(season1Dir, actNFOFilename)

	actProxyURL := strm.GenerateSeriesProxyURL(s.cfg, series.ID, 1, 1, "base")

	strmUpdated, err := strm.WriteSTRMFile(actStrmPath, actProxyURL)
	if err == nil {
		if strmUpdated {
			created++
			strmCreated = true
		} else {
			preserved++
		}
	}

	actPlot := fmt.Sprintf("Play this episode to dynamically populate all seasons and episodes for %s in Emby.", cleanTitle)
	nfoUpdated, err := nfo.WriteEpisodeNFO(actNFOPath, "Play to Activate Series", 1, 1, actPlot, series.TMDBID)
	if err == nil {
		if nfoUpdated {
			created++
		} else {
			preserved++
		}
	}
	diskDur = time.Since(diskStart)

	// 3. Download poster.jpg & fanart.jpg
	artStart := time.Now()
	artCreated := artwork.DownloadSeriesArtwork(seriesDir, series, s.cfg)
	artDur = time.Since(artStart)
	created += artCreated

	return created, preserved, strmCreated, artDur, diskDur, nil
}

// PopulateActivatedSeries (Pass 2): Loads all episodes from Dispatcharr and writes real .strm files on disk.
func (s *Syncer) PopulateActivatedSeries(ctx context.Context, seriesID int, cleanTitle, seriesDir string) (created, preserved int, err error) {
	// Auto-resolve canonical seriesDir and cleanTitle if not provided
	if seriesDir == "" || cleanTitle == "" || strings.HasPrefix(cleanTitle, "Series ") {
		if s.database != nil {
			st, _ := s.database.GetSeriesState(seriesID)
			if st != nil && st.FolderName != "" {
				seriesDir = filepath.Join(s.cfg.OutputDir, "TV Shows", st.FolderName)
				cleanTitle = st.Title
			}
		}
		if seriesDir == "" || cleanTitle == "" || strings.HasPrefix(cleanTitle, "Series ") {
			if srs, fetchErr := s.client.FetchSingleSeries(ctx, seriesID); fetchErr == nil && srs != nil {
				cleanTitle = sanitizer.CleanTitle(srs.Name)
				folderName := sanitizer.BuildMovieFolderName(cleanTitle, srs.Year, srs.TMDBID, srs.IMDBID)
				seriesDir = filepath.Join(s.cfg.OutputDir, "TV Shows", folderName)
				if s.database != nil {
					_ = s.database.UpsertInitialSeriesState(seriesID, srs.TMDBID, srs.IMDBID, cleanTitle, srs.Year, folderName)
				}
			}
		}
	}
	if seriesDir == "" {
		seriesDir = filepath.Join(s.cfg.OutputDir, "TV Shows", fmt.Sprintf("Series %d", seriesID))
	}
	if cleanTitle == "" {
		cleanTitle = fmt.Sprintf("Series %d", seriesID)
	}

	episodes, err := s.client.FetchSeriesEpisodes(ctx, seriesID)
	if err != nil {
		if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
			s.tripCircuitBreaker()
		}
		return 0, 0, fmt.Errorf("failed to fetch episodes for series ID %d: %w", seriesID, err)
	}

	// If NFO is disabled, clean up tvshow.nfo if present
	if !s.cfg.EnableNFO {
		tvshowNFOPath := filepath.Join(seriesDir, "tvshow.nfo")
		if _, statErr := os.Stat(tvshowNFOPath); statErr == nil {
			_ = os.Remove(tvshowNFOPath)
		}
	}

	// TODO: Legacy migration cleanup - auto-cleans any old "... - S01E01 - Play to Activate Series.*" placeholder files from early pre-v1.0 builds. Can be safely removed in a future release.
	season1Dir := filepath.Join(seriesDir, "Season 01")
	if entries, readErr := os.ReadDir(season1Dir); readErr == nil {
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "Play to Activate Series") {
				_ = os.Remove(filepath.Join(season1Dir, entry.Name()))
			}
		}
	}

	hasS01E01 := false
	for _, ep := range episodes {
		if ep.SeasonNumber == 1 && ep.EpisodeNumber == 1 {
			hasS01E01 = true
			break
		}
	}

	// If show does not actually have Season 1 Episode 1 upstream (e.g. only Season 2+), clean up Pass 1 placeholder
	if !hasS01E01 {
		placeholderStrm := filepath.Join(season1Dir, fmt.Sprintf("%s - S01E01.strm", sanitizer.SanitizeFileName(cleanTitle)))
		placeholderNFO := filepath.Join(season1Dir, fmt.Sprintf("%s - S01E01.nfo", sanitizer.SanitizeFileName(cleanTitle)))
		_ = os.Remove(placeholderStrm)
		_ = os.Remove(placeholderNFO)
		_ = os.Remove(season1Dir)
	}

	for _, ep := range episodes {
		seasonDir := filepath.Join(seriesDir, fmt.Sprintf("Season %02d", ep.SeasonNumber))
		filename := fmt.Sprintf("%s - S%02dE%02d.strm", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber)
		strmPath := filepath.Join(seasonDir, filename)

		nfoFilename := fmt.Sprintf("%s - S%02dE%02d.nfo", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber)
		nfoPath := filepath.Join(seasonDir, nfoFilename)

		proxyURL := strm.GenerateSeriesProxyURL(s.cfg, seriesID, ep.SeasonNumber, ep.EpisodeNumber, "base")

		ext := ep.ContainerExtension
		if ext == "" {
			ext = "mkv"
		}

		// Record real stream 302 target URL in SQLite
		var realTargetURL string
		if ep.UUID != "" && s.cfg.APIKey != "" {
			realTargetURL = fmt.Sprintf("%s/proxy/vod/episode/%s?api_key=%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), ep.UUID, s.cfg.APIKey)
		} else {
			realTargetURL = fmt.Sprintf("%s/series/%s/%s/%d.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, ep.ID, ext)
		}
		if s.database != nil {
			s.database.SaveStreamVariant(&db.StreamVariant{
				ItemID:             seriesID,
				ItemType:           "series",
				SeasonNum:          ep.SeasonNumber,
				EpisodeNum:         ep.EpisodeNumber,
				VariantKey:         "base",
				TargetURL:          realTargetURL,
				Quality:            "1080p",
				StreamID:           fmt.Sprintf("%d", ep.ID),
				ContainerExtension: ext,
			})
		}

		updated, err := strm.WriteSTRMFile(strmPath, proxyURL)
		if err != nil {
			log.Printf("[Syncer] Error writing episode STRM %s: %v", strmPath, err)
			continue
		}

		if updated {
			created++
		} else {
			preserved++
		}

		if s.cfg.EnableNFO {
			// Write or update Episode NFO with real title & plot when NFOs are enabled
			epPlot := ep.Description
			if epPlot == "" {
				epPlot = fmt.Sprintf("Episode %d of %s Season %d", ep.EpisodeNumber, cleanTitle, ep.SeasonNumber)
			}
			epTitle := ep.Name
			if epTitle == "" {
				epTitle = fmt.Sprintf("Episode %d", ep.EpisodeNumber)
			} else {
				epTitle = sanitizer.CleanEpisodeTitle(ep.Name, cleanTitle, ep.SeasonNumber, ep.EpisodeNumber)
			}
			nUpdated, nErr := nfo.WriteEpisodeNFO(nfoPath, epTitle, ep.SeasonNumber, ep.EpisodeNumber, epPlot, ep.TMDBID)
			if nErr == nil {
				if nUpdated {
					created++
				} else {
					preserved++
				}
			}
		} else {
			// When NFO is disabled, remove any existing episode NFO (like Pass 1 S01E01 activation NFO)
			if _, statErr := os.Stat(nfoPath); statErr == nil {
				_ = os.Remove(nfoPath)
			}
			// Touch S01E01.strm when removing its NFO so Emby automatically fetches fresh metadata from TMDB
			if ep.SeasonNumber == 1 && ep.EpisodeNumber == 1 {
				now := time.Now()
				_ = os.Chtimes(strmPath, now, now)
			}
		}
	}

	// Record series as activated in SQLite
	if s.database != nil {
		s.database.SetSeriesActivated(seriesID, "", cleanTitle)
	}

	return created, preserved, nil
}

func (s *Syncer) processSingleMovie(ctx context.Context, movie models.Movie, dryRun bool) (created, preserved int, err error) {
	c, p, _, _, _, err := s.processSingleMovieProfiled(ctx, movie, dryRun)
	return c, p, err
}

func (s *Syncer) processSingleMovieProfiled(ctx context.Context, movie models.Movie, dryRun bool) (created, preserved int, strmCreated bool, artDur, diskDur time.Duration, err error) {
	cleanTitle := sanitizer.CleanTitle(movie.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, movie.Year, movie.TMDBID, movie.IMDBID)
	movieDir := filepath.Join(s.cfg.OutputDir, "Movies", folderName)

	if s.database != nil {
		_ = s.database.UpsertInitialMovieState(movie.ID, movie.TMDBID, movie.IMDBID, cleanTitle, movie.Year, folderName)
	}

	if dryRun {
		log.Printf("[DRY-RUN] Movie '%s' (TMDB %s) -> Folder: %s", cleanTitle, movie.TMDBID, folderName)
		return 1, 0, true, 0, 0, nil
	}

	diskStart := time.Now()
	// 1. Generate or purge .nfo metadata file based on EnableNFO
	nfoPath := filepath.Join(movieDir, fmt.Sprintf("%s.nfo", sanitizer.SanitizeFileName(cleanTitle)))
	if s.cfg.EnableNFO {
		nfoUpdated, err := nfo.WriteMovieNFO(nfoPath, movie)
		if err == nil {
			if nfoUpdated {
				created++
			} else {
				preserved++
			}
		}
	} else {
		// Clean up NFO if present when disabled
		if _, statErr := os.Stat(nfoPath); statErr == nil {
			_ = os.Remove(nfoPath)
		}
	}

	// 2. Write clean extensionless .strm file pointing to /play/movie/{movie_id}
	strmFilename := fmt.Sprintf("%s.strm", sanitizer.SanitizeFileName(cleanTitle))
	strmPath := filepath.Join(movieDir, strmFilename)
	proxyURL := strm.GenerateMovieProxyURL(s.cfg, movie.ID, "base")

	updated, err := strm.WriteSTRMFile(strmPath, proxyURL)
	if err != nil {
		log.Printf("[Syncer] Error writing movie STRM %s: %v", strmPath, err)
	} else if updated {
		created++
		strmCreated = true
	} else {
		preserved++
	}

	// Check if this movie has already been activated by user; if so, maintain its quality variants
	if s.database != nil {
		st, _ := s.database.GetMovieState(movie.ID)
		if st != nil && st.IsActivated {
			c, p, _, err := s.PopulateActivatedMovie(ctx, movie.ID, cleanTitle, movieDir, movie.Year, movie.TMDBID)
			diskDur = time.Since(diskStart)
			return created + c, preserved + p, strmCreated || (c > 0), 0, diskDur, err
		}
	}
	diskDur = time.Since(diskStart)

	// 3. Download artwork if logo/backdrop present
	artStart := time.Now()
	artCreated := artwork.DownloadMovieArtwork(movieDir, movie, s.cfg)
	artDur = time.Since(artStart)
	created += artCreated

	return created, preserved, strmCreated, artDur, diskDur, nil
}

// PopulateActivatedMovie resolves backing provider streams for a movie, classifies quality tiers, saves variants to SQLite, writes secondary variant .strm files on disk, and marks movie as activated.
func (s *Syncer) PopulateActivatedMovie(ctx context.Context, movieID int, cleanTitle, movieDir string, year int, tmdbID string) (created, preserved int, baseTargetURL string, err error) {
	if movieDir == "" || cleanTitle == "" || strings.HasPrefix(cleanTitle, "Movie ") {
		if s.database != nil {
			st, _ := s.database.GetMovieState(movieID)
			if st != nil && st.FolderName != "" {
				movieDir = filepath.Join(s.cfg.OutputDir, "Movies", st.FolderName)
				cleanTitle = st.Title
				year = st.Year
				tmdbID = st.TMDBID
			}
		}
	}

	var movieUUID string
	if mv, fetchErr := s.client.FetchSingleMovie(ctx, movieID); fetchErr == nil && mv != nil {
		movieUUID = mv.UUID
		if cleanTitle == "" || strings.HasPrefix(cleanTitle, "Movie ") {
			cleanTitle = sanitizer.CleanTitle(mv.Name)
			year = mv.Year
			tmdbID = mv.TMDBID
			folderName := sanitizer.BuildMovieFolderName(cleanTitle, mv.Year, mv.TMDBID, mv.IMDBID)
			movieDir = filepath.Join(s.cfg.OutputDir, "Movies", folderName)
			if s.database != nil {
				_ = s.database.UpsertInitialMovieState(movieID, mv.TMDBID, mv.IMDBID, cleanTitle, mv.Year, folderName)
			}
		}
	}

	if movieDir == "" {
		movieDir = filepath.Join(s.cfg.OutputDir, "Movies", fmt.Sprintf("Movie %d", movieID))
	}
	if cleanTitle == "" {
		cleanTitle = fmt.Sprintf("Movie %d", movieID)
	}

	relations, err := s.client.FetchMovieProviders(ctx, movieID)
	if err != nil {
		if err == client.ErrRateLimited && s.cfg.CircuitBreakerEnabled {
			s.tripCircuitBreaker()
		}
		log.Printf("[Syncer] Warning: Could not fetch provider relations for movie ID %d: %v", movieID, err)
	}

	if len(relations) > 0 {
		classified := versioning.ClassifyMovieStreams(relations, cleanTitle, year)
		for _, v := range classified {
			varKey := "base"
			if v.Quality == models.Quality4K {
				varKey = "4k"
			}

			targetUUID := v.MovieUUID
			if targetUUID == "" {
				targetUUID = movieUUID
			}

			var realTargetURL string
			if targetUUID != "" && s.cfg.APIKey != "" {
				realTargetURL = fmt.Sprintf("%s/proxy/vod/movie/%s?api_key=%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), targetUUID, s.cfg.APIKey)
			} else {
				realTargetURL = fmt.Sprintf("%s/movie/%s/%s/%d.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, movieID, v.Container)
			}

			if varKey == "base" && baseTargetURL == "" {
				baseTargetURL = realTargetURL
			}

			if s.database != nil {
				_ = s.database.SaveStreamVariant(&db.StreamVariant{
					ItemID:             movieID,
					ItemType:           "movie",
					VariantKey:         varKey,
					TargetURL:          realTargetURL,
					Quality:            string(v.Quality),
					StreamID:           v.StreamID,
					ContainerExtension: v.Container,
				})
			}

			// If secondary variant (e.g. 4K), write secondary .strm file on disk for Emby's version dropdown menu
			if varKey != "base" {
				secStrmPath := filepath.Join(movieDir, v.Filename)
				secProxyURL := strm.GenerateMovieProxyURL(s.cfg, movieID, varKey)
				updated, wErr := strm.WriteSTRMFile(secStrmPath, secProxyURL)
				if wErr == nil {
					if updated {
						created++
					} else {
						preserved++
					}
				}
			}
		}
	}

	if s.database != nil {
		_ = s.database.SetMovieActivated(movieID, tmdbID, cleanTitle)
	}

	return created, preserved, baseTargetURL, nil
}

func (s *Syncer) triggerEmbyRefresh(ctx context.Context) {
	embyEndpoint := fmt.Sprintf("%s/Library/Refresh", strings.TrimRight(s.cfg.EmbyURL, "/"))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embyEndpoint, nil)
	if err != nil {
		log.Printf("[Emby] Failed to create library refresh request: %v", err)
		return
	}
	req.Header.Set("X-Emby-Token", s.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", s.cfg.EmbyAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[Emby] Library refresh request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Println("[Emby] Successfully triggered Emby library rescan.")
	} else {
		log.Printf("[Emby] Emby library refresh returned status %d", resp.StatusCode)
	}
}

// TriggerTargetedEmbySeriesRefresh triggers a targeted Emby rescan for the activated series by TMDB ID or title.
func (s *Syncer) TriggerTargetedEmbySeriesRefresh(ctx context.Context, seriesID int) {
	if s.cfg.EmbyURL == "" || s.cfg.EmbyAPIKey == "" {
		return
	}
	go func() {
		var tmdbID, imdbID, title string
		if s.database != nil {
			if st, _ := s.database.GetSeriesState(seriesID); st != nil {
				tmdbID = st.TMDBID
				imdbID = st.IMDBID
				title = st.Title
			}
		}

		// Helper to search Emby by query parameter and return internal Item ID
		findEmbyItemID := func(param string) string {
			searchURL := fmt.Sprintf("%s/Items?%s&IncludeItemTypes=Series&Recursive=true", strings.TrimRight(s.cfg.EmbyURL, "/"), param)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, searchURL, nil)
			if err != nil {
				return ""
			}
			req.Header.Set("X-Emby-Token", s.cfg.EmbyAPIKey)
			req.Header.Set("X-MediaBrowser-Token", s.cfg.EmbyAPIKey)

			resp, err := http.DefaultClient.Do(req)
			if err != nil || resp == nil {
				return ""
			}
			defer resp.Body.Close()

			var searchResult struct {
				Items []struct {
					ID string `json:"Id"`
				} `json:"Items"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&searchResult); err == nil && len(searchResult.Items) > 0 {
				return searchResult.Items[0].ID
			}
			return ""
		}

		var embyItemID string
		if tmdbID != "" {
			embyItemID = findEmbyItemID(fmt.Sprintf("AnyProviderIdEquals=tmdb.%s", tmdbID))
		}
		if embyItemID == "" && imdbID != "" {
			cleanIMDB := imdbID
			if !strings.HasPrefix(strings.ToLower(cleanIMDB), "tt") {
				cleanIMDB = "tt" + cleanIMDB
			}
			embyItemID = findEmbyItemID(fmt.Sprintf("AnyProviderIdEquals=imdb.%s", cleanIMDB))
		}
		if embyItemID == "" && title != "" {
			escapedTitle := url.QueryEscape(title)
			embyItemID = findEmbyItemID(fmt.Sprintf("searchTerm=%s", escapedTitle))
		}

		if embyItemID != "" {
			// Trigger targeted item refresh directly on the specific series item
			refreshURL := fmt.Sprintf("%s/Items/%s/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh&ImageRefreshMode=Default", strings.TrimRight(s.cfg.EmbyURL, "/"), embyItemID)
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, refreshURL, nil)
			if err == nil {
				req.Header.Set("X-Emby-Token", s.cfg.EmbyAPIKey)
				req.Header.Set("X-MediaBrowser-Token", s.cfg.EmbyAPIKey)
				req.Header.Set("Content-Type", "application/json")
				resp, dErr := http.DefaultClient.Do(req)
				if dErr == nil && resp != nil {
					defer resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						log.Printf("[Emby] Successfully triggered targeted series item refresh for %q (Emby Item ID: %s)", title, embyItemID)
						return
					}
				}
			}
		}

		// Fallback to global refresh if item was not found in Emby
		s.triggerEmbyRefresh(context.Background())
	}()
}
