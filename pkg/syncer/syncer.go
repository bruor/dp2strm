package syncer

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dp2strm/pkg/artwork"
	"dp2strm/pkg/client"
	"dp2strm/pkg/models"
	"dp2strm/pkg/nfo"
	"dp2strm/pkg/sanitizer"
	"dp2strm/pkg/strm"
	"dp2strm/pkg/versioning"
)

type Syncer struct {
	cfg            *models.Config
	client         *client.Client
	circuitTripped bool
	mu             sync.Mutex
}

func NewSyncer(cfg *models.Config, client *client.Client) *Syncer {
	return &Syncer{
		cfg:    cfg,
		client: client,
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

		// Stop if we hit configured batch limit
		if s.cfg.BatchSize > 0 && totalProcessed >= s.cfg.BatchSize {
			log.Printf("[Syncer] Reached configured batch size limit (%d movies). Stopping sync pass.", s.cfg.BatchSize)
			break
		}
	}

	duration := time.Since(start)
	log.Printf("[Syncer] Movie Sync Complete in %v: %d movies processed, %d files created/updated, %d preserved.",
		duration, totalProcessed, totalCreated, totalPreserved)

	// Trigger Emby Refresh if new/updated files were created
	if totalCreated > 0 && s.cfg.EmbyURL != "" && s.cfg.EmbyAPIKey != "" && !dryRun {
		s.triggerEmbyRefresh(ctx)
	}

	return nil
}

// SyncSeries executes a full TV series sync pass from Dispatcharr REST API to disk.
func (s *Syncer) SyncSeries(ctx context.Context, dryRun bool) error {
	log.Println("[Syncer] Starting Series Sync Pass...")
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

			created, preserved, err := s.processSingleSeries(ctx, series, dryRun)
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
	log.Printf("[Syncer] Series Sync Complete in %v: %d series processed, %d files created/updated, %d preserved.",
		duration, totalProcessed, totalCreated, totalPreserved)

	return nil
}

func (s *Syncer) processSingleSeries(ctx context.Context, series models.Series, dryRun bool) (created, preserved int, err error) {
	cleanTitle := sanitizer.CleanTitle(series.Name)
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, series.Year, series.TMDBID)
	seriesDir := filepath.Join(s.cfg.OutputDir, "TV Shows", folderName)

	episodes, err := s.client.FetchSeriesEpisodes(ctx, series.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to fetch episodes for series ID %d: %w", series.ID, err)
	}

	if dryRun {
		log.Printf("[DRY-RUN] Series '%s' (TMDB %s) -> Folder: %s (%d episodes)", cleanTitle, series.TMDBID, folderName, len(episodes))
		return len(episodes), 0, nil
	}

	for _, ep := range episodes {
		seasonDir := filepath.Join(seriesDir, fmt.Sprintf("Season %02d", ep.SeasonNumber))
		epTitle := sanitizer.CleanEpisodeTitle(ep.Name, series.Name, ep.SeasonNumber, ep.EpisodeNumber)
		filename := fmt.Sprintf("%s - S%02dE%02d - %s.strm", sanitizer.SanitizeFileName(cleanTitle), ep.SeasonNumber, ep.EpisodeNumber, epTitle)
		strmPath := filepath.Join(seasonDir, filename)

		proxyURL := strm.GenerateSeriesProxyURL(s.cfg, series.ID, fmt.Sprintf("%d", ep.ID), "mp4")

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

	return created, preserved, nil
}

func (s *Syncer) processSingleMovie(ctx context.Context, movie models.Movie, dryRun bool) (created, preserved int, err error) {
	// Clean movie title (strip quality tags like 4K/1080p for directory name)
	cleanTitle := sanitizer.CleanTitle(movie.Name)

	// Strict TMDB Primary Key directory name
	folderName := sanitizer.BuildMovieFolderName(cleanTitle, movie.Year, movie.TMDBID)
	movieDir := filepath.Join(s.cfg.OutputDir, "Movies", folderName)

	// Fetch all provider relations for this movie
	relations, err := s.client.FetchMovieProviders(ctx, movie.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to fetch providers for movie ID %d: %w", movie.ID, err)
	}

	if len(relations) == 0 {
		return 0, 0, nil
	}

	// Classify streams via 4-Tier Quality Waterfall
	classified := versioning.ClassifyMovieStreams(relations, cleanTitle, movie.Year)

	if dryRun {
		log.Printf("[DRY-RUN] Movie '%s' (TMDB %s) -> Folder: %s (%d versions)", cleanTitle, movie.TMDBID, folderName, len(classified))
		for _, v := range classified {
			log.Printf("   ├── STRM: %s [%s Quality, Bitrate: %d]", v.Filename, v.Quality, v.Bitrate)
		}
		return len(classified), 0, nil
	}

	// Generate .nfo metadata file once per movie folder
	nfoPath := filepath.Join(movieDir, fmt.Sprintf("%s.nfo", sanitizer.SanitizeFileName(cleanTitle)))
	nfoUpdated, err := nfo.WriteMovieNFO(nfoPath, movie)
	if err != nil {
		log.Printf("[Syncer] Error writing NFO for %s: %v", cleanTitle, err)
	} else if nfoUpdated {
		created++
	} else {
		preserved++
	}

	// Download poster.jpg & fanart.jpg into .strm item directory if missing/updated
	artCreated := artwork.DownloadMovieArtwork(movieDir, movie)
	created += artCreated

	// Write .strm files for each version variant
	for _, v := range classified {
		strmPath := filepath.Join(movieDir, v.Filename)
		proxyURL := strm.GenerateMovieProxyURL(s.cfg, v.StreamID, v.Container)

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
