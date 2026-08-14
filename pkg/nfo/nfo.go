package nfo

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"dp2strm/pkg/models"
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
		Title:    movie.Name,
		Year:     movie.Year,
		Plot:     movie.Description,
		Genre:    movie.Genre,
		Rating:   movie.Rating,
		UniqueID: uniqueIDs,
	}

	output, err := xml.MarshalIndent(nfoData, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to marshal NFO XML: %w", err)
	}

	xmlHeader := []byte(xml.Header + string(output))

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
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

	if err := os.WriteFile(filePath, xmlHeader, 0644); err != nil {
		return false, fmt.Errorf("failed to write NFO file %s: %w", filePath, err)
	}

	return true, nil
}
