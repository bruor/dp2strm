package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dp2strm/pkg/models"
)

var ErrRateLimited = fmt.Errorf("HTTP 429 Too Many Requests (Rate Limited)")

// Client handles REST API interactions with the Dispatcharr server.
type Client struct {
	baseURL    string
	apiKey     string
	ordering   string
	httpClient *http.Client
}

// NewClient initializes a new Dispatcharr REST API Client with optional ordering.
func NewClient(baseURL, apiKey string, ordering ...string) *Client {
	normalized := strings.TrimRight(baseURL, "/")
	ord := "-created_at"
	if len(ordering) > 0 && ordering[0] != "" {
		ord = ordering[0]
	}
	return &Client{
		baseURL:  normalized,
		apiKey:   apiKey,
		ordering: ord,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// SetOrdering sets the server-side ordering query parameter (e.g. "-updated_at,-id").
func (c *Client) SetOrdering(ordering string) {
	c.ordering = ordering
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
		return nil, ErrRateLimited
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, resp.Status)
	}

	return resp, nil
}

// FetchMoviePage retrieves movies from GET /api/vod/movies/ supporting both paginated and raw array responses.
func (c *Client) FetchMoviePage(ctx context.Context, page, pageSize int) (*models.PaginatedResponse[models.Movie], error) {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))
	if c.ordering != "" {
		query.Set("ordering", c.ordering)
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/vod/movies/", query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read movie page body: %w", err)
	}

	// 1. Try decoding as paginated object {"results": [...]}
	var paginated models.PaginatedResponse[models.Movie]
	if err := json.Unmarshal(bodyBytes, &paginated); err == nil && paginated.Results != nil {
		return &paginated, nil
	}

	// 2. Try decoding as direct JSON array [...]
	var list []models.Movie
	if err := json.Unmarshal(bodyBytes, &list); err == nil {
		return &models.PaginatedResponse[models.Movie]{
			Count:   len(list),
			Next:    nil,
			Results: list,
		}, nil
	}

	snippet := string(bodyBytes)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	log.Printf("[Client] Error: /api/vod/movies/ page %d returned invalid JSON (%d bytes): %s", page, len(bodyBytes), snippet)
	return nil, fmt.Errorf("failed to parse /api/vod/movies/ page %d response (%d bytes): %s", page, len(bodyBytes), snippet)
}

// FetchSingleMovie retrieves a single movie by ID via GET /api/vod/movies/{id}/
func (c *Client) FetchSingleMovie(ctx context.Context, movieID int) (*models.Movie, error) {
	path := fmt.Sprintf("/api/vod/movies/%d/", movieID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var movie models.Movie
	if err := json.NewDecoder(resp.Body).Decode(&movie); err != nil {
		return nil, fmt.Errorf("failed to decode movie JSON for ID %d: %w", movieID, err)
	}

	return &movie, nil
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

// FetchSeriesPage retrieves a single page of TV series from GET /api/vod/series/ supporting both paginated and raw array responses.
func (c *Client) FetchSeriesPage(ctx context.Context, page, pageSize int) (*models.PaginatedResponse[models.Series], error) {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", page))
	query.Set("page_size", fmt.Sprintf("%d", pageSize))
	if c.ordering != "" {
		query.Set("ordering", c.ordering)
	}

	resp, err := c.doRequest(ctx, http.MethodGet, "/api/vod/series/", query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read series page body: %w", err)
	}

	// 1. Try decoding as paginated object {"results": [...]}
	var paginated models.PaginatedResponse[models.Series]
	if err := json.Unmarshal(bodyBytes, &paginated); err == nil && paginated.Results != nil {
		return &paginated, nil
	}

	// 2. Try decoding as direct JSON array [...]
	var list []models.Series
	if err := json.Unmarshal(bodyBytes, &list); err == nil {
		return &models.PaginatedResponse[models.Series]{
			Count:   len(list),
			Next:    nil,
			Results: list,
		}, nil
	}

	snippet := string(bodyBytes)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	log.Printf("[Client] Error: /api/vod/series/ page %d returned invalid JSON (%d bytes): %s", page, len(bodyBytes), snippet)
	return nil, fmt.Errorf("failed to parse /api/vod/series/ page %d response (%d bytes): %s", page, len(bodyBytes), snippet)
}

// FetchSingleSeries retrieves a single series by ID via GET /api/vod/series/{id}/
func (c *Client) FetchSingleSeries(ctx context.Context, seriesID int) (*models.Series, error) {
	path := fmt.Sprintf("/api/vod/series/%d/", seriesID)
	resp, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var series models.Series
	if err := json.NewDecoder(resp.Body).Decode(&series); err != nil {
		return nil, fmt.Errorf("failed to decode series JSON for ID %d: %w", seriesID, err)
	}

	return &series, nil
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
	if err := json.Unmarshal(bodyBytes, &episodes); err == nil && len(episodes) > 0 {
		return episodes, nil
	}

	// 2. Try decoding as paginated object {"results": [...]}
	var paginated struct {
		Results []models.Episode `json:"results"`
	}
	if err := json.Unmarshal(bodyBytes, &paginated); err == nil && len(paginated.Results) > 0 {
		return paginated.Results, nil
	}

	// 3. Try decoding as XC nested map {"episodes": {"1": [...], "2": [...]}}
	var xcObj struct {
		Episodes map[string][]models.Episode `json:"episodes"`
	}
	if err := json.Unmarshal(bodyBytes, &xcObj); err == nil && len(xcObj.Episodes) > 0 {
		var allEps []models.Episode
		for _, seasonEps := range xcObj.Episodes {
			allEps = append(allEps, seasonEps...)
		}
		return allEps, nil
	}

	// 4. Try decoding as {"episodes": [...]}
	var xcArray struct {
		Episodes []models.Episode `json:"episodes"`
	}
	if err := json.Unmarshal(bodyBytes, &xcArray); err == nil && len(xcArray.Episodes) > 0 {
		return xcArray.Episodes, nil
	}

	// 5. Try decoding as direct map[string][]models.Episode
	var seasonsMap map[string][]models.Episode
	if err := json.Unmarshal(bodyBytes, &seasonsMap); err == nil && len(seasonsMap) > 0 {
		var allEps []models.Episode
		for _, seasonEps := range seasonsMap {
			allEps = append(allEps, seasonEps...)
		}
		return allEps, nil
	}

	return nil, fmt.Errorf("failed to parse series episodes response for series ID %d: %s", seriesID, string(bodyBytes))
}

// FetchMyProfile retrieves the authenticated user's profile and XC streaming credentials from GET /api/accounts/users/me/
func (c *Client) FetchMyProfile(ctx context.Context) (username, xcPassword string, err error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/accounts/users/me/", nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var data struct {
		Username         string `json:"username"`
		CustomProperties struct {
			XCPassword string `json:"xc_password"`
		} `json:"custom_properties"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", fmt.Errorf("failed to decode user profile: %w", err)
	}

	return data.Username, data.CustomProperties.XCPassword, nil
}
