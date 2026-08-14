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

## 🨨 Queued / Roadmap Features (Remaining)

- [ ] **Predictive Fetch-Ahead Engine ("Tap & Bail" for Next X Episodes)**: When playing episode `S01E01`, fire an async goroutine (500ms timeout) to pre-fetch upcoming episodes (`S01E02`, `S01E03` based on `FETCH_AHEAD_COUNT: 2`) to hydrate `streamcache`.
- [ ] **Retention Grace Period & Cleanup Manager (`RETENTION_GRACE_DAYS`)**: Soft-delete state tracker map (`.dp2strm_state.json`) that buffers deleted streams for 7 days before removing `.strm` files from disk when content disappears from Dispatcharr.
- [ ] **Category Filtering (`INCLUDE_CATEGORIES` / `EXCLUDE_CATEGORIES`)**: Filter out unwanted VOD/Series categories (e.g. adult content or unneeded provider groups).
- [ ] **Targeted Item Refresh Hook for Emby API**: Trigger `POST {emby_url}/Items/{series_id}/Refresh?Recursive=true` when dummy activation episode is played.
- [ ] **`streamcache` Transparent URL Rewrite Rules**: Rewrite 302 redirect targets from Dispatcharr URLs to `STREAMCACHE_URL` endpoints when `STREAMCACHE_URL` is configured.
