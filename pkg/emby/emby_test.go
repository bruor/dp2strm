package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			query := r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			if query == "AnyProviderIdEquals=tmdb.254528&IncludeItemTypes=Series&Recursive=true" {
				json.NewEncoder(w).Encode(map[string]any{
					"Items": []map[string]any{
						{"Id": "49", "Name": "The Hawk"},
					},
				})
			} else if query == "AnyProviderIdEquals=imdb.tt31038430&IncludeItemTypes=Series&Recursive=true" {
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
	if err := client.CancelRunningLibraryScan(ctx); err != nil {
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
