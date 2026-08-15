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

// FindSeriesItemID resolves Emby's internal Item ID for a TV series using TMDB ID, IMDb ID, or Title.
func (c *Client) FindSeriesItemID(ctx context.Context, tmdbID, imdbID, title string) (string, error) {
	if tmdbID != "" {
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=tmdb.%s&IncludeItemTypes=Series", tmdbID)); id != "" {
			return id, nil
		}
	}
	if imdbID != "" {
		cleanIMDB := imdbID
		if !strings.HasPrefix(strings.ToLower(cleanIMDB), "tt") {
			cleanIMDB = "tt" + cleanIMDB
		}
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=imdb.%s&IncludeItemTypes=Series", cleanIMDB)); id != "" {
			return id, nil
		}
	}
	if title != "" {
		escaped := url.QueryEscape(title)
		if id := c.queryItemID(ctx, fmt.Sprintf("searchTerm=%s&IncludeItemTypes=Series", escaped)); id != "" {
			return id, nil
		}
	}
	return "", nil
}

// FindMovieItemID resolves Emby's internal Item ID for a Movie using TMDB ID, IMDb ID, or Title.
func (c *Client) FindMovieItemID(ctx context.Context, tmdbID, imdbID, title string) (string, error) {
	if tmdbID != "" {
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=tmdb.%s&IncludeItemTypes=Movie", tmdbID)); id != "" {
			return id, nil
		}
	}
	if imdbID != "" {
		cleanIMDB := imdbID
		if !strings.HasPrefix(strings.ToLower(cleanIMDB), "tt") {
			cleanIMDB = "tt" + cleanIMDB
		}
		if id := c.queryItemID(ctx, fmt.Sprintf("AnyProviderIdEquals=imdb.%s&IncludeItemTypes=Movie", cleanIMDB)); id != "" {
			return id, nil
		}
	}
	if title != "" {
		escaped := url.QueryEscape(title)
		if id := c.queryItemID(ctx, fmt.Sprintf("searchTerm=%s&IncludeItemTypes=Movie", escaped)); id != "" {
			return id, nil
		}
	}
	return "", nil
}

func (c *Client) queryItemID(ctx context.Context, queryParam string) string {
	if !c.IsConfigured() {
		return ""
	}
	searchURL := fmt.Sprintf("%s/Items?%s&Recursive=true", strings.TrimRight(c.cfg.EmbyURL, "/"), queryParam)
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
			ID string `json:"Id"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err == nil && len(res.Items) > 0 {
		return res.Items[0].ID
	}
	return ""
}

// RefreshItemByID sends a targeted metadata refresh directly to a specific series or movie item in Emby.
func (c *Client) RefreshItemByID(ctx context.Context, itemID string) error {
	if !c.IsConfigured() || itemID == "" {
		return nil
	}

	refreshURL := fmt.Sprintf("%s/Items/%s/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh&ImageRefreshMode=Default", strings.TrimRight(c.cfg.EmbyURL, "/"), itemID)
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

// VerifyItemIndexed polls Emby up to maxWait for an item to appear in Emby's index.
func (c *Client) VerifyItemIndexed(ctx context.Context, itemType, tmdbID, imdbID, title string, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		var itemID string
		var err error
		if itemType == "series" {
			itemID, err = c.FindSeriesItemID(ctx, tmdbID, imdbID, title)
		} else {
			itemID, err = c.FindMovieItemID(ctx, tmdbID, imdbID, title)
		}

		if err == nil && itemID != "" {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
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
		return err
	}
	defer resp.Body.Close()
	return nil
}
