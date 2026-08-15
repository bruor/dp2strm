# dp2strm Master Feature Roadmap & Progress Checklist

This document tracks all completed features and remaining roadmap items for `dp2strm`.

---

## 🟩 Implemented & Verified Features (100% PASS)

- [x] **Dispatcharr REST API Client (`pkg/client`)**: Direct authentication via `X-API-Key` header for `/api/vod/movies/` and `/api/vod/series/`. Supports paginated fetching (`PAGE_SIZE: 100`, `PAGE_WORKERS: 5`) and raw JSON array unwrapping.
- [x] **Strict TMDB Primary Key Boundary**: Directories strictly named `Movies/Title (Year) [tmdbid=XXXXX]/`. Ensures different movies with matching titles (*Becoming 2020*) stay in separate folders.
- [x] **Emby Multi-Version Hierarchy Naming**: 1080p Base default stream = `Movie.strm`; 4K secondary stream = `Movie - 4K.strm` (*The Thing 1982*).
- [x] **4-Tier Quality Classification Waterfall**: Category ➜ Title ➜ Probe ➜ Fallback (0 network calls for category-tagged 4K content).
- [x] **Circuit Breaker & Safe Fallback**: Halts active sync pass on HTTP 429 Too Many Requests rate limits and logs clear warnings without crashing; self-heals on subsequent runs.
- [x] **Credential-Free STRM File Generator & `mtime` Preservation**: Writes `.strm` files with extensionless URLs and preserves timestamps via `os.Chtimes` to avoid Emby library re-scans.
- [x] **Metadata NFO XML Generator (`pkg/nfo`)**: Creates `movie.nfo` and `tvshow.nfo` with `mtime` timestamp preservation. Movie NFOs are generated with 100% real metadata from Pass 1.
- [x] **Artwork Downloader (`pkg/artwork`)**: Downloads `poster.jpg` and `fanart.jpg` into `.strm` item folders and tracks `?v=<hash>` query parameters to auto-update artwork when changed.
- [x] **`dp2strm` Playback Handler & Clean Extensionless URL Architecture (`pkg/proxy`)**:
  - Serves sub-50ms HTTP `302 Found` redirects.
  - Movie URLs: `/play/movie/{movie_id}` (or `/play/movie/{movie_id}/4k`)
  - Series URLs: `/play/series/{series_id}/{season}/{episode}`
  - Completely obfuscates upstream subscriber credentials (`agy/agypassword`) from disk files and Emby library.
- [x] **On-Demand TV Series Activation & Clean-Up Lifecycle**:
  - **Pass 1**: Generates `tvshow.nfo`, posters, and `Season 01/Show Name - S01E01.strm` & `S01E01.nfo` ("Play to Activate Series").
  - **Pass 2 (On Play)**: Immediate (<50ms) 302 redirect to stream, while an async worker populates all remaining episode `.strm` files and updates `S01E01.nfo` in-place with real episode title/plot.
- [x] **On-Demand Movie Quality Activation & Multi-Version Resolution**:
  - **Pass 1**: Generates initial `Movie.strm` base file pointing to `/play/movie/{id}`.
  - **On Play**: `handleMoviePlay` resolves backing provider streams via `/api/vod/movies/{id}/providers/`, classifies Base 1080p vs 4K tiers, saves variants to SQLite (`stream_variants`), writes secondary `Movie - 4K.strm` files to disk for Emby's version dropdown, and marks movie active in `movie_state`.
  - **Subsequent Passes**: Scheduled sync passes re-verify and update all quality variant `.strm` files (`Movie.strm`, `Movie - 4K.strm`) on disk.
- [x] **Smart SQLite State Engine (`pkg/db`)**:
  - Pure-Go CGO-free SQLite (`modernc.org/sqlite`).
  - Tracks `series_state`, `movie_state`, and `stream_variants`.
  - Unactivated & `Ended` series skip fetching episode lists on scheduled sync runs (0 API calls!).
  - `Continuing` series skip checks until `current_date >= next_episode_air_date`.
- [x] **Predictive Fetch-Ahead Engine**: Pre-fetches upcoming episodes (`S01E02`, `S01E03` based on `FETCH_AHEAD_COUNT: 2`) to hydrate `streamcache`.
- [x] **`streamcache` Base64 URL Path Rewriter**: Optional integration with `streamcache` transparent proxy using URL-safe Base64 encoded query paths (`<STREAMCACHE_URL>?url=<escaped_target>`).
- [x] **Conditional Emby Refresh Trigger Rules**: Triggers Emby library refresh (`POST {emby_url}/Library/Refresh`) ONLY if files were created or deleted (`createdCount > 0`).
- [x] **Cron Expression & Timezone-Aware Scheduler**: Supports 5-field cron syntax (`SYNC_CRON`) and IANA timezones (`TZ`).
- [x] **Dynamic Shared-Pool Batching & Budget Rollover (`BATCH_SIZE`)**: Shared pool between Movies and Series with 50/50 initial split and dynamic rollover for unused allocations. Counts strictly newly created `.strm` media files.
- [x] **Server-Side Prioritized Ordering (`DISPATCHARR_ORDERING`)**: Queries `/api/vod/movies/` and `/api/vod/series/` with `?ordering=-created_at` to sync recent additions and fresh episode drops first.
- [x] **Native Dispatcharr VOD Proxy Streaming Engine**:
  - Automatically redirects playback directly to Dispatcharr's Native VOD Proxy (`/proxy/vod/movie/{uuid}` and `/proxy/vod/episode/{uuid}`).
  - Authenticates via `DISPATCHARR_API_KEY`, rendering separate XC subscriber credentials optional.
- [x] **Duplicate Year & Clean NFO Title Sanitization**:
  - Strips trailing release years from NFO `<title>` and `<originaltitle>` tags to eliminate duplicate year rendering in Emby UI.
  - Automatically collapses duplicate consecutive year tags (e.g. `(2022) (2022)` ➔ `(2022)`) across all folders and filenames.
- [x] **Category / Group / Adult Filtering Managed in Dispatcharr**: Filtering managed directly upstream in Dispatcharr.

---

## 🨨 Queued / Roadmap Features

- [ ] **Retention Grace Period & Cleanup Manager (`RETENTION_GRACE_DAYS`)**:
  - Soft-delete state tracker map in SQLite that buffers deleted streams for 7 days before removing `.strm` files from disk when content disappears from Dispatcharr.
- [ ] **Future Cleanup / Deprecation**:
  - Remove temporary pre-v1.0 migration sweeps for legacy `*Play to Activate Series*` filenames in `pkg/syncer/syncer.go`.
