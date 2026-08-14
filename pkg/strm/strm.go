package strm

import (
	"fmt"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
)

// GenerateMovieProxyURL builds the generic credential-free playback URL pointing to dp2strm proxy server.
func GenerateMovieProxyURL(cfg *models.Config, streamID, container string) string {
	ext := container
	if ext == "" {
		ext = "mkv"
	}
	return fmt.Sprintf("%s/play/movie/%s.%s", cfg.ProxyPublicURL, streamID, ext)
}

// GenerateSeriesProxyURL builds the generic credential-free playback URL for episodes.
func GenerateSeriesProxyURL(cfg *models.Config, seriesID int, streamID, container string) string {
	ext := container
	if ext == "" {
		ext = "mp4"
	}
	return fmt.Sprintf("%s/play/series/%d/%s.%s", cfg.ProxyPublicURL, seriesID, streamID, ext)
}

// WriteSTRMFile writes a .strm file on disk, preserving mtime if the content is identical.
// Returns (updated bool, err error).
func WriteSTRMFile(filePath string, content string) (bool, error) {
	// Ensure target directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Check existing file
	existingInfo, err := os.Stat(filePath)
	if err == nil {
		// File exists, check content
		existingBytes, readErr := os.ReadFile(filePath)
		if readErr == nil && string(existingBytes) == content {
			// Content is identical! Preserve original mtime
			mtime := existingInfo.ModTime()
			atime := mtime // Use modtime as accesstime
			_ = os.Chtimes(filePath, atime, mtime)
			return false, nil // Not updated (preserved)
		}
	}

	// Write new content
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("failed to write strm file %s: %w", filePath, err)
	}

	return true, nil // Updated
}
