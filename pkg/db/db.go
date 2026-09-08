package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dp2strm/pkg/models"
	"dp2strm/pkg/sanitizer"
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
	Status             string // "Continuing", "Ended", etc.
	IsActivated        bool
	ActivatedAt        *time.Time
	LastEpisodeCount   int
	NextEpisodeAirDate string
	LastModified       int64
	LastCheckedAt      *time.Time
}

// MovieState represents the database record for a Movie activation and variant state.
type MovieState struct {
	MovieID       int
	TMDBID        string
	IMDBID        string
	Title         string
	Year          int
	FolderName    string
	IsActivated   bool
	ActivatedAt   *time.Time
	LastModified  int64
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

	// Limit SQLite connection pool to 1 to guarantee serialized thread safety and eliminate lock contention
	conn.SetMaxOpenConns(1)

	// Enable WAL mode & busy timeout for high-speed concurrent read/write safety
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA busy_timeout=10000;",
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

	// Deduplicate any historical ghost records from past Dispatcharr ID re-assignment cycles
	if _, err := d.DeduplicateSupersededStates(); err != nil {
		log.Printf("[DB] Warning during deduplication: %v", err)
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

	CREATE TABLE IF NOT EXISTS sync_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		item_type TEXT NOT NULL,
		item_id INTEGER NOT NULL,
		action TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 10,
		status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0,
		last_error TEXT DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(item_type, item_id, action)
	);

	CREATE INDEX IF NOT EXISTS idx_series_state_activated ON series_state(is_activated, status);
	CREATE INDEX IF NOT EXISTS idx_series_state_tmdb ON series_state(tmdb_id);
	CREATE INDEX IF NOT EXISTS idx_series_state_imdb ON series_state(imdb_id);
	CREATE INDEX IF NOT EXISTS idx_series_state_title_year ON series_state(title, year);
	CREATE INDEX IF NOT EXISTS idx_movie_state_activated ON movie_state(is_activated);
	CREATE INDEX IF NOT EXISTS idx_movie_state_tmdb ON movie_state(tmdb_id);
	CREATE INDEX IF NOT EXISTS idx_movie_state_imdb ON movie_state(imdb_id);
	CREATE INDEX IF NOT EXISTS idx_movie_state_title_year ON movie_state(title, year);
	CREATE INDEX IF NOT EXISTS idx_stream_variants_lookup ON stream_variants(item_id, item_type, season_num, episode_num, variant_key);
	CREATE INDEX IF NOT EXISTS idx_sync_queue_poll ON sync_queue(status, priority DESC, id ASC);
	`

	if _, err := d.conn.Exec(schema); err != nil {
		return err
	}

	// Schema migration for existing tables if columns are missing
	migrations := []string{
		"ALTER TABLE series_state ADD COLUMN year INTEGER DEFAULT 0;",
		"ALTER TABLE series_state ADD COLUMN folder_name TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE series_state ADD COLUMN imdb_id TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE series_state ADD COLUMN last_checked_at DATETIME;",
		"ALTER TABLE movie_state ADD COLUMN year INTEGER DEFAULT 0;",
		"ALTER TABLE movie_state ADD COLUMN folder_name TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE movie_state ADD COLUMN imdb_id TEXT NOT NULL DEFAULT '';",
		"ALTER TABLE movie_state ADD COLUMN last_checked_at DATETIME;",
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
		folder_name = CASE WHEN series_state.folder_name != '' THEN series_state.folder_name ELSE EXCLUDED.folder_name END,
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

// FindSeriesByTitleAndYear finds a series record by matching title and optionally year.
func (d *DB) FindSeriesByTitleAndYear(title string, year int) (*SeriesState, error) {
	var query string
	var row *sql.Row
	if year > 0 {
		query = `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state WHERE title = ? AND year = ? LIMIT 1`
		row = d.conn.QueryRow(query, title, year)
	} else {
		query = `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state WHERE title = ? LIMIT 1`
		row = d.conn.QueryRow(query, title)
	}
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

// GetUnactivatedSeries returns all unactivated series records from SQLite.
func (d *DB) GetUnactivatedSeries() ([]*SeriesState, error) {
	query := `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state WHERE is_activated = 0`
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SeriesState
	for rows.Next() {
		var s SeriesState
		var actAt, chkAt sql.NullTime
		if err := rows.Scan(&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.Status, &s.IsActivated, &actAt, &s.LastEpisodeCount, &s.NextEpisodeAirDate, &s.LastModified, &chkAt); err != nil {
			continue
		}
		if actAt.Valid {
			s.ActivatedAt = &actAt.Time
		}
		if chkAt.Valid {
			s.LastCheckedAt = &chkAt.Time
		}
		results = append(results, &s)
	}
	return results, nil
}

// GetAllSeriesStates returns all series records from SQLite.
func (d *DB) GetAllSeriesStates() ([]*SeriesState, error) {
	query := `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state`
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SeriesState
	for rows.Next() {
		var s SeriesState
		var actAt, chkAt sql.NullTime
		if err := rows.Scan(&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.Status, &s.IsActivated, &actAt, &s.LastEpisodeCount, &s.NextEpisodeAirDate, &s.LastModified, &chkAt); err != nil {
			continue
		}
		if actAt.Valid {
			s.ActivatedAt = &actAt.Time
		}
		if chkAt.Valid {
			s.LastCheckedAt = &chkAt.Time
		}
		results = append(results, &s)
	}
	return results, nil
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

// GetSeriesIDByIdentifier resolves a series identifier string (e.g. "tmdb:218510", "imdb:tt12345", or "26078") to the active Dispatcharr series ID.
func (d *DB) GetSeriesIDByIdentifier(identifier string) (int, string, error) {
	raw := strings.TrimSpace(identifier)
	if raw == "" {
		return 0, "", fmt.Errorf("empty series identifier")
	}

	// 1. TMDB ID Prefix
	if strings.HasPrefix(strings.ToLower(raw), "tmdb:") {
		tmdb := strings.TrimPrefix(raw, "tmdb:")
		tmdb = strings.TrimPrefix(tmdb, "TMDB:")
		var seriesID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT series_id, tmdb_id FROM series_state WHERE tmdb_id = ? ORDER BY series_id DESC LIMIT 1`, tmdb).Scan(&seriesID, &foundTMDB)
		if err == sql.ErrNoRows {
			return 0, tmdb, nil
		}
		return seriesID, foundTMDB, err
	}

	// 2. IMDb ID Prefix
	if strings.HasPrefix(strings.ToLower(raw), "imdb:") {
		imdb := strings.TrimPrefix(raw, "imdb:")
		imdb = strings.TrimPrefix(imdb, "IMDB:")
		var seriesID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT series_id, tmdb_id FROM series_state WHERE imdb_id = ? ORDER BY series_id DESC LIMIT 1`, imdb).Scan(&seriesID, &foundTMDB)
		if err == sql.ErrNoRows {
			return 0, "", nil
		}
		return seriesID, foundTMDB, err
	}

	// 3. Pure Numeric ID
	if numID, err := strconv.Atoi(raw); err == nil && numID > 0 {
		var seriesID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT series_id, tmdb_id FROM series_state WHERE series_id = ? LIMIT 1`, numID).Scan(&seriesID, &foundTMDB)
		if err == nil {
			return seriesID, foundTMDB, nil
		}
		return numID, "", nil
	}

	return 0, "", fmt.Errorf("unrecognized series identifier format: %s", identifier)
}

// FindSeriesByFingerprint searches for an active series ID using stored TMDB ID or title + year when an ID has gone stale.
func (d *DB) FindSeriesByFingerprint(oldSeriesID int, title string, year int) (int, error) {
	if oldSeriesID > 0 {
		var tmdbID string
		var oldTitle string
		var oldYear int
		_ = d.conn.QueryRow(`SELECT tmdb_id, title, year FROM series_state WHERE series_id = ?`, oldSeriesID).Scan(&tmdbID, &oldTitle, &oldYear)
		if tmdbID != "" {
			var activeID int
			err := d.conn.QueryRow(`SELECT series_id FROM series_state WHERE tmdb_id = ? AND series_id != ? ORDER BY series_id DESC LIMIT 1`, tmdbID, oldSeriesID).Scan(&activeID)
			if err == nil && activeID > 0 {
				return activeID, nil
			}
		}
		if title == "" {
			title = oldTitle
		}
		if year <= 0 {
			year = oldYear
		}
	}

	if title != "" && year > 0 {
		var activeID int
		err := d.conn.QueryRow(`SELECT series_id FROM series_state WHERE title = ? AND year = ? AND series_id != ? ORDER BY series_id DESC LIMIT 1`, title, year, oldSeriesID).Scan(&activeID)
		if err == nil && activeID > 0 {
			return activeID, nil
		}
	}

	return 0, sql.ErrNoRows
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

// FindMovieByTitleAndYear finds a movie record by matching title and optionally year.
func (d *DB) FindMovieByTitleAndYear(title string, year int) (*MovieState, error) {
	var query string
	var row *sql.Row
	if year > 0 {
		query = `SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at FROM movie_state WHERE title = ? AND year = ? LIMIT 1`
		row = d.conn.QueryRow(query, title, year)
	} else {
		query = `SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at FROM movie_state WHERE title = ? LIMIT 1`
		row = d.conn.QueryRow(query, title)
	}
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

// GetAllMovieStates returns all movie records from SQLite.
func (d *DB) GetAllMovieStates() ([]*MovieState, error) {
	query := `SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at FROM movie_state`
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*MovieState
	for rows.Next() {
		var m MovieState
		var actAt, chkAt sql.NullTime
		if err := rows.Scan(&m.MovieID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.FolderName, &m.IsActivated, &actAt, &chkAt); err != nil {
			continue
		}
		if actAt.Valid {
			m.ActivatedAt = &actAt.Time
		}
		if chkAt.Valid {
			m.LastCheckedAt = &chkAt.Time
		}
		results = append(results, &m)
	}
	return results, nil
}

// GetMovieIDByIdentifier resolves a movie identifier string (e.g. "tmdb:1739639", "imdb:tt36615831", or "1198086") to the active Dispatcharr movie ID.
func (d *DB) GetMovieIDByIdentifier(identifier string) (int, string, error) {
	raw := strings.TrimSpace(identifier)
	if raw == "" {
		return 0, "", fmt.Errorf("empty movie identifier")
	}

	// 1. TMDB ID Prefix
	if strings.HasPrefix(strings.ToLower(raw), "tmdb:") {
		tmdb := strings.TrimPrefix(raw, "tmdb:")
		tmdb = strings.TrimPrefix(tmdb, "TMDB:")
		var movieID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT movie_id, tmdb_id FROM movie_state WHERE tmdb_id = ? ORDER BY movie_id DESC LIMIT 1`, tmdb).Scan(&movieID, &foundTMDB)
		if err == sql.ErrNoRows {
			return 0, tmdb, nil
		}
		return movieID, foundTMDB, err
	}

	// 2. IMDb ID Prefix
	if strings.HasPrefix(strings.ToLower(raw), "imdb:") {
		imdb := strings.TrimPrefix(raw, "imdb:")
		imdb = strings.TrimPrefix(imdb, "IMDB:")
		var movieID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT movie_id, tmdb_id FROM movie_state WHERE imdb_id = ? ORDER BY movie_id DESC LIMIT 1`, imdb).Scan(&movieID, &foundTMDB)
		if err == sql.ErrNoRows {
			return 0, "", nil
		}
		return movieID, foundTMDB, err
	}

	// 3. Pure Numeric ID
	if numID, err := strconv.Atoi(raw); err == nil && numID > 0 {
		var movieID int
		var foundTMDB string
		err := d.conn.QueryRow(`SELECT movie_id, tmdb_id FROM movie_state WHERE movie_id = ? LIMIT 1`, numID).Scan(&movieID, &foundTMDB)
		if err == nil {
			return movieID, foundTMDB, nil
		}
		return numID, "", nil
	}

	return 0, "", fmt.Errorf("unrecognized movie identifier format: %s", identifier)
}

// FindMovieByFingerprint searches for an active movie ID using stored TMDB ID or title + year when an ID has gone stale.
func (d *DB) FindMovieByFingerprint(oldMovieID int, title string, year int) (int, error) {
	if oldMovieID > 0 {
		var tmdbID string
		var oldTitle string
		var oldYear int
		_ = d.conn.QueryRow(`SELECT tmdb_id, title, year FROM movie_state WHERE movie_id = ?`, oldMovieID).Scan(&tmdbID, &oldTitle, &oldYear)
		if tmdbID != "" {
			var activeID int
			err := d.conn.QueryRow(`SELECT movie_id FROM movie_state WHERE tmdb_id = ? AND movie_id != ? ORDER BY movie_id DESC LIMIT 1`, tmdbID, oldMovieID).Scan(&activeID)
			if err == nil && activeID > 0 {
				return activeID, nil
			}
		}
		if title == "" {
			title = oldTitle
		}
		if year <= 0 {
			year = oldYear
		}
	}

	if title != "" && year > 0 {
		var activeID int
		err := d.conn.QueryRow(`SELECT movie_id FROM movie_state WHERE title = ? AND year = ? AND movie_id != ? ORDER BY movie_id DESC LIMIT 1`, title, year, oldMovieID).Scan(&activeID)
		if err == nil && activeID > 0 {
			return activeID, nil
		}
	}

	return 0, sql.ErrNoRows
}

// FindMovieSupersededState checks if an older/stale record exists matching the same TMDB ID or (Title + Year) under a different movie ID.
func (d *DB) FindMovieSupersededState(movieID int, tmdbID string, title string, year int) (*MovieState, error) {
	cleanTitle := sanitizer.CleanTitle(title)
	if tmdbID != "" {
		var m MovieState
		var actAt, chkAt sql.NullTime
		err := d.conn.QueryRow(`
			SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at 
			FROM movie_state 
			WHERE tmdb_id = ? AND movie_id != ? 
			ORDER BY movie_id DESC LIMIT 1`, tmdbID, movieID).Scan(
			&m.MovieID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.FolderName, &m.IsActivated, &actAt, &chkAt)
		if err == nil {
			if actAt.Valid {
				m.ActivatedAt = &actAt.Time
			}
			if chkAt.Valid {
				m.LastCheckedAt = &chkAt.Time
			}
			return &m, nil
		}
	}
	if cleanTitle != "" && year > 0 {
		var m MovieState
		var actAt, chkAt sql.NullTime
		err := d.conn.QueryRow(`
			SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at 
			FROM movie_state 
			WHERE (title = ? OR title = ?) AND year = ? AND movie_id != ? 
			ORDER BY movie_id DESC LIMIT 1`, cleanTitle, title, year, movieID).Scan(
			&m.MovieID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.FolderName, &m.IsActivated, &actAt, &chkAt)
		if err == nil {
			if actAt.Valid {
				m.ActivatedAt = &actAt.Time
			}
			if chkAt.Valid {
				m.LastCheckedAt = &chkAt.Time
			}
			return &m, nil
		}
	}
	return nil, sql.ErrNoRows
}

// FindSeriesSupersededState checks if an older/stale record exists matching the same TMDB ID or (Title + Year) under a different series ID.
func (d *DB) FindSeriesSupersededState(seriesID int, tmdbID string, title string, year int) (*SeriesState, error) {
	cleanTitle := sanitizer.CleanTitle(title)
	if tmdbID != "" {
		var s SeriesState
		var actAt, chkAt sql.NullTime
		err := d.conn.QueryRow(`
			SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at 
			FROM series_state 
			WHERE tmdb_id = ? AND series_id != ? 
			ORDER BY series_id DESC LIMIT 1`, tmdbID, seriesID).Scan(
			&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.IsActivated, &actAt, &chkAt)
		if err == nil {
			if actAt.Valid {
				s.ActivatedAt = &actAt.Time
			}
			if chkAt.Valid {
				s.LastCheckedAt = &chkAt.Time
			}
			return &s, nil
		}
	}
	if cleanTitle != "" && year > 0 {
		var s SeriesState
		var actAt, chkAt sql.NullTime
		err := d.conn.QueryRow(`
			SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at 
			FROM series_state 
			WHERE (title = ? OR title = ?) AND year = ? AND series_id != ? 
			ORDER BY series_id DESC LIMIT 1`, cleanTitle, title, year, seriesID).Scan(
			&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.IsActivated, &actAt, &chkAt)
		if err == nil {
			if actAt.Valid {
				s.ActivatedAt = &actAt.Time
			}
			if chkAt.Valid {
				s.LastCheckedAt = &chkAt.Time
			}
			return &s, nil
		}
	}
	return nil, sql.ErrNoRows
}

// DeduplicateSupersededStates cleans up historical duplicate records from movie_state and series_state,
// keeping only the latest ID for each unique TMDB ID or (Title + Year) combination.
// It also cleans up orphaned stream_variants and obsolete entries in soft_deleted_items.
func (d *DB) DeduplicateSupersededStates() (int64, error) {
	// 1. Before deleting, transfer any activated status from older duplicates to the newest row for movies
	_, _ = d.conn.Exec(`
		UPDATE movie_state
		SET is_activated = 1
		WHERE movie_id IN (
			SELECT MAX(movie_id)
			FROM movie_state
			WHERE tmdb_id != '' AND tmdb_id != '0'
			GROUP BY tmdb_id
			HAVING COUNT(*) > 1 AND SUM(is_activated) > 0
		);
	`)

	// 2. Prune duplicate movies by tmdb_id (keeping highest movie_id)
	resMvTMDB, err := d.conn.Exec(`
		DELETE FROM movie_state
		WHERE tmdb_id != '' AND tmdb_id != '0'
		  AND movie_id NOT IN (
			  SELECT MAX(movie_id)
			  FROM movie_state
			  WHERE tmdb_id != '' AND tmdb_id != '0'
			  GROUP BY tmdb_id
		  );
	`)
	if err != nil {
		return 0, err
	}
	delMvTMDB, _ := resMvTMDB.RowsAffected()

	// 3. Prune duplicate movies without TMDB ID by (title, year)
	resMvTitle, err := d.conn.Exec(`
		DELETE FROM movie_state
		WHERE (tmdb_id = '' OR tmdb_id = '0')
		  AND title != '' AND year > 0
		  AND movie_id NOT IN (
			  SELECT MAX(movie_id)
			  FROM movie_state
			  WHERE (tmdb_id = '' OR tmdb_id = '0')
			    AND title != '' AND year > 0
			  GROUP BY title, year
		  );
	`)
	if err != nil {
		return delMvTMDB, err
	}
	delMvTitle, _ := resMvTitle.RowsAffected()

	// 4. Transfer activated status for series duplicates
	_, _ = d.conn.Exec(`
		UPDATE series_state
		SET is_activated = 1
		WHERE series_id IN (
			SELECT MAX(series_id)
			FROM series_state
			WHERE tmdb_id != '' AND tmdb_id != '0'
			GROUP BY tmdb_id
			HAVING COUNT(*) > 1 AND SUM(is_activated) > 0
		);
	`)

	// 5. Prune duplicate series by tmdb_id
	resSrsTMDB, err := d.conn.Exec(`
		DELETE FROM series_state
		WHERE tmdb_id != '' AND tmdb_id != '0'
		  AND series_id NOT IN (
			  SELECT MAX(series_id)
			  FROM series_state
			  WHERE tmdb_id != '' AND tmdb_id != '0'
			  GROUP BY tmdb_id
		  );
	`)
	if err != nil {
		return delMvTMDB + delMvTitle, err
	}
	delSrsTMDB, _ := resSrsTMDB.RowsAffected()

	// 6. Prune duplicate series without TMDB ID by (title, year)
	resSrsTitle, err := d.conn.Exec(`
		DELETE FROM series_state
		WHERE (tmdb_id = '' OR tmdb_id = '0')
		  AND title != '' AND year > 0
		  AND series_id NOT IN (
			  SELECT MAX(series_id)
			  FROM series_state
			  WHERE (tmdb_id = '' OR tmdb_id = '0')
			    AND title != '' AND year > 0
			  GROUP BY title, year
		  );
	`)
	if err != nil {
		return delMvTMDB + delMvTitle + delSrsTMDB, err
	}
	delSrsTitle, _ := resSrsTitle.RowsAffected()

	// 7. Clean up orphaned stream_variants
	_, _ = d.conn.Exec(`DELETE FROM stream_variants WHERE item_type = 'movie' AND item_id NOT IN (SELECT movie_id FROM movie_state);`)
	_, _ = d.conn.Exec(`DELETE FROM stream_variants WHERE item_type = 'series' AND item_id NOT IN (SELECT series_id FROM series_state);`)

	// 8. Clean up soft_deleted_items that match active folder names or (Title + Year) in movie_state or series_state
	_, _ = d.conn.Exec(`
		DELETE FROM soft_deleted_items
		WHERE EXISTS (
			SELECT 1 FROM movie_state ms 
			WHERE soft_deleted_items.item_path = ms.folder_name 
			   OR soft_deleted_items.item_path LIKE '%/' || ms.folder_name
			   OR (ms.title != '' AND ms.year > 0 AND soft_deleted_items.item_path LIKE '%/' || ms.title || ' (' || ms.year || ')%')
		) OR EXISTS (
			SELECT 1 FROM series_state ss 
			WHERE soft_deleted_items.item_path = ss.folder_name 
			   OR soft_deleted_items.item_path LIKE '%/' || ss.folder_name
			   OR (ss.title != '' AND ss.year > 0 AND soft_deleted_items.item_path LIKE '%/' || ss.title || ' (' || ss.year || ')%')
		);
	`)

	totalPruned := delMvTMDB + delMvTitle + delSrsTMDB + delSrsTitle
	if totalPruned > 0 {
		log.Printf("[DB] Deduplicated database: pruned %d obsolete ghost records (%d movies by TMDB, %d movies by Title, %d series by TMDB, %d series by Title).",
			totalPruned, delMvTMDB, delMvTitle, delSrsTMDB, delSrsTitle)
	}

	return totalPruned, nil
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
		folder_name = CASE WHEN movie_state.folder_name != '' THEN movie_state.folder_name ELSE EXCLUDED.folder_name END,
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

// GetMovieStreamVariants retrieves all stream variant mappings for a movie.
func (d *DB) GetMovieStreamVariants(movieID int) ([]*StreamVariant, error) {
	query := `SELECT item_id, item_type, season_num, episode_num, variant_key, target_url, quality, stream_id, container_extension FROM stream_variants WHERE item_id = ? AND item_type = 'movie'`
	rows, err := d.conn.Query(query, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variants []*StreamVariant
	for rows.Next() {
		var v StreamVariant
		if err := rows.Scan(&v.ItemID, &v.ItemType, &v.SeasonNum, &v.EpisodeNum, &v.VariantKey, &v.TargetURL, &v.Quality, &v.StreamID, &v.ContainerExtension); err != nil {
			return nil, err
		}
		variants = append(variants, &v)
	}
	return variants, rows.Err()
}

// DeleteStreamVariant removes a specific stream variant for a movie or episode from SQLite.
func (d *DB) DeleteStreamVariant(itemID int, itemType, variantKey string) error {
	query := `DELETE FROM stream_variants WHERE item_id = ? AND item_type = ? AND variant_key = ?`
	_, err := d.conn.Exec(query, itemID, itemType, variantKey)
	return err
}

// --- Soft Delete Tracking Queries ---

// SoftDeleteItem records an item path as soft deleted with the current timestamp if not already tracked.
// Returns true if newly inserted, false if already tracked in the soft delete grace period.
func (d *DB) SoftDeleteItem(itemPath, itemType string) (bool, error) {
	query := `
	INSERT INTO soft_deleted_items (item_path, item_type, soft_deleted_at)
	VALUES (?, ?, ?)
	ON CONFLICT(item_path) DO NOTHING;
	`
	res, err := d.conn.Exec(query, itemPath, itemType, time.Now())
	if err != nil {
		return false, err
	}
	ra, _ := res.RowsAffected()
	return ra > 0, nil
}

// GetSoftDeletedPathsMap returns a lookup set of all item paths and folder names currently held in soft deletion.
func (d *DB) GetSoftDeletedPathsMap() (map[string]bool, error) {
	query := `SELECT item_path FROM soft_deleted_items`
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]bool)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			res[p] = true
			res[filepath.Base(p)] = true
		}
	}
	return res, nil
}

// PurgeExpiredSoftDeletedItem removes an expired item from soft deletion and cleans up its database records.
func (d *DB) PurgeExpiredSoftDeletedItem(itemPath string) error {
	folderName := filepath.Base(itemPath)

	// Clean up movie record and stream variants if matches
	var movieID int
	if err := d.conn.QueryRow(`SELECT movie_id FROM movie_state WHERE folder_name = ?`, folderName).Scan(&movieID); err == nil && movieID > 0 {
		_ = d.DeleteMovieState(movieID)
		_ = d.DeleteAllStreamVariantsForItem(movieID, "movie")
	} else {
		_ = d.DeleteMovieStateByFolder(folderName)
	}

	// Clean up series record and stream variants if matches
	var seriesID int
	if err := d.conn.QueryRow(`SELECT series_id FROM series_state WHERE folder_name = ?`, folderName).Scan(&seriesID); err == nil && seriesID > 0 {
		_ = d.DeleteSeriesState(seriesID)
		_ = d.DeleteAllStreamVariantsForItem(seriesID, "series")
	} else {
		_ = d.DeleteSeriesStateByFolder(folderName)
	}

	return d.RemoveSoftDeletedItem(itemPath)
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

// GetPendingSoftDeletedCount returns the total number of items currently held in the retention grace period.
func (d *DB) GetPendingSoftDeletedCount() (int, error) {
	query := `SELECT COUNT(*) FROM soft_deleted_items`
	var count int
	err := d.conn.QueryRow(query).Scan(&count)
	return count, err
}

// GetStaleMovieStates returns movies that were not updated since cutoffTime (i.e. disappeared from upstream catalog).
func (d *DB) GetStaleMovieStates(cutoffTime time.Time) ([]*MovieState, error) {
	query := `SELECT movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, last_checked_at FROM movie_state WHERE last_checked_at < ?`
	rows, err := d.conn.Query(query, cutoffTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*MovieState
	for rows.Next() {
		var m MovieState
		var chkAt sql.NullTime
		if err := rows.Scan(&m.MovieID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.FolderName, &m.IsActivated, &chkAt); err != nil {
			continue
		}
		if chkAt.Valid {
			m.LastCheckedAt = &chkAt.Time
		}
		results = append(results, &m)
	}
	return results, nil
}

// GetStaleSeriesStates returns series that were not updated since cutoffTime (i.e. disappeared from upstream catalog).
func (d *DB) GetStaleSeriesStates(cutoffTime time.Time) ([]*SeriesState, error) {
	query := `SELECT series_id, tmdb_id, imdb_id, title, year, folder_name, status, is_activated, activated_at, last_episode_count, next_episode_air_date, last_modified, last_checked_at FROM series_state WHERE last_checked_at < ?`
	rows, err := d.conn.Query(query, cutoffTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SeriesState
	for rows.Next() {
		var s SeriesState
		var actAt, chkAt sql.NullTime
		if err := rows.Scan(&s.SeriesID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.FolderName, &s.Status, &s.IsActivated, &actAt, &s.LastEpisodeCount, &s.NextEpisodeAirDate, &s.LastModified, &chkAt); err != nil {
			continue
		}
		if actAt.Valid {
			s.ActivatedAt = &actAt.Time
		}
		if chkAt.Valid {
			s.LastCheckedAt = &chkAt.Time
		}
		results = append(results, &s)
	}
	return results, nil
}

// DeleteMovieState permanently deletes a movie record from SQLite.
func (d *DB) DeleteMovieState(movieID int) error {
	_, err := d.conn.Exec(`DELETE FROM movie_state WHERE movie_id = ?`, movieID)
	return err
}

// DeleteMovieStateByFolder permanently deletes a movie record matching folder_name.
func (d *DB) DeleteMovieStateByFolder(folderName string) error {
	_, err := d.conn.Exec(`DELETE FROM movie_state WHERE folder_name = ?`, folderName)
	return err
}

// DeleteSeriesState permanently deletes a series record from SQLite.
func (d *DB) DeleteSeriesState(seriesID int) error {
	_, err := d.conn.Exec(`DELETE FROM series_state WHERE series_id = ?`, seriesID)
	return err
}

// DeleteSeriesStateByFolder permanently deletes a series record matching folder_name.
func (d *DB) DeleteSeriesStateByFolder(folderName string) error {
	_, err := d.conn.Exec(`DELETE FROM series_state WHERE folder_name = ?`, folderName)
	return err
}

// DeleteAllStreamVariantsForItem removes all stream variants associated with an item.
func (d *DB) DeleteAllStreamVariantsForItem(itemID int, itemType string) error {
	_, err := d.conn.Exec(`DELETE FROM stream_variants WHERE item_id = ? AND item_type = ?`, itemID, itemType)
	return err
}

// --- Sync Queue Queries ---

// EnqueueTask adds or updates a task in the sync queue. If task already exists, it boosts priority if higher and resets to pending.
func (d *DB) EnqueueTask(itemType string, itemID int, action string, priority int) error {
	now := time.Now()
	query := `
	INSERT INTO sync_queue (item_type, item_id, action, priority, status, attempts, created_at, updated_at)
	VALUES (?, ?, ?, ?, 'pending', 0, ?, ?)
	ON CONFLICT(item_type, item_id, action) DO UPDATE SET
		priority = CASE WHEN EXCLUDED.priority > sync_queue.priority THEN EXCLUDED.priority ELSE sync_queue.priority END,
		status = CASE WHEN sync_queue.status = 'completed' THEN 'pending' ELSE sync_queue.status END,
		updated_at = EXCLUDED.updated_at;
	`
	_, err := d.conn.Exec(query, itemType, itemID, action, priority, now, now)
	return err
}

// PopNextTask atomically retrieves and marks in-progress the next pending task.
// If there are high-priority tasks (VIP >= 50), they are returned first.
// Otherwise, it favors preferredType to support balanced round-robin between movies and series.
func (d *DB) PopNextTask(preferredType string) (*models.QueueItem, error) {
	now := time.Now()

	// 1. First check for any high-priority tasks (VIP user activations >= 50)
	highPriorityQuery := `
	SELECT id, item_type, item_id, action, priority, status, attempts, last_error, created_at, updated_at
	FROM sync_queue
	WHERE status = 'pending' AND priority >= 50
	ORDER BY priority DESC, id ASC
	LIMIT 1
	`
	item, err := d.scanQueueRow(d.conn.QueryRow(highPriorityQuery))
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	// 2. If no VIP task, try preferred item type (round-robin)
	if item == nil && preferredType != "" {
		roundRobinQuery := `
		SELECT id, item_type, item_id, action, priority, status, attempts, last_error, created_at, updated_at
		FROM sync_queue
		WHERE status = 'pending' AND item_type = ?
		ORDER BY priority DESC, id ASC
		LIMIT 1
		`
		item, err = d.scanQueueRow(d.conn.QueryRow(roundRobinQuery, preferredType))
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	}

	// 3. Fallback to any pending task
	if item == nil {
		fallbackQuery := `
		SELECT id, item_type, item_id, action, priority, status, attempts, last_error, created_at, updated_at
		FROM sync_queue
		WHERE status = 'pending'
		ORDER BY priority DESC, id ASC
		LIMIT 1
		`
		item, err = d.scanQueueRow(d.conn.QueryRow(fallbackQuery))
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
	}

	if item == nil {
		return nil, nil
	}

	// Atomically mark in_progress
	updateQuery := `UPDATE sync_queue SET status = 'in_progress', updated_at = ? WHERE id = ? AND status = 'pending'`
	res, uErr := d.conn.Exec(updateQuery, now, item.ID)
	if uErr != nil {
		return nil, uErr
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// Another worker claimed this task in the microsecond interval, retry
		return d.PopNextTask(preferredType)
	}

	item.Status = "in_progress"
	item.UpdatedAt = now
	return item, nil
}

func (d *DB) scanQueueRow(row *sql.Row) (*models.QueueItem, error) {
	var item models.QueueItem
	var lastErr sql.NullString
	err := row.Scan(
		&item.ID,
		&item.ItemType,
		&item.ItemID,
		&item.Action,
		&item.Priority,
		&item.Status,
		&item.Attempts,
		&lastErr,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastErr.Valid {
		item.LastError = lastErr.String
	}
	return &item, nil
}

// MarkTaskComplete removes a completed task from the sync queue.
func (d *DB) MarkTaskComplete(taskID int) error {
	query := `DELETE FROM sync_queue WHERE id = ?`
	_, err := d.conn.Exec(query, taskID)
	return err
}

// MarkTaskFailed records failure on a task, increments attempts, and marks status as failed.
func (d *DB) MarkTaskFailed(taskID int, errMsg string) error {
	query := `UPDATE sync_queue SET status = 'failed', attempts = attempts + 1, last_error = ?, updated_at = ? WHERE id = ?`
	_, err := d.conn.Exec(query, errMsg, time.Now(), taskID)
	return err
}

// ResetInProgressTasks resets any hanging in_progress tasks back to pending (e.g. on daemon startup).
func (d *DB) ResetInProgressTasks() error {
	query := `UPDATE sync_queue SET status = 'pending', updated_at = ? WHERE status = 'in_progress'`
	_, err := d.conn.Exec(query, time.Now())
	return err
}

// QueueStats represents queue status and breakdown by media type and task action type.
type QueueStats struct {
	Pending           int
	InProgress        int
	Failed            int
	MovieAdds         int // movie + pass1
	MovieReconciles   int // movie + reconcile_movie
	MovieActivations  int // movie + activate
	MovieDeletes      int // movie + delete
	SeriesAdds        int // series + pass1
	SeriesUpdates     int // series + update_episodes
	SeriesReconciles  int // series + reconcile_series
	SeriesActivations int // series + activate
	SeriesDeletes     int // series + delete
}

func (q QueueStats) TotalMovies() int {
	return q.MovieAdds + q.MovieReconciles + q.MovieActivations + q.MovieDeletes
}

func (q QueueStats) TotalSeries() int {
	return q.SeriesAdds + q.SeriesUpdates + q.SeriesReconciles + q.SeriesActivations + q.SeriesDeletes
}

// GetQueueStats returns the count of pending, in_progress, and failed tasks in the queue.
func (d *DB) GetQueueStats() (pending, inProgress, failed int, err error) {
	stats, err := d.GetDetailedQueueStats()
	if err != nil {
		return 0, 0, 0, err
	}
	return stats.Pending, stats.InProgress, stats.Failed, nil
}

// GetDetailedQueueStats returns full queue counts and breakdown by media type and action type.
func (d *DB) GetDetailedQueueStats() (QueueStats, error) {
	query := `
	SELECT 
		SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'in_progress' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'movie' AND action = 'pass1' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'movie' AND action LIKE 'reconcile%' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'movie' AND action = 'activate' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'movie' AND action = 'delete' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'series' AND action = 'pass1' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'series' AND action = 'update_episodes' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'series' AND action LIKE 'reconcile%' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'series' AND action = 'activate' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END),
		SUM(CASE WHEN item_type = 'series' AND action = 'delete' AND status IN ('pending', 'in_progress') THEN 1 ELSE 0 END)
	FROM sync_queue
	`
	var p, ip, f sql.NullInt64
	var mAdds, mRec, mAct, mDel sql.NullInt64
	var sAdds, sUpd, sRec, sAct, sDel sql.NullInt64
	err := d.conn.QueryRow(query).Scan(&p, &ip, &f, &mAdds, &mRec, &mAct, &mDel, &sAdds, &sUpd, &sRec, &sAct, &sDel)
	if err != nil {
		return QueueStats{}, err
	}
	var stats QueueStats
	if p.Valid {
		stats.Pending = int(p.Int64)
	}
	if ip.Valid {
		stats.InProgress = int(ip.Int64)
	}
	if f.Valid {
		stats.Failed = int(f.Int64)
	}
	if mAdds.Valid {
		stats.MovieAdds = int(mAdds.Int64)
	}
	if mRec.Valid {
		stats.MovieReconciles = int(mRec.Int64)
	}
	if mAct.Valid {
		stats.MovieActivations = int(mAct.Int64)
	}
	if mDel.Valid {
		stats.MovieDeletes = int(mDel.Int64)
	}
	if sAdds.Valid {
		stats.SeriesAdds = int(sAdds.Int64)
	}
	if sUpd.Valid {
		stats.SeriesUpdates = int(sUpd.Int64)
	}
	if sRec.Valid {
		stats.SeriesReconciles = int(sRec.Int64)
	}
	if sAct.Valid {
		stats.SeriesActivations = int(sAct.Int64)
	}
	if sDel.Valid {
		stats.SeriesDeletes = int(sDel.Int64)
	}
	return stats, nil
}

// GetRecentQueueTasks returns the most recent queue tasks (filtered by status if provided).
func (d *DB) GetRecentQueueTasks(limit int, statusFilter string) ([]models.QueueItem, error) {
	if limit <= 0 {
		limit = 50
	}
	var query string
	var rows *sql.Rows
	var err error
	if statusFilter != "" {
		query = `SELECT id, item_type, item_id, action, priority, status, attempts, last_error, created_at, updated_at FROM sync_queue WHERE status = ? ORDER BY updated_at DESC LIMIT ?`
		rows, err = d.conn.Query(query, statusFilter, limit)
	} else {
		query = `SELECT id, item_type, item_id, action, priority, status, attempts, last_error, created_at, updated_at FROM sync_queue ORDER BY updated_at DESC LIMIT ?`
		rows, err = d.conn.Query(query, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.QueueItem
	for rows.Next() {
		var item models.QueueItem
		var lastErr sql.NullString
		if err := rows.Scan(&item.ID, &item.ItemType, &item.ItemID, &item.Action, &item.Priority, &item.Status, &item.Attempts, &lastErr, &item.CreatedAt, &item.UpdatedAt); err == nil {
			if lastErr.Valid {
				item.LastError = lastErr.String
			}
			items = append(items, item)
		}
	}
	return items, nil
}

// BatchStageMovies stages an entire page of movies in a single atomic SQLite transaction.
// It updates movie metadata in movie_state and only enqueues new Pass 1 creation tasks for items not yet in movie_state.
func (d *DB) BatchStageMovies(items []models.Movie) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	queueStmt, err := tx.Prepare(`
	INSERT INTO sync_queue (item_type, item_id, action, priority, status, attempts, created_at, updated_at)
	SELECT 'movie', ?, 'pass1', 10, 'pending', 0, ?, ?
	WHERE NOT EXISTS (
		SELECT 1 FROM movie_state 
		WHERE movie_id = ? 
		   OR (? != '' AND ? != '0' AND tmdb_id = ?)
		   OR (? != '' AND ? > 0 AND title = ? AND year = ?)
	);
	`)
	if err != nil {
		return err
	}
	defer queueStmt.Close()

	stateStmt, err := tx.Prepare(`
	INSERT INTO movie_state (movie_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at)
	VALUES (?, ?, ?, ?, ?, ?, 
		COALESCE((SELECT is_activated FROM movie_state WHERE tmdb_id = ? AND ? != '' AND ? != '0' ORDER BY is_activated DESC LIMIT 1), 0),
		(SELECT activated_at FROM movie_state WHERE tmdb_id = ? AND ? != '' AND ? != '0' AND is_activated = 1 ORDER BY activated_at ASC LIMIT 1),
		?
	)
	ON CONFLICT(movie_id) DO UPDATE SET
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE movie_state.tmdb_id END,
		imdb_id = CASE WHEN EXCLUDED.imdb_id != '' THEN EXCLUDED.imdb_id ELSE movie_state.imdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE movie_state.title END,
		year = CASE WHEN EXCLUDED.year > 0 THEN EXCLUDED.year ELSE movie_state.year END,
		folder_name = CASE WHEN movie_state.folder_name != '' THEN movie_state.folder_name ELSE EXCLUDED.folder_name END,
		is_activated = CASE WHEN EXCLUDED.is_activated = 1 THEN 1 ELSE movie_state.is_activated END,
		activated_at = CASE WHEN EXCLUDED.activated_at IS NOT NULL THEN EXCLUDED.activated_at ELSE movie_state.activated_at END,
		last_checked_at = EXCLUDED.last_checked_at;
	`)
	if err != nil {
		return err
	}
	defer stateStmt.Close()

	supersedeMvStmt, err := tx.Prepare(`
	DELETE FROM movie_state
	WHERE movie_id != ? AND (
		(? != '' AND ? != '0' AND tmdb_id = ?)
		OR ((? = '' OR ? = '0') AND ? != '' AND ? > 0 AND title = ? AND year = ?)
	);
	`)
	if err != nil {
		return err
	}
	defer supersedeMvStmt.Close()

	transferMvVariantsStmt, err := tx.Prepare(`
	UPDATE OR IGNORE stream_variants
	SET item_id = ?
	WHERE item_type = 'movie' AND item_id = ?;
	`)
	if err != nil {
		return err
	}
	defer transferMvVariantsStmt.Close()

	delSoftStmt, err := tx.Prepare(`
	DELETE FROM soft_deleted_items WHERE item_path = ? OR item_path LIKE '%/' || ?;
	`)
	if err != nil {
		return err
	}
	defer delSoftStmt.Close()

	for _, mv := range items {
		cleanTitle := sanitizer.CleanTitle(mv.Name)
		folderName := sanitizer.BuildMovieFolderName(cleanTitle, mv.Year, mv.TMDBID, mv.IMDBID)

		if _, err := queueStmt.Exec(mv.ID, now, now, mv.ID, mv.TMDBID, mv.TMDBID, mv.TMDBID, cleanTitle, mv.Year, cleanTitle, mv.Year); err != nil {
			return err
		}
		if _, err := stateStmt.Exec(mv.ID, mv.TMDBID, mv.IMDBID, cleanTitle, mv.Year, folderName, mv.TMDBID, mv.TMDBID, mv.TMDBID, mv.TMDBID, mv.TMDBID, mv.TMDBID, now); err != nil {
			return err
		}
		// Prune older superseded movie_state rows for this exact same movie
		_, _ = supersedeMvStmt.Exec(mv.ID, mv.TMDBID, mv.TMDBID, mv.TMDBID, mv.TMDBID, mv.TMDBID, cleanTitle, mv.Year, cleanTitle, mv.Year)
		_, _ = delSoftStmt.Exec(folderName, folderName)
	}

	return tx.Commit()
}

// BatchStageSeries stages an entire page of TV series in a single atomic SQLite transaction.
// It updates series metadata in series_state and only enqueues new Pass 1 creation tasks for items not yet in series_state.
func (d *DB) BatchStageSeries(items []models.Series) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	queueStmt, err := tx.Prepare(`
	INSERT INTO sync_queue (item_type, item_id, action, priority, status, attempts, created_at, updated_at)
	SELECT 'series', ?, 'pass1', 10, 'pending', 0, ?, ?
	WHERE NOT EXISTS (
		SELECT 1 FROM series_state 
		WHERE series_id = ? 
		   OR (? != '' AND ? != '0' AND tmdb_id = ?)
		   OR (? != '' AND ? > 0 AND title = ? AND year = ?)
	);
	`)
	if err != nil {
		return err
	}
	defer queueStmt.Close()

	stateStmt, err := tx.Prepare(`
	INSERT INTO series_state (series_id, tmdb_id, imdb_id, title, year, folder_name, is_activated, activated_at, last_checked_at)
	VALUES (?, ?, ?, ?, ?, ?, 
		COALESCE((SELECT is_activated FROM series_state WHERE tmdb_id = ? AND ? != '' AND ? != '0' ORDER BY is_activated DESC LIMIT 1), 0),
		(SELECT activated_at FROM series_state WHERE tmdb_id = ? AND ? != '' AND ? != '0' AND is_activated = 1 ORDER BY activated_at ASC LIMIT 1),
		?
	)
	ON CONFLICT(series_id) DO UPDATE SET
		tmdb_id = CASE WHEN EXCLUDED.tmdb_id != '' THEN EXCLUDED.tmdb_id ELSE series_state.tmdb_id END,
		imdb_id = CASE WHEN EXCLUDED.imdb_id != '' THEN EXCLUDED.imdb_id ELSE series_state.imdb_id END,
		title = CASE WHEN EXCLUDED.title != '' THEN EXCLUDED.title ELSE series_state.title END,
		year = CASE WHEN EXCLUDED.year > 0 THEN EXCLUDED.year ELSE series_state.year END,
		folder_name = CASE WHEN series_state.folder_name != '' THEN series_state.folder_name ELSE EXCLUDED.folder_name END,
		is_activated = CASE WHEN EXCLUDED.is_activated = 1 THEN 1 ELSE series_state.is_activated END,
		activated_at = CASE WHEN EXCLUDED.activated_at IS NOT NULL THEN EXCLUDED.activated_at ELSE series_state.activated_at END,
		last_checked_at = EXCLUDED.last_checked_at;
	`)
	if err != nil {
		return err
	}
	defer stateStmt.Close()

	supersedeSrsStmt, err := tx.Prepare(`
	DELETE FROM series_state
	WHERE series_id != ? AND (
		(? != '' AND ? != '0' AND tmdb_id = ?)
		OR ((? = '' OR ? = '0') AND ? != '' AND ? > 0 AND title = ? AND year = ?)
	);
	`)
	if err != nil {
		return err
	}
	defer supersedeSrsStmt.Close()

	delSoftStmt, err := tx.Prepare(`
	DELETE FROM soft_deleted_items WHERE item_path = ? OR item_path LIKE '%/' || ?;
	`)
	if err != nil {
		return err
	}
	defer delSoftStmt.Close()

	for _, srs := range items {
		cleanTitle := sanitizer.CleanTitle(srs.Name)
		folderName := sanitizer.BuildMovieFolderName(cleanTitle, srs.Year, srs.TMDBID, srs.IMDBID)

		if _, err := queueStmt.Exec(srs.ID, now, now, srs.ID, srs.TMDBID, srs.TMDBID, srs.TMDBID, cleanTitle, srs.Year, cleanTitle, srs.Year); err != nil {
			return err
		}
		if _, err := stateStmt.Exec(srs.ID, srs.TMDBID, srs.IMDBID, cleanTitle, srs.Year, folderName, srs.TMDBID, srs.TMDBID, srs.TMDBID, srs.TMDBID, srs.TMDBID, srs.TMDBID, now); err != nil {
			return err
		}
		// Prune older superseded series_state rows for this exact same series
		_, _ = supersedeSrsStmt.Exec(srs.ID, srs.TMDBID, srs.TMDBID, srs.TMDBID, srs.TMDBID, srs.TMDBID, cleanTitle, srs.Year, cleanTitle, srs.Year)
		_, _ = delSoftStmt.Exec(folderName, folderName)
	}

	return tx.Commit()
}

// UpdateMovieFolderName updates the folder name recorded for a movie when the reconciler migrates its directory on disk.
func (d *DB) UpdateMovieFolderName(movieID int, folderName string) error {
	_, err := d.conn.Exec(`UPDATE movie_state SET folder_name = ? WHERE movie_id = ?`, folderName, movieID)
	return err
}

// UpdateSeriesFolderName updates the folder name recorded for a series when the reconciler migrates its directory on disk.
func (d *DB) UpdateSeriesFolderName(seriesID int, folderName string) error {
	_, err := d.conn.Exec(`UPDATE series_state SET folder_name = ? WHERE series_id = ?`, folderName, seriesID)
	return err
}
