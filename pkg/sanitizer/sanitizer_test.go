package sanitizer

import (
	"testing"
)

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"4K: The Thing (1982)", "The Thing (1982)"},
		{"Avatar 2160p UHD HDR", "Avatar"},
		{"The Movie 1080p HEVC WEB-DL", "The Movie"},
		{"Plain Title (2020)", "Plain Title (2020)"},
		{"+1 (2013)", "+1 (2013)"},
		{"Yahoo+ (2022) (2022)", "Yahoo+ (2022)"},
		{"#Horror (2015) (2015)", "#Horror (2015)"},
		{"102 Dalmatians (2000) (2000)", "102 Dalmatians (2000)"},
		{"Deep Purple California Jam 1974 (1974) (1974)", "Deep Purple California Jam 1974 (1974)"},
		{"Bracketed Year [2022] (2022)", "Bracketed Year (2022)"},
		{"Fantasia 2000 (2000)", "Fantasia 2000 (2000)"},
		{"Fear Street 1978 (2021)", "Fear Street 1978 (2021)"},
	}

	for _, tt := range tests {
		got := CleanTitle(tt.input)
		if got != tt.expected {
			t.Errorf("CleanTitle(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildMovieFolderName(t *testing.T) {
	tests := []struct {
		title    string
		year     int
		tmdbID   string
		expected string
	}{
		{"The Thing", 1982, "1091", "The Thing (1982) [tmdbid=1091]"},
		{"The Thing (1982)", 1982, "1091", "The Thing (1982) [tmdbid=1091]"}, // Prevents year duplication!
		{"Becoming", 2020, "699280", "Becoming (2020) [tmdbid=699280]"},
		{"Becoming", 2020, "526052", "Becoming (2020) [tmdbid=526052]"},
		{CleanTitle("Yahoo+ (2022) (2022)"), 2022, "1002442", "Yahoo+ (2022) [tmdbid=1002442]"},
		{CleanTitle("102 Dalmatians (2000) (2000)"), 2000, "10481", "102 Dalmatians (2000) [tmdbid=10481]"},
	}

	for _, tt := range tests {
		got := BuildMovieFolderName(tt.title, tt.year, tt.tmdbID)
		if got != tt.expected {
			t.Errorf("BuildMovieFolderName(%q, %d, %q) = %q; want %q", tt.title, tt.year, tt.tmdbID, got, tt.expected)
		}
	}
}

func TestCleanEpisodeTitle(t *testing.T) {
	tests := []struct {
		epName     string
		seriesName string
		season     int
		episode    int
		expected   string
	}{
		{"#RichKids of Beverly Hills (2014) - S01E01 - #WelcometoBH", "#RichKids of Beverly Hills (2014)", 1, 1, "#WelcometoBH"},
		{"007 Road to a Million - S01E01 - EPISODE ONE", "007 Road to a Million", 1, 1, "EPISODE ONE"},
		{"Episode 1", "Sample Show", 1, 1, "Episode 1"},
	}

	for _, tt := range tests {
		got := CleanEpisodeTitle(tt.epName, tt.seriesName, tt.season, tt.episode)
		if got != tt.expected {
			t.Errorf("CleanEpisodeTitle(%q, %q, %d, %d) = %q; want %q", tt.epName, tt.seriesName, tt.season, tt.episode, got, tt.expected)
		}
	}
}
