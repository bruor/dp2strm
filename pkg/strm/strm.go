package strm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dp2strm/pkg/models"
)

// STRMWriteStatus represents the outcome of writing a STRM file on disk.
type STRMWriteStatus int

const (
	STRMPreserved STRMWriteStatus = iota // Content identical, mtime preserved
	STRMUpdated                          // Existing file updated in-place (mtime preserved)
	STRMCreated                          // Brand-new file created on disk
)

// FormatMovieIdentifier selects the most permanent identifier for a movie (TMDB > IMDb > Dispatcharr ID).
func FormatMovieIdentifier(tmdbID, imdbID string, movieID int) string {
	if t := strings.TrimSpace(tmdbID); t != "" && t != "0" {
		return "tmdb:" + t
	}
	if i := strings.TrimSpace(imdbID); i != "" {
		return "imdb:" + i
	}
	return strconv.Itoa(movieID)
}

// FormatSeriesIdentifier selects the most permanent identifier for a TV series (TMDB > IMDb > Dispatcharr ID).
func FormatSeriesIdentifier(tmdbID, imdbID string, seriesID int) string {
	if t := strings.TrimSpace(tmdbID); t != "" && t != "0" {
		return "tmdb:" + t
	}
	if i := strings.TrimSpace(imdbID); i != "" {
		return "imdb:" + i
	}
	return strconv.Itoa(seriesID)
}

// getBaseProxyURL extracts and normalizes the base URL, appending cfg.Port if missing and non-standard.
func getBaseProxyURL(cfg *models.Config) string {
	if cfg == nil {
		return "http://localhost:8080"
	}
	rawURL := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if rawURL == "" {
		if cfg.Port > 0 {
			return fmt.Sprintf("http://localhost:%d", cfg.Port)
		}
		return "http://localhost:8080"
	}

	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		if parsed.Port() == "" && cfg.Port > 0 && cfg.Port != 80 && cfg.Port != 443 {
			if !strings.Contains(parsed.Host, ":") {
				parsed.Host = fmt.Sprintf("%s:%d", parsed.Host, cfg.Port)
				return strings.TrimRight(parsed.String(), "/")
			}
		}
	}
	return rawURL
}

// GenerateMovieProxyURL builds the clean extensionless playback URL pointing to dp2strm playback handler.
// Format: /play/movie/{identifier} or /play/movie/{identifier}/{variant_key}
func GenerateMovieProxyURL(cfg *models.Config, identifier string, variantKey string) string {
	base := getBaseProxyURL(cfg)
	if variantKey == "" || variantKey == "base" {
		return fmt.Sprintf("%s/play/movie/%s", base, identifier)
	}
	return fmt.Sprintf("%s/play/movie/%s/%s", base, identifier, variantKey)
}

// GenerateSeriesProxyURL builds the clean extensionless playback URL for TV episodes.
// Format: /play/series/{identifier}/{season}/{episode} or /play/series/{identifier}/{season}/{episode}/{variant_key}
func GenerateSeriesProxyURL(cfg *models.Config, identifier string, season int, episode int, variantKey string) string {
	base := getBaseProxyURL(cfg)
	if variantKey == "" || variantKey == "base" {
		return fmt.Sprintf("%s/play/series/%s/%d/%d", base, identifier, season, episode)
	}
	return fmt.Sprintf("%s/play/series/%s/%d/%d/%s", base, identifier, season, episode, variantKey)
}

// WriteSTRMFileDetailed writes a .strm file on disk and categorizes the result (Preserved, Updated in-place, Created new).
func WriteSTRMFileDetailed(filePath string, content string) (STRMWriteStatus, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return STRMPreserved, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	existingInfo, err := os.Stat(filePath)
	if err == nil {
		existingBytes, readErr := os.ReadFile(filePath)
		if readErr == nil && strings.TrimSpace(string(existingBytes)) == strings.TrimSpace(content) {
			mtime := existingInfo.ModTime()
			_ = os.Chtimes(filePath, mtime, mtime)
			return STRMPreserved, nil // Identical content preserved
		}

		// File exists but content changed -> update in-place, preserving original mtime
		if wErr := os.WriteFile(filePath, []byte(content), 0666); wErr != nil {
			return STRMPreserved, fmt.Errorf("failed to write strm file %s: %w", filePath, wErr)
		}
		_ = os.Chtimes(filePath, existingInfo.ModTime(), existingInfo.ModTime())
		return STRMUpdated, nil // In-place URL update
	}

	// Brand-new file creation
	if wErr := os.WriteFile(filePath, []byte(content), 0666); wErr != nil {
		return STRMPreserved, fmt.Errorf("failed to write strm file %s: %w", filePath, wErr)
	}

	return STRMCreated, nil // Brand-new file created
}

// WriteSTRMFile writes a .strm file on disk, preserving mtime if the content is identical.
// Returns (updatedOrCreated bool, err error).
func WriteSTRMFile(filePath string, content string) (bool, error) {
	status, err := WriteSTRMFileDetailed(filePath, content)
	return status != STRMPreserved, err
}
