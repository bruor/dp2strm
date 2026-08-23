package versioning

import (
	"fmt"
	"sort"
	"strings"

	"dp2strm/pkg/models"
	"dp2strm/pkg/sanitizer"
)

// ClassifyMovieStreams processes provider relations for a movie and returns classified StreamVersion entries.
func ClassifyMovieStreams(relations []models.ProviderRelation, cleanTitle string, year int) []models.StreamVersion {
	if len(relations) == 0 {
		return nil
	}

	type tempStream struct {
		relation models.ProviderRelation
		quality  models.QualityTier
		bitrate  int
		rawName  string
		category string
	}

	tempStreams := make([]tempStream, 0, len(relations))

	for _, rel := range relations {
		catName := ""
		if rel.Category != nil {
			catName = rel.Category.Name
		}

		rawName := ""
		var bitrate int
		if cp := rel.CustomProperties; cp != nil {
			if bd, ok := cp["basic_data"].(map[string]any); ok {
				if n, ok := bd["name"].(string); ok {
					rawName = n
				}
			}
			if di, ok := cp["detailed_info"].(map[string]any); ok {
				if b, ok := di["bitrate"].(float64); ok {
					bitrate = int(b)
				} else if bInt, ok := di["bitrate"].(int); ok {
					bitrate = bInt
				}
			}
		}

		tier := classifySingleStream(catName, rawName, bitrate, rel)
		tempStreams = append(tempStreams, tempStream{
			relation: rel,
			quality:  tier,
			bitrate:  bitrate,
			rawName:  rawName,
			category: catName,
		})
	}

	// Sort streams: Base first, then 4K variants
	sort.SliceStable(tempStreams, func(i, j int) bool {
		if tempStreams[i].quality != tempStreams[j].quality {
			return tempStreams[i].quality == models.QualityBase // Base comes first
		}
		return tempStreams[i].bitrate > tempStreams[j].bitrate
	})

	results := make([]models.StreamVersion, 0, len(tempStreams))
	baseCount := 0
	fourKCount := 0

	for _, ts := range tempStreams {
		var suffix string
		var varKey string
		if ts.quality == models.QualityBase {
			if baseCount == 0 {
				suffix = "" // Base default version (clean filename)
				varKey = "base"
			} else {
				suffix = fmt.Sprintf(" - Version %d", baseCount+1)
				varKey = fmt.Sprintf("version%d", baseCount+1)
			}
			baseCount++
		} else {
			if fourKCount == 0 {
				suffix = " - 4K"
				varKey = "4k"
			} else {
				suffix = fmt.Sprintf(" - 4K (%d)", fourKCount+1)
				varKey = fmt.Sprintf("4k-%d", fourKCount+1)
			}
			fourKCount++
		}

		safeBaseName := sanitizer.SanitizeFileName(cleanTitle)
		yearStr := ""
		if year > 0 {
			expectedYear := fmt.Sprintf("(%d)", year)
			if !strings.HasSuffix(safeBaseName, expectedYear) {
				yearStr = fmt.Sprintf(" (%d)", year)
			}
		}

		ext := ts.relation.ContainerExtension
		if ext == "" {
			ext = "mkv"
		}

		filename := fmt.Sprintf("%s%s%s.%s", safeBaseName, yearStr, suffix, "strm")

		providerName := ""
		if ts.relation.M3UAccount != nil {
			providerName = ts.relation.M3UAccount.Name
		}

		movieUUID := ""
		if ts.relation.Movie != nil {
			movieUUID = ts.relation.Movie.UUID
		}

		results = append(results, models.StreamVersion{
			RelationID: ts.relation.ID,
			StreamID:   ts.relation.StreamID,
			MovieUUID:  movieUUID,
			Container:  ext,
			Quality:    ts.quality,
			VariantKey: varKey,
			Provider:   providerName,
			Bitrate:    ts.bitrate,
			Filename:   filename,
		})
	}

	return results
}

func classifySingleStream(catName, rawName string, bitrate int, rel models.ProviderRelation) models.QualityTier {
	upperCat := strings.ToUpper(catName)
	upperName := strings.ToUpper(rawName)

	// Tier 1: Category Name Check
	if strings.Contains(upperCat, "4K") || strings.Contains(upperCat, "2160") || strings.Contains(upperCat, "UHD") {
		return models.Quality4K
	}

	// Tier 2: Stream Title Parsing
	if strings.Contains(upperName, "4K") || strings.Contains(upperName, "2160") || strings.Contains(upperName, "UHD") {
		return models.Quality4K
	}

	// Tier 3: Technical Bitrate Check
	if bitrate >= 25000 {
		return models.Quality4K
	}

	// Default fallback: Base 1080p
	return models.QualityBase
}
