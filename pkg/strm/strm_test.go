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
		ProxyPublicURL: "http://localhost:8080",
	}

	url := GenerateSeriesProxyURL(cfg, 30998, 1, 1, "mkv")
	want := "http://localhost:8080/play/series/30998/1/1.mkv"
	if url != want {
		t.Errorf("GenerateSeriesProxyURL got %q; want %q", url, want)
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
