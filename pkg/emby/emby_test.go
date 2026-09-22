package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dp2strm/pkg/models"
)

func TestEmbyDiscoveryAndTargetedRefresh(t *testing.T) {
	scanState := "Running"
	// Setup mock Emby server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Emby-Token")
		if token != "test_api_key" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/Library/VirtualFolders":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"Name":           "TV shows",
					"CollectionType": "tvshows",
					"ItemId":         "3",
					"Locations":      []string{"/media/strm/TV Shows"},
					"LibraryOptions": map[string]any{
						"EnableRealtimeMonitor": true,
					},
				},
				{
					"Name":           "Movies",
					"CollectionType": "movies",
					"ItemId":         "4",
					"Locations":      []string{"/media/strm/Movies"},
					"LibraryOptions": map[string]any{
						"EnableRealtimeMonitor": false,
					},
				},
			})
		case "/Items":
			w.Header().Set("Content-Type", "application/json")
			pID := r.URL.Query().Get("AnyProviderIdEquals")
			if pID == "tmdb.254528" {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{
						{"Id": "49", "Name": "The Hawk"},
					},
				})
			} else if pID == "imdb.tt31038430" {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{
						{"Id": "5", "Name": "The Hunting Wives"},
					},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{},
				})
			}
		case "/Shows/49/Episodes":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{"IndexNumber": 99, "ParentIndexNumber": 0},
				},
			})
		case "/Sessions":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"Id":         "sess-1",
					"Client":     "Emby Web",
					"DeviceName": "Chrome",
					"NowPlayingItem": map[string]any{
						"Id":                "100",
						"Name":              "⚡ Play to Activate Series",
						"SeriesId":          "49",
						"SeriesName":        "The Hawk",
						"Type":              "Episode",
						"IndexNumber":       1,
						"ParentIndexNumber": 1,
						"ProviderIds": map[string]string{
							"Tmdb": "254528",
						},
					},
				},
			})
		case "/ScheduledTasks":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"Id":                        "task-12345",
					"Key":                       "RefreshLibrary",
					"State":                     scanState,
					"CurrentProgressPercentage": 45.0,
				},
			})
		case "/ScheduledTasks/Running/task-12345":
			if r.Method == http.MethodDelete {
				scanState = "Idle"
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		case "/Items/49/Refresh", "/Items/3/Refresh", "/Items/4/Refresh":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cfg := &models.Config{
		EmbyURL:    mockServer.URL,
		EmbyAPIKey: "test_api_key",
	}

	client := NewClient(cfg)
	ctx := context.Background()

	// 1. Test DiscoverLibraries
	if err := client.DiscoverLibraries(ctx); err != nil {
		t.Fatalf("DiscoverLibraries failed: %v", err)
	}

	if !client.HasRealtimeMonitor("series") {
		t.Errorf("Expected TV RealtimeMonitor=true")
	}
	if client.HasRealtimeMonitor("movie") {
		t.Errorf("Expected Movies RealtimeMonitor=false")
	}

	// 2. Test FindSeriesItemID via TMDB ID
	id, err := client.FindSeriesItemID(ctx, "254528", "", "The Hawk")
	if err != nil || id != "49" {
		t.Fatalf("Expected Series ID '49', got %q, err: %v", id, err)
	}

	// 3. Test FindSeriesItemID via IMDb ID
	id2, err := client.FindSeriesItemID(ctx, "", "tt31038430", "The Hunting Wives")
	if err != nil || id2 != "5" {
		t.Fatalf("Expected Series ID '5', got %q, err: %v", id2, err)
	}

	// 4. Test RefreshItemByID
	if err := client.RefreshItemByID(ctx, "49"); err != nil {
		t.Errorf("RefreshItemByID failed: %v", err)
	}

	// 5. Test RefreshTVLibrary
	if err := client.RefreshTVLibrary(ctx); err != nil {
		t.Errorf("RefreshTVLibrary failed: %v", err)
	}

	// 6. Test VerifyItemIndexed
	if !client.VerifyItemIndexed(ctx, "series", "254528", "", "The Hawk", 2*time.Second) {
		t.Errorf("Expected VerifyItemIndexed to return true for '254528'")
	}

	// 7. Test CancelRunningLibraryScan
	if _, err := client.CancelRunningLibraryScan(ctx); err != nil {
		t.Errorf("CancelRunningLibraryScan failed: %v", err)
	}

	// 8. Test IsSeriesPlaying and WaitForPlaybackSession
	if !client.IsSeriesPlaying(ctx, "254528", "", "The Hawk") {
		t.Errorf("Expected IsSeriesPlaying to return true for TMDB 254528")
	}
	if !client.WaitForPlaybackSession(ctx, "254528", "", "The Hawk", 1*time.Second) {
		t.Errorf("Expected WaitForPlaybackSession to return true for TMDB 254528")
	}
	if client.IsSeriesPlaying(ctx, "999999", "", "Nonexistent") {
		t.Errorf("Expected IsSeriesPlaying to return false for nonexistent show")
	}
}

func TestWaitForScanCompletion(t *testing.T) {
	step := 0
	progresses := []float64{5.0, 15.0, 25.0, 45.0, 65.0, 85.0, 100.0}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ScheduledTasks":
			w.Header().Set("Content-Type", "application/json")
			if step < len(progresses) {
				pct := progresses[step]
				step++
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"Id":                        "task-12345",
						"Key":                       "RefreshLibrary",
						"State":                     "Running",
						"CurrentProgressPercentage": pct,
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{
						"Id":                        "task-12345",
						"Key":                       "RefreshLibrary",
						"State":                     "Idle",
						"CurrentProgressPercentage": 0.0,
					},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cfg := &models.Config{
		EmbyURL:    mockServer.URL,
		EmbyAPIKey: "test_key",
	}
	client := NewClient(cfg)

	completed := client.WaitForScanCompletion(context.Background(), 10*time.Second)
	if !completed {
		t.Errorf("Expected WaitForScanCompletion to return true")
	}
}

func TestFindSeriesItemIDDisambiguation(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/Library/VirtualFolders":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"Name":           "TV shows",
					"CollectionType": "tvshows",
					"ItemId":         "103",
					"Locations":      []string{"/media/strm/TV Shows", "/mnt/data/TV Shows"},
					"LibraryOptions": map[string]any{"EnableRealtimeMonitor": true},
				},
			})
		case r.URL.Path == "/Items" && r.URL.Query().Get("AnyProviderIdEquals") == "tmdb.90521":
			// Return two items: one local (/mnt/data) and one STRM (/media/strm)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{
					{
						"Id":   "38845669",
						"Name": "Love Island USA",
						"Path": "/mnt/data/TV Shows/Love Island USA",
					},
					{
						"Id":   "41244834",
						"Name": "Love Island USA",
						"Path": "/media/strm/TV Shows/Love Island USA (2019) [tmdbid=90521]",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cfg := &models.Config{
		EmbyURL:    mockServer.URL,
		EmbyAPIKey: "test_key",
	}
	client := NewClient(cfg)
	ctx := context.Background()
	_ = client.DiscoverLibraries(ctx)

	// 1. Without folder name, auto-discovered TV library path (/media/strm/TV Shows) prefers the STRM item
	idAutoBase, err := client.FindSeriesItemID(ctx, "90521", "", "Love Island USA")
	if err != nil || idAutoBase != "41244834" {
		t.Errorf("Expected auto-discovered STRM ID 41244834, got %q, err %v", idAutoBase, err)
	}

	// 2. With expected STRM folder name, disambiguates to the STRM series (41244834)
	idWithFolder, err := client.FindSeriesItemID(ctx, "90521", "", "Love Island USA", "Love Island USA (2019) [tmdbid=90521]")
	if err != nil || idWithFolder != "41244834" {
		t.Errorf("Expected STRM series ID 41244834, got %q, err %v", idWithFolder, err)
	}

	// 3. With local folder name, disambiguates to the local media series (38845669)
	idLocalFolder, err := client.FindSeriesItemID(ctx, "90521", "", "Love Island USA", "Love Island USA")
	if err != nil || idLocalFolder != "38845669" {
		t.Errorf("Expected local media series ID 38845669, got %q, err %v", idLocalFolder, err)
	}
}

func TestWaitForSeriesEpisodesIndexed(t *testing.T) {
	pollCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/ScheduledTasks":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Key": "RefreshLibrary", "State": "Idle"},
			})
		case strings.HasPrefix(r.URL.Path, "/Shows/41244834/Episodes"):
			pollCount++
			if pollCount == 1 {
				// Initial state: only 1 placeholder episode
				_ = json.NewEncoder(w).Encode(map[string]any{
					"TotalRecordCount": 1,
					"Items": []map[string]any{
						{
							"IndexNumber":       1,
							"ParentIndexNumber": 1,
							"Name":              "⚡ Play to Activate Series",
						},
					},
				})
			} else {
				// Second poll: newly scanned 10 episodes
				_ = json.NewEncoder(w).Encode(map[string]any{
					"TotalRecordCount": 10,
					"Items": []map[string]any{
						{
							"IndexNumber":       1,
							"ParentIndexNumber": 1,
							"Name":              "Episode 1",
						},
						{
							"IndexNumber":       2,
							"ParentIndexNumber": 1,
							"Name":              "Episode 2",
						},
					},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	cfg := &models.Config{
		EmbyURL:    mockServer.URL,
		EmbyAPIKey: "test_key",
	}
	client := NewClient(cfg)
	ctx := context.Background()

	count, confirmed := client.WaitForSeriesEpisodesIndexed(ctx, "41244834", 2, 5*time.Second)
	if !confirmed {
		t.Fatalf("Expected WaitForSeriesEpisodesIndexed to confirm")
	}
	if count != 10 {
		t.Errorf("Expected 10 episodes, got %d", count)
	}
}

