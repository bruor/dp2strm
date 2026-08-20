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
- [x] **Configurable Metadata NFO XML Generator (`pkg/nfo`)**: Creates `movie.nfo` and `tvshow.nfo` with `mtime` timestamp preservation when `ENABLE_NFO=true` (disabled by default so Emby scrapes TMDB/TVDB directly). Unactivated `S00E99.nfo` instructions are always created in Pass 1, then purged on activation.
- [x] **Artwork Downloader (`pkg/artwork`)**: Downloads `poster.jpg` and `fanart.jpg` into `.strm` item folders and tracks `?v=<hash>` query parameters to auto-update artwork when changed.
- [x] **`dp2strm` Playback Handler & Clean Extensionless URL Architecture (`pkg/proxy`)**:
  - Serves sub-50ms HTTP `302 Found` redirects.
  - Movie URLs: `/play/movie/{movie_id}` (or `/play/movie/{movie_id}/4k`)
  - Completely obfuscates upstream subscriber credentials (`agy/agypassword`) from disk files and Emby library.
- [x] **Specials-Based TV Series Activation & Video Bumper Architecture (`pkg/proxy`, `pkg/syncer`, `pkg/assets`)**:
  - **Pass 1**: Generates posters, and `Specials/Show Name - S00E99 - Activate Series.strm` & `S00E99.nfo` (`⚡ Play to Activate Series` instructions).
  - **Pass 2 (On Play)**: Immediate stream of embedded 5-second **FastStart MP4 video bumper (`activated.mp4`)**, while an async background worker halts any active background global Emby scans (`DELETE /ScheduledTasks/Running/{Id}`), writes Season 1 episodes, and issues a targeted single-item Emby refresh (`POST /Items/{SeriesId}/Refresh`). **`S01E01` remains 100% unwatched** in the user's library history!
- [x] **Universal TaskManager Batch Synchronization & Dynamic ETA (`pkg/emby`, `pkg/syncer`)**:
  - Micro-batches (`EMBY_BATCH_SIZE`, default: 25) write directly to disk, trigger Emby's `Scan media library` scheduled task (`POST /Library/Refresh`), and poll `GET /ScheduledTasks` once every 1s until Emby reaches **100% Idle** before releasing the next batch.
  - Replaced legacy per-item sleeps with zero-contention TaskManager synchronization.
  - Real-time overall queue progress logging (`Processed/Total [%]`, remaining items, and estimated batches remaining).
  - Profiling of Emby library scan durations with dynamic ETA calculation based on rolling average refresh times.
- [x] **Unified Multi-Tier Priority Queue Engine (`pkg/db`, `pkg/syncer`)**:
  - SQLite `sync_queue` ordered by `priority DESC, id ASC`.
  - `PriorityVIP (100)`: On-demand user playback activations (immediate preemption & Emby background scan cancellation).
  - `PriorityReconcile (50)`: Daily reconciler disk upgrades & maintenance.
  - `PriorityAiring (30)`: Active series episode drops & background season chunks.
  - `PriorityBatch (10)`: Catalog ingestion backlog.
- [x] **Direct SQLite State Daily Reconciler (`pkg/db`, `pkg/syncer`)**:
  - Reconciler queries `GetUnactivatedSeries()` directly from SQLite (`WHERE is_activated = 0`), discovering legacy `Season 01` folders or missing specials across thousands of shows in $<1$s and enqueuing them at `PriorityReconcile`.
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

