# dp2strm Master Feature Roadmap & Progress Checklist

This document tracks all completed features and remaining roadmap items for `dp2strm`.

---

## 🟩 Implemented & Verified Features (100% PASS)

- [x] **Dispatcharr REST API Client (`pkg/client`)**: Direct authentication via `X-API-Key` header for `/api/vod/movies/` and `/api/vod/series/`.
- [x] **Strict TMDB Primary Key Boundary**: Directories strictly named `Movies/Title (Year) [tmdbid=XXXXX]/`. Ensures different movies with matching titles (*Becoming 2020*) stay in separate folders.
- [x] **Emby Version Hierarchy Naming**: 1080p Base default stream = `Movie.strm`; 4K secondary stream = `Movie - 4K.strm` (*The Thing 1982*).
- [x] **4-Tier Quality Classification Waterfall**: Category ➜ Title ➜ Probe ➜ Fallback (0 network calls for category-tagged 4K content).
- [x] **Circuit Breaker & Safe Fallback**: Protects against 429 rate limits; self-heals on subsequent sync runs.
- [x] **Credential-Free STRM File Generator & `mtime` Preservation**: Writes `.strm` files and preserves timestamps via `os.Chtimes` to avoid Emby library re-scans.
- [x] **Metadata NFO XML Generator (`pkg/nfo`)**: Creates `movie.nfo` and `tvshow.nfo` with `mtime` timestamp preservation.
- [x] **Artwork Downloader (`pkg/artwork`)**: Downloads `poster.jpg` and `fanart.jpg` into `.strm` item folders and tracks `?v=<hash>` query parameters to auto-update artwork when changed.
- [x] **Semantic TV Series URL Scheme**: `/play/series/<series_id>/<season>/<episode>.<ext>` format.
- [x] **HTTP Client Playback Handler (`pkg/proxy`)**: Serves instant HTTP `302 Found` redirects.
- [x] **TV Series Activation Episode (`S00E00`)**: Creates `Season 00/Show Name - S00E00 - Pre-Fetch Cache Activation.strm` and fires async `S01E01` pre-fetch.
- [x] **Cloud-Native Environment Variable Mode**: 100% configurable via Docker env vars without requiring `config.yaml`.
- [x] **Docker Packaging (`Dockerfile` & `docker-compose.yml`)**: Multi-stage Docker build + well-commented `docker-compose.yml`.
- [x] **Unit Test Suite (`100% PASS`)**: Sanitizer, versioning, STRM `mtime`, NFO updates, artwork hash tracking, configless env vars, and semantic URLs.

---

## 🨨 Queued / Roadmap Features (Agreed Architecture Specifications)

- [ ] **Primary Stream Defaulting for `streamcache` Deduplication**:
  - **Rule**: The base `.strm` file (`Movie.strm` / `S01E01.strm`) ALWAYS points to the primary standardized stream.
  - **Why**: 95%+ of users click "Play" without opening the Version dropdown menu. Forcing all base files to hit the primary stream URL maximizes shared `streamcache` hit ratios and prevents spinning up duplicate upstream provider streams.
  - **Secondary Versions**: Alternative 4K streams (`Movie - 4K.strm`) or backup provider streams (`Movie - 1080p (Backup).strm`) are written as secondary files for manual user selection in Emby's Version dropdown menu.

- [ ] **On-Demand TV Series Activation & Clean-Up Lifecycle (`S01E01 - Play to Activate Series.strm`)**:
  - **Pass 1 (Lightweight Initial Sync)**: For each TV show, generate `tvshow.nfo`, `poster.jpg`, `fanart.jpg`, and a single placeholder activation episode in `Season 01/`: `Show Name - S01E01 - Play to Activate Series.strm` and matching `.nfo` instructing the user to play the episode to activate full series content in Emby.
  - **Pass 2 (On-Demand Activation & Population)**: When played:
    1. Fetch full season/episode list for `series_id` from Dispatcharr (`/api/vod/series/{id}/episodes/`).
    2. Populate real `.strm` files on disk for all seasons/episodes (`S01E01`, `S01E02`, etc.).
    3. Automatically delete temporary placeholder files (`Show Name - S01E01 - Play to Activate Series.strm` & `.nfo`).
    4. Issue targeted Emby library refresh (`POST /Items/{series_id}/Refresh?Recursive=true`).
    5. Issue HTTP `302 Found` redirect directly to the real `S01E01` stream URL so video playback starts instantly.

- [ ] **Smart Series Update Scheduling Engine (`modernc.org/sqlite`)**:
  - **Pure-Go CGO-Free SQLite Database**: Store state in `/data/strm/dp2strm.db` using `modernc.org/sqlite` (100% CGO-free, single static Alpine binary compatible).
  - **Activated Series Tracking**: Track which TV series are activated by users (`is_activated = 1`). Unactivated series skip fetching episode lists on scheduled sync runs (0 API calls!).
  - **Ended vs Continuing Series**:
    - `Ended` series: One-shot activation and population on disk. Marked `status = 'Ended'` and permanently skipped on future sync runs.
    - `Continuing` series: Record `next_episode_air_date` from Dispatcharr/TMDB. Skip scheduled sync checks for this series until `current_date >= next_episode_air_date`.
  - **`last_modified` Timestamp Check**: Check Dispatcharr's `last_modified` series timestamp. If unchanged, skip processing (`0 disk edits`, `0 Emby rescans`).

- [ ] **Predictive Fetch-Ahead Engine ("Tap & Bail" for Next X Episodes)**:
  - When a user plays `S01E01`, spawn an async background worker (500ms HTTP GET timeout) to pre-fetch upcoming episodes (`S01E02`, `S01E03` based on `FETCH_AHEAD_COUNT: 2`) to hydrate `streamcache`.

- [ ] **Retention Grace Period & Cleanup Manager (`RETENTION_GRACE_DAYS`)**:
  - Soft-delete state tracker map in SQLite that buffers deleted streams for 7 days before removing `.strm` files from disk when content disappears from Dispatcharr.

- [ ] **Conditional Emby Refresh Trigger Rules**:
  - **Rule**: Trigger Emby library refresh (`POST {emby_url}/Library/Refresh`) **ONLY** if `createdCount > 0` or `deletedCount > 0`.
  - **URL Edits & Unchanged Files**: If a sync run only updates text inside existing `.strm` files or preserves timestamps via `os.Chtimes` (`createdCount == 0 && deletedCount == 0`), `dp2strm` **skips calling Emby**.

- [ ] **Cron Expression & Timezone Scheduler (`SYNC_CRON` & `TZ`)**:
  - **Cron Syntax**: Support standard 5-field cron syntax (`SYNC_CRON="0 3 * * *"` for 3:00 AM daily sync).
  - **Timezone Awareness**: Parse `TZ` environment variable (e.g. `TZ=America/New_York`) using Go's `import _ "time/tzdata"` to evaluate execution times in the user's local timezone with automatic Daylight Saving Time (DST) handling.
  - **Priority**: `SYNC_CRON` ➜ `SYNC_INTERVAL` ➜ `SYNC_INTERVAL=0` / `--sync-once`.

- [ ] **`streamcache` Base64 URL Path Rewrite Rules**:
  - **Rule**: When `STREAMCACHE_URL` is configured (e.g. `http://streamcache:8080`), `dp2strm` rewrites direct Dispatcharr content URLs into URL-safe Base64 encoded paths:
    `<STREAMCACHE_URL>/u/<base64_url>`
  - **Example**: `http://streamcache:8080/u/aHR0cHM6Ly9kcC5leGFtcGxlLmNvbS9tb3ZpZS9hZ3kvYWd5cGFzc3dvcmQvMTA1ODkwMi5ta3Y=`
  - **Why**: Hides plain-text subscriber credentials from `.strm` files on disk and media player logs while providing a clean, character-safe URL path.
