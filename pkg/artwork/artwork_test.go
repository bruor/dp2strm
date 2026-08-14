package artwork

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadArtworkHashTracking(t *testing.T) {
	// Mock image server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query().Get("v")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("image_data_version_" + v))
	}))
	defer ts.Close()

	tempDir := t.TempDir()
	posterPath := filepath.Join(tempDir, "poster.jpg")

	urlV1 := ts.URL + "/image.jpg?v=1111"
	urlV2 := ts.URL + "/image.jpg?v=2222"

	// 1. Initial download with v=1111
	created1, err := DownloadArtwork(posterPath, urlV1)
	if err != nil {
		t.Fatalf("DownloadArtwork v1 failed: %v", err)
	}
	if !created1 {
		t.Errorf("expected created = true for initial download")
	}

	// 2. Second pass with SAME v=1111 -> Must skip download!
	created2, err := DownloadArtwork(posterPath, urlV1)
	if err != nil {
		t.Fatalf("DownloadArtwork v1 second pass failed: %v", err)
	}
	if created2 {
		t.Errorf("expected created = false when hash parameter is unchanged")
	}

	// 3. Third pass with NEW v=2222 -> Must detect hash change & update!
	created3, err := DownloadArtwork(posterPath, urlV2)
	if err != nil {
		t.Fatalf("DownloadArtwork v2 failed: %v", err)
	}
	if !created3 {
		t.Errorf("expected created = true when ?v=hash parameter changes")
	}

	// Verify updated image content
	data, _ := os.ReadFile(posterPath)
	if string(data) != "image_data_version_2222" {
		t.Errorf("artwork content not updated! got %q; want %q", string(data), "image_data_version_2222")
	}
}
