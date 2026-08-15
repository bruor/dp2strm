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
- [x] **Configurable Metadata NFO XML Generator (`pkg/nfo`)**: Creates `movie.nfo` and `tvshow.nfo` with `mtime` timestamp preservation when `ENABLE_NFO=true` (disabled by default so Emby scrapes TMDB/TVDB directly). Unactivated `S01E01.nfo` is created in Pass 1, then purged and touched on activation.
- [x] **Artwork Downloader (`pkg/artwork`)**: Downloads `poster.jpg` and `fanart.jpg` into `.strm` item folders and tracks `?v=<hash>` query parameters to auto-update artwork when changed.
- [x] **`dp2strm` Playback Handler & Clean Extensionless URL Architecture (`pkg/proxy`)**:
  - Serves sub-50ms HTTP `302 Found` redirects.
  - Movie URLs: `/play/movie/{movie_id}` (or `/play/movie/{movie_id}/4k`)
  - Series URLs: `/play/series/{series_id}/{season}/{episode}`
  - Completely obfuscates upstream subscriber credentials (`agy/agypassword`) from disk files and Emby library.
- [x] **On-Demand TV Series Activation & Clean-Up Lifecycle**:
  - **Pass 1**: Generates posters, and `Season 01/Show Name - S01E01.strm` & `S01E01.nfo` ("Play to Activate Series").
  - **Pass 2 (On Play)**: Immediate (<50ms) 302 redirect to stream, while an async worker populates all remaining episode `.strm` files, purges `S01E01.nfo` (when NFO disabled), and touches `S01E01.strm` to trigger Emby metadata lookup.
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

- [x] **Staged-Queue & Worker Pipeline Architecture**:
  - **Catalog Stager (`DISPATCHARR_WORKERS`)**: Fast, lightweight catalog staging on a timer (default: `4h`), populating the SQLite `sync_queue` without blocking on disk I/O or Emby scans.
  - **Paced Emby Processing Workers (`EMBY_WORKERS`)**: Workers process queue items one-by-one with **Round-Robin balancing (1 Movie ⇄ 1 TV Show)** and configurable pacing delay (`EMBY_PACE_DELAY=100ms`) to protect Emby from CPU overload.
  - **⭐ VIP On-Demand User Playback (Priority 100)**: When a user presses play on an unactivated show, it jumps directly to the front of the queue; the currently running item wraps up cleanly, and the requested show is activated immediately with `<50ms` instant 302 playback redirection.
- [x] **Zero-Config Emby Library Auto-Discovery (`pkg/emby`)**:
  - Automatically queries Emby's VirtualFolders API on startup to detect TV and Movies library IDs, mount locations, and `RealtimeMonitor` status.
  - Supports multi-library environments with optional name targeting (`EMBY_TV_LIBRARY`, `EMBY_MOVIES_LIBRARY`).
- [x] **Targeted Online Provider ID Refresh (`POST /Items/{Id}/Refresh`)**:
  - Refreshes series and movies directly via TMDB/IMDb IDs (`AnyProviderIdEquals=tmdb.XXXXX`), rescanning only that specific show's folder in `<1s` without running global library scans.
- [x] **Multi-Provider ID Support (TMDB & IMDb)**:
  - Folder naming and SQLite state engine track both TMDB and IMDb IDs (`[tmdbid=XXXXX]` or `[imdbid=ttXXXXX]`).
- [x] **Activation Loop Deduplication & Single-Flight Locking**:
  - Concurrency `sync.Map` prevents rapid back-to-back Emby probe bursts from launching duplicate activation workers.
- [x] **Season 2+ Series Handling**:
  - Gracefully detects series starting on Season 2+, purges temporary Season 01 placeholders on activation, and redirects playback to the earliest available episode.
- [x] **Daily Deep Disk Reconciler (`RECONCILE_INTERVAL`)**:
  - Configurable daily janitor (default: `24h` / `0 3 * * *`) that reconciles the on-disk `.strm` tree against SQLite state, safely pruning dead files, empty directories, and expired soft-deleted items.
- [x] **Retention Grace Period & Cleanup Manager (`RETENTION_GRACE_DAYS`)**:
  - Soft-delete state tracker table in SQLite (`soft_deleted_items`) that buffers deleted streams for 7 days before removing `.strm` files from disk when content disappears from Dispatcharr.

---

## 🨨 Queued / Future Optimization Items

- [ ] **Future Cleanup / Deprecation**:
  - Remove temporary pre-v1.0 migration sweeps for legacy `*Play to Activate Series*` filenames in `pkg/syncer/syncer.go`.

