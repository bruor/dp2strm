package strm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dp2strm/pkg/models"
)

func TestFormatIdentifiers(t *testing.T) {
	// Movie tests
	if got := FormatMovieIdentifier("1739639", "tt36615831", 1198086); got != "tmdb:1739639" {
		t.Errorf("FormatMovieIdentifier with TMDB got %q; want %q", got, "tmdb:1739639")
	}
	if got := FormatMovieIdentifier("", "tt36615831", 1198086); got != "imdb:tt36615831" {
		t.Errorf("FormatMovieIdentifier with IMDb got %q; want %q", got, "imdb:tt36615831")
	}
	if got := FormatMovieIdentifier("", "", 1198086); got != "1198086" {
		t.Errorf("FormatMovieIdentifier fallback got %q; want %q", got, "1198086")
	}

	// Series tests
	if got := FormatSeriesIdentifier("218510", "tt12345", 26078); got != "tmdb:218510" {
		t.Errorf("FormatSeriesIdentifier with TMDB got %q; want %q", got, "tmdb:218510")
	}
	if got := FormatSeriesIdentifier("", "tt12345", 26078); got != "imdb:tt12345" {
		t.Errorf("FormatSeriesIdentifier with IMDb got %q; want %q", got, "imdb:tt12345")
	}
	if got := FormatSeriesIdentifier("", "", 26078); got != "26078" {
		t.Errorf("FormatSeriesIdentifier fallback got %q; want %q", got, "26078")
	}
}

func TestGenerateSeriesProxyURL(t *testing.T) {
	cfg := &models.Config{
		URL: "http://localhost:8080",
	}

	url := GenerateSeriesProxyURL(cfg, "tmdb:218510", 1, 1, "base")
	want := "http://localhost:8080/play/series/tmdb:218510/1/1"
	if url != want {
		t.Errorf("GenerateSeriesProxyURL got %q; want %q", url, want)
	}

	urlNumeric := GenerateSeriesProxyURL(cfg, "30998", 1, 1, "base")
	wantNumeric := "http://localhost:8080/play/series/30998/1/1"
	if urlNumeric != wantNumeric {
		t.Errorf("GenerateSeriesProxyURL numeric got %q; want %q", urlNumeric, wantNumeric)
	}
}

func TestGenerateMovieProxyURL(t *testing.T) {
	cfg := &models.Config{
		URL:  "http://dp2strm:9199",
		Port: 9199,
	}

	// Base version with TMDB
	baseURL := GenerateMovieProxyURL(cfg, "tmdb:1739639", "base")
	wantBase := "http://dp2strm:9199/play/movie/tmdb:1739639"
	if baseURL != wantBase {
		t.Errorf("GenerateMovieProxyURL base got %q; want %q", baseURL, wantBase)
	}

	// 4K variant version with TMDB
	fourKURL := GenerateMovieProxyURL(cfg, "tmdb:1739639", "4k")
	wantFourK := "http://dp2strm:9199/play/movie/tmdb:1739639/4k"
	if fourKURL != wantFourK {
		t.Errorf("GenerateMovieProxyURL 4k got %q; want %q", fourKURL, wantFourK)
	}

	// Numeric fallback
	numURL := GenerateMovieProxyURL(cfg, "1086665", "base")
	wantNum := "http://dp2strm:9199/play/movie/1086665"
	if numURL != wantNum {
		t.Errorf("GenerateMovieProxyURL numeric got %q; want %q", numURL, wantNum)
	}

	// URL missing port with custom port configured
	cfgNoPort := &models.Config{
		URL:  "http://dp2strm",
		Port: 9199,
	}
	fourKURLNormalized := GenerateMovieProxyURL(cfgNoPort, "tmdb:1739639", "4k")
	if fourKURLNormalized != wantFourK {
		t.Errorf("GenerateMovieProxyURL normalized 4k got %q; want %q", fourKURLNormalized, wantFourK)
	}
}

func TestWriteSTRMFileDetailedAndMtimePreservation(t *testing.T) {
	tempDir := t.TempDir()
	strmPath := filepath.Join(tempDir, "test.strm")
	content := "http://localhost:8080/play/movie/123.mkv"

	// 1. Initial write -> STRMCreated
	status, err := WriteSTRMFileDetailed(strmPath, content)
	if err != nil {
		t.Fatalf("WriteSTRMFileDetailed failed: %v", err)
	}
	if status != STRMCreated {
		t.Errorf("expected status = STRMCreated for initial write, got %v", status)
	}

	// Set mtime to 1 hour ago
	pastTime := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(strmPath, pastTime, pastTime); err != nil {
		t.Fatalf("failed to set past mtime: %v", err)
	}

	// 2. Second write with IDENTICAL content -> STRMPreserved
	statusSecond, err := WriteSTRMFileDetailed(strmPath, content)
	if err != nil {
		t.Fatalf("WriteSTRMFileDetailed second call failed: %v", err)
	}
	if statusSecond != STRMPreserved {
		t.Errorf("expected status = STRMPreserved when content is unchanged, got %v", statusSecond)
	}

	// Verify mtime was preserved
	info, err := os.Stat(strmPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if info.ModTime().Unix() != pastTime.Unix() {
		t.Errorf("mtime changed! got %v; want %v", info.ModTime(), pastTime)
	}

	// 3. Third write with UPDATED content (in-place URL rewrite) -> STRMUpdated
	newContent := "http://localhost:8080/play/movie/tmdb:123.mkv"
	statusThird, err := WriteSTRMFileDetailed(strmPath, newContent)
	if err != nil {
		t.Fatalf("WriteSTRMFileDetailed third call failed: %v", err)
	}
	if statusThird != STRMUpdated {
		t.Errorf("expected status = STRMUpdated for in-place URL rewrite, got %v", statusThird)
	}

	// Verify mtime was still preserved after in-place URL update!
	infoUpdated, err := os.Stat(strmPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if infoUpdated.ModTime().Unix() != pastTime.Unix() {
		t.Errorf("mtime changed after in-place URL update! got %v; want %v", infoUpdated.ModTime(), pastTime)
	}
}
