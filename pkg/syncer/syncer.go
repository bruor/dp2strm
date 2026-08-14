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

// SyncMovies executes a full movie sync pass from Dispatcharr REST API to disk.
func (s *Syncer) SyncMovies(ctx context.Context, dryRun bool) error {
	log.Println("[Syncer] Starting Movie Sync Pass...")
	start := time.Now()

	totalProcessed := 0
	totalCreated := 0
	totalPreserved := 0
	page := 1
	pageSize := 100

	for {
		log.Printf("[Syncer] Fetching movie page %d (size %d)...", page, pageSize)
		resp, err := s.client.FetchMoviePage(ctx, page, pageSize)
		if err != nil {
			return fmt.Errorf("failed to fetch movie page %d: %w", page, err)
		}

		if len(resp.Results) == 0 {
			break
		}

		for _, movie := range resp.Results {
			totalProcessed++

			created, preserved, err := s.processSingleMovie(ctx, movie, dryRun)
			if err != nil {
				log.Printf("[Syncer] Warning: Error processing movie '%s' (ID %d): %v", movie.Name, movie.ID, err)
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
			log.Printf("[Syncer] Reached configured batch size limit (%d movies). Stopping sync pass.", s.cfg.BatchSize)
			break
		}
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
		realTargetURL := fmt.Sprintf("%s/series/agy/agypassword/%d.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), ep.ID, ep.ContainerExtension)
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
	cleanTitle := sanitizer.CleanTitle(movie.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, movie.Year, movie.TMDBID)
	movieDir := filepath.Join(s.cfg.OutputDir, "Movies", folderName)

	relations, err := s.client.FetchMovieProviders(ctx, movie.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to fetch providers for movie ID %d: %w", movie.ID, err)
	}

	if len(relations) == 0 {
		return 0, 0, nil
	}

	classified := versioning.ClassifyMovieStreams(relations, cleanTitle, movie.Year)

	if dryRun {
		log.Printf("[DRY-RUN] Movie '%s' (TMDB %s) -> Folder: %s (%d versions)", cleanTitle, movie.TMDBID, folderName, len(classified))
		return len(classified), 0, nil
	}

	// Generate .nfo metadata file
	nfoPath := filepath.Join(movieDir, fmt.Sprintf("%s.nfo", sanitizer.SanitizeFileName(cleanTitle)))
	nfoUpdated, err := nfo.WriteMovieNFO(nfoPath, movie)
	if err != nil {
		log.Printf("[Syncer] Error writing NFO for %s: %v", cleanTitle, err)
	} else if nfoUpdated {
		created++
	} else {
		preserved++
	}

	artCreated := artwork.DownloadMovieArtwork(movieDir, movie)
	created += artCreated

	// Save variants to database & write clean extensionless .strm files
	for _, v := range classified {
		variantKey := "base"
		if strings.Contains(v.Filename, "- 4K") {
			variantKey = "4k"
		}

		strmPath := filepath.Join(movieDir, v.Filename)
		proxyURL := strm.GenerateMovieProxyURL(s.cfg, movie.ID, variantKey)

		// Record real stream target URL in SQLite
		realTargetURL := fmt.Sprintf("%s/movie/agy/agypassword/%s.%s", strings.TrimRight(s.cfg.DispatcharrURL, "/"), v.StreamID, v.Container)
		if s.database != nil {
			s.database.SaveStreamVariant(&db.StreamVariant{
				ItemID:             movie.ID,
				ItemType:           "movie",
				VariantKey:         variantKey,
				TargetURL:          realTargetURL,
				Quality:            string(v.Quality),
				StreamID:           v.StreamID,
				ContainerExtension: v.Container,
			})
		}

		updated, err := strm.WriteSTRMFile(strmPath, proxyURL)
		if err != nil {
			log.Printf("[Syncer] Error writing STRM file %s: %v", strmPath, err)
			continue
		}

		if updated {
			created++
		} else {
			preserved++
		}
	}

	return created, preserved, nil
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
