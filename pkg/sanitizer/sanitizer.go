package sanitizer

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	illegalCharsRegex         = regexp.MustCompile(`[\\/:*?"<>|]`)
	qualityTagRegex           = regexp.MustCompile(`(?i)\b(4K|UHD|2160p|1080p|720p|480p|HDR|HDR10|DV|Dolby Vision|HEVC|H265|x265|REMUX|WEB-DL|BluRay|HDTV)\b`)
	consecutiveYearGroupRegex = regexp.MustCompile(`(?:\s*[\(\[]\s*(?:19\d{2}|20\d{2})\s*[\)\]]){2,}`)
	parenthesizedYearRegex    = regexp.MustCompile(`[\(\[]\s*(19\d{2}|20\d{2})\s*[\)\]]`)
	multipleSpaceRegex        = regexp.MustCompile(`\s+`)
)

// SanitizeFileName cleans a string to be safe for Linux/Windows filesystems.
func SanitizeFileName(name string) string {
	cleaned := illegalCharsRegex.ReplaceAllString(name, "")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = multipleSpaceRegex.ReplaceAllString(cleaned, " ")
	return cleaned
}

// CleanTitle removes quality/resolution/codec tags and collapses duplicate parenthesized year tags.
func CleanTitle(title string) string {
	cleaned := qualityTagRegex.ReplaceAllString(title, "")

	// Collapse repeated parenthesized/bracketed year groups (e.g. "(2022) (2022)" -> "(2022)")
	cleaned = consecutiveYearGroupRegex.ReplaceAllStringFunc(cleaned, func(match string) string {
		submatches := parenthesizedYearRegex.FindAllStringSubmatch(match, -1)
		if len(submatches) > 0 {
			// Extract first year and collapse to single "(YYYY)"
			return fmt.Sprintf(" (%s)", submatches[0][1])
		}
		return match
	})

	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.TrimPrefix(cleaned, ":")
	cleaned = strings.TrimSuffix(cleaned, ":")
	cleaned = strings.TrimSpace(cleaned)
	cleaned = multipleSpaceRegex.ReplaceAllString(cleaned, " ")
	if cleaned == "" {
		return title // Fallback if entire title was quality tags
	}
	return cleaned
}

// BuildMovieFolderName constructs the strict Emby directory name with TMDB ID:
// e.g. "The Thing (1982) [tmdbid=1091]"
func BuildMovieFolderName(cleanTitle string, year int, tmdbID string) string {
	safeTitle := SanitizeFileName(cleanTitle)

	var yearStr string
	if year > 0 {
		expectedYear := fmt.Sprintf("(%d)", year)
		if !strings.HasSuffix(safeTitle, expectedYear) {
			yearStr = fmt.Sprintf(" (%d)", year)
		}
	}

	var tmdbStr string
	if tmdbID != "" {
		tmdbStr = fmt.Sprintf(" [tmdbid=%s]", tmdbID)
	}

	return fmt.Sprintf("%s%s%s", safeTitle, yearStr, tmdbStr)
}

// CleanEpisodeTitle strips duplicate series title and season/episode tags from episode names.
func CleanEpisodeTitle(epName, seriesName string, season, episode int) string {
	cleaned := SanitizeFileName(epName)
	cleanSeries := SanitizeFileName(seriesName)

	// Remove leading series name if present
	if strings.HasPrefix(strings.ToLower(cleaned), strings.ToLower(cleanSeries)) {
		cleaned = strings.TrimSpace(cleaned[len(cleanSeries):])
		cleaned = strings.TrimPrefix(cleaned, "-")
		cleaned = strings.TrimSpace(cleaned)
	}

	// Remove S01E01 / S1E1 pattern if present
	seasonEpTag := fmt.Sprintf("S%02dE%02d", season, episode)
	if idx := strings.Index(strings.ToUpper(cleaned), seasonEpTag); idx >= 0 {
		cleaned = strings.TrimSpace(cleaned[idx+len(seasonEpTag):])
		cleaned = strings.TrimPrefix(cleaned, "-")
		cleaned = strings.TrimSpace(cleaned)
	}

	// Remove leading hyphen/colon again if any
	cleaned = strings.TrimPrefix(cleaned, "-")
	cleaned = strings.TrimPrefix(cleaned, ":")
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return fmt.Sprintf("Episode %d", episode)
	}
	return cleaned
}
