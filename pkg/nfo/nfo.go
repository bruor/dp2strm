package nfo

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
	"dp2strm/pkg/sanitizer"
)

type MovieNFO struct {
	XMLName  xml.Name   `xml:"movie"`
	Title    string     `xml:"title"`
	Year     int        `xml:"year,omitempty"`
	Plot     string     `xml:"plot,omitempty"`
	Genre    string     `xml:"genre,omitempty"`
	Rating   string     `xml:"rating,omitempty"`
	UniqueID []UniqueID `xml:"uniqueid"`
}

type TVShowNFO struct {
	XMLName       xml.Name   `xml:"tvshow"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Genre         string     `xml:"genre,omitempty"`
	Rating        string     `xml:"rating,omitempty"`
	UniqueID      []UniqueID `xml:"uniqueid"`
}

type EpisodeNFO struct {
	XMLName  xml.Name   `xml:"episodedetails"`
	Title    string     `xml:"title"`
	Season   int        `xml:"season"`
	Episode  int        `xml:"episode"`
	Plot     string     `xml:"plot,omitempty"`
	UniqueID []UniqueID `xml:"uniqueid"`
}

type UniqueID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

// WriteMovieNFO generates a Kodi/Emby compatible .nfo file for a movie.
func WriteMovieNFO(filePath string, movie models.Movie) (bool, error) {
	uniqueIDs := []UniqueID{}
	if movie.TMDBID != "" {
		uniqueIDs = append(uniqueIDs, UniqueID{Type: "tmdb", Default: "true", Value: movie.TMDBID})
	}
	if movie.IMDBID != "" {
		uniqueIDs = append(uniqueIDs, UniqueID{Type: "imdb", Value: movie.IMDBID})
	}

	nfoData := MovieNFO{
		Title:    sanitizer.StripYearFromTitle(movie.Name, movie.Year),
		Year:     movie.Year,
		Plot:     movie.Description,
		Genre:    movie.Genre,
		Rating:   movie.Rating,
		UniqueID: uniqueIDs,
	}

	return writeNFOData(filePath, nfoData)
}

// WriteTVShowNFO generates a Kodi/Emby compatible tvshow.nfo file for a TV series.
func WriteTVShowNFO(filePath string, series models.Series) (bool, error) {
	uniqueIDs := []UniqueID{}
	if series.TMDBID != "" {
		uniqueIDs = append(uniqueIDs, UniqueID{Type: "tmdb", Default: "true", Value: series.TMDBID})
	}
	if series.IMDBID != "" {
		uniqueIDs = append(uniqueIDs, UniqueID{Type: "imdb", Value: series.IMDBID})
	}

	cleanTitle := sanitizer.StripYearFromTitle(series.Name, series.Year)
	nfoData := TVShowNFO{
		Title:         cleanTitle,
		OriginalTitle: cleanTitle,
		Year:          series.Year,
		Plot:          series.Description,
		Genre:         series.Genre,
		Rating:        series.Rating,
		UniqueID:      uniqueIDs,
	}

	return writeNFOData(filePath, nfoData)
}

// WriteEpisodeNFO generates a Kodi/Emby compatible episode .nfo file.
func WriteEpisodeNFO(filePath string, title string, season, ep int, plot string, tmdbID string) (bool, error) {
	uniqueIDs := []UniqueID{}
	if tmdbID != "" {
		uniqueIDs = append(uniqueIDs, UniqueID{Type: "tmdb", Default: "true", Value: tmdbID})
	}

	nfoData := EpisodeNFO{
		Title:    title,
		Season:   season,
		Episode:  ep,
		Plot:     plot,
		UniqueID: uniqueIDs,
	}

	return writeNFOData(filePath, nfoData)
}

func writeNFOData(filePath string, nfoStruct interface{}) (bool, error) {
	output, err := xml.MarshalIndent(nfoStruct, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to marshal NFO XML: %w", err)
	}

	xmlHeader := []byte(xml.Header + string(output))

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return false, fmt.Errorf("failed to create NFO directory %s: %w", dir, err)
	}

	// Check existing file for mtime preservation
	if existingInfo, err := os.Stat(filePath); err == nil {
		if existingBytes, readErr := os.ReadFile(filePath); readErr == nil {
			if string(existingBytes) == string(xmlHeader) {
				mtime := existingInfo.ModTime()
				_ = os.Chtimes(filePath, mtime, mtime)
				return false, nil // Preserved
			}
		}
	}

	if err := os.WriteFile(filePath, xmlHeader, 0666); err != nil {
		return false, fmt.Errorf("failed to write NFO file %s: %w", filePath, err)
	}

	return true, nil
}
