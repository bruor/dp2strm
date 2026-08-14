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

- [ ] **On-Demand TV Series Activation & Clean-Up Lifecycle (`S01E01 - Play to Activate Series.strm`)**:
  - **Pass 1 (Lightweight Initial Sync)**: For each TV show, generate `tvshow.nfo`, `poster.jpg`, `fanart.jpg`, and a single placeholder activation episode in `Season 01/`: `Show Name - S01E01 - Play to Activate Series.strm` and matching `.nfo` instructing the user to play the episode to activate full series content in Emby.
  - **Pass 2 (On-Demand Activation & Population)**: When played:
    1. Fetch full season/episode list for `series_id` from Dispatcharr (`/api/vod/series/{id}/episodes/`).
    2. Populate real `.strm` files on disk for all seasons/episodes (`S01E01`, `S01E02`, etc.).
    3. Automatically delete temporary placeholder files (`Show Name - S01E01 - Play to Activate Series.strm` & `.nfo`).
    4. Issue targeted Emby library refresh (`POST /Items/{series_id}/Refresh?Recursive=true`).
    5. Issue HTTP `302 Found` redirect directly to the real `S01E01` stream URL so video playback starts instantly.
- [ ] **Predictive Fetch-Ahead Engine ("Tap & Bail" for Next X Episodes)**: When playing episode `S01E01`, fire an async goroutine (500ms timeout) to pre-fetch upcoming episodes (`S01E02`, `S01E03` based on `FETCH_AHEAD_COUNT: 2`) to hydrate `streamcache`.
- [ ] **Retention Grace Period & Cleanup Manager (`RETENTION_GRACE_DAYS`)**: Soft-delete state tracker map (`.dp2strm_state.json`) that buffers deleted streams for 7 days before removing `.strm` files from disk when content disappears from Dispatcharr.
- [ ] **Category Filtering (`INCLUDE_CATEGORIES` / `EXCLUDE_CATEGORIES`)**: Filter out unwanted VOD/Series categories (e.g. adult content or unneeded provider groups).
- [ ] **Targeted Item Refresh Hook for Emby API**: Trigger `POST {emby_url}/Items/{series_id}/Refresh?Recursive=true` when activation episode is played.
- [ ] **`streamcache` Transparent URL Rewrite Rules**: Rewrite 302 redirect targets from Dispatcharr URLs to `STREAMCACHE_URL` endpoints when `STREAMCACHE_URL` is configured.
