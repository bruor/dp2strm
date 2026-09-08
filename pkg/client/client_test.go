package client

import (
	"context"
	"net/http"
	"net/http/httptest"
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
