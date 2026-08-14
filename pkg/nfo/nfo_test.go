package nfo

import (
	"os"
	"path/filepath"
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
