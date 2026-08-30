package nfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dp2strm/pkg/models"
)

func TestWriteMovieNFOUpdateAndMtimePreservation(t *testing.T) {
	tempDir := t.TempDir()
	nfoPath := filepath.Join(tempDir, "movie.nfo")

	movieV1 := models.Movie{
		Name:        "The Thing",
		Year:        1982,
		TMDBID:      "1091",
		Rating:      "8.0",
		Description: "Initial plot summary",
	}

	// 1. Initial write
	updated, err := WriteMovieNFO(nfoPath, movieV1)
	if err != nil {
		t.Fatalf("WriteMovieNFO initial failed: %v", err)
	}
	if !updated {
		t.Errorf("expected updated = true for initial write")
	}

	// Set mtime to 2 hours ago
	pastTime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(nfoPath, pastTime, pastTime); err != nil {
		t.Fatalf("failed to set past mtime: %v", err)
	}

	// 2. Write with IDENTICAL data -> mtime must be preserved!
	updatedIdentical, err := WriteMovieNFO(nfoPath, movieV1)
	if err != nil {
		t.Fatalf("WriteMovieNFO identical failed: %v", err)
	}
	if updatedIdentical {
		t.Errorf("expected updated = false when data is unchanged")
	}

	info, err := os.Stat(nfoPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.ModTime().Unix() != pastTime.Unix() {
		t.Errorf("mtime changed for identical data! got %v; want %v", info.ModTime(), pastTime)
	}

	// 3. Write with NEW DISCOVERED DATA (updated plot & rating) -> file must update!
	movieV2 := models.Movie{
		Name:        "The Thing",
		Year:        1982,
		TMDBID:      "1091",
		Rating:      "8.1", // New rating!
		Description: "Updated detailed plot summary from probe",
	}

	updatedNewData, err := WriteMovieNFO(nfoPath, movieV2)
	if err != nil {
		t.Fatalf("WriteMovieNFO new data failed: %v", err)
	}
	if !updatedNewData {
		t.Errorf("expected updated = true when new metadata is discovered")
	}
}

func TestCleanTitleInNFO(t *testing.T) {
	tempDir := t.TempDir()
	movieNFOPath := filepath.Join(tempDir, "movie.nfo")
	tvshowNFOPath := filepath.Join(tempDir, "tvshow.nfo")

	// Movie with year in raw provider name
	movie := models.Movie{
		Name:   "Avatar (2009)",
		Year:   2009,
		TMDBID: "19995",
	}
	if _, err := WriteMovieNFO(movieNFOPath, movie); err != nil {
		t.Fatalf("WriteMovieNFO failed: %v", err)
	}

	movieContent, err := os.ReadFile(movieNFOPath)
	if err != nil {
		t.Fatalf("ReadFile movie.nfo failed: %v", err)
	}
	if !strings.Contains(string(movieContent), "<title>Avatar</title>") {
		t.Errorf("Expected <title>Avatar</title> in movie.nfo, got:\n%s", string(movieContent))
	}

	// TV Show with year in raw provider name
	series := models.Series{
		Name:   "Breaking Bad (2008)",
		Year:   2008,
		TMDBID: "1396",
	}
	if _, err := WriteTVShowNFO(tvshowNFOPath, series); err != nil {
		t.Fatalf("WriteTVShowNFO failed: %v", err)
	}

	seriesContent, err := os.ReadFile(tvshowNFOPath)
	if err != nil {
		t.Fatalf("ReadFile tvshow.nfo failed: %v", err)
	}
	if !strings.Contains(string(seriesContent), "<title>Breaking Bad</title>") {
		t.Errorf("Expected <title>Breaking Bad</title> in tvshow.nfo, got:\n%s", string(seriesContent))
	}
}

func TestActivationEpisodeNFOContent(t *testing.T) {
	tempDir := t.TempDir()
	actNFOPath := filepath.Join(tempDir, "Rebus (2024) - S00E99 - Activate Series.nfo")

	updated, err := WriteActivationEpisodeNFO(actNFOPath, "Rebus (2024)")
	if err != nil {
		t.Fatalf("WriteActivationEpisodeNFO failed: %v", err)
	}
	if !updated {
		t.Errorf("expected updated = true for initial activation NFO write")
	}

	contentBytes, err := os.ReadFile(actNFOPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	content := string(contentBytes)

	expectedTags := []string{
		"<title>⚡ Play to Activate Series</title>",
		"<season>0</season>",
		"<episode>99</episode>",
		"<watched>false</watched>",
		"<playcount>0</playcount>",
		"<runtime>1</runtime>",
	}

	for _, tag := range expectedTags {
		if !strings.Contains(content, tag) {
			t.Errorf("Expected activation NFO to contain %q, got:\n%s", tag, content)
		}
	}
}

func TestEpisodeNFOUnplayedAndRuntime(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Episode with duration 2700s (45 minutes)
	ep1Path := filepath.Join(tempDir, "S01E01.nfo")
	_, err := WriteEpisodeNFO(ep1Path, "Pilot", 1, 1, "The first episode", "12345", 2700)
	if err != nil {
		t.Fatalf("WriteEpisodeNFO failed: %v", err)
	}

	content1, err := os.ReadFile(ep1Path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	strContent1 := string(content1)

	if !strings.Contains(strContent1, "<watched>false</watched>") {
		t.Errorf("Expected <watched>false</watched>, got:\n%s", strContent1)
	}
	if !strings.Contains(strContent1, "<playcount>0</playcount>") {
		t.Errorf("Expected <playcount>0</playcount>, got:\n%s", strContent1)
	}
	if !strings.Contains(strContent1, "<runtime>45</runtime>") {
		t.Errorf("Expected <runtime>45</runtime>, got:\n%s", strContent1)
	}

	// 2. Episode with duration 0 (fallback to 1 minute)
	ep2Path := filepath.Join(tempDir, "S01E02.nfo")
	_, err = WriteEpisodeNFO(ep2Path, "Episode 2", 1, 2, "Second episode", "12346", 0)
	if err != nil {
		t.Fatalf("WriteEpisodeNFO failed: %v", err)
	}

	content2, err := os.ReadFile(ep2Path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	strContent2 := string(content2)

	if !strings.Contains(strContent2, "<runtime>1</runtime>") {
		t.Errorf("Expected fallback <runtime>1</runtime>, got:\n%s", strContent2)
	}
}

func TestMovieNFOUnplayedAndRuntime(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Movie with duration 7200s (120 minutes)
	movie1Path := filepath.Join(tempDir, "movie1.nfo")
	movie1 := models.Movie{
		Name:         "Inception",
		Year:         2010,
		TMDBID:       "27205",
		DurationSecs: 7200,
	}
	_, err := WriteMovieNFO(movie1Path, movie1)
	if err != nil {
		t.Fatalf("WriteMovieNFO failed: %v", err)
	}

	content1, err := os.ReadFile(movie1Path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	strContent1 := string(content1)

	if !strings.Contains(strContent1, "<watched>false</watched>") {
		t.Errorf("Expected <watched>false</watched>, got:\n%s", strContent1)
	}
	if !strings.Contains(strContent1, "<playcount>0</playcount>") {
		t.Errorf("Expected <playcount>0</playcount>, got:\n%s", strContent1)
	}
	if !strings.Contains(strContent1, "<runtime>120</runtime>") {
		t.Errorf("Expected <runtime>120</runtime>, got:\n%s", strContent1)
	}

	// 2. Movie with duration 0 (fallback to 1 minute)
	movie2Path := filepath.Join(tempDir, "movie2.nfo")
	movie2 := models.Movie{
		Name:         "Unknown Duration Movie",
		Year:         2024,
		TMDBID:       "99999",
		DurationSecs: 0,
	}
	_, err = WriteMovieNFO(movie2Path, movie2)
	if err != nil {
		t.Fatalf("WriteMovieNFO failed: %v", err)
	}

	content2, err := os.ReadFile(movie2Path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	strContent2 := string(content2)

	if !strings.Contains(strContent2, "<runtime>1</runtime>") {
		t.Errorf("Expected fallback <runtime>1</runtime>, got:\n%s", strContent2)
	}
}
