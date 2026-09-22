package sanitizer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	illegalCharsRegex         = regexp.MustCompile(`[\\/:*?"<>|]`)
	qualityTagRegex           = regexp.MustCompile(`(?i)\b(4K|UHD|2160p|1080p|720p|480p|HDR|HDR10|DV|Dolby Vision|HEVC|H265|x265|REMUX|WEB-DL|BluRay|HDTV)\b`)
	consecutiveYearGroupRegex = regexp.MustCompile(`(?:\s*[\(\[]\s*(?:19\d{2}|20\d{2})\s*[\)\]]){2,}`)
	parenthesizedYearRegex    = regexp.MustCompile(`[\(\[]\s*(19\d{2}|20\d{2})\s*[\)\]]`)
	trailingYearRegex         = regexp.MustCompile(`\s*[\(\[]\s*(?:19\d{2}|20\d{2})\s*[\)\]]\s*$`)
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

// StripYearFromTitle removes trailing parenthesized or bracketed release year from the title for clean NFO <title> tags.
func StripYearFromTitle(title string, year int) string {
	cleaned := CleanTitle(title)
	for {
		loc := trailingYearRegex.FindStringIndex(cleaned)
		if loc == nil {
			break
		}
		candidate := strings.TrimSpace(cleaned[:loc[0]])
		if candidate == "" {
			break // Do not strip if the entire title is the year (e.g. "(2012)")
		}
		cleaned = candidate
	}
	if cleaned == "" {
		return CleanTitle(title)
	}
	return cleaned
}

// BuildMovieFolderName constructs the strict Emby directory name with TMDB ID or IMDb ID:
// e.g. "The Thing (1982) [tmdbid=1091]" or "The Hunting Wives (2025) [imdbid=tt31038430]"
func BuildMovieFolderName(cleanTitle string, year int, tmdbID string, imdbIDs ...string) string {
	safeTitle := SanitizeFileName(cleanTitle)

	var yearStr string
	if year > 0 {
		expectedYear := fmt.Sprintf("(%d)", year)
		if !strings.HasSuffix(safeTitle, expectedYear) {
			yearStr = fmt.Sprintf(" (%d)", year)
		}
	}

	var idStr string
	if tmdbID != "" {
		idStr = fmt.Sprintf(" [tmdbid=%s]", tmdbID)
	} else if len(imdbIDs) > 0 && imdbIDs[0] != "" {
		imdb := imdbIDs[0]
		if !strings.HasPrefix(strings.ToLower(imdb), "tt") && !strings.Contains(imdb, "=") {
			imdb = "tt" + imdb
		}
		idStr = fmt.Sprintf(" [imdbid=%s]", imdb)
	}

	return fmt.Sprintf("%s%s%s", safeTitle, yearStr, idStr)
}

// GetLegacyFolderCandidates returns all plausible non-ID legacy folder names on disk for an item:
// e.g. for ("Girl from the North Country (2024)", 2024):
// returns ["Girl from the North Country (2024)", "Girl from the North Country"]
func GetLegacyFolderCandidates(rawTitle string, year int) []string {
	clean := CleanTitle(rawTitle)
	bare := StripYearFromTitle(clean, year)
	safeBare := SanitizeFileName(bare)
	safeClean := SanitizeFileName(clean)

	seen := make(map[string]bool)
	var candidates []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			candidates = append(candidates, name)
		}
	}

	if year > 0 && safeBare != "" {
		add(fmt.Sprintf("%s (%d)", safeBare, year))
	}
	add(safeClean)
	add(safeBare)

	return candidates
}

// BuildMovieFilename returns the canonical base .strm filename for a movie:
// e.g. "Avatar (2009).strm" or "Dark Beauty.strm" (if year is 0).
func BuildMovieFilename(cleanTitle string, year int) string {
	bare := StripYearFromTitle(cleanTitle, year)
	safeBare := SanitizeFileName(bare)
	if safeBare == "" {
		safeBare = SanitizeFileName(cleanTitle)
	}
	if year > 0 {
		return fmt.Sprintf("%s (%d).strm", safeBare, year)
	}
	return fmt.Sprintf("%s.strm", safeBare)
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

// ExtractYearAndTitle extracts the clean title and year from a folder name like "The Hawk (2026)".
func ExtractYearAndTitle(folderName string) (title string, year int) {
	name := folderName
	if idx := strings.Index(name, "["); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	submatches := parenthesizedYearRegex.FindAllStringSubmatch(name, -1)
	if len(submatches) > 0 {
		lastSub := submatches[len(submatches)-1]
		if len(lastSub) > 1 {
			if y, err := strconv.Atoi(lastSub[1]); err == nil {
				year = y
			}
		}
	}
	title = StripYearFromTitle(name, year)
	if title == "" {
		title = name
	}
	return title, year
}
