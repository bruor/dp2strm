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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dp2strm/pkg/artwork"
	"dp2strm/pkg/client"
	"dp2strm/pkg/db"
	"dp2strm/pkg/emby"
	"dp2strm/pkg/models"
	"dp2strm/pkg/nfo"
	"dp2strm/pkg/sanitizer"
	"dp2strm/pkg/strm"
	"dp2strm/pkg/versioning"
)

type Syncer struct {
	cfg            *models.Config
	client         *client.Client
	embyClient     *emby.Client
	database       *db.DB
	circuitTripped bool
	workerCancel   context.CancelFunc
	workerWg       sync.WaitGroup
	mu             sync.Mutex

	// VIP Series Activation coordinator
	activationMu      sync.Mutex
	activationCond    *sync.Cond
	activeActivations int64

	// Queue progress and Emby scan timing metrics
	progressMu         sync.Mutex
	completedTasks     int64
	completedScanCount int64
	totalScanDuration  time.Duration
	lastScanDuration   time.Duration
}

func NewSyncer(cfg *models.Config, client *client.Client, database *db.DB) *Syncer {
	s := &Syncer{
		cfg:        cfg,
		client:     client,
		embyClient: emby.NewClient(cfg),
		database:   database,
	}
	s.activationCond = sync.NewCond(&s.activationMu)
	return s
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

	// Automatically migrate legacy folder without ID if it exists on disk (e.g. "TV Shows/Title (Year)")
	if series.Year > 0 {
		legacyFolderName := fmt.Sprintf("%s (%d)", sanitizer.SanitizeFileName(cleanTitle), series.Year)
		legacyDir := filepath.Join(s.cfg.OutputDir, "TV Shows", legacyFolderName)
		if legacyDir != seriesDir {
			if _, statErr := os.Stat(legacyDir); statErr == nil {
				if _, canonicalErr := os.Stat(seriesDir); os.IsNotExist(canonicalErr) {
					if rErr := os.Rename(legacyDir, seriesDir); rErr == nil {
						log.Printf("[Syncer] Migrated legacy TV show directory without ID: %q -> %q", legacyFolderName, folderName)
					}
				}
			}
		}
	}

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

	// 2. Write Pass 1 Season 01 S01E01 Activation placeholder
	season1Dir := filepath.Join(seriesDir, "Season 01")

	// Auto-clean any legacy Specials S00E99 placeholder from earlier builds
	legacySpecialsDir := filepath.Join(seriesDir, "Specials")
	if entries, readErr := os.ReadDir(legacySpecialsDir); readErr == nil {
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "Activate Series") || strings.Contains(entry.Name(), "S00E99") {
				_ = os.Remove(filepath.Join(legacySpecialsDir, entry.Name()))
			}
		}
		// If Specials is now empty, remove it
		if remaining, _ := os.ReadDir(legacySpecialsDir); len(remaining) == 0 {
			_ = os.Remove(legacySpecialsDir)
		}
	}

	actStrmFilename := fmt.Sprintf("%s - S01E01.strm", sanitizer.SanitizeFileName(cleanTitle))
	actStrmPath := filepath.Join(season1Dir, actStrmFilename)

	actNFOFilename := fmt.Sprintf("%s - S01E01.nfo", sanitizer.SanitizeFileName(cleanTitle))
	actNFOPath := filepath.Join(season1Dir, actNFOFilename)

	seriesIdent := strm.FormatSeriesIdentifier(series.TMDBID, series.IMDBID, series.ID)
	actProxyURL := strm.GenerateSeriesProxyURL(s.cfg, seriesIdent, models.ActivationSeasonNumber, models.ActivationEpisodeNumber, "base")

	writeStatus, err := strm.WriteSTRMFileDetailed(actStrmPath, actProxyURL)
	if err == nil {
		if writeStatus == strm.STRMCreated {
			created++
			strmCreated = true
		} else {
			preserved++
		}
	}

	nfoUpdated, err := nfo.WriteActivationEpisodeNFO(actNFOPath, cleanTitle)
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

	// Auto-clean Pass 1 Season 01 S01E01 activation NFO if ENABLE_NFO is false
	if !s.cfg.EnableNFO {
		season1Dir := filepath.Join(seriesDir, "Season 01")
		if entries, rErr := os.ReadDir(season1Dir); rErr == nil {
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".nfo") && strings.Contains(entry.Name(), "S01E01") {
					_ = os.Remove(filepath.Join(season1Dir, entry.Name()))
				}
			}
		}
	}

	// Auto-clean any legacy Specials S00E99 activation placeholder
	specialsDir := filepath.Join(seriesDir, "Specials")
	if entries, rErr := os.ReadDir(specialsDir); rErr == nil {
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "Activate Series") || strings.Contains(entry.Name(), "S00E99") {
				_ = os.Remove(filepath.Join(specialsDir, entry.Name()))
			}
		}
	}

	// Check if upstream Dispatcharr actually has real specials (Season 0)
	hasRealSpecials := false
	hasS01E01 := false
	for _, ep := range episodes {
		if ep.SeasonNumber == 0 {
			hasRealSpecials = true
		}
		if ep.SeasonNumber == 1 && ep.EpisodeNumber == 1 {
			hasS01E01 = true
		}
	}
	if !hasRealSpecials {
		_ = os.Remove(specialsDir)
	}

	// Clean up temporary Season 01 directory if show starts at Season 2+
	if !hasS01E01 {
		season1Dir := filepath.Join(seriesDir, "Season 01")
		if entries, rErr := os.ReadDir(season1Dir); rErr == nil {
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "S01E01") {
					_ = os.Remove(filepath.Join(season1Dir, entry.Name()))
				}
			}
		}
		if remaining, _ := os.ReadDir(season1Dir); len(remaining) == 0 {
			_ = os.Remove(season1Dir)
		}
	}

	maxBatch := s.cfg.EmbyBatchSize
	if maxBatch <= 0 {
		maxBatch = 25
	}
	remainingUnwritten := 0

	for _, ep := range episodes {
		seasonDir := filepath.Join(seriesDir, fmt.Sprintf("Season %02d", ep.SeasonNumber))
		filename := fmt.Sprintf("%s - S%02dE%02d.strm", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber)
		strmPath := filepath.Join(seasonDir, filename)

		nfoFilename := fmt.Sprintf("%s - S%02dE%02d.nfo", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber)
		nfoPath := filepath.Join(seasonDir, nfoFilename)

		seriesIdent := ""
		if s.database != nil {
			if st, _ := s.database.GetSeriesState(seriesID); st != nil {
				seriesIdent = strm.FormatSeriesIdentifier(st.TMDBID, st.IMDBID, seriesID)
			}
		}
		if seriesIdent == "" {
			seriesIdent = strconv.Itoa(seriesID)
		}
		proxyURL := strm.GenerateSeriesProxyURL(s.cfg, seriesIdent, ep.SeasonNumber, ep.EpisodeNumber, "base")

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

		// If file doesn't exist and we reached the batch quota for new items in this pass, track remainder and defer
		if _, statErr := os.Stat(strmPath); os.IsNotExist(statErr) && created >= maxBatch {
			remainingUnwritten++
			continue
		}

		writeStatus, err := strm.WriteSTRMFileDetailed(strmPath, proxyURL)
		if err != nil {
			log.Printf("[Syncer] Error writing episode STRM %s: %v", strmPath, err)
			continue
		}

		if writeStatus == strm.STRMCreated {
			created++
		} else {
			preserved++
		}

		if s.cfg.EnableNFO {
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
			nUpdated, nErr := nfo.WriteEpisodeNFO(nfoPath, epTitle, ep.SeasonNumber, ep.EpisodeNumber, epPlot, ep.TMDBID, ep.DurationSecs)
			if nErr == nil && nUpdated {
				created++
			}
		} else {
			if _, statErr := os.Stat(nfoPath); statErr == nil {
				_ = os.Remove(nfoPath)
			}
			if ep.SeasonNumber == 1 && ep.EpisodeNumber == 1 {
				now := time.Now()
				_ = os.Chtimes(strmPath, now, now)
			}
		}
	}

	// If more episodes remain to be written on disk, queue background batch task
	if remainingUnwritten > 0 && s.database != nil {
		log.Printf("[Syncer] Series %d (%s): Wrote batch of %d episodes (%d remaining). Enqueueing background batch...",
			seriesID, cleanTitle, created, remainingUnwritten)
		_ = s.database.EnqueueTask("series", seriesID, "update_episodes", models.PriorityAiring)
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

	// Automatically migrate legacy folder without ID if it exists on disk (e.g. "Movies/Title (Year)")
	if movie.Year > 0 {
		legacyFolderName := fmt.Sprintf("%s (%d)", sanitizer.SanitizeFileName(cleanTitle), movie.Year)
		legacyDir := filepath.Join(s.cfg.OutputDir, "Movies", legacyFolderName)
		if legacyDir != movieDir {
			if _, statErr := os.Stat(legacyDir); statErr == nil {
				if _, canonicalErr := os.Stat(movieDir); os.IsNotExist(canonicalErr) {
					if rErr := os.Rename(legacyDir, movieDir); rErr == nil {
						log.Printf("[Syncer] Migrated legacy Movie directory without ID: %q -> %q", legacyFolderName, folderName)
					}
				}
			}
		}
	}

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

	// 2. Write clean extensionless .strm file pointing to /play/movie/{identifier}
	movieIdent := strm.FormatMovieIdentifier(movie.TMDBID, movie.IMDBID, movie.ID)
	strmFilename := fmt.Sprintf("%s.strm", sanitizer.SanitizeFileName(cleanTitle))
	strmPath := filepath.Join(movieDir, strmFilename)
	proxyURL := strm.GenerateMovieProxyURL(s.cfg, movieIdent, "base")

	writeStatus, err := strm.WriteSTRMFileDetailed(strmPath, proxyURL)
	if err != nil {
		log.Printf("[Syncer] Error writing movie STRM %s: %v", strmPath, err)
	} else if writeStatus == strm.STRMCreated {
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
			varKey := v.VariantKey
			if varKey == "" {
				varKey = "base"
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

			// If secondary variant (e.g. 4K, 4k-2, version2), write secondary .strm file on disk for Emby's version dropdown menu
			if varKey != "base" {
				secStrmPath := filepath.Join(movieDir, v.Filename)
				movieIdent := strm.FormatMovieIdentifier(tmdbID, "", movieID)
				secProxyURL := strm.GenerateMovieProxyURL(s.cfg, movieIdent, varKey)
				writeStatus, wErr := strm.WriteSTRMFileDetailed(secStrmPath, secProxyURL)
				if wErr == nil {
					if writeStatus == strm.STRMCreated {
						created++
					} else {
						preserved++
					}
				}
			}
		}

		// Detect disappeared variants for this movie
		if s.database != nil {
			activeKeys := make(map[string]bool)
			for _, v := range classified {
				activeKeys[v.VariantKey] = true
			}
			if existingVariants, err := s.database.GetMovieStreamVariants(movieID); err == nil {
				for _, ev := range existingVariants {
					if ev.VariantKey != "" && ev.VariantKey != "base" && !activeKeys[ev.VariantKey] {
						varFilename := ""
						if ev.Quality == "4K" || strings.HasPrefix(ev.VariantKey, "4k") {
							if ev.VariantKey == "4k" {
								varFilename = fmt.Sprintf("%s - 4K.strm", sanitizer.SanitizeFileName(cleanTitle))
							} else {
								idx := strings.TrimPrefix(ev.VariantKey, "4k-")
								varFilename = fmt.Sprintf("%s - 4K (%s).strm", sanitizer.SanitizeFileName(cleanTitle), idx)
							}
						} else {
							idx := strings.TrimPrefix(ev.VariantKey, "version")
							varFilename = fmt.Sprintf("%s - Version %s.strm", sanitizer.SanitizeFileName(cleanTitle), idx)
						}
						orphanPath := filepath.Join(movieDir, varFilename)
						if s.cfg.RetentionGraceDays > 0 {
							_ = s.database.SoftDeleteItem(orphanPath, "variant")
							log.Printf("[Syncer] Disappeared variant %q for Movie %d (%s) marked for soft deletion (%d-day grace period).", ev.VariantKey, movieID, cleanTitle, s.cfg.RetentionGraceDays)
						} else {
							_ = os.Remove(orphanPath)
							_ = s.database.DeleteStreamVariant(movieID, "movie", ev.VariantKey)
							log.Printf("[Syncer] Pruned disappeared variant %q for Movie %d (%s) from disk and database.", ev.VariantKey, movieID, cleanTitle)
						}
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
	if s.embyClient != nil && s.embyClient.IsConfigured() {
		log.Println("[Emby] Triggering Emby library rescan and waiting for idle...")
		scanStart := time.Now()
		_ = s.embyClient.TriggerGlobalRefresh(ctx)
		time.Sleep(2 * time.Second)
		if s.embyClient.WaitForScanCompletion(ctx, 24*time.Hour) {
			scanDur := time.Since(scanStart)
			s.recordEmbyScanDuration(scanDur)
			log.Printf("[Emby] Library scan complete in %s.", formatDuration(scanDur))
		}
		return
	}

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

// BeginVIPActivation acquires a VIP series activation lock, halts any active global library scan,
// and pauses background batch workers until all active series activations are complete.
func (s *Syncer) BeginVIPActivation(ctx context.Context, seriesID int) func() {
	s.activationMu.Lock()
	s.activeActivations++
	activeCount := s.activeActivations
	s.activationMu.Unlock()

	log.Printf("[VIP Activation] ⚡ VIP lock acquired for Series ID %d (active: %d). Pausing background batch workers...", seriesID, activeCount)
	_ = s.CancelRunningEmbyScan(ctx)

	return func() {
		s.activationMu.Lock()
		s.activeActivations--
		if s.activeActivations < 0 {
			s.activeActivations = 0
		}
		remaining := s.activeActivations
		if remaining == 0 {
			s.activationCond.Broadcast()
		}
		s.activationMu.Unlock()

		if remaining == 0 {
			log.Printf("[VIP Activation] ⚡ All VIP series activations completed. Resuming background batch workers.")
		} else {
			log.Printf("[VIP Activation] ⚡ VIP activation for Series ID %d completed (%d still active).", seriesID, remaining)
		}
	}
}

// WaitForVIPActivations blocks if any VIP series activations are currently in progress.
func (s *Syncer) WaitForVIPActivations(ctx context.Context) {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	for s.activeActivations > 0 {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.activationCond.Wait()
	}
}

// IsVIPActivationInProgress returns whether any VIP activations are currently active.
func (s *Syncer) IsVIPActivationInProgress() bool {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	return s.activeActivations > 0
}

// CancelRunningEmbyScan cancels any active global media library scan in Emby to immediately clear the way for VIP item activations.
func (s *Syncer) CancelRunningEmbyScan(ctx context.Context) error {
	if s.embyClient != nil && s.embyClient.IsConfigured() {
		return s.embyClient.CancelRunningLibraryScan(ctx)
	}
	return nil
}

// TriggerTargetedEmbySeriesRefresh triggers a targeted Emby rescan for the activated series by TMDB ID or title.
func (s *Syncer) TriggerTargetedEmbySeriesRefresh(ctx context.Context, seriesID int) {
	if s.cfg.EmbyURL == "" || s.cfg.EmbyAPIKey == "" {
		return
	}

	// Stop any background global scan to ensure the targeted item refresh is processed with zero delay
	if s.embyClient != nil && s.embyClient.IsConfigured() {
		_ = s.embyClient.CancelRunningLibraryScan(ctx)
	}

	var tmdbID, imdbID, title string
	if s.database != nil {
		if st, _ := s.database.GetSeriesState(seriesID); st != nil {
			tmdbID = st.TMDBID
			imdbID = st.IMDBID
			title = st.Title
		}
	}

	// Wait for active playback session in Emby before firing refresh (up to ActivationRefreshDelay timeout)
	if s.embyClient != nil && s.embyClient.IsConfigured() && s.cfg.ActivationRefreshDelay > 0 {
		s.embyClient.WaitForPlaybackSession(ctx, tmdbID, imdbID, title, s.cfg.ActivationRefreshDelay)
	}

	// Helper to search Emby by query parameter and return internal Item ID
	findEmbyItemID := func(param string) string {
		searchURL := fmt.Sprintf("%s/Items?%s&IncludeItemTypes=Series&Recursive=true", strings.TrimRight(s.cfg.EmbyURL, "/"), param)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
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
		if s.embyClient != nil && s.embyClient.IsConfigured() {
			_ = s.embyClient.RefreshItemByID(ctx, embyItemID)
			_ = s.embyClient.RefreshSeriesEpisodes(ctx, embyItemID)
			log.Printf("[Emby] Successfully triggered targeted series item refresh for %q (Emby Item ID: %s)", title, embyItemID)
			return
		}
	}

	// Fallback to global refresh if item was not found in Emby
	s.triggerEmbyRefresh(ctx)
}

// --- Background Worker Pipeline & Stager ---

// StartWorkers initializes Emby library discovery, resets in-progress queue items, and launches background Emby workers and reconciler.
func (s *Syncer) StartWorkers(ctx context.Context) {
	// 1. Auto-discover Emby libraries
	if s.embyClient.IsConfigured() {
		if err := s.embyClient.DiscoverLibraries(ctx); err != nil {
			log.Printf("[Emby] Library auto-discovery notice: %v", err)
		}
	}

	// 2. Reset hanging in_progress tasks back to pending
	if s.database != nil {
		if err := s.database.ResetInProgressTasks(); err != nil {
			log.Printf("[Syncer] Error resetting in-progress tasks: %v", err)
		}
	}

	// 3. Run initial startup disk reconciliation audit to get an accurate snapshot of on-disk state BEFORE workers start
	_ = s.ReconcileDiskState(ctx)

	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel

	numWorkers := s.cfg.EmbyWorkers
	if numWorkers <= 0 {
		numWorkers = 1
	}

	batchSize := s.cfg.EmbyBatchSize
	if batchSize <= 0 {
		batchSize = 25
	}
	summary := s.GetQueueProgressSummary()
	log.Printf("[Queue] Starting %d Emby processing worker(s) (Batch Size: %d) | Initial Queue: %s | %s", numWorkers, batchSize, summary.RemainingString(), summary.EstimateString())
	for i := 1; i <= numWorkers; i++ {
		s.workerWg.Add(1)
		go s.embyWorkerLoop(workerCtx, i)
	}

	// Start background scheduled reconciler loop
	s.workerWg.Add(1)
	go s.reconcilerLoop(workerCtx)
}

// StopWorkers cleanly stops all active workers and the reconciler.
func (s *Syncer) StopWorkers() {
	if s.workerCancel != nil {
		s.workerCancel()
	}
	s.workerWg.Wait()
}

// EnqueueUserActivation queues a series for VIP on-demand activation (Priority 100).
func (s *Syncer) EnqueueUserActivation(seriesID int) error {
	if s.database == nil {
		return nil
	}
	return s.database.EnqueueTask("series", seriesID, "activate", models.PriorityVIP)
}

// StageCatalogDelta fetches the latest movies and series catalog from Dispatcharr and populates the SQLite sync_queue.
func (s *Syncer) StageCatalogDelta(ctx context.Context) error {
	log.Println("[Stager] Starting Dispatcharr catalog ingest & delta staging pass...")
	start := time.Now()
	s.resetCircuitBreaker()
	s.resetCompletedTasks()

	if s.database == nil {
		return fmt.Errorf("database required for staging")
	}

	pageSize := s.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	workers := s.cfg.DispatcharrWorkers
	if workers <= 0 {
		workers = 1
	}

	// 1. Stage Movies
	moviesCount := 0
	page1Movies, err := s.client.FetchMoviePage(ctx, 1, pageSize)
	if err == nil && page1Movies != nil {
		_ = s.database.BatchStageMovies(page1Movies.Results)
		moviesCount += len(page1Movies.Results)

		totalMoviePages := (page1Movies.Count + pageSize - 1) / pageSize
		for p := 2; p <= totalMoviePages; p++ {
			if s.isCircuitTripped() {
				break
			}
			mvs, pErr := s.client.FetchMoviePage(ctx, p, pageSize)
			if pErr != nil || mvs == nil {
				continue
			}
			_ = s.database.BatchStageMovies(mvs.Results)
			moviesCount += len(mvs.Results)
		}
	}

	// 2. Stage Series
	seriesCount := 0
	page1Series, sErr := s.client.FetchSeriesPage(ctx, 1, pageSize)
	if sErr == nil && page1Series != nil {
		_ = s.database.BatchStageSeries(page1Series.Results)
		seriesCount += len(page1Series.Results)

		totalSeriesPages := (page1Series.Count + pageSize - 1) / pageSize
		for p := 2; p <= totalSeriesPages; p++ {
			if s.isCircuitTripped() {
				break
			}
			seriesList, pErr := s.client.FetchSeriesPage(ctx, p, pageSize)
			if pErr != nil || seriesList == nil {
				continue
			}
			_ = s.database.BatchStageSeries(seriesList.Results)
			seriesCount += len(seriesList.Results)
		}
	}

	// 3. Detect disappeared Movies and Series (only if circuit breaker was not tripped and full sync completed)
	if !s.isCircuitTripped() && s.database != nil {
		moviesRoot := filepath.Join(s.cfg.OutputDir, "Movies")
		if staleMovies, err := s.database.GetStaleMovieStates(start); err == nil && len(staleMovies) > 0 {
			for _, sm := range staleMovies {
				folderName := sm.FolderName
				if folderName == "" {
					folderName = sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(sm.Title), sm.Year, sm.TMDBID, sm.IMDBID)
				}
				moviePath := filepath.Join(moviesRoot, folderName)
				if s.cfg.RetentionGraceDays > 0 {
					_ = s.database.SoftDeleteItem(moviePath, "movie")
					log.Printf("[Stager] Disappeared movie %q (ID %d) marked for soft deletion (%d-day grace period).", sm.Title, sm.MovieID, s.cfg.RetentionGraceDays)
				} else {
					_ = os.RemoveAll(moviePath)
					_ = s.database.DeleteMovieState(sm.MovieID)
					_ = s.database.DeleteStreamVariant(sm.MovieID, "movie", "base")
					log.Printf("[Stager] Pruned disappeared movie %q (ID %d) from disk and database.", sm.Title, sm.MovieID)
				}
			}
		}

		tvRoot := filepath.Join(s.cfg.OutputDir, "TV Shows")
		if staleSeries, err := s.database.GetStaleSeriesStates(start); err == nil && len(staleSeries) > 0 {
			for _, ss := range staleSeries {
				folderName := ss.FolderName
				if folderName == "" {
					folderName = sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(ss.Title), ss.Year, ss.TMDBID, ss.IMDBID)
				}
				seriesPath := filepath.Join(tvRoot, folderName)
				if s.cfg.RetentionGraceDays > 0 {
					_ = s.database.SoftDeleteItem(seriesPath, "series")
					log.Printf("[Stager] Disappeared TV show %q (ID %d) marked for soft deletion (%d-day grace period).", ss.Title, ss.SeriesID, s.cfg.RetentionGraceDays)
				} else {
					_ = os.RemoveAll(seriesPath)
					_ = s.database.DeleteSeriesState(ss.SeriesID)
					log.Printf("[Stager] Pruned disappeared TV show %q (ID %d) from disk and database.", ss.Title, ss.SeriesID)
				}
			}
		}
	}

	summary := s.GetQueueProgressSummary()
	log.Printf("[Stager] Catalog delta staged in %v. Total: %d movies, %d series. Queue state -> %s | %s | %s",
		time.Since(start), moviesCount, seriesCount, summary.ProgressString(), summary.RemainingString(), summary.EstimateString())
	return nil
}

type itemSummary struct {
	ItemType    string
	ItemID      int
	TMDBID      string
	IMDBID      string
	Title       string
	STRMCreated bool
}

// embyWorkerLoop processes queue items in adaptive batches with idle debounce and Emby verification.
func (s *Syncer) embyWorkerLoop(ctx context.Context, workerID int) {
	defer s.workerWg.Done()
	log.Printf("[Queue] Worker #%d online.", workerID)

	preferredType := "series" // Start alternating with series
	processedCount := 0
	batchSize := s.cfg.EmbyBatchSize
	if batchSize <= 0 {
		batchSize = 25
	}

	var batchTasks []*models.QueueItem
	var newItemsCreated int
	var lastCreatedItem *itemSummary
	lastMaintenanceFlush := time.Now()
	loggedEmpty := false

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Queue] Worker #%d shutting down.", workerID)
			return
		default:
		}

		if s.database == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		// Pause worker if any VIP series activations are in flight
		s.WaitForVIPActivations(ctx)

		// If Emby is actively running a media library scan, pause worker to let Emby finish cleanly
		if s.embyClient.IsConfigured() && s.embyClient.IsLibraryScanRunning(ctx) {
			log.Printf("[Ingest] Emby is currently performing a media library scan. Pausing worker until scan finishes...")
			scanStart := time.Now()
			if s.embyClient.WaitForScanCompletion(ctx, 24*time.Hour) {
				scanDur := time.Since(scanStart)
				s.recordEmbyScanDuration(scanDur)
				log.Printf("[Ingest] Emby media library scan completed in %s. Resuming worker.", formatDuration(scanDur))
			}
		}

		// Check again after scan wait in case an activation just cancelled the scan
		s.WaitForVIPActivations(ctx)

		task, err := s.database.PopNextTask(preferredType)
		if err != nil {
			log.Printf("[Queue] Error polling queue: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		if task == nil {
			// If we have items in the current unverified batch, finish verification now
			if len(batchTasks) > 0 {
				if newItemsCreated > 0 {
					s.verifyAndFlushBatch(ctx, workerID, batchTasks, lastCreatedItem, newItemsCreated)
				} else {
					s.flushMaintenanceBatch(ctx, workerID, batchTasks, time.Since(lastMaintenanceFlush))
				}
				batchTasks = nil
				newItemsCreated = 0
				lastCreatedItem = nil
				lastMaintenanceFlush = time.Now()
			}
			if !loggedEmpty {
				summary := s.GetQueueProgressSummary()
				if summary.RemainingTasks == 0 {
					if summary.CompletedTasks > 0 {
						log.Printf("[Queue] 🎉 All sync queue tasks completed! Overall Progress: %s. Library is fully in sync with Emby.", summary.ProgressString())
					}
					loggedEmpty = true
				}
			}
			time.Sleep(1 * time.Second)
			continue
		}

		loggedEmpty = false

		// Alternate preferred type for balanced round-robin
		if task.ItemType == "series" {
			preferredType = "movie"
		} else {
			preferredType = "series"
		}

		processedCount++
		// Process task
		pErr, summary := s.processQueueTask(ctx, task)
		if pErr != nil {
			log.Printf("[Queue] Task %d failed (%s %d: %s): %v", task.ID, task.ItemType, task.ItemID, task.Action, pErr)
			_ = s.database.MarkTaskFailed(task.ID, pErr.Error())
		} else {
			batchTasks = append(batchTasks, task)
			if summary != nil && summary.STRMCreated {
				newItemsCreated++
				lastCreatedItem = summary
			}
		}

		// Flush rules:
		// 1. When batch limit of NEWLY CREATED items is reached, trigger Emby batch ingestion
		if newItemsCreated >= batchSize {
			s.verifyAndFlushBatch(ctx, workerID, batchTasks, lastCreatedItem, newItemsCreated)
			batchTasks = nil
			newItemsCreated = 0
			lastCreatedItem = nil
			lastMaintenanceFlush = time.Now()
		} else if newItemsCreated == 0 && (time.Since(lastMaintenanceFlush) >= 10*time.Second || len(batchTasks) >= 1000) {
			// 2. Pure maintenance tasks: flush every 10s on a timer (or max 1000 items in buffer)
			s.flushMaintenanceBatch(ctx, workerID, batchTasks, time.Since(lastMaintenanceFlush))
			batchTasks = nil
			lastMaintenanceFlush = time.Now()
		}
	}
}

func (s *Syncer) flushMaintenanceBatch(ctx context.Context, workerID int, tasks []*models.QueueItem, elapsed time.Duration) {
	if len(tasks) == 0 {
		return
	}
	for _, t := range tasks {
		_ = s.database.MarkTaskComplete(t.ID)
	}

	s.progressMu.Lock()
	s.completedTasks += int64(len(tasks))
	s.progressMu.Unlock()

	summary := s.GetQueueProgressSummary()

	// Calculate rate in items/second
	seconds := elapsed.Seconds()
	rate := 0
	if seconds > 0 {
		rate = int(float64(len(tasks)) / seconds)
	}

	// Calculate maintenance ETA
	etaStr := "calculating..."
	if rate > 0 && summary.RemainingTasks > 0 {
		remSecs := summary.RemainingTasks / rate
		etaStr = formatDuration(time.Duration(remSecs) * time.Second)
	}

	var mRec, sRec int
	for _, t := range tasks {
		if t.ItemType == "movie" {
			mRec++
		} else {
			sRec++
		}
	}
	breakdown := ""
	if mRec > 0 && sRec > 0 {
		breakdown = fmt.Sprintf("%d Movies, %d Series", mRec, sRec)
	} else if mRec > 0 {
		breakdown = fmt.Sprintf("%d Movie Reconciles", mRec)
	} else {
		breakdown = fmt.Sprintf("%d Series Reconciles", sRec)
	}

	log.Printf("[Maintenance] Verified %d items in %s (%s) | Progress: %s | Rate: %d items/s | %d remaining (Est: ~%s)",
		len(tasks), formatDuration(elapsed), breakdown, summary.ProgressString(), rate, summary.RemainingTasks, etaStr)
}

func (s *Syncer) verifyAndFlushBatch(ctx context.Context, workerID int, tasks []*models.QueueItem, lastCreated *itemSummary, newCount int) {
	if len(tasks) == 0 {
		return
	}

	var mAdds, mRec, mAct, mDel int
	var sAdds, sUpd, sRec, sAct, sDel int
	for _, t := range tasks {
		if t.ItemType == "movie" {
			switch {
			case t.Action == "pass1":
				mAdds++
			case strings.HasPrefix(t.Action, "reconcile"):
				mRec++
			case t.Action == "activate":
				mAct++
			case t.Action == "delete":
				mDel++
			}
		} else {
			switch {
			case t.Action == "pass1":
				sAdds++
			case t.Action == "update_episodes":
				sUpd++
			case strings.HasPrefix(t.Action, "reconcile"):
				sRec++
			case t.Action == "activate":
				sAct++
			case t.Action == "delete":
				sDel++
			}
		}
		_ = s.database.MarkTaskComplete(t.ID)
	}

	s.progressMu.Lock()
	s.completedTasks += int64(len(tasks))
	s.progressMu.Unlock()

	var batchSections []string
	totalM := mAdds + mRec + mAct + mDel
	totalS := sAdds + sUpd + sRec + sAct + sDel
	if mStr := formatActionBreakdown(totalM, "Movies", mAdds, 0, mRec, mAct, mDel); mStr != "" {
		batchSections = append(batchSections, mStr)
	}
	if sStr := formatActionBreakdown(totalS, "Series", sAdds, sUpd, sRec, sAct, sDel); sStr != "" {
		batchSections = append(batchSections, sStr)
	}
	batchSummaryStr := strings.Join(batchSections, " | ")
	if batchSummaryStr == "" {
		batchSummaryStr = fmt.Sprintf("%d total tasks", len(tasks))
	} else {
		batchSummaryStr = fmt.Sprintf("%d total tasks [%s]", len(tasks), batchSummaryStr)
	}

	if !s.embyClient.IsConfigured() || newCount == 0 {
		return
	}

	// Pause if any VIP activations are in flight before triggering global scan
	s.WaitForVIPActivations(ctx)

	summaryBeforeScan := s.GetQueueProgressSummary()
	log.Printf("[Ingest] Batch complete: %d new .strm file(s) written (%s). Triggering Emby library scan... | Progress: %s | %s | %s",
		newCount, batchSummaryStr, summaryBeforeScan.ProgressString(), summaryBeforeScan.RemainingString(), summaryBeforeScan.EstimateString())

	scanStart := time.Now()

	// 1. Trigger global Emby media library scan (activates ScheduledTask Key: RefreshLibrary)
	_ = s.embyClient.TriggerGlobalRefresh(ctx)

	// 2. Wait 2s for Emby task state to transition to Running
	time.Sleep(2 * time.Second)

	// 3. Wait until Emby finishes scanning and returns to 100% Idle
	log.Printf("[Ingest] Waiting for Emby to complete scan and reach Idle before next batch...")
	if s.embyClient.WaitForScanCompletion(ctx, 24*time.Hour) {
		scanDur := time.Since(scanStart)
		s.recordEmbyScanDuration(scanDur)
		summary := s.GetQueueProgressSummary()
		log.Printf("[Ingest] Emby scan completed in %s (avg: %s/batch across %d scan(s)). Ready for next batch. | Progress: %s | %s | %s",
			formatDuration(scanDur), formatDuration(summary.AvgScanDuration), summary.CompletedScans,
			summary.ProgressString(), summary.RemainingString(), summary.EstimateString())
	}

	// If a VIP activation cancelled the scan during WaitForScanCompletion, wait for it to finish
	s.WaitForVIPActivations(ctx)
}

func (s *Syncer) processQueueTask(ctx context.Context, task *models.QueueItem) (error, *itemSummary) {
	summary := &itemSummary{
		ItemType: task.ItemType,
		ItemID:   task.ItemID,
	}

	switch task.Action {
	case "pass1":
		if task.ItemType == "series" {
			st, err := s.database.GetSeriesState(task.ItemID)
			if err != nil || st == nil {
				srs, fErr := s.client.FetchSingleSeries(ctx, task.ItemID)
				if fErr != nil || srs == nil {
					return fmt.Errorf("series not found: %w", fErr), nil
				}
				_, _, strmCreated, _, _, _ := s.processSingleSeriesPass1Profiled(ctx, *srs, false)
				summary.Title = srs.Name
				summary.TMDBID = srs.TMDBID
				summary.IMDBID = srs.IMDBID
				summary.STRMCreated = strmCreated
			} else {
				seriesModel := models.Series{
					ID:     st.SeriesID,
					TMDBID: st.TMDBID,
					IMDBID: st.IMDBID,
					Name:   st.Title,
					Year:   st.Year,
				}
				_, _, strmCreated, _, _, _ := s.processSingleSeriesPass1Profiled(ctx, seriesModel, false)
				summary.Title = st.Title
				summary.TMDBID = st.TMDBID
				summary.IMDBID = st.IMDBID
				summary.STRMCreated = strmCreated
			}
			// Send targeted Media/Updated notification to Emby
			if s.embyClient.IsConfigured() && summary.STRMCreated {
				folderName := ""
				if st != nil && st.FolderName != "" {
					folderName = st.FolderName
				}
				if folderName != "" {
					_ = s.embyClient.NotifyMediaUpdated(ctx, "series", folderName)
				}
			}
		} else {
			mst, err := s.database.GetMovieState(task.ItemID)
			if err != nil || mst == nil {
				mv, fErr := s.client.FetchSingleMovie(ctx, task.ItemID)
				if fErr != nil || mv == nil {
					return fmt.Errorf("movie not found: %w", fErr), nil
				}
				_, _, strmCreated, _, _, _ := s.processSingleMovieProfiled(ctx, *mv, false)
				summary.Title = mv.Name
				summary.TMDBID = mv.TMDBID
				summary.IMDBID = mv.IMDBID
				summary.STRMCreated = strmCreated
				if s.database != nil {
					mst, _ = s.database.GetMovieState(task.ItemID)
				}
			} else {
				movieModel := models.Movie{
					ID:     mst.MovieID,
					TMDBID: mst.TMDBID,
					IMDBID: mst.IMDBID,
					Name:   mst.Title,
					Year:   mst.Year,
				}
				_, _, strmCreated, _, _, _ := s.processSingleMovieProfiled(ctx, movieModel, false)
				summary.Title = mst.Title
				summary.TMDBID = mst.TMDBID
				summary.IMDBID = mst.IMDBID
				summary.STRMCreated = strmCreated
			}
			// Send targeted Media/Updated notification to Emby
			if s.embyClient.IsConfigured() && summary.STRMCreated {
				folderName := ""
				if mst != nil && mst.FolderName != "" {
					folderName = mst.FolderName
				}
				if folderName != "" {
					_ = s.embyClient.NotifyMediaUpdated(ctx, "movie", folderName)
				}
			}
		}
		return nil, summary

	case "reconcile_series":
		st, err := s.database.GetSeriesState(task.ItemID)
		if err != nil || st == nil {
			return nil, summary
		}
		summary.Title = st.Title
		summary.TMDBID = st.TMDBID
		summary.IMDBID = st.IMDBID

		tvShowsRoot := filepath.Join(s.cfg.OutputDir, "TV Shows")
		canonicalFolderName := sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(st.Title), st.Year, st.TMDBID, st.IMDBID)
		currentFolderName := st.FolderName
		if currentFolderName == "" {
			currentFolderName = canonicalFolderName
		}
		currentDir := filepath.Join(tvShowsRoot, currentFolderName)
		canonicalDir := filepath.Join(tvShowsRoot, canonicalFolderName)

		// 1. Check if folder needs migration to canonical name (e.g. ID upgrade)
		if currentDir != canonicalDir {
			if _, statErr := os.Stat(currentDir); statErr == nil {
				if _, canonicalErr := os.Stat(canonicalDir); os.IsNotExist(canonicalErr) {
					if rErr := os.Rename(currentDir, canonicalDir); rErr == nil {
						log.Printf("[Worker] Reconciled TV Show folder: %q -> %q", currentFolderName, canonicalFolderName)
						summary.STRMCreated = true
						if s.database != nil {
							_ = s.database.UpdateSeriesFolderName(st.SeriesID, canonicalFolderName)
						}
						currentDir = canonicalDir
						st.FolderName = canonicalFolderName
					}
				}
			}
		}

		// 2. If unactivated, upgrade placeholder to Season 01 S01E01 standard and clean legacy Specials
		if !st.IsActivated {
			season1Dir := filepath.Join(currentDir, "Season 01")
			actStrmFilename := fmt.Sprintf("%s - S01E01.strm", sanitizer.SanitizeFileName(st.Title))
			actStrmPath := filepath.Join(season1Dir, actStrmFilename)
			seriesIdent := strm.FormatSeriesIdentifier(st.TMDBID, st.IMDBID, st.SeriesID)
			actProxyURL := strm.GenerateSeriesProxyURL(s.cfg, seriesIdent, models.ActivationSeasonNumber, models.ActivationEpisodeNumber, "base")

			writeStatus, _ := strm.WriteSTRMFileDetailed(actStrmPath, actProxyURL)
			if writeStatus == strm.STRMCreated {
				summary.STRMCreated = true
			}
			actNFOPath := filepath.Join(season1Dir, fmt.Sprintf("%s - S01E01.nfo", sanitizer.SanitizeFileName(st.Title)))
			_, _ = nfo.WriteActivationEpisodeNFO(actNFOPath, st.Title)

			// Clean up legacy Specials S00E99 placeholder
			specialsDir := filepath.Join(currentDir, "Specials")
			if spEntries, spErr := os.ReadDir(specialsDir); spErr == nil && len(spEntries) > 0 {
				_ = os.Remove(filepath.Join(specialsDir, fmt.Sprintf("%s - S00E99 - Activate Series.strm", sanitizer.SanitizeFileName(st.Title))))
				_ = os.Remove(filepath.Join(specialsDir, fmt.Sprintf("%s - S00E99 - Activate Series.nfo", sanitizer.SanitizeFileName(st.Title))))
				if remaining, _ := os.ReadDir(specialsDir); len(remaining) == 0 {
					_ = os.Remove(specialsDir)
				}
			}
		} else {
			// If already activated, ensure all episode STRM files on disk contain canonical proxy URLs (in-place updates)
			created, _, err := s.PopulateActivatedSeries(ctx, st.SeriesID, "", "")
			if err == nil && created > 0 {
				summary.STRMCreated = true
			}
		}

		if s.embyClient.IsConfigured() && summary.STRMCreated {
			_ = s.embyClient.NotifyMediaUpdated(ctx, "series", st.FolderName)
		}
		return nil, summary

	case "reconcile_movie":
		mst, err := s.database.GetMovieState(task.ItemID)
		if err != nil || mst == nil {
			return nil, summary
		}
		summary.Title = mst.Title
		summary.TMDBID = mst.TMDBID
		summary.IMDBID = mst.IMDBID

		moviesRoot := filepath.Join(s.cfg.OutputDir, "Movies")
		canonicalFolderName := sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(mst.Title), mst.Year, mst.TMDBID, mst.IMDBID)
		currentFolderName := mst.FolderName
		if currentFolderName == "" {
			currentFolderName = canonicalFolderName
		}
		currentDir := filepath.Join(moviesRoot, currentFolderName)
		canonicalDir := filepath.Join(moviesRoot, canonicalFolderName)

		// 1. Check if folder needs migration to canonical name (e.g. ID upgrade)
		if currentDir != canonicalDir {
			if _, statErr := os.Stat(currentDir); statErr == nil {
				if _, canonicalErr := os.Stat(canonicalDir); os.IsNotExist(canonicalErr) {
					if rErr := os.Rename(currentDir, canonicalDir); rErr == nil {
						log.Printf("[Worker] Reconciled Movie folder: %q -> %q", currentFolderName, canonicalFolderName)
						summary.STRMCreated = true
						if s.database != nil {
							_ = s.database.UpdateMovieFolderName(mst.MovieID, canonicalFolderName)
						}
						currentDir = canonicalDir
						mst.FolderName = canonicalFolderName
					}
				}
			}
		}

		// 2. Ensure Movie.strm contains the canonical proxy URL (in-place update with mtime preservation)
		strmPath := filepath.Join(currentDir, fmt.Sprintf("%s.strm", sanitizer.SanitizeFileName(mst.Title)))
		expectedIdent := strm.FormatMovieIdentifier(mst.TMDBID, mst.IMDBID, mst.MovieID)
		expectedURL := strm.GenerateMovieProxyURL(s.cfg, expectedIdent, "base")

		writeStatus, wErr := strm.WriteSTRMFileDetailed(strmPath, expectedURL)
		if wErr == nil {
			if writeStatus == strm.STRMCreated {
				summary.STRMCreated = true
				log.Printf("[Reconciler] Restored missing Movie STRM file on disk: %q", strmPath)
			}
		}

		// 3. If activated, also reconcile secondary quality variant STRM files
		if mst.IsActivated && s.database != nil {
			variants, _ := s.database.GetMovieStreamVariants(mst.MovieID)
			for _, v := range variants {
				if v.VariantKey != "" && v.VariantKey != "base" {
					varFilename := ""
					if v.Quality == "4K" || strings.HasPrefix(v.VariantKey, "4k") {
						if v.VariantKey == "4k" {
							varFilename = fmt.Sprintf("%s - 4K.strm", sanitizer.SanitizeFileName(mst.Title))
						} else {
							idx := strings.TrimPrefix(v.VariantKey, "4k-")
							varFilename = fmt.Sprintf("%s - 4K (%s).strm", sanitizer.SanitizeFileName(mst.Title), idx)
						}
					} else {
						idx := strings.TrimPrefix(v.VariantKey, "version")
						varFilename = fmt.Sprintf("%s - Version %s.strm", sanitizer.SanitizeFileName(mst.Title), idx)
					}
					varStrmPath := filepath.Join(currentDir, varFilename)
					varURL := strm.GenerateMovieProxyURL(s.cfg, expectedIdent, v.VariantKey)
					vStatus, _ := strm.WriteSTRMFileDetailed(varStrmPath, varURL)
					if vStatus == strm.STRMCreated {
						summary.STRMCreated = true
					}
				}
			}
		}

		if s.embyClient.IsConfigured() && summary.STRMCreated {
			_ = s.embyClient.NotifyMediaUpdated(ctx, "movie", mst.FolderName)
		}
		return nil, summary

	case "activate":
		if task.ItemType == "series" {
			_, _, err := s.PopulateActivatedSeries(ctx, task.ItemID, "", "")
			if err != nil {
				return err, nil
			}
			s.TriggerTargetedEmbySeriesRefresh(ctx, task.ItemID)
		}
		return nil, summary

	case "update_episodes":
		if task.ItemType == "series" {
			created, _, err := s.PopulateActivatedSeries(ctx, task.ItemID, "", "")
			if err != nil {
				return err, nil
			}
			if created > 0 {
				summary.STRMCreated = true
			}
			s.TriggerTargetedEmbySeriesRefresh(ctx, task.ItemID)
		}
		return nil, summary

	case "delete":
		return nil, summary
	}

	return nil, summary
}

// reconcilerLoop runs the periodic disk reconciler at the configured interval.
func (s *Syncer) reconcilerLoop(ctx context.Context) {
	defer s.workerWg.Done()

	interval := s.cfg.ReconcileInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	log.Printf("[Reconciler] Scheduled disk reconciler active every: %v", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.ReconcileDiskState(ctx)
		}
	}
}

// ReconcileDiskState stages daily reconciliation tasks into the priority queue.
func (s *Syncer) ReconcileDiskState(ctx context.Context) error {
	log.Println("[Reconciler] Starting daily disk reconciliation scan...")
	start := time.Now()
	reconcileCount := 0

	// 1. Purge expired soft-deleted items
	if s.database != nil && s.cfg.RetentionGraceDays > 0 {
		expiredPaths, err := s.database.GetExpiredSoftDeletedItems(s.cfg.RetentionGraceDays)
		if err == nil && len(expiredPaths) > 0 {
			log.Printf("[Reconciler] Purging %d expired soft-deleted items from disk...", len(expiredPaths))
			for _, p := range expiredPaths {
				_ = os.RemoveAll(p)
				_ = s.database.RemoveSoftDeletedItem(p)
			}
		}
	}

	// 2. Scan and enqueue TV Shows needing ID upgrade, Specials S00E99 upgrade, or episode URL reconciliation
	tvShowsRoot := filepath.Join(s.cfg.OutputDir, "TV Shows")
	if s.database != nil {
		allSeries, err := s.database.GetAllSeriesStates()
		if err == nil {
			for _, st := range allSeries {
				canonicalFolderName := sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(st.Title), st.Year, st.TMDBID, st.IMDBID)
				currentFolderName := st.FolderName
				if currentFolderName == "" {
					currentFolderName = canonicalFolderName
				}
				seriesDirPath := filepath.Join(tvShowsRoot, currentFolderName)
				canonicalDirPath := filepath.Join(tvShowsRoot, canonicalFolderName)

				needsReconcile := false
				// Check if on-disk folder needs migration to canonical name (e.g. ID upgrade)
				if seriesDirPath != canonicalDirPath {
					if _, statErr := os.Stat(seriesDirPath); statErr == nil {
						needsReconcile = true
					}
				}

				if !st.IsActivated {
					season1Dir := filepath.Join(seriesDirPath, "Season 01")
					specialsDir := filepath.Join(seriesDirPath, "Specials")
					actStrm := filepath.Join(season1Dir, fmt.Sprintf("%s - S01E01.strm", sanitizer.SanitizeFileName(st.Title)))
					actNFO := filepath.Join(season1Dir, fmt.Sprintf("%s - S01E01.nfo", sanitizer.SanitizeFileName(st.Title)))

					expectedIdent := strm.FormatSeriesIdentifier(st.TMDBID, st.IMDBID, st.SeriesID)
					expectedActURL := strm.GenerateSeriesProxyURL(s.cfg, expectedIdent, models.ActivationSeasonNumber, models.ActivationEpisodeNumber, "base")

					// Check if on-disk legacy Specials exists
					if _, spErr := os.Stat(specialsDir); spErr == nil {
						needsReconcile = true
					}
					// Check if Season 01 activation placeholder STRM or NFO is missing or has outdated URL
					if contentBytes, actErr := os.ReadFile(actStrm); actErr != nil {
						needsReconcile = true
					} else if strings.TrimSpace(string(contentBytes)) != strings.TrimSpace(expectedActURL) {
						needsReconcile = true
					}
					if _, actNFOErr := os.Stat(actNFO); os.IsNotExist(actNFOErr) {
						needsReconcile = true
					} else if nfoBytes, readErr := os.ReadFile(actNFO); readErr == nil {
						if !strings.Contains(string(nfoBytes), "<watched>false</watched>") || !strings.Contains(string(nfoBytes), "<runtime>") {
							needsReconcile = true
						}
					}
				}

				if needsReconcile {
					priority := models.PriorityReconcile
					if !st.IsActivated {
						priority = models.PriorityMigration
					}
					_ = s.database.EnqueueTask("series", st.SeriesID, "reconcile_series", priority)
					reconcileCount++
				}
			}
		}
	}

	// 3. Scan and enqueue Movies needing folder ID upgrade, missing STRM file restoration, or URL upgrade
	moviesRoot := filepath.Join(s.cfg.OutputDir, "Movies")
	if s.database != nil {
		allMovies, err := s.database.GetAllMovieStates()
		if err == nil {
			for _, mst := range allMovies {
				canonicalFolderName := sanitizer.BuildMovieFolderName(sanitizer.CleanTitle(mst.Title), mst.Year, mst.TMDBID, mst.IMDBID)
				currentFolderName := mst.FolderName
				if currentFolderName == "" {
					currentFolderName = canonicalFolderName
				}
				movieDir := filepath.Join(moviesRoot, currentFolderName)
				canonicalDir := filepath.Join(moviesRoot, canonicalFolderName)
				strmPath := filepath.Join(movieDir, fmt.Sprintf("%s.strm", sanitizer.SanitizeFileName(mst.Title)))

				expectedIdent := strm.FormatMovieIdentifier(mst.TMDBID, mst.IMDBID, mst.MovieID)
				expectedURL := strm.GenerateMovieProxyURL(s.cfg, expectedIdent, "base")

				needsReconcile := false
				// Check if on-disk folder needs migration to canonical name (e.g. ID upgrade)
				if movieDir != canonicalDir {
					if _, statErr := os.Stat(movieDir); statErr == nil {
						needsReconcile = true
					}
				}
				if contentBytes, statErr := os.ReadFile(strmPath); statErr != nil {
					needsReconcile = true
				} else if strings.TrimSpace(string(contentBytes)) != strings.TrimSpace(expectedURL) {
					needsReconcile = true
				}

				if needsReconcile {
					_ = s.database.EnqueueTask("movie", mst.MovieID, "reconcile_movie", models.PriorityReconcile)
					reconcileCount++
				}
			}
		}
	}

	log.Printf("[Reconciler] Reconciliation scan complete in %v. %d maintenance task(s) enqueued at PriorityReconcile.", time.Since(start), reconcileCount)
	return nil
}

func formatActionBreakdown(total int, label string, adds, updates, reconciles, acts, dels int) string {
	if total == 0 {
		return ""
	}
	var sub []string
	if adds > 0 {
		sub = append(sub, fmt.Sprintf("%d Adds", adds))
	}
	if updates > 0 {
		sub = append(sub, fmt.Sprintf("%d Updates", updates))
	}
	if reconciles > 0 {
		sub = append(sub, fmt.Sprintf("%d Reconciles", reconciles))
	}
	if acts > 0 {
		sub = append(sub, fmt.Sprintf("%d Activations", acts))
	}
	if dels > 0 {
		sub = append(sub, fmt.Sprintf("%d Deletes", dels))
	}
	if len(sub) == 0 {
		return fmt.Sprintf("%d %s", total, label)
	}
	return fmt.Sprintf("%d %s: %s", total, label, strings.Join(sub, ", "))
}

// QueueProgressSummary captures real-time queue completion status and Emby refresh time estimations.
type QueueProgressSummary struct {
	CompletedTasks    int64
	PendingTasks      int
	InProgressTasks   int
	FailedTasks       int
	RemainingTasks    int
	TotalTasks        int64
	ProgressPct       float64
	BatchSize         int
	RemainingBatches  int
	AvgScanDuration   time.Duration
	LastScanDuration  time.Duration
	CompletedScans    int64
	EstimatedDuration time.Duration
	HasScanHistory    bool
	// Media Type & Action Breakdown
	MovieAdds         int
	MovieReconciles   int
	MovieActivations  int
	MovieDeletes      int
	SeriesAdds        int
	SeriesUpdates     int
	SeriesReconciles  int
	SeriesActivations int
	SeriesDeletes     int
}

func (p QueueProgressSummary) TotalMovies() int {
	return p.MovieAdds + p.MovieReconciles + p.MovieActivations + p.MovieDeletes
}

func (p QueueProgressSummary) TotalSeries() int {
	return p.SeriesAdds + p.SeriesUpdates + p.SeriesReconciles + p.SeriesActivations + p.SeriesDeletes
}

// GetQueueProgressSummary queries database queue state and computes overall progress and estimated time.
func (s *Syncer) GetQueueProgressSummary() QueueProgressSummary {
	s.progressMu.Lock()
	completed := s.completedTasks
	scans := s.completedScanCount
	totalScanDur := s.totalScanDuration
	lastScanDur := s.lastScanDuration
	s.progressMu.Unlock()

	var qStats db.QueueStats
	if s.database != nil {
		qStats, _ = s.database.GetDetailedQueueStats()
	}

	remaining := qStats.Pending + qStats.InProgress
	total := completed + int64(remaining)

	var pct float64
	if total > 0 {
		pct = (float64(completed) / float64(total)) * 100.0
	} else if completed > 0 && remaining == 0 {
		pct = 100.0
	}

	batchSize := s.cfg.EmbyBatchSize
	if batchSize <= 0 {
		batchSize = 25
	}

	remainingBatches := 0
	if remaining > 0 {
		remainingBatches = (remaining + batchSize - 1) / batchSize
	}

	var avgScanDur time.Duration
	var estDur time.Duration
	hasHistory := scans > 0

	if hasHistory {
		avgScanDur = totalScanDur / time.Duration(scans)
		estDur = time.Duration(remainingBatches) * avgScanDur
	}

	return QueueProgressSummary{
		CompletedTasks:    completed,
		PendingTasks:      qStats.Pending,
		InProgressTasks:   qStats.InProgress,
		FailedTasks:       qStats.Failed,
		RemainingTasks:    remaining,
		TotalTasks:        total,
		ProgressPct:       pct,
		BatchSize:         batchSize,
		RemainingBatches:  remainingBatches,
		AvgScanDuration:   avgScanDur,
		LastScanDuration:  lastScanDur,
		CompletedScans:    scans,
		EstimatedDuration: estDur,
		HasScanHistory:    hasHistory,
		MovieAdds:         qStats.MovieAdds,
		MovieReconciles:   qStats.MovieReconciles,
		MovieActivations:  qStats.MovieActivations,
		MovieDeletes:      qStats.MovieDeletes,
		SeriesAdds:        qStats.SeriesAdds,
		SeriesUpdates:     qStats.SeriesUpdates,
		SeriesReconciles:  qStats.SeriesReconciles,
		SeriesActivations: qStats.SeriesActivations,
		SeriesDeletes:     qStats.SeriesDeletes,
	}
}

func (s *Syncer) recordEmbyScanDuration(dur time.Duration) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.totalScanDuration += dur
	s.lastScanDuration = dur
	s.completedScanCount++
}

func (s *Syncer) resetCompletedTasks() {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.completedTasks = 0
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60

	if h > 0 {
		if m > 0 {
			return fmt.Sprintf("%dh %02dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	if m > 0 {
		if sec > 0 {
			return fmt.Sprintf("%dm %02ds", m, sec)
		}
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", sec)
}

func (p QueueProgressSummary) ProgressString() string {
	return fmt.Sprintf("%d/%d (%.1f%%)", p.CompletedTasks, p.TotalTasks, p.ProgressPct)
}

func (p QueueProgressSummary) RemainingString() string {
	if p.RemainingTasks == 0 {
		return "0 tasks left"
	}
	var sections []string
	if mStr := formatActionBreakdown(p.TotalMovies(), "Movies", p.MovieAdds, 0, p.MovieReconciles, p.MovieActivations, p.MovieDeletes); mStr != "" {
		sections = append(sections, mStr)
	}
	if sStr := formatActionBreakdown(p.TotalSeries(), "Series", p.SeriesAdds, p.SeriesUpdates, p.SeriesReconciles, p.SeriesActivations, p.SeriesDeletes); sStr != "" {
		sections = append(sections, sStr)
	}

	breakdown := ""
	if len(sections) > 0 {
		breakdown = fmt.Sprintf(" [%s]", strings.Join(sections, " | "))
	}

	if p.RemainingBatches <= 1 {
		return fmt.Sprintf("%d task(s) remaining%s (~1 batch)", p.RemainingTasks, breakdown)
	}
	return fmt.Sprintf("%d task(s) remaining%s (~%d batches)", p.RemainingTasks, breakdown, p.RemainingBatches)
}

func (p QueueProgressSummary) EstimateString() string {
	if p.RemainingTasks == 0 {
		return "Sync complete"
	}
	if !p.HasScanHistory {
		return "Est. remaining: calculating after first scan..."
	}
	return fmt.Sprintf("Est. remaining: ~%s (avg %s/batch)", formatDuration(p.EstimatedDuration), formatDuration(p.AvgScanDuration))
}

