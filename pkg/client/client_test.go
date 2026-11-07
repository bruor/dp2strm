package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientFetchMoviePage(t *testing.T) {
	// Test 1: Paginated response {"count": 1, "results": [...]}
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count": 1, "next": null, "previous": null, "results": [{"id": 101, "name": "Test Movie", "year": 2024}]}`))
	}))
	defer ts1.Close()

	c1 := NewClient(ts1.URL, "test-key")
	resp1, err := c1.FetchMoviePage(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("FetchMoviePage paginated failed: %v", err)
	}
	if len(resp1.Results) != 1 || resp1.Results[0].ID != 101 {
		t.Errorf("Unexpected results: %+v", resp1)
	}

	// Test 2: Direct raw JSON array [...]
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id": 102, "name": "Raw Array Movie", "year": 2023}]`))
	}))
	defer ts2.Close()

	c2 := NewClient(ts2.URL, "test-key")
	resp2, err := c2.FetchMoviePage(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("FetchMoviePage raw array failed: %v", err)
	}
	if len(resp2.Results) != 1 || resp2.Results[0].ID != 102 {
		t.Errorf("Unexpected results: %+v", resp2)
	}
}

func TestClientFetchSeriesPage(t *testing.T) {
	// Test 1: Paginated response
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count": 1, "next": null, "previous": null, "results": [{"id": 201, "name": "Test Series", "year": 2024}]}`))
	}))
	defer ts1.Close()

	c1 := NewClient(ts1.URL, "test-key")
	resp1, err := c1.FetchSeriesPage(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("FetchSeriesPage paginated failed: %v", err)
	}
	if len(resp1.Results) != 1 || resp1.Results[0].ID != 201 {
		t.Errorf("Unexpected results: %+v", resp1)
	}

	// Test 2: Raw JSON array
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id": 202, "name": "Raw Series", "year": 2022}]`))
	}))
	defer ts2.Close()

	c2 := NewClient(ts2.URL, "test-key")
	resp2, err := c2.FetchSeriesPage(context.Background(), 1, 100)
	if err != nil {
		t.Fatalf("FetchSeriesPage raw array failed: %v", err)
	}
	if len(resp2.Results) != 1 || resp2.Results[0].ID != 202 {
		t.Errorf("Unexpected results: %+v", resp2)
	}
}

func TestClientRateLimiting(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	_, err := c.FetchMoviePage(context.Background(), 1, 100)
	if err != ErrRateLimited {
		t.Errorf("Expected ErrRateLimited, got: %v", err)
	}
}

func TestClientFetchSingleEntities(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/api/vod/movies/123/" {
			_, _ = w.Write([]byte(`{"id": 123, "name": "Avatar", "year": 2009, "tmdb_id": "19995"}`))
		} else if r.URL.Path == "/api/vod/series/456/" {
			_, _ = w.Write([]byte(`{"id": 456, "name": "Breaking Bad", "year": 2008, "tmdb_id": "1396"}`))
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	mv, err := c.FetchSingleMovie(context.Background(), 123)
	if err != nil {
		t.Fatalf("FetchSingleMovie failed: %v", err)
	}
	if mv.Name != "Avatar" || mv.TMDBID != "19995" {
		t.Errorf("Unexpected movie data: %+v", mv)
	}

	srs, err := c.FetchSingleSeries(context.Background(), 456)
	if err != nil {
		t.Fatalf("FetchSingleSeries failed: %v", err)
	}
	if srs.Name != "Breaking Bad" || srs.TMDBID != "1396" {
		t.Errorf("Unexpected series data: %+v", srs)
	}
}

func TestClientOrdering(t *testing.T) {
	var movieOrdering, seriesOrdering string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if strings.HasPrefix(r.URL.Path, "/api/vod/movies/") {
			movieOrdering = r.URL.Query().Get("ordering")
			_, _ = w.Write([]byte(`{"count": 0, "results": []}`))
		} else if strings.HasPrefix(r.URL.Path, "/api/vod/series/") {
			seriesOrdering = r.URL.Query().Get("ordering")
			_, _ = w.Write([]byte(`{"count": 0, "results": []}`))
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key", "-created_at")
	_, _ = c.FetchMoviePage(context.Background(), 1, 10)
	_, _ = c.FetchSeriesPage(context.Background(), 1, 10)

	if movieOrdering != "-created_at" {
		t.Errorf("Expected movie ordering '-created_at', got: '%s'", movieOrdering)
	}
	if seriesOrdering != "-created_at" {
		t.Errorf("Expected series ordering '-created_at', got: '%s'", seriesOrdering)
	}
}

func TestClientInvalidJSONError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>502 Bad Gateway</body></html>`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	_, errMv := c.FetchMoviePage(context.Background(), 1, 100)
	if errMv == nil {
		t.Errorf("Expected error for HTML/unparseable movie response, got nil")
	}

	_, errSrs := c.FetchSeriesPage(context.Background(), 1, 100)
	if errSrs == nil {
		t.Errorf("Expected error for HTML/unparseable series response, got nil")
	}
}

func TestClientFetchSeriesEpisodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		switch r.URL.Path {
		case "/api/vod/series/1/episodes/":
			_, _ = w.Write([]byte(`[]`))
		case "/api/vod/series/2/episodes/":
			_, _ = w.Write([]byte(`{}`))
		case "/api/vod/series/3/episodes/":
			_, _ = w.Write([]byte(`{"results": []}`))
		case "/api/vod/series/4/episodes/":
			_, _ = w.Write([]byte(`[{"id": 1001, "season_number": 1, "episode_number": 1, "title": "Pilot"}]`))
		case "/api/vod/series/5/episodes/":
			_, _ = w.Write([]byte(`{"episodes": {"1": [{"id": 1002, "season_number": 1, "episode_number": 2, "title": "Episode 2"}]}}`))
		case "/api/vod/series/6/episodes/":
			_, _ = w.Write([]byte(`<html>Error</html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	ctx := context.Background()

	// 1. Empty array
	eps1, err1 := c.FetchSeriesEpisodes(ctx, 1)
	if err1 != nil {
		t.Fatalf("Expected nil error for empty array [], got: %v", err1)
	}
	if len(eps1) != 0 {
		t.Errorf("Expected 0 episodes for [], got %d", len(eps1))
	}

	// 2. Empty object
	eps2, err2 := c.FetchSeriesEpisodes(ctx, 2)
	if err2 != nil {
		t.Fatalf("Expected nil error for empty object {}, got: %v", err2)
	}
	if len(eps2) != 0 {
		t.Errorf("Expected 0 episodes for {}, got %d", len(eps2))
	}

	// 3. Paginated empty results
	eps3, err3 := c.FetchSeriesEpisodes(ctx, 3)
	if err3 != nil {
		t.Fatalf("Expected nil error for empty paginated results, got: %v", err3)
	}
	if len(eps3) != 0 {
		t.Errorf("Expected 0 episodes for empty results, got %d", len(eps3))
	}

	// 4. Direct array with items
	eps4, err4 := c.FetchSeriesEpisodes(ctx, 4)
	if err4 != nil {
		t.Fatalf("Expected nil error for direct array, got: %v", err4)
	}
	if len(eps4) != 1 || eps4[0].ID != 1001 {
		t.Errorf("Unexpected episodes: %+v", eps4)
	}

	// 5. XC nested map with items
	eps5, err5 := c.FetchSeriesEpisodes(ctx, 5)
	if err5 != nil {
		t.Fatalf("Expected nil error for XC nested map, got: %v", err5)
	}
	if len(eps5) != 1 || eps5[0].ID != 1002 {
		t.Errorf("Unexpected episodes: %+v", eps5)
	}

	// 6. Invalid HTML response should return parse error
	_, err6 := c.FetchSeriesEpisodes(ctx, 6)
	if err6 == nil {
		t.Fatalf("Expected error for HTML response, got nil")
	}
}

func TestTriggerSeriesProviderInfo(t *testing.T) {
	var capturedQuery url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/vod/series/123/provider-info/" {
			capturedQuery = r.URL.Query()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id": 123, "episodes_fetched": true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-api-key", "")
	res, err := c.TriggerSeriesProviderInfo(context.Background(), 123, 456)
	if err != nil {
		t.Fatalf("Expected nil error, got: %v", err)
	}
	if res["episodes_fetched"] != true {
		t.Errorf("Unexpected result: %v", res)
	}
	if capturedQuery.Get("include_episodes") != "true" {
		t.Errorf("Expected include_episodes=true, got: %q", capturedQuery.Get("include_episodes"))
	}
	if capturedQuery.Get("relation_id") != "456" {
		t.Errorf("Expected relation_id=456, got: %q", capturedQuery.Get("relation_id"))
	}
}

func TestClientExcludeAdultQueryParam(t *testing.T) {
	var capturedQuery url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count": 0, "results": []}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-api-key")

	// 1. By default, is_adult should not be set
	_, err := c.FetchMoviePage(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("FetchMoviePage failed: %v", err)
	}
	if capturedQuery.Get("is_adult") != "" {
		t.Errorf("Expected is_adult to be empty by default, got: %q", capturedQuery.Get("is_adult"))
	}

	// 2. When SetExcludeAdult(true), is_adult=false must be present
	c.SetExcludeAdult(true)
	_, err = c.FetchMoviePage(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("FetchMoviePage failed: %v", err)
	}
	if capturedQuery.Get("is_adult") != "false" {
		t.Errorf("Expected is_adult=false, got: %q", capturedQuery.Get("is_adult"))
	}

	// 3. When SetExcludeAdult(false), is_adult is cleared
	c.SetExcludeAdult(false)
	_, err = c.FetchMoviePage(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("FetchMoviePage failed: %v", err)
	}
	if capturedQuery.Get("is_adult") != "" {
		t.Errorf("Expected is_adult to be empty, got: %q", capturedQuery.Get("is_adult"))
	}
}

func TestClientFetchCategories(t *testing.T) {
	// Paginated response
	tsPaginated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vod/categories/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count": 2, "results": [{"id": 1, "name": "Action", "category_type": "movie"}, {"id": 2, "name": "Adult 18+", "category_type": "movie"}]}`))
	}))
	defer tsPaginated.Close()

	c := NewClient(tsPaginated.URL, "test-key")
	cats, err := c.FetchCategories(context.Background(), "movie")
	if err != nil {
		t.Fatalf("FetchCategories paginated failed: %v", err)
	}
	if len(cats) != 2 {
		t.Fatalf("Expected 2 categories, got %d", len(cats))
	}
	if cats[0].Name != "Action" || cats[1].Name != "Adult 18+" {
		t.Errorf("Unexpected categories: %+v", cats)
	}

	// Array response
	tsArray := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vod/categories/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id": 3, "name": "XXX Series", "category_type": "series"}]`))
	}))
	defer tsArray.Close()

	c2 := NewClient(tsArray.URL, "test-key")
	cats2, err := c2.FetchCategories(context.Background())
	if err != nil {
		t.Fatalf("FetchCategories array failed: %v", err)
	}
	if len(cats2) != 1 || cats2[0].Name != "XXX Series" {
		t.Errorf("Unexpected categories: %+v", cats2)
	}
}

func TestClientFetchItemIDsByCategory(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/vod/movies/" {
			page := r.URL.Query().Get("page")
			cat := r.URL.Query().Get("category")
			if cat != "Adult" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			if page == "1" {
				_, _ = w.Write([]byte(`{"count": 3, "next": "http://.../?page=2", "results": [{"id": 10}, {"id": 20}]}`))
			} else {
				_, _ = w.Write([]byte(`{"count": 3, "next": null, "results": [{"id": 30}]}`))
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "test-key")
	ids, err := c.FetchItemIDsByCategory(context.Background(), "movies", "Adult")
	if err != nil {
		t.Fatalf("FetchItemIDsByCategory failed: %v", err)
	}
	expectedIDs := []int{10, 20, 30}
	if len(ids) != len(expectedIDs) {
		t.Fatalf("Expected %d IDs, got %d (%v)", len(expectedIDs), len(ids), ids)
	}
	for i, id := range expectedIDs {
		if ids[i] != id {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], id)
		}
	}
}



