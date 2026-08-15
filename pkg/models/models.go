package models

import "time"

// Config represents the application configuration.
type Config struct {
	DispatcharrURL        string        `json:"dispatcharr_url"`
	APIKey                string        `json:"api_key"`
	XCUser                string        `json:"xc_user"`
	XCPassword            string        `json:"xc_password"`
	OutputDir             string        `json:"output_dir"`
	StreamcacheURL        string        `json:"streamcache_url"`
	FetchAheadCount       int           `json:"fetch_ahead_count"`
	RetentionGraceDays    int           `json:"retention_grace_days"`
	Port                  int           `json:"port"`
	URL                   string        `json:"url"`
	SyncInterval          time.Duration `json:"sync_interval"`
	SyncCron              string        `json:"sync_cron"`
	TZ                    string        `json:"tz"`
	BatchSize             int           `json:"batch_size"`
	SkipStartupSync       bool          `json:"skip_startup_sync"`
	CircuitBreakerEnabled bool          `json:"circuit_breaker_enabled"`
	MaxProbeConcurrency   int           `json:"max_probe_concurrency"`
	PageSize              int           `json:"page_size"`
	PageWorkers           int           `json:"page_workers"`
	EmbyURL               string        `json:"emby_url"`
	EmbyAPIKey            string        `json:"emby_api_key"`
	Ordering              string        `json:"ordering"`
}

// Category represents a VOD category.
type Category struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	CategoryType string `json:"category_type"`
}

// Movie represents a Dispatcharr movie entity.
type Movie struct {
	ID               int                `json:"id"`
	UUID             string             `json:"uuid"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Year             int                `json:"year"`
	Rating           string             `json:"rating"`
	Genre            string             `json:"genre"`
	DurationSecs     int                `json:"duration_secs"`
	TMDBID           string             `json:"tmdb_id"`
	IMDBID           string             `json:"imdb_id"`
	IsAdult          bool               `json:"is_adult"`
	CustomProperties map[string]any     `json:"custom_properties"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	Providers        []ProviderRelation `json:"providers,omitempty"`
}

// Series represents a Dispatcharr TV series entity.
type Series struct {
	ID               int                `json:"id"`
	UUID             string             `json:"uuid"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Year             int                `json:"year"`
	Rating           string             `json:"rating"`
	Genre            string             `json:"genre"`
	TMDBID           string             `json:"tmdb_id"`
	IMDBID           string             `json:"imdb_id"`
	CustomProperties map[string]any     `json:"custom_properties"`
	EpisodeCount     int                `json:"episode_count"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	Providers        []ProviderRelation `json:"providers,omitempty"`
}

// Episode represents an episode of a TV series.
type Episode struct {
	ID                 int    `json:"id"`
	UUID               string `json:"uuid"`
	Name               string `json:"name"`
	Description        string `json:"description"`
	SeasonNumber       int    `json:"season_number"`
	EpisodeNumber      int    `json:"episode_number"`
	DurationSecs       int    `json:"duration_secs"`
	TMDBID             string `json:"tmdb_id"`
	IMDBID             string `json:"imdb_id"`
	SeriesID           int    `json:"series_id"`
	ContainerExtension string `json:"container_extension"`
}

// ProviderRelation represents an M3UMovieRelation or M3UEpisodeRelation.
type ProviderRelation struct {
	ID                 int               `json:"id"`
	Movie              *Movie            `json:"movie,omitempty"`
	Category           *Category         `json:"category,omitempty"`
	M3UAccount         *M3UAccountInfo   `json:"m3u_account,omitempty"`
	StreamID           string            `json:"stream_id"`
	ContainerExtension string            `json:"container_extension"`
	QualityInfo        map[string]string `json:"quality_info,omitempty"`
	CustomProperties   map[string]any    `json:"custom_properties,omitempty"`
}

// M3UAccountInfo represents basic provider account details.
type M3UAccountInfo struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
}

// QualityTier represents classified stream quality for version naming.
type QualityTier string

const (
	QualityBase QualityTier = "Base" // 1080p / Standard HD default
	Quality4K   QualityTier = "4K"   // 4K UHD secondary version
)

// StreamVersion holds classified stream information for STRM generation.
type StreamVersion struct {
	RelationID int
	StreamID   string
	MovieUUID  string
	Container  string
	Quality    QualityTier
	Provider   string
	Bitrate    int
	Filename   string // e.g. "The Thing (1982).strm" or "The Thing (1982) - 4K.strm"
}

// PaginatedResponse represents Dispatcharr REST API paginated list response.
type PaginatedResponse[T any] struct {
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
	Results  []T     `json:"results"`
}
