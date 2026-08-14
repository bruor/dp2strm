package strm

import (
	"fmt"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
)

// GenerateMovieProxyURL builds the generic credential-free playback URL pointing to dp2strm playback handler.
func GenerateMovieProxyURL(cfg *models.Config, streamID, container string) string {
	ext := container
	if ext == "" {
		ext = "mkv"
	}
	return fmt.Sprintf("%s/play/movie/%s.%s", cfg.ProxyPublicURL, streamID, ext)
}

// GenerateSeriesProxyURL builds the semantic credential-free playback URL for TV episodes.
// Format: /play/series/<series_id>/<season>/<episode>.<ext>
func GenerateSeriesProxyURL(cfg *models.Config, seriesID int, season int, episode int, container string) string {
	ext := container
	if ext == "" {
		ext = "mkv"
	}
	return fmt.Sprintf("%s/play/series/%d/%d/%d.%s", cfg.ProxyPublicURL, seriesID, season, episode, ext)
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
