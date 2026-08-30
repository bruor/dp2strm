package models

import (
	"encoding/json"
	"time"
)

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
	ReconcileInterval     time.Duration `json:"reconcile_interval"`
	ReconcileCron         string        `json:"reconcile_cron"`
	TZ                    string        `json:"tz"`
	BatchSize             int           `json:"batch_size"`
	SkipStartupSync       bool          `json:"skip_startup_sync"`
	CircuitBreakerEnabled bool          `json:"circuit_breaker_enabled"`
	MaxProbeConcurrency   int           `json:"max_probe_concurrency"`
	PageSize              int           `json:"page_size"`
	PageWorkers           int           `json:"page_workers"`
	DispatcharrWorkers    int           `json:"dispatcharr_workers"`
	EmbyWorkers           int           `json:"emby_workers"`
	EmbyPaceDelay         time.Duration `json:"emby_pace_delay"`
	EmbyBatchSize         int           `json:"emby_batch_size"`
	EmbyBatchIdleDelay    time.Duration `json:"emby_batch_idle_delay"`
	EmbyURL               string        `json:"emby_url"`
	EmbyAPIKey            string        `json:"emby_api_key"`
	EmbyMediaPath         string        `json:"emby_media_path"`
	EmbyTVLibrary         string        `json:"emby_tv_library"`
	EmbyMoviesLibrary     string        `json:"emby_movies_library"`
	Ordering               string        `json:"ordering"`
	EnableNFO              bool          `json:"enable_nfo"`
	ActivationRefreshDelay time.Duration `json:"activation_refresh_delay"`
}

// Special activation placeholder identifiers (Season 01 Episode 01)
const (
	ActivationSeasonNumber  = 1
	ActivationEpisodeNumber = 1
)

// Queue priorities (Strict Priority Ordering: VIP > Reconcile > Airing > Batch > Migration)
const (
	PriorityVIP       = 100 // On-demand user playback / manual activation
	PriorityReconcile = 50  // Daily reconciliation & maintenance
	PriorityAiring    = 30  // Airing show episode updates
	PriorityBatch     = 10  // Batch catalog intake
	PriorityMigration = 5   // Background legacy placeholder migrations (Specials -> Season 01)
)

// QueueItem represents a task in the SQLite persistent sync_queue.
type QueueItem struct {
	ID        int       `json:"id"`
	ItemType  string    `json:"item_type"` // "series" or "movie"
	ItemID    int       `json:"item_id"`
	Action    string    `json:"action"` // "pass1", "activate", "update_episodes", "delete"
	Priority  int       `json:"priority"`
	Status    string    `json:"status"` // "pending", "in_progress", "completed", "failed"
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

// UnmarshalJSON handles both standard Django REST and XC API schema variations for episode objects.
func (e *Episode) UnmarshalJSON(data []byte) error {
	type Alias Episode
	aux := struct {
		*Alias
		AltSeason     int    `json:"season"`
		AltEpisodeNum int    `json:"episode_num"`
		AltTitle      string `json:"title"`
		AltOverview   string `json:"overview"`
		AltPlot       string `json:"plot"`
	}{
		Alias: (*Alias)(e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if e.SeasonNumber == 0 && aux.AltSeason != 0 {
		e.SeasonNumber = aux.AltSeason
	}
	if e.EpisodeNumber == 0 && aux.AltEpisodeNum != 0 {
		e.EpisodeNumber = aux.AltEpisodeNum
	}
	if e.Name == "" && aux.AltTitle != "" {
		e.Name = aux.AltTitle
	}
	if e.Description == "" {
		if aux.AltOverview != "" {
			e.Description = aux.AltOverview
		} else if aux.AltPlot != "" {
			e.Description = aux.AltPlot
		}
	}
	return nil
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
	VariantKey string // e.g. "base", "4k", "4k-2", "version2"
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
