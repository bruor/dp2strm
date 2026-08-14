package artwork

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"dp2strm/pkg/models"
)

// DownloadArtwork downloads an image from imgURL to targetPath.
// If the image file exists AND the recorded version URL matches imgURL (including ?v=hash), it skips the download.
// If imgURL changed (new ?v=hash), it updates the image automatically.
func DownloadArtwork(targetPath, imgURL string) (bool, error) {
	if imgURL == "" {
		return false, nil
	}

	urlTrackerPath := targetPath + ".url"

	// Check if artwork file AND matching URL tracker exist on disk
	if _, err := os.Stat(targetPath); err == nil {
		if cachedURLBytes, err := os.ReadFile(urlTrackerPath); err == nil {
			if strings.TrimSpace(string(cachedURLBytes)) == strings.TrimSpace(imgURL) {
				return false, nil // Preserved (URL hash matches!)
			}
		}
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	resp, err := http.Get(imgURL)
	if err != nil {
		return false, fmt.Errorf("failed to download image %s: %w", imgURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP error %d downloading image %s", resp.StatusCode, imgURL)
	}

	outFile, err := os.Create(targetPath)
	if err != nil {
		return false, fmt.Errorf("failed to create artwork file %s: %w", targetPath, err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return false, fmt.Errorf("failed to save artwork to %s: %w", targetPath, err)
	}

	// Record current image URL (including ?v=hash) to track future updates
	_ = os.WriteFile(urlTrackerPath, []byte(imgURL), 0644)

	return true, nil // Updated
}

// DownloadMovieArtwork saves poster.jpg and fanart.jpg for a movie in the .strm directory.
func DownloadMovieArtwork(movieDir string, movie models.Movie) (postersCreated int) {
	var posterURL string
	if logo, ok := movie.CustomProperties["logo"].(map[string]any); ok {
		if urlStr, ok := logo["url"].(string); ok && urlStr != "" {
			posterURL = urlStr
		} else if cacheStr, ok := logo["cache_url"].(string); ok && cacheStr != "" {
			posterURL = cacheStr
		}
	}

	if posterURL != "" {
		posterPath := filepath.Join(movieDir, "poster.jpg")
		if created, err := DownloadArtwork(posterPath, posterURL); err == nil && created {
			postersCreated++
		}
	}

	if backdrops, ok := movie.CustomProperties["backdrop_path"].([]any); ok && len(backdrops) > 0 {
		if backdropURL, ok := backdrops[0].(string); ok && backdropURL != "" {
			fanartPath := filepath.Join(movieDir, "fanart.jpg")
			if created, err := DownloadArtwork(fanartPath, backdropURL); err == nil && created {
				postersCreated++
			}
		}
	}

	return postersCreated
}
