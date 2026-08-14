package strm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dp2strm/pkg/models"
)

func TestGenerateSeriesProxyURL(t *testing.T) {
	cfg := &models.Config{
		URL: "http://localhost:8080",
	}

	url := GenerateSeriesProxyURL(cfg, 30998, 1, 1, "base")
	want := "http://localhost:8080/play/series/30998/1/1"
	if url != want {
		t.Errorf("GenerateSeriesProxyURL got %q; want %q", url, want)
	}
}

func TestGenerateMovieProxyURL(t *testing.T) {
	cfg := &models.Config{
		URL:  "http://dp2strm:9199",
		Port: 9199,
	}

	// Base version
	baseURL := GenerateMovieProxyURL(cfg, 1086665, "base")
	wantBase := "http://dp2strm:9199/play/movie/1086665"
	if baseURL != wantBase {
		t.Errorf("GenerateMovieProxyURL base got %q; want %q", baseURL, wantBase)
	}

	// 4K variant version
	fourKURL := GenerateMovieProxyURL(cfg, 1086665, "4k")
	wantFourK := "http://dp2strm:9199/play/movie/1086665/4k"
	if fourKURL != wantFourK {
		t.Errorf("GenerateMovieProxyURL 4k got %q; want %q", fourKURL, wantFourK)
	}

	// URL missing port with custom port configured
	cfgNoPort := &models.Config{
		URL:  "http://dp2strm",
		Port: 9199,
	}
	fourKURLNormalized := GenerateMovieProxyURL(cfgNoPort, 1086665, "4k")
	if fourKURLNormalized != wantFourK {
		t.Errorf("GenerateMovieProxyURL normalized 4k got %q; want %q", fourKURLNormalized, wantFourK)
	}
}

func TestWriteSTRMFileAndMtimePreservation(t *testing.T) {
	tempDir := t.TempDir()
	strmPath := filepath.Join(tempDir, "test.strm")
	content := "http://localhost:8080/play/movie/123.mkv"

	// 1. Initial write
	updated, err := WriteSTRMFile(strmPath, content)
	if err != nil {
		t.Fatalf("WriteSTRMFile failed: %v", err)
	}
	if !updated {
		t.Errorf("expected updated = true for initial write")
	}

	// Set mtime to 1 hour ago
	pastTime := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(strmPath, pastTime, pastTime); err != nil {
		t.Fatalf("failed to set past mtime: %v", err)
	}

	// 2. Second write with IDENTICAL content
	updatedSecond, err := WriteSTRMFile(strmPath, content)
	if err != nil {
		t.Fatalf("WriteSTRMFile second call failed: %v", err)
	}
	if updatedSecond {
		t.Errorf("expected updated = false when content is unchanged")
	}

	// Verify mtime was preserved!
	info, err := os.Stat(strmPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}

	if info.ModTime().Unix() != pastTime.Unix() {
		t.Errorf("mtime changed! got %v; want %v", info.ModTime(), pastTime)
	}
}
