package artwork

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
)

// DownloadArtwork downloads an image from a URL and saves it to targetPath if it doesn't already exist.
func DownloadArtwork(targetPath, imgURL string) (bool, error) {
	if imgURL == "" {
		return false, nil
	}

	// Check if artwork already exists on disk
	if _, err := os.Stat(targetPath); err == nil {
		return false, nil // Preserved
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

	return true, nil
}

// DownloadMovieArtwork saves poster.jpg and fanart.jpg for a movie if image URLs exist.
func DownloadMovieArtwork(movieDir string, movie models.Movie) (postersCreated int) {
	// Poster URL from logo or custom properties
	var posterURL string
	if logo, ok := movie.CustomProperties["logo"].(map[string]any); ok {
		if urlStr, ok := logo["url"].(string); ok {
			posterURL = urlStr
		}
	}

	if posterURL != "" {
		posterPath := filepath.Join(movieDir, "poster.jpg")
		if created, err := DownloadArtwork(posterPath, posterURL); err == nil && created {
			postersCreated++
		}
	}

	// Backdrop URL for fanart.jpg
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
