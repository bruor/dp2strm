package emby

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dp2strm/pkg/models"
)

// Client handles zero-config discovery and targeted rescanning with an Emby server.
type Client struct {
	cfg        *models.Config
	httpClient *http.Client
	mu         sync.RWMutex

	// Discovered library attributes
	tvLibraryID           string
	tvLibraryPath         string
	tvRealtimeMonitor     bool
	moviesLibraryID       string
	moviesLibraryPath     string
	moviesRealtimeMonitor bool
	discovered            bool
}

// NewClient initializes a new Emby API client.
func NewClient(cfg *models.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// IsConfigured returns true if EmbyURL and EmbyAPIKey are set.
func (c *Client) IsConfigured() bool {
	return c.cfg.EmbyURL != "" && c.cfg.EmbyAPIKey != ""
}

// DiscoverLibraries queries Emby's VirtualFolders API to automatically discover TV and Movies libraries, IDs, mount paths, and RealtimeMonitor status.
func (c *Client) DiscoverLibraries(ctx context.Context) error {
	if !c.IsConfigured() {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	reqURL := fmt.Sprintf("%s/Library/VirtualFolders", strings.TrimRight(c.cfg.EmbyURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create VirtualFolders request: %w", err)
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("VirtualFolders API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("VirtualFolders API returned status %d", resp.StatusCode)
	}

	var virtualFolders []struct {
		Name           string   `json:"Name"`
		CollectionType string   `json:"CollectionType"`
		ItemID         string   `json:"ItemId"`
		Locations      []string `json:"Locations"`
		LibraryOptions struct {
			EnableRealtimeMonitor bool `json:"EnableRealtimeMonitor"`
		} `json:"LibraryOptions"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&virtualFolders); err != nil {
		return fmt.Errorf("failed to decode VirtualFolders response: %w", err)
	}

	for _, folder := range virtualFolders {
		loc := ""
		if len(folder.Locations) > 0 {
			loc = folder.Locations[0]
		}

		// Match TV Shows library
		isTV := folder.CollectionType == "tvshows"
		if c.cfg.EmbyTVLibrary != "" && strings.EqualFold(folder.Name, c.cfg.EmbyTVLibrary) {
			isTV = true
		}
		if isTV && (c.tvLibraryID == "" || strings.EqualFold(folder.Name, c.cfg.EmbyTVLibrary)) {
			c.tvLibraryID = folder.ItemID
			c.tvLibraryPath = loc
			c.tvRealtimeMonitor = folder.LibraryOptions.EnableRealtimeMonitor
			log.Printf("[Emby] Auto-discovered TV library %q (ID: %s, Mount: %q, RealtimeMonitor: %v)", folder.Name, folder.ItemID, loc, c.tvRealtimeMonitor)
		}

		// Match Movies library
		isMovies := folder.CollectionType == "movies"
		if c.cfg.EmbyMoviesLibrary != "" && strings.EqualFold(folder.Name, c.cfg.EmbyMoviesLibrary) {
			isMovies = true
		}
		if isMovies && (c.moviesLibraryID == "" || strings.EqualFold(folder.Name, c.cfg.EmbyMoviesLibrary)) {
			c.moviesLibraryID = folder.ItemID
			c.moviesLibraryPath = loc
			c.moviesRealtimeMonitor = folder.LibraryOptions.EnableRealtimeMonitor
			log.Printf("[Emby] Auto-discovered Movies library %q (ID: %s, Mount: %q, RealtimeMonitor: %v)", folder.Name, folder.ItemID, loc, c.moviesRealtimeMonitor)
		}
	}

	if c.cfg.EmbyMediaPath != "" {
		log.Printf("[Emby] Using explicit EMBY_MEDIA_PATH override: %q", c.cfg.EmbyMediaPath)
	}

	c.discovered = true
	return nil
}

// GetTVLibraryPath returns the active TV library path in Emby (overridden by EMBY_MEDIA_PATH if set).
func (c *Client) GetTVLibraryPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cfg.EmbyMediaPath != "" {
		return filepath.Join(c.cfg.EmbyMediaPath, "TV Shows")
	}
	return c.tvLibraryPath
}

// GetMoviesLibraryPath returns the active Movies library path in Emby (overridden by EMBY_MEDIA_PATH if set).
func (c *Client) GetMoviesLibraryPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cfg.EmbyMediaPath != "" {
		return filepath.Join(c.cfg.EmbyMediaPath, "Movies")
	}
	return c.moviesLibraryPath
}

// FindSeriesItemID resolves Emby's internal Item ID for a TV series using TMDB ID, IMDb ID, or Title.
// An optional expectedFolderName can be supplied to disambiguate if multiple items share the same provider IDs.
func (c *Client) FindSeriesItemID(ctx context.Context, tmdbID, imdbID, title string, expectedFolderName ...string) (string, error) {
	folderName := ""
	if len(expectedFolderName) > 0 {
		folderName = expectedFolderName[0]
	}
	tvDir := c.GetTVLibraryPath()

	if tmdbID != "" {
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=tmdb.%s&IncludeItemTypes=Series", tmdbID), folderName, tvDir); id != "" {
			return id, nil
		}
	}
	if imdbID != "" {
		cleanIMDB := imdbID
		if !strings.HasPrefix(strings.ToLower(cleanIMDB), "tt") {
			cleanIMDB = "tt" + cleanIMDB
		}
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=imdb.%s&IncludeItemTypes=Series", cleanIMDB), folderName, tvDir); id != "" {
			return id, nil
		}
	}
	if title != "" {
		escaped := url.QueryEscape(title)
		if id := c.queryItemID(ctx, fmt.Sprintf("searchTerm=%s&IncludeItemTypes=Series", escaped), folderName, tvDir); id != "" {
			return id, nil
		}
	}
	return "", nil
}

// FindMovieItemID resolves Emby's internal Item ID for a Movie using TMDB ID, IMDb ID, or Title.
// An optional expectedFolderName can be supplied to disambiguate if multiple items share the same provider IDs.
func (c *Client) FindMovieItemID(ctx context.Context, tmdbID, imdbID, title string, expectedFolderName ...string) (string, error) {
	folderName := ""
	if len(expectedFolderName) > 0 {
		folderName = expectedFolderName[0]
	}
	movieDir := c.GetMoviesLibraryPath()

	if tmdbID != "" {
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=tmdb.%s&IncludeItemTypes=Movie", tmdbID), folderName, movieDir); id != "" {
			return id, nil
		}
	}
	if imdbID != "" {
		cleanIMDB := imdbID
		if !strings.HasPrefix(strings.ToLower(cleanIMDB), "tt") {
			cleanIMDB = "tt" + cleanIMDB
		}
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=imdb.%s&IncludeItemTypes=Movie", cleanIMDB), folderName, movieDir); id != "" {
			return id, nil
		}
	}
	if title != "" {
		escaped := url.QueryEscape(title)
		if id := c.queryItemID(ctx, fmt.Sprintf("searchTerm=%s&IncludeItemTypes=Movie", escaped), folderName, movieDir); id != "" {
			return id, nil
		}
	}
	return "", nil
}

func (c *Client) queryItemID(ctx context.Context, queryParam, expectedFolderName, expectedBaseDir string) string {
	if !c.IsConfigured() {
		return ""
	}
	searchURL := fmt.Sprintf("%s/Items?%s&Recursive=true&Fields=Path", strings.TrimRight(c.cfg.EmbyURL, "/"), queryParam)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil || resp == nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var res struct {
		Items []struct {
			ID   string `json:"Id"`
			Path string `json:"Path"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || len(res.Items) == 0 {
		return ""
	}

	// 1. If only 1 item and no filter specified, return immediately
	if len(res.Items) == 1 && expectedFolderName == "" && expectedBaseDir == "" {
		return res.Items[0].ID
	}

	// 2. Exact match on both expectedBaseDir prefix and expectedFolderName tail
	if expectedFolderName != "" && expectedBaseDir != "" {
		for _, it := range res.Items {
			if it.Path != "" && (strings.HasPrefix(it.Path, expectedBaseDir) || strings.Contains(it.Path, expectedBaseDir)) &&
				(filepath.Base(it.Path) == expectedFolderName || strings.HasSuffix(filepath.Clean(it.Path), expectedFolderName)) {
				return it.ID
			}
		}
	}

	// 3. Match on expectedFolderName tail
	if expectedFolderName != "" {
		for _, it := range res.Items {
			if it.Path != "" && (filepath.Base(it.Path) == expectedFolderName || strings.HasSuffix(filepath.Clean(it.Path), expectedFolderName)) {
				return it.ID
			}
		}
	}

	// 4. Match on expectedBaseDir prefix
	if expectedBaseDir != "" {
		for _, it := range res.Items {
			if it.Path != "" && (strings.HasPrefix(it.Path, expectedBaseDir) || strings.Contains(it.Path, expectedBaseDir)) {
				return it.ID
			}
		}
	}

	// Fallback to first item
	return res.Items[0].ID
}

// RefreshItemByID sends a targeted metadata refresh directly to a specific series, season, or movie item in Emby.
func (c *Client) RefreshItemByID(ctx context.Context, itemID string) error {
	if !c.IsConfigured() || itemID == "" {
		return nil
	}

	refreshURL := fmt.Sprintf("%s/Items/%s/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh&ImageRefreshMode=Default&ReplaceAllMetadata=true&ReplaceAllImages=false", strings.TrimRight(c.cfg.EmbyURL, "/"), itemID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("targeted item refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("targeted item refresh returned status %d", resp.StatusCode)
	}
	return nil
}

// RefreshSeriesEpisodes queries Emby for child episodes under a series and explicitly triggers ReplaceAllMetadata on S01E01 (and any activation placeholder episodes).
func (c *Client) RefreshSeriesEpisodes(ctx context.Context, seriesID string) error {
	if !c.IsConfigured() || seriesID == "" {
		return nil
	}

	epURL := fmt.Sprintf("%s/Shows/%s/Episodes", strings.TrimRight(c.cfg.EmbyURL, "/"), seriesID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, epURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil || resp == nil {
		return err
	}
	defer resp.Body.Close()

	var epRes struct {
		Items []struct {
			ID                string `json:"Id"`
			Name              string `json:"Name"`
			IndexNumber       int    `json:"IndexNumber"`
			ParentIndexNumber int    `json:"ParentIndexNumber"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&epRes); err != nil {
		return err
	}

	for _, ep := range epRes.Items {
		// Refresh S01E01 and any episode that carries an activation placeholder name
		if (ep.ParentIndexNumber == 1 && ep.IndexNumber == 1) || strings.Contains(ep.Name, "Activate") {
			_ = c.RefreshItemByID(ctx, ep.ID)
		}
	}
	return nil
}

// Session represents an Emby client playback session from GET /Sessions.
type Session struct {
	ID             string `json:"Id"`
	Client         string `json:"Client"`
	DeviceName     string `json:"DeviceName"`
	NowPlayingItem *struct {
		ID                string            `json:"Id"`
		Name              string            `json:"Name"`
		SeriesID          string            `json:"SeriesId"`
		SeriesName        string            `json:"SeriesName"`
		Type              string            `json:"Type"`
		IndexNumber       int               `json:"IndexNumber"`
		ParentIndexNumber int               `json:"ParentIndexNumber"`
		ProviderIDs       map[string]string `json:"ProviderIds"`
	} `json:"NowPlayingItem"`
}

// IsSeriesPlaying checks if a specific series is currently in an active playback session in Emby.
func (c *Client) IsSeriesPlaying(ctx context.Context, tmdbID, imdbID, title string) bool {
	if !c.IsConfigured() {
		return false
	}

	sessionsURL := fmt.Sprintf("%s/Sessions", strings.TrimRight(c.cfg.EmbyURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionsURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil || resp == nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var sessions []Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return false
	}

	cleanIMDb := imdbID
	if cleanIMDb != "" && !strings.HasPrefix(strings.ToLower(cleanIMDb), "tt") {
		cleanIMDb = "tt" + cleanIMDb
	}
	cleanTitle := strings.ToLower(strings.TrimSpace(title))

	for _, s := range sessions {
		if s.NowPlayingItem == nil {
			continue
		}
		item := s.NowPlayingItem
		if tmdbID != "" && item.ProviderIDs != nil {
			if tmdbVal, ok := item.ProviderIDs["Tmdb"]; ok && tmdbVal == tmdbID {
				return true
			}
			if tmdbVal, ok := item.ProviderIDs["tmdb"]; ok && tmdbVal == tmdbID {
				return true
			}
		}
		if cleanIMDb != "" && item.ProviderIDs != nil {
			if imdbVal, ok := item.ProviderIDs["Imdb"]; ok && strings.EqualFold(imdbVal, cleanIMDb) {
				return true
			}
			if imdbVal, ok := item.ProviderIDs["imdb"]; ok && strings.EqualFold(imdbVal, cleanIMDb) {
				return true
			}
		}
		if cleanTitle != "" && strings.EqualFold(strings.TrimSpace(item.SeriesName), cleanTitle) {
			return true
		}
	}
	return false
}

// WaitForPlaybackSession polls Emby's GET /Sessions until the specified series is verified to be actively playing, or until maxWait elapses.
func (c *Client) WaitForPlaybackSession(ctx context.Context, tmdbID, imdbID, title string, maxWait time.Duration) bool {
	if !c.IsConfigured() || maxWait <= 0 {
		return false
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(maxWait)

	for {
		select {
		case <-ctx.Done():
			return false
		case <-timeout:
			return false
		case <-ticker.C:
			if c.IsSeriesPlaying(ctx, tmdbID, imdbID, title) {
				return true
			}
		}
	}
}

// RefreshTVLibrary refreshes only the TV library folder in Emby.
func (c *Client) RefreshTVLibrary(ctx context.Context) error {
	c.mu.RLock()
	tvID := c.tvLibraryID
	c.mu.RUnlock()

	if tvID != "" {
		return c.RefreshItemByID(ctx, tvID)
	}
	return c.TriggerGlobalRefresh(ctx)
}

// RefreshMoviesLibrary refreshes only the Movies library folder in Emby.
func (c *Client) RefreshMoviesLibrary(ctx context.Context) error {
	c.mu.RLock()
	movieID := c.moviesLibraryID
	c.mu.RUnlock()

	if movieID != "" {
		return c.RefreshItemByID(ctx, movieID)
	}
	return c.TriggerGlobalRefresh(ctx)
}

// TriggerGlobalRefresh sends a global library refresh request as fallback.
func (c *Client) TriggerGlobalRefresh(ctx context.Context) error {
	if !c.IsConfigured() {
		return nil
	}

	refreshURL := fmt.Sprintf("%s/Library/Refresh", strings.TrimRight(c.cfg.EmbyURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("global refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("global refresh returned status %d", resp.StatusCode)
	}
	return nil
}

// HasRealtimeMonitor returns whether the specified media library has RealtimeMonitor active.
func (c *Client) HasRealtimeMonitor(itemType string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if itemType == "series" {
		return c.tvRealtimeMonitor
	}
	return c.moviesRealtimeMonitor
}

// VerifyItemIndexed polls Emby dynamically up to maxWait for an item (or newly written special/episode) to appear in Emby's index.
func (c *Client) VerifyItemIndexed(ctx context.Context, itemType, tmdbID, imdbID, title string, maxWait time.Duration) bool {
	if !c.IsConfigured() {
		return true
	}
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		if itemType == "series" {
			seriesID, err := c.FindSeriesItemID(ctx, tmdbID, imdbID, title)
			if err == nil && seriesID != "" {
				// Query Emby to confirm if Season 0 (Specials) or any episode has been indexed under this series
				epURL := fmt.Sprintf("%s/Shows/%s/Episodes?Season=0", strings.TrimRight(c.cfg.EmbyURL, "/"), seriesID)
				req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, epURL, nil)
				if reqErr == nil {
					req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
					req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
					if resp, rErr := c.httpClient.Do(req); rErr == nil {
						var epRes struct {
							Items []struct {
								IndexNumber int `json:"IndexNumber"`
							} `json:"Items"`
						}
						_ = json.NewDecoder(resp.Body).Decode(&epRes)
						resp.Body.Close()
						if len(epRes.Items) > 0 {
							return true // Confirmed on Emby! Special episode is indexed!
						}
					}
				}
			}
		} else {
			movieID, err := c.FindMovieItemID(ctx, tmdbID, imdbID, title)
			if err == nil && movieID != "" {
				return true // Confirmed on Emby! Movie is indexed!
			}
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

// WaitForSeriesEpisodesIndexed polls Emby's GET /Shows/{seriesID}/Episodes until the series is reported
// to have at least minEpisodes indexed, or until the S01E01 placeholder name changes, or until maxWait elapses.
// While waiting, it aggressively cancels any active background global media library scans to prioritize the targeted series.
func (c *Client) WaitForSeriesEpisodesIndexed(ctx context.Context, embySeriesID string, minEpisodes int, maxWait time.Duration) (int, bool) {
	if !c.IsConfigured() || embySeriesID == "" {
		return 0, true
	}
	if minEpisodes <= 0 {
		minEpisodes = 2 // Default: more than just the initial placeholder episode
	}
	if maxWait <= 0 {
		maxWait = 25 * time.Second
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	timeout := time.After(maxWait)
	lastCount := 0

	for {
		// 1. Proactively cancel any running global scan that might lock Emby's library
		_, _ = c.CancelRunningLibraryScan(ctx)

		// 2. Query Emby for episode count and names under this specific series
		epURL := fmt.Sprintf("%s/Shows/%s/Episodes", strings.TrimRight(c.cfg.EmbyURL, "/"), embySeriesID)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, epURL, nil)
		if err == nil {
			req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
			req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
			if resp, rErr := c.httpClient.Do(req); rErr == nil {
				var epRes struct {
					TotalRecordCount int `json:"TotalRecordCount"`
					Items            []struct {
						Name              string `json:"Name"`
						IndexNumber       int    `json:"IndexNumber"`
						ParentIndexNumber int    `json:"ParentIndexNumber"`
					} `json:"Items"`
				}
				if decErr := json.NewDecoder(resp.Body).Decode(&epRes); decErr == nil {
					count := epRes.TotalRecordCount
					if count == 0 && len(epRes.Items) > 0 {
						count = len(epRes.Items)
					}
					lastCount = count
					// Condition A: Emby has indexed multiple episodes
					if count >= minEpisodes {
						resp.Body.Close()
						return count, true
					}
					// Condition B: S01E01 metadata was updated (no longer has the activation placeholder name)
					for _, ep := range epRes.Items {
						if ep.ParentIndexNumber == 1 && ep.IndexNumber == 1 && ep.Name != "" && !strings.Contains(ep.Name, "Activate") {
							resp.Body.Close()
							return count, true
						}
					}
				}
				resp.Body.Close()
			}
		}

		select {
		case <-ctx.Done():
			return lastCount, false
		case <-timeout:
			return lastCount, false
		case <-ticker.C:
		}
	}
}

// NotifyMediaUpdated sends a POST /Library/Media/Updated notification to Emby for a specific item folder path.
func (c *Client) NotifyMediaUpdated(ctx context.Context, itemType, folderName string) error {
	if !c.IsConfigured() || folderName == "" {
		return nil
	}

	c.mu.RLock()
	basePath := c.moviesLibraryPath
	if itemType == "series" {
		basePath = c.tvLibraryPath
	}
	if c.cfg.EmbyMediaPath != "" {
		if itemType == "series" {
			basePath = filepath.Join(c.cfg.EmbyMediaPath, "TV Shows")
		} else {
			basePath = filepath.Join(c.cfg.EmbyMediaPath, "Movies")
		}
	}
	c.mu.RUnlock()

	if basePath == "" {
		return nil
	}

	embyItemPath := filepath.Join(basePath, folderName)
	updateURL := fmt.Sprintf("%s/Library/Media/Updated", strings.TrimRight(c.cfg.EmbyURL, "/"))
	body := map[string]any{
		"Updates": []map[string]string{
			{
				"Path":       embyItemPath,
				"UpdateType": "Created",
			},
		},
	}
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, updateURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("media updated notification failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("media updated notification returned status %d", resp.StatusCode)
	}
	return nil
}

// WaitForScanCompletion polls Emby scheduled tasks until the media library scan reaches Idle.
func (c *Client) WaitForScanCompletion(ctx context.Context, maxWait time.Duration) bool {
	if !c.IsConfigured() {
		return true
	}
	deadline := time.Now().Add(maxWait)
	lastLoggedPct := -1
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/ScheduledTasks", strings.TrimRight(c.cfg.EmbyURL, "/")), nil)
		if err != nil {
			return false
		}
		req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
		req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

		resp, err := c.httpClient.Do(req)
		if err != nil || resp == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		var tasks []struct {
			Key                       string  `json:"Key"`
			State                     string  `json:"State"`
			CurrentProgressPercentage float64 `json:"CurrentProgressPercentage"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&tasks)
		resp.Body.Close()

		scanning := false
		for _, t := range tasks {
			if t.Key == "RefreshLibrary" && t.State == "Running" {
				scanning = true
				bucket := int(t.CurrentProgressPercentage) / 20 * 20
				if bucket > 100 {
					bucket = 100
				}
				if lastLoggedPct == -1 || bucket > lastLoggedPct {
					log.Printf("[Emby] Active media library scan progress: %.1f%%", t.CurrentProgressPercentage)
					lastLoggedPct = bucket
				}
				break
			}
		}
		if !scanning {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

// IsLibraryScanRunning checks if Emby's media library scan is actively running.
func (c *Client) IsLibraryScanRunning(ctx context.Context) bool {
	if !c.IsConfigured() {
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/ScheduledTasks", strings.TrimRight(c.cfg.EmbyURL, "/")), nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
	req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil || resp == nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var tasks []struct {
		Key   string `json:"Key"`
		State string `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err == nil {
		for _, t := range tasks {
			if t.Key == "RefreshLibrary" && t.State == "Running" {
				return true
			}
		}
	}
	return false
}

// CancelRunningLibraryScan checks if a global media library scan (RefreshLibrary) is currently running or cancelling in Emby,
// sends cancellation, and ensures it reaches Idle state so high-priority activations are not delayed or blocked by queued scans.
// Returns whether an active scan was actually running and cancelled.
func (c *Client) CancelRunningLibraryScan(ctx context.Context) (bool, error) {
	if !c.IsConfigured() {
		return false, nil
	}

	deadline := time.Now().Add(5 * time.Second)
	cancelledAny := false

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return cancelledAny, ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/ScheduledTasks", strings.TrimRight(c.cfg.EmbyURL, "/")), nil)
		if err != nil {
			return cancelledAny, err
		}
		req.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
		req.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)

		resp, err := c.httpClient.Do(req)
		if err != nil || resp == nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return cancelledAny, fmt.Errorf("ScheduledTasks returned status %d", resp.StatusCode)
		}

		var tasks []struct {
			ID    string `json:"Id"`
			Key   string `json:"Key"`
			State string `json:"State"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&tasks)
		resp.Body.Close()

		stillBusy := false
		for _, t := range tasks {
			if t.Key == "RefreshLibrary" {
				if t.State == "Running" {
					stillBusy = true
					log.Printf("[Emby] ⚡ VIP Activation Priority: Stopping active global media library scan (Task ID: %s)...", t.ID)
					stopURL := fmt.Sprintf("%s/ScheduledTasks/Running/%s", strings.TrimRight(c.cfg.EmbyURL, "/"), t.ID)
					stopReq, sErr := http.NewRequestWithContext(ctx, http.MethodDelete, stopURL, nil)
					if sErr == nil {
						stopReq.Header.Set("X-Emby-Token", c.cfg.EmbyAPIKey)
						stopReq.Header.Set("X-MediaBrowser-Token", c.cfg.EmbyAPIKey)
						if stopResp, dErr := c.httpClient.Do(stopReq); dErr == nil && stopResp != nil {
							stopResp.Body.Close()
							cancelledAny = true
						}
					}
				} else if t.State == "Cancelling" {
					stillBusy = true
					cancelledAny = true
				}
				break
			}
		}

		if !stillBusy {
			if cancelledAny {
				log.Printf("[Emby] Active media library scan confirmed stopped and Idle.")
			}
			return cancelledAny, nil
		}

		time.Sleep(200 * time.Millisecond)
	}

	return cancelledAny, nil
}
