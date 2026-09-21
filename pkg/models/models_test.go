package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSeriesUnmarshalJSON(t *testing.T) {
	// Case 1: Numeric episode_count
	dataNum := []byte(`{
		"id": 101,
		"name": "Test Show",
		"episode_count": 24,
		"created_at": "2026-08-01T12:00:00Z",
		"updated_at": "2026-09-01T12:00:00Z"
	}`)
	var s1 Series
	if err := json.Unmarshal(dataNum, &s1); err != nil {
		t.Fatalf("Failed to unmarshal numeric episode_count: %v", err)
	}
	if s1.EpisodeCount != 24 {
		t.Errorf("Expected EpisodeCount=24, got %d", s1.EpisodeCount)
	}
	if s1.Name != "Test Show" {
		t.Errorf("Expected Name='Test Show', got %q", s1.Name)
	}
	expectedUpdate := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if !s1.UpdatedAt.Equal(expectedUpdate) {
		t.Errorf("Expected UpdatedAt=%v, got %v", expectedUpdate, s1.UpdatedAt)
	}

	// Case 2: String episode_count (e.g. from Django REST schema)
	dataStr := []byte(`{
		"id": 102,
		"name": "String Count Show",
		"episode_count": "48",
		"created_at": "2026-08-01T12:00:00Z",
		"updated_at": "2026-09-10T15:30:00Z"
	}`)
	var s2 Series
	if err := json.Unmarshal(dataStr, &s2); err != nil {
		t.Fatalf("Failed to unmarshal string episode_count: %v", err)
	}
	if s2.EpisodeCount != 48 {
		t.Errorf("Expected EpisodeCount=48 from string, got %d", s2.EpisodeCount)
	}

	// Case 3: Omitted or null episode_count
	dataNull := []byte(`{
		"id": 103,
		"name": "Null Count Show"
	}`)
	var s3 Series
	if err := json.Unmarshal(dataNull, &s3); err != nil {
		t.Fatalf("Failed to unmarshal null episode_count: %v", err)
	}
	if s3.EpisodeCount != 0 {
		t.Errorf("Expected EpisodeCount=0, got %d", s3.EpisodeCount)
	}
}
