package syncer

import (
	"context"
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

// SyncMovies executes a full movie sync pass from Dispatcharr REST API to disk with parallel page fetching.
func (s *Syncer) SyncMovies(ctx context.Context, dryRun bool) error {
	log.Println("[Syncer] Starting Parallel Movie Sync Pass...")
	start := time.Now()

	pageSize := s.cfg.PageSize
	if pageSize <= 0 {
		pageSize = 1000
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
		return fmt.Errorf("failed to fetch movie page 1: %w", err)
	}
	log.Printf("[Syncer] Movie Page 1 API Fetch took %v", apiDuration)

	if len(firstResp.Results) == 0 {
		log.Println("[Syncer] Movie Sync Complete: 0 movies found.")
		return nil
	}

	totalCount := firstResp.Count
	if totalCount <= 0 {
		totalCount = len(firstResp.Results)
	}
	totalPages := (totalCount + pageSize - 1) / pageSize

	log.Printf("[Syncer] Found %d total movies across %d pages.", totalCount, totalPages)

	var totalProcessed int64
	var totalCreated int64
	var totalPreserved int64

	processPage := func(pageNum int, movies []models.Movie) {
		atomic.AddInt64(&totalProcessed, int64(len(movies)))
		var wg sync.WaitGroup
		sem := make(chan struct{}, concurrency)

		var pCreated, pPreserved int64
		var pArtDur, pDiskDur time.Duration
		var mu sync.Mutex

		pStart := time.Now()
		for _, m := range movies {
			wg.Add(1)
			sem <- struct{}{}
			go func(movie models.Movie) {
				defer func() {
					<-sem
					wg.Done()
				}()
				c, p, aDur, dDur, err := s.processSingleMovieProfiled(ctx, movie, dryRun)
				if err != nil {
					log.Printf("[Syncer] Warning: Error processing movie '%s' (ID %d): %v", movie.Name, movie.ID, err)
					return
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
	if totalPages > 1 {
		pageWorkers := s.cfg.PageWorkers
		if pageWorkers <= 0 {
			pageWorkers = 4
		}
		pageChan := make(chan int, totalPages)
		for p := 2; p <= totalPages; p++ {
			if s.cfg.BatchSize > 0 && (p-1)*pageSize >= s.cfg.BatchSize {
				break
			}
			pageChan <- p
		}
		close(pageChan)

		var pageWg sync.WaitGroup
		for w := 0; w < pageWorkers; w++ {
			pageWg.Add(1)
			go func() {
				defer pageWg.Done()
				for pageNum := range pageChan {
					fStart := time.Now()
					resp, err := s.client.FetchMoviePage(ctx, pageNum, pageSize)
					fDuration := time.Since(fStart)
					if err != nil {
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

	duration := time.Since(start)
	log.Printf("[Syncer] Movie Sync Complete in %v: %d movies processed, %d files created/updated, %d preserved.",
		duration, totalProcessed, totalCreated, totalPreserved)

	// Trigger Emby Refresh ONLY if files were created or deleted
	if totalCreated > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}

	return nil
}

// SyncSeries Pass 1: Generates initial lightweight TV series tree with S01E01 Activation placeholder.
func (s *Syncer) SyncSeries(ctx context.Context, dryRun bool) error {
	log.Println("[Syncer] Starting Pass 1 TV Series Sync Pass...")
	start := time.Now()

	totalProcessed := 0
	totalCreated := 0
	totalPreserved := 0
	page := 1
	pageSize := 100

	for {
		log.Printf("[Syncer] Fetching series page %d (size %d)...", page, pageSize)
		resp, err := s.client.FetchSeriesPage(ctx, page, pageSize)
		if err != nil {
			return fmt.Errorf("failed to fetch series page %d: %w", page, err)
		}

		if len(resp.Results) == 0 {
			break
		}

		for _, series := range resp.Results {
			totalProcessed++

			created, preserved, err := s.processSingleSeriesPass1(ctx, series, dryRun)
			if err != nil {
				log.Printf("[Syncer] Warning: Error processing series '%s' (ID %d): %v", series.Name, series.ID, err)
				continue
			}

			totalCreated += created
			totalPreserved += preserved
		}

		if resp.Next == nil {
			break
		}
		page++

		if s.cfg.BatchSize > 0 && totalProcessed >= s.cfg.BatchSize {
			log.Printf("[Syncer] Reached configured batch size limit (%d series). Stopping series sync pass.", s.cfg.BatchSize)
			break
		}
	}

	duration := time.Since(start)
	log.Printf("[Syncer] Pass 1 Series Sync Complete in %v: %d series processed, %d files created/updated, %d preserved.",
		duration, totalProcessed, totalCreated, totalPreserved)

	if totalCreated > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}

	return nil
}

// processSingleSeriesPass1 creates the lightweight initial folder, tvshow.nfo, poster/fanart, and Season 01 Activation placeholder.
func (s *Syncer) processSingleSeriesPass1(ctx context.Context, series models.Series, dryRun bool) (created, preserved int, err error) {
	cleanTitle := sanitizer.CleanTitle(series.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, series.Year, series.TMDBID)
	seriesDir := filepath.Join(s.cfg.OutputDir, "TV Shows", folderName)

	if dryRun {
		log.Printf("[DRY-RUN] Pass 1 Series '%s' (TMDB %s) -> Folder: %s", cleanTitle, series.TMDBID, folderName)
		return 1, 0, nil
	}

	// 1. Write tvshow.nfo
	tvshowNFOPath := filepath.Join(seriesDir, "tvshow.nfo")
	nfoUpdated, err := nfo.WriteTVShowNFO(tvshowNFOPath, series)
	if err == nil {
		if nfoUpdated {
			created++
		} else {
			preserved++
		}
	}

	// 2. Download poster.jpg & fanart.jpg
	artCreated := artwork.DownloadSeriesArtwork(seriesDir, series)
	created += artCreated

	// Check if this series has already been activated by user
	if s.database != nil {
		st, _ := s.database.GetSeriesState(series.ID)
		if st != nil && st.IsActivated {
			// Already activated! Execute full episode sync for this series
			return s.PopulateActivatedSeries(ctx, series.ID, cleanTitle, seriesDir)
		}
	}

	// 3. Write Pass 1 Season 01 Activation Episode placeholder
	season1Dir := filepath.Join(seriesDir, "Season 01")
	actStrmFilename := fmt.Sprintf("%s - S01E01 - Play to Activate Series.strm", sanitizer.SanitizeFileName(cleanTitle))
	actStrmPath := filepath.Join(season1Dir, actStrmFilename)

	actNFOFilename := fmt.Sprintf("%s - S01E01 - Play to Activate Series.nfo", sanitizer.SanitizeFileName(cleanTitle))
	actNFOPath := filepath.Join(season1Dir, actNFOFilename)

	// Playback URL points to dp2strm clean extensionless endpoint
	actProxyURL := strm.GenerateSeriesProxyURL(s.cfg, series.ID, 1, 1, "base")

	strmUpdated, err := strm.WriteSTRMFile(actStrmPath, actProxyURL)
	if err == nil {
		if strmUpdated {
			created++
		} else {
			preserved++
		}
	}

	actPlot := fmt.Sprintf("Play this episode to dynamically populate all seasons and episodes for %s in Emby.", cleanTitle)
	nfoUpdated, err = nfo.WriteEpisodeNFO(actNFOPath, "Play Episode to Activate Series Content", 1, 1, actPlot, series.TMDBID)
	if err == nil {
		if nfoUpdated {
			created++
		} else {
			preserved++
		}
	}

	return created, preserved, nil
}

// PopulateActivatedSeries (Pass 2): Loads all episodes from Dispatcharr and writes real .strm files on disk.
func (s *Syncer) PopulateActivatedSeries(ctx context.Context, seriesID int, cleanTitle, seriesDir string) (created, preserved int, err error) {
	episodes, err := s.client.FetchSeriesEpisodes(ctx, seriesID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to fetch episodes for series ID %d: %w", seriesID, err)
	}

	// Delete Pass 1 Activation placeholder files if present
	season1Dir := filepath.Join(seriesDir, "Season 01")
	actStrmFilename := fmt.Sprintf("%s - S01E01 - Play to Activate Series.strm", sanitizer.SanitizeFileName(cleanTitle))
	actNFOFilename := fmt.Sprintf("%s - S01E01 - Play to Activate Series.nfo", sanitizer.SanitizeFileName(cleanTitle))
	_ = os.Remove(filepath.Join(season1Dir, actStrmFilename))
	_ = os.Remove(filepath.Join(season1Dir, actNFOFilename))

	for _, ep := range episodes {
		seasonDir := filepath.Join(seriesDir, fmt.Sprintf("Season %02d", ep.SeasonNumber))
		epTitle := sanitizer.CleanEpisodeTitle(ep.Name, cleanTitle, ep.SeasonNumber, ep.EpisodeNumber)
		filename := fmt.Sprintf("%s - S%02dE%02d - %s.strm", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber, epTitle)
		strmPath := filepath.Join(seasonDir, filename)

		proxyURL := strm.GenerateSeriesProxyURL(s.cfg, seriesID, ep.SeasonNumber, ep.EpisodeNumber, "base")

		// Record real stream 302 target URL in SQLite
		realTargetURL := fmt.Sprintf("%s/series/%s/%s/%d.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), s.cfg.XCUser, s.cfg.XCPassword, ep.ID, ep.ContainerExtension)
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
				ContainerExtension: ep.ContainerExtension,
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
	}

	// Record series as activated in SQLite
	if s.database != nil {
		s.database.SetSeriesActivated(seriesID, "", cleanTitle)
	}

	return created, preserved, nil
}

func (s *Syncer) processSingleMovie(ctx context.Context, movie models.Movie, dryRun bool) (created, preserved int, err error) {
	c, p, _, _, err := s.processSingleMovieProfiled(ctx, movie, dryRun)
	return c, p, err
}

func (s *Syncer) processSingleMovieProfiled(ctx context.Context, movie models.Movie, dryRun bool) (created, preserved int, artDur, diskDur time.Duration, err error) {
	cleanTitle := sanitizer.CleanTitle(movie.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, movie.Year, movie.TMDBID)
	movieDir := filepath.Join(s.cfg.OutputDir, "Movies", folderName)

	if dryRun {
		log.Printf("[DRY-RUN] Movie '%s' (TMDB %s) -> Folder: %s", cleanTitle, movie.TMDBID, folderName)
		return 1, 0, 0, 0, nil
	}

	diskStart := time.Now()
	// 1. Generate .nfo metadata file
	nfoPath := filepath.Join(movieDir, fmt.Sprintf("%s.nfo", sanitizer.SanitizeFileName(cleanTitle)))
	nfoUpdated, err := nfo.WriteMovieNFO(nfoPath, movie)
	if err == nil {
		if nfoUpdated {
			created++
		} else {
			preserved++
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
	} else {
		preserved++
	}
	diskDur = time.Since(diskStart)

	// 3. Download artwork if logo/backdrop present
	artStart := time.Now()
	artCreated := artwork.DownloadMovieArtwork(movieDir, movie)
	artDur = time.Since(artStart)
	created += artCreated

	return created, preserved, artDur, diskDur, nil
}

func (s *Syncer) triggerEmbyRefresh(ctx context.Context) {
	embyEndpoint := fmt.Sprintf("%s/Library/Refresh", strings.TrimRight(s.cfg.EmbyURL, "/"))
	query := url.Values{}
	query.Set("api_key", s.cfg.EmbyAPIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embyEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		log.Printf("[Emby] Failed to create library refresh request: %v", err)
		return
	}

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

// TriggerTargetedEmbySeriesRefresh sends a targeted item refresh to Emby when a series is activated.
func (s *Syncer) TriggerTargetedEmbySeriesRefresh(ctx context.Context, seriesID int) {
	if s.cfg.EmbyURL == "" || s.cfg.EmbyAPIKey == "" {
		return
	}

	embyEndpoint := fmt.Sprintf("%s/Items/%d/Refresh", strings.TrimRight(s.cfg.EmbyURL, "/"), seriesID)
	query := url.Values{}
	query.Set("Recursive", "true")
	query.Set("ImageRefreshMode", "Full")
	query.Set("MetadataRefreshMode", "Full")
	query.Set("api_key", s.cfg.EmbyAPIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, embyEndpoint+"?"+query.Encode(), nil)
	if err != nil {
		log.Printf("[Emby] Failed to create targeted series refresh request: %v", err)
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp != nil {
		resp.Body.Close()
		log.Printf("[Emby] Triggered targeted Emby series refresh for Series ID %d", seriesID)
	}
}
