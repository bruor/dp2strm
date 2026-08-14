package versioning

import (
	"testing"

	"dp2strm/pkg/models"
)

func TestClassifyMovieStreams(t *testing.T) {
	// Proof case: The Thing (1982) with 2 relations (1080p Horror + 4K Movies)
	relations := []models.ProviderRelation{
		{
			ID:                 267361,
			StreamID:           "1608573",
			ContainerExtension: "mkv",
			Category:           &models.Category{Name: "Horror"},
			M3UAccount:         &models.M3UAccountInfo{Name: "Infinity"},
			CustomProperties: map[string]any{
				"basic_data": map[string]any{
					"name": "The Thing (1982)",
				},
				"detailed_info": map[string]any{
					"bitrate": 19260,
				},
			},
		},
		{
			ID:                 278597,
			StreamID:           "1826821",
			ContainerExtension: "mkv",
			Category:           &models.Category{Name: "4K Movies"},
			M3UAccount:         &models.M3UAccountInfo{Name: "Infinity"},
			CustomProperties: map[string]any{
				"basic_data": map[string]any{
					"name": "4K: The Thing (1982)",
				},
				"detailed_info": map[string]any{
					"bitrate": 99339,
				},
			},
		},
	}

	versions := ClassifyMovieStreams(relations, "The Thing (1982)", 1982)

	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}

	// First version MUST be Base default version: The Thing (1982).strm
	baseVersion := versions[0]
	if baseVersion.Quality != models.QualityBase {
		t.Errorf("version 0 quality = %s; want %s", baseVersion.Quality, models.QualityBase)
	}
	if baseVersion.Filename != "The Thing (1982).strm" {
		t.Errorf("version 0 filename = %q; want %q", baseVersion.Filename, "The Thing (1982).strm")
	}

	// Second version MUST be 4K secondary version: The Thing (1982) - 4K.strm
	fourKVersion := versions[1]
	if fourKVersion.Quality != models.Quality4K {
		t.Errorf("version 1 quality = %s; want %s", fourKVersion.Quality, models.Quality4K)
	}
	if fourKVersion.Filename != "The Thing (1982) - 4K.strm" {
		t.Errorf("version 1 filename = %q; want %q", fourKVersion.Filename, "The Thing (1982) - 4K.strm")
	}
}
