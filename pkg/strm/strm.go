package strm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dp2strm/pkg/models"
)

// GenerateMovieProxyURL builds the clean extensionless playback URL pointing to dp2strm playback handler.
// Format: /play/movie/{movie_id} or /play/movie/{movie_id}/{variant_key}
func GenerateMovieProxyURL(cfg *models.Config, movieID int, variantKey string) string {
	base := strings.TrimRight(cfg.URL, "/")
	if variantKey == "" || variantKey == "base" {
		return fmt.Sprintf("%s/play/movie/%d", base, movieID)
	}
	return fmt.Sprintf("%s/play/movie/%d/%s", base, movieID, variantKey)
}

// GenerateSeriesProxyURL builds the clean extensionless playback URL for TV episodes.
// Format: /play/series/{series_id}/{season}/{episode} or /play/series/{series_id}/{season}/{episode}/{variant_key}
func GenerateSeriesProxyURL(cfg *models.Config, seriesID int, season int, episode int, variantKey string) string {
	base := strings.TrimRight(cfg.URL, "/")
	if variantKey == "" || variantKey == "base" {
		return fmt.Sprintf("%s/play/series/%d/%d/%d", base, seriesID, season, episode)
	}
	return fmt.Sprintf("%s/play/series/%d/%d/%d/%s", base, seriesID, season, episode, variantKey)
}

// WriteSTRMFile writes a .strm file on disk, preserving mtime if the content is identical.
// Returns (updated bool, err error).
func WriteSTRMFile(filePath string, content string) (bool, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	existingInfo, err := os.Stat(filePath)
	if err == nil {
		existingBytes, readErr := os.ReadFile(filePath)
		if readErr == nil && string(existingBytes) == content {
			mtime := existingInfo.ModTime()
			atime := mtime
			_ = os.Chtimes(filePath, atime, mtime)
			return false, nil // Preserved
		}
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("failed to write strm file %s: %w", filePath, err)
	}

	return true, nil // Updated
}
