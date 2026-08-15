package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB encapsulates the SQLite connection pool and helper methods.
type DB struct {
	conn *sql.DB
}

// SeriesState represents the database record for a TV series activation and schedule state.
type SeriesState struct {
	SeriesID           int
	TMDBID             string
	IMDBID             string
	Title              string
	Year               int
	FolderName         string
	Status             string // "Continuing" or "Ended"
	IsActivated        bool
	ActivatedAt        *time.Time
	LastEpisodeCount   int
	NextEpisodeAirDate string
	LastModified       int64
	LastCheckedAt      *time.Time
}

// MovieState represents the database record for a movie activation state.
type MovieState struct {
	MovieID       int
	TMDBID        string
	IMDBID        string
	Title         string
	Year          int
	FolderName    string
	IsActivated   bool
	ActivatedAt   *time.Time
	LastCheckedAt *time.Time
}

// StreamVariant represents a 302 target stream mapping for a movie or episode variant.
type StreamVariant struct {
	ItemID             int
	ItemType           string // "movie" or "series"
	SeasonNum          int
	EpisodeNum         int
	VariantKey         string // "base", "4k", etc.
	TargetURL          string
	Quality            string
	StreamID           string
	ContainerExtension string
	UpdatedAt          time.Time
}

// Open initializes the SQLite database at dbPath, enabling WAL mode and applying schema migrations.
func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return nil, fmt.Errorf("failed to create db directory %s: %w", dir, err)
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database at %s: %w", dbPath, err)
	}

	// Enable WAL mode & busy timeout for high-speed concurrent read/write safety
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA synchronous=NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			log.Printf("[DB] Warning executing pragma %s: %v", p, err)
		}
	}

	d := &DB{conn: conn}
	if err := d.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return d, nil
}

// Close closes the underlying SQLite database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS series_state (
		series_id INTEGER PRIMARY KEY,
		tmdb_id TEXT NOT NULL DEFAULT '',
		imdb_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		year INTEGER DEFAULT 0,
		folder_name TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'Continuing',
		is_activated BOOLEAN NOT NULL DEFAULT 0,
		activated_at DATETIME,
		last_episode_count INTEGER DEFAULT 0,
		next_episode_air_date TEXT DEFAULT '',
		last_modified INTEGER DEFAULT 0,
		last_checked_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS movie_state (
		movie_id INTEGER PRIMARY KEY,
		tmdb_id TEXT NOT NULL DEFAULT '',
		imdb_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		year INTEGER DEFAULT 0,
		folder_name TEXT NOT NULL DEFAULT '',
		is_activated BOOLEAN NOT NULL DEFAULT 0,
		activated_at DATETIME,
		last_checked_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS stream_variants (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		item_id INTEGER NOT NULL,
		item_type TEXT NOT NULL,
		season_num INTEGER DEFAULT 0,
		episode_num INTEGER DEFAULT 0,
		variant_key TEXT NOT NULL DEFAULT 'base',
		target_url TEXT NOT NULL,
		quality TEXT NOT NULL DEFAULT '',
		stream_id TEXT NOT NULL DEFAULT '',
		container_extension TEXT NOT NULL DEFAULT 'mkv',
		updated_at DATETIME NOT NULL,
		UNIQUE(item_id, item_type, season_num, episode_num, variant_key)
	);

	CREATE TABLE IF NOT EXISTS soft_deleted_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		item_path TEXT UNIQUE NOT NULL,
		item_type TEXT NOT NULL,
		soft_deleted_at DATETIME NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_series_state_activated ON series_state(is_activated, status);
	CREATE INDEX IF NOT EXISTS idx_movie_state_activated ON movie_state(is_activated);
	CREATE INDEX IF NOT EXISTS idx_stream_variants_lookup ON stream_variants(item_id, item_type, season_num, episode_num, variant_key);
	`

	if _, err := d.conn.Exec(schema); err != nil {
		return err
	}

	// Schema migration for existing tables if columns are missing
	migrations := []string{
		"ALTER TABLE series_state ADD COLUMN year INTEGER DEFAULT 0;",
		"ALTER TABLE series_state ADD COLUMN folder_name TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE series_state ADD COLUMN imdb_id TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE movie_state ADD COLUMN year INTEGER DEFAULT 0;",
		"ALTER TABLE movie_state ADD COLUMN folder_name TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE movie_state ADD COLUMN imdb_id TEXT NOT NULL DEFAULT '';",
	}
	for _, m := range migrations {
		_, _ = d.conn.Exec(m) // Ignore error if column already exists
	}

	return nil
}

// --- Series State Queries ---

// GetSeriesState retrieves the series state by series ID.
func (d *DB) GetSeriesState(seriesID int) (*SeriesState, error) {
	query := `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state WHERE series_id = ?`
	row := d.conn.QueryRow(query, seriesID)

	var s SeriesState
	var actAt, chkAt sql.NullTime
	err := row.Scan(&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.Status, &s.IsActivated, &actAt, &s.LastEpisodeCount, &s.NextEpisodeAirDate, &s.LastModified, &chkAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if actAt.Valid {
		s.ActivatedAt = &actAt.Time
	}
	if chkAt.Valid {
		s.LastCheckedAt = &chkAt.Time
	}
	return &s, nil
}

// UpsertInitialSeriesState records title, tmdbID, imdbID, year, folderName during Pass 1 without altering activation state.
func (d *DB) UpsertInitialSeriesState(seriesID int, tmdbID, imdbID, title string, year int, folderName string) error {
	now := time.Now()
	query := `
	INSERT INTO series_state (series_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, last_checked_at)
	VALUES (?, ?, ?, ?, ?, ?, 0, ?)
	ON CONFLICT(series_id) DO UPDATE SET
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE series_state.tmdb_id END,
		imdb_id = CASE WHEN EXCLUDED.imdb_id != '' THEN EXCLUDED.imdb_id ELSE series_state.imdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE series_state.title END,
		year = CASE WHEN EXCLUDED.year > 0 THEN EXCLUDED.year ELSE series_state.year END,
		folder_name = CASE WHEN EXCLUDED.folder_name != '' THEN EXCLUDED.folder_name ELSE series_state.folder_name END,
		last_checked_at = EXCLUDED.last_checked_at;
	`
	_, err := d.conn.Exec(query, seriesID, tmdbID, imdbID, title, year, folderName, now)
	return err
}

// SetSeriesActivated marks a series as activated by the user and records the activation timestamp.
func (d *DB) SetSeriesActivated(seriesID int, tmdbID, title string) error {
	now := time.Now()
	query := `
	INSERT INTO series_state (series_id, tmdb_id, title, is_activated, activated_at, last_checked_at)
	VALUES (?, ?, ?, 1, ?, ?)
	ON CONFLICT(series_id) DO UPDATE SET
		is_activated = 1,
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE series_state.tmdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE series_state.title END,
		activated_at = COALESCE(series_state.activated_at, EXCLUDED.activated_at),
		last_checked_at = EXCLUDED.last_checked_at;
	`
	_, err := d.conn.Exec(query, seriesID, tmdbID, title, now, now)
	return err
}

// UpdateSeriesMetadata updates status, episode count, next air date, and last_modified timestamp for a series.
func (d *DB) UpdateSeriesMetadata(seriesID int, status string, epCount int, nextAirDate string, lastModified int64) error {
	now := time.Now()
	query := `
	UPDATE series_state SET
		status = ?,
		last_episode_count = ?,
		next_episode_air_date = ?,
		last_modified = ?,
		last_checked_at = ?
	WHERE series_id = ?;
	`
	_, err := d.conn.Exec(query, status, epCount, nextAirDate, lastModified, now, seriesID)
	return err
}

// GetActiveSeriesToSync returns all activated series that need episode checking (skips Ended series and series with future next_episode_air_date).
func (d *DB) GetActiveSeriesToSync(currentDate string) ([]int, error) {
	query := `
	SELECT series_id FROM series_state
	WHERE is_activated = 1
	  AND status != 'Ended'
	  AND (next_episode_air_date = '' OR next_episode_air_date <= ?);
	`
	rows, err := d.conn.Query(query, currentDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// --- Movie State Queries ---

// GetMovieState retrieves the movie state by movie ID.
func (d *DB) GetMovieState(movieID int) (*MovieState, error) {
	query := `SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at FROM movie_state WHERE movie_id = ?`
	row := d.conn.QueryRow(query, movieID)

	var m MovieState
	var actAt, chkAt sql.NullTime
	err := row.Scan(&m.MovieID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.FolderName, &m.IsActivated, &actAt, &chkAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if actAt.Valid {
		m.ActivatedAt = &actAt.Time
	}
	if chkAt.Valid {
		m.LastCheckedAt = &chkAt.Time
	}
	return &m, nil
}

// UpsertInitialMovieState records title, tmdbID, imdbID, year, folderName during Pass 1 without altering activation state.
func (d *DB) UpsertInitialMovieState(movieID int, tmdbID, imdbID, title string, year int, folderName string) error {
	now := time.Now()
	query := `
	INSERT INTO movie_state (movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, last_checked_at)
	VALUES (?, ?, ?, ?, ?, ?, 0, ?)
	ON CONFLICT(movie_id) DO UPDATE SET
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE movie_state.tmdb_id END,
		imdb_id = CASE WHEN EXCLUDED.imdb_id != '' THEN EXCLUDED.imdb_id ELSE movie_state.imdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE movie_state.title END,
		year = CASE WHEN EXCLUDED.year > 0 THEN EXCLUDED.year ELSE movie_state.year END,
		folder_name = CASE WHEN EXCLUDED.folder_name != '' THEN EXCLUDED.folder_name ELSE movie_state.folder_name END,
		last_checked_at = EXCLUDED.last_checked_at;
	`
	_, err := d.conn.Exec(query, movieID, tmdbID, imdbID, title, year, folderName, now)
	return err
}

// SetMovieActivated marks a movie as activated by the user and records the activation timestamp.
func (d *DB) SetMovieActivated(movieID int, tmdbID, title string) error {
	now := time.Now()
	query := `
	INSERT INTO movie_state (movie_id, tmdb_id, title, is_activated, activated_at, last_checked_at)
	VALUES (?, ?, ?, 1, ?, ?)
	ON CONFLICT(movie_id) DO UPDATE SET
		is_activated = 1,
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE movie_state.tmdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE movie_state.title END,
		activated_at = COALESCE(movie_state.activated_at, EXCLUDED.activated_at),
		last_checked_at = EXCLUDED.last_checked_at;
	`
	_, err := d.conn.Exec(query, movieID, tmdbID, title, now, now)
	return err
}

// --- Stream Variants Queries (Sub-50ms 302 Redirect Target Lookups) ---

// SaveStreamVariant inserts or updates a stream target mapping for a movie or episode.
func (d *DB) SaveStreamVariant(v *StreamVariant) error {
	query := `
	INSERT INTO stream_variants (item_id, item_type, season_num, episode_num, variant_key, target_url, quality, stream_id, container_extension, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(item_id, item_type, season_num, episode_num, variant_key) DO UPDATE SET
		target_url = EXCLUDED.target_url,
		quality = EXCLUDED.quality,
		stream_id = EXCLUDED.stream_id,
		container_extension = EXCLUDED.container_extension,
		updated_at = EXCLUDED.updated_at;
	`
	_, err := d.conn.Exec(query, v.ItemID, v.ItemType, v.SeasonNum, v.EpisodeNum, v.VariantKey, v.TargetURL, v.Quality, v.StreamID, v.ContainerExtension, time.Now())
	return err
}

// GetMovieTargetURL retrieves the target 302 stream URL for a movie and variant key ("base", "4k").
func (d *DB) GetMovieTargetURL(movieID int, variantKey string) (string, error) {
	if variantKey == "" {
		variantKey = "base"
	}
	query := `SELECT target_url FROM stream_variants WHERE item_id = ? AND item_type = 'movie' AND variant_key = ?`
	var targetURL string
	err := d.conn.QueryRow(query, movieID, variantKey).Scan(&targetURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return targetURL, err
}

// GetSeriesTargetURL retrieves the target 302 stream URL for a TV episode and variant key ("base", "4k").
func (d *DB) GetSeriesTargetURL(seriesID, seasonNum, episodeNum int, variantKey string) (string, error) {
	if variantKey == "" {
		variantKey = "base"
	}
	query := `SELECT target_url FROM stream_variants WHERE item_id = ? AND item_type = 'series' AND season_num = ? AND episode_num = ? AND variant_key = ?`
	var targetURL string
	err := d.conn.QueryRow(query, seriesID, seasonNum, episodeNum, variantKey).Scan(&targetURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return targetURL, err
}

// GetFirstAvailableSeriesTargetURL retrieves the earliest available episode stream URL for a series (e.g. if show only has Season 2+).
func (d *DB) GetFirstAvailableSeriesTargetURL(seriesID int, variantKey string) (string, error) {
	if variantKey == "" {
		variantKey = "base"
	}
	query := `SELECT target_url FROM stream_variants WHERE item_id = ? AND item_type = 'series' AND variant_key = ? ORDER BY season_num ASC, episode_num ASC LIMIT 1`
	var targetURL string
	err := d.conn.QueryRow(query, seriesID, variantKey).Scan(&targetURL)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return targetURL, err
}

// --- Soft Delete Tracking Queries ---

// SoftDeleteItem records an item path as soft deleted with the current timestamp.
func (d *DB) SoftDeleteItem(itemPath, itemType string) error {
	query := `
	INSERT INTO soft_deleted_items (item_path, item_type, soft_deleted_at)
	VALUES (?, ?, ?)
	ON CONFLICT(item_path) DO UPDATE SET soft_deleted_at = EXCLUDED.soft_deleted_at;
	`
	_, err := d.conn.Exec(query, itemPath, itemType, time.Now())
	return err
}

// GetExpiredSoftDeletedItems returns item paths that have exceeded retentionGraceDays.
func (d *DB) GetExpiredSoftDeletedItems(retentionGraceDays int) ([]string, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionGraceDays)
	query := `SELECT item_path FROM soft_deleted_items WHERE soft_deleted_at <= ?`
	rows, err := d.conn.Query(query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// RemoveSoftDeletedItem removes an item from soft delete tracking after hard purge.
func (d *DB) RemoveSoftDeletedItem(itemPath string) error {
	query := `DELETE FROM soft_deleted_items WHERE item_path = ?`
	_, err := d.conn.Exec(query, itemPath)
	return err
}
