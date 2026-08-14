package artwork

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dp2strm/pkg/models"
)

var artworkClient = &http.Client{
	Timeout: 10 * time.Second,
}

// DownloadArtwork downloads an image from imgURL to targetPath.
// If the image file exists AND the recorded version URL matches imgURL (including ?v=hash), it skips the download.
// If imgURL changed (new ?v=hash), it updates the image automatically.
func DownloadArtwork(targetPath, imgURL string) (bool, error) {
	return DownloadArtworkWithAuth(targetPath, imgURL, "", "")
}

// DownloadArtworkWithAuth downloads an image, normalizing relative URLs against baseURL and attaching X-API-Key if targeting Dispatcharr.
func DownloadArtworkWithAuth(targetPath, imgURL, baseURL, apiKey string) (bool, error) {
	if imgURL == "" {
		return false, nil
	}

	fullURL := imgURL
	if !strings.HasPrefix(fullURL, "http://") && !strings.HasPrefix(fullURL, "https://") {
		if baseURL != "" {
			fullURL = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(fullURL, "/")
		} else {
			return false, fmt.Errorf("relative image URL without baseURL: %s", imgURL)
		}
	}

	urlTrackerPath := targetPath + ".url"

	// Check if artwork file AND matching URL tracker exist on disk
	if _, err := os.Stat(targetPath); err == nil {
		if cachedURLBytes, err := os.ReadFile(urlTrackerPath); err == nil {
			if strings.TrimSpace(string(cachedURLBytes)) == strings.TrimSpace(fullURL) {
				return false, nil // Preserved (URL hash matches!)
			}
		}
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request for image %s: %w", fullURL, err)
	}
	if apiKey != "" && baseURL != "" && strings.HasPrefix(fullURL, strings.TrimRight(baseURL, "/")) {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := artworkClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to download image %s: %w", fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP error %d downloading image %s", resp.StatusCode, fullURL)
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
	_ = os.WriteFile(urlTrackerPath, []byte(fullURL), 0666)

	return true, nil // Updated
}

// DownloadMovieArtwork saves poster.jpg and fanart.jpg for a movie in the .strm directory.
func DownloadMovieArtwork(movieDir string, movie models.Movie, cfg ...*models.Config) (postersCreated int) {
	baseURL := ""
	apiKey := ""
	if len(cfg) > 0 && cfg[0] != nil {
		baseURL = cfg[0].DispatcharrURL
		apiKey = cfg[0].APIKey
	}

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
		if created, err := DownloadArtworkWithAuth(posterPath, posterURL, baseURL, apiKey); err == nil && created {
			postersCreated++
		}
	}

	if backdrops, ok := movie.CustomProperties["backdrop_path"].([]any); ok && len(backdrops) > 0 {
		if backdropURL, ok := backdrops[0].(string); ok && backdropURL != "" {
			fanartPath := filepath.Join(movieDir, "fanart.jpg")
			if created, err := DownloadArtworkWithAuth(fanartPath, backdropURL, baseURL, apiKey); err == nil && created {
				postersCreated++
			}
		}
	}

	return postersCreated
}

// DownloadSeriesArtwork saves poster.jpg and fanart.jpg for a TV series in the item directory.
func DownloadSeriesArtwork(seriesDir string, series models.Series, cfg ...*models.Config) (artworkCreated int) {
	baseURL := ""
	apiKey := ""
	if len(cfg) > 0 && cfg[0] != nil {
		baseURL = cfg[0].DispatcharrURL
		apiKey = cfg[0].APIKey
	}

	var posterURL string
	if logo, ok := series.CustomProperties["logo"].(map[string]any); ok {
		if urlStr, ok := logo["url"].(string); ok && urlStr != "" {
			posterURL = urlStr
		} else if cacheStr, ok := logo["cache_url"].(string); ok && cacheStr != "" {
			posterURL = cacheStr
		}
	}

	if posterURL != "" {
		posterPath := filepath.Join(seriesDir, "poster.jpg")
		if created, err := DownloadArtworkWithAuth(posterPath, posterURL, baseURL, apiKey); err == nil && created {
			artworkCreated++
		}
	}

	if backdrops, ok := series.CustomProperties["backdrop_path"].([]any); ok && len(backdrops) > 0 {
		if backdropURL, ok := backdrops[0].(string); ok && backdropURL != "" {
			fanartPath := filepath.Join(seriesDir, "fanart.jpg")
			if created, err := DownloadArtworkWithAuth(fanartPath, backdropURL, baseURL, apiKey); err == nil && created {
				artworkCreated++
			}
		}
	}

	return artworkCreated
}
