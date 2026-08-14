package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dp2strm/pkg/models"
)

// Client handles REST API interactions with the Dispatcharr server.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient initializes a new Dispatcharr REST API Client.
func NewClient(baseURL, apiKey string) *Client {
	normalized := strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: normalized,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dp2strm/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP 429 Too Many Requests (Rate Limited)")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	return resp, nil
}

// FetchMoviePage retrieves a single page of movies from GET /api/vod/movies/
func (c *Client) FetchMoviePage(ctx context.Context, page, pageSize int) (*models.PaginatedResponse[models.Movie], error) {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/vod/movies/", query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result models.PaginatedResponse[models.Movie]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode movie list JSON: %w", err)
	}

	return &result, nil
}

// FetchMovieProviders retrieves provider streams for a movie via GET /api/vod/movies/{id}/providers/
func (c *Client) FetchMovieProviders(ctx context.Context, movieID int) ([]models.ProviderRelation, error) {
	path := fmt.Sprintf("/api/vod/movies/%d/providers/", movieID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read movie providers body: %w", err)
	}

	// 1. Try decoding as direct JSON array [...]
	var relations []models.ProviderRelation
	if err := json.Unmarshal(bodyBytes, &relations); err == nil {
		return relations, nil
	}

	// 2. Try decoding as paginated object {"results": [...]}
	var paginated struct {
		Results []models.ProviderRelation `json:"results"`
	}
	if err := json.Unmarshal(bodyBytes, &paginated); err == nil {
		return paginated.Results, nil
	}

	return nil, fmt.Errorf("failed to parse movie providers response for movie ID %d", movieID)
}

// TriggerMovieProviderInfo fetches detailed provider info via GET /api/vod/movies/{id}/provider-info/
func (c *Client) TriggerMovieProviderInfo(ctx context.Context, movieID, relationID int) (map[string]any, error) {
	path := fmt.Sprintf("/api/vod/movies/%d/provider-info/", movieID)
	query := url.Values{}
	if relationID > 0 {
		query.Set("relation_id", fmt.Sprintf("%d", relationID))
	}

	resp, err := c.doRequest(ctx, http.MethodGet, path, query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode provider info JSON: %w", err)
	}

	return result, nil
}

// FetchSeriesPage retrieves a single page of TV series from GET /api/vod/series/
func (c *Client) FetchSeriesPage(ctx context.Context, page, pageSize int) (*models.PaginatedResponse[models.Series], error) {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/vod/series/", query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result models.PaginatedResponse[models.Series]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode series list JSON: %w", err)
	}

	return &result, nil
}

// FetchSeriesEpisodes retrieves episodes for a series via GET /api/vod/series/{id}/episodes/
func (c *Client) FetchSeriesEpisodes(ctx context.Context, seriesID int) ([]models.Episode, error) {
	path := fmt.Sprintf("/api/vod/series/%d/episodes/", seriesID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read series episodes body: %w", err)
	}

	// 1. Try decoding as direct JSON array [...]
	var episodes []models.Episode
	if err := json.Unmarshal(bodyBytes, &episodes); err == nil {
		return episodes, nil
	}

	// 2. Try decoding as paginated object {"results": [...]}
	var paginated struct {
		Results []models.Episode `json:"results"`
	}
	if err := json.Unmarshal(bodyBytes, &paginated); err == nil {
		return paginated.Results, nil
	}

	return nil, fmt.Errorf("failed to parse series episodes response for series ID %d", seriesID)
}
