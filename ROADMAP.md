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
- [x] **Configurable Metadata NFO XML Generator (`pkg/nfo`)**: Creates `movie.nfo` and `tvshow.nfo` with `mtime` timestamp preservation when `ENABLE_NFO=true` (disabled by default so Emby scrapes TMDB/TVDB directly). Unactivated `S01E01.nfo` instructions (`<watched>false</watched>`, `<playcount>0</playcount>`, `<runtime>1</runtime>`) are always created in Pass 1, then purged on activation.
- [x] **Artwork Downloader (`pkg/artwork`)**: Downloads `poster.jpg` and `fanart.jpg` into `.strm` item folders and tracks `?v=<hash>` query parameters to auto-update artwork when changed.
- [x] **`dp2strm` Playback Handler & Clean Extensionless URL Architecture (`pkg/proxy`)**:
  - Serves sub-50ms HTTP `302 Found` redirects.
  - Movie URLs: `/play/movie/{movie_id}` (or `/play/movie/{movie_id}/4k`)
  - Completely obfuscates upstream subscriber credentials (`agy/agypassword`) from disk files and Emby library.
- [x] **On-Demand TV Series Activation & Session-Aware Refresh Lifecycle (`Season 01/S01E01`) (`pkg/proxy`, `pkg/syncer`, `pkg/emby`)**:
  - **Pass 1**: Generates posters, and `Season 01/Show Name - S01E01.strm` & `S01E01.nfo` (`⚡ Play to Activate Series` instructions). Shows cleanly as **Unplayed** in Emby and appears in **Next Up**.
  - **Pass 2 (On Play)**: Immediate **HTTP 302 Found** redirect to real episode stream (zero bumper delay!). For shows starting at Season 2+, streams embedded 5-second `activated.mp4` bumper.
  - **Session-Aware Refresh**: Verifies active playback session in Emby (`GET /Sessions`, configurable `ACTIVATION_REFRESH_DELAY=10s`), then fires targeted refresh (`POST /Items/{Id}/Refresh?ReplaceAllMetadata=true`) replacing `"⚡ Play to Activate Series"` with `"Pilot"` without interrupting playback.
- [x] **Universal TaskManager Batch Synchronization & Dynamic ETA (`pkg/emby`, `pkg/syncer`)**:
  - Micro-batches (`EMBY_BATCH_SIZE`, default: 25) write directly to disk, trigger Emby's `Scan media library` scheduled task (`POST /Library/Refresh`), and poll `GET /ScheduledTasks` once every 1s until Emby reaches **100% Idle** before releasing the next batch.
  - Replaced legacy per-item sleeps with zero-contention TaskManager synchronization.
  - Real-time overall queue progress and task action breakdown logging (`Processed/Total [%]`, remaining items & batches, broken down by Adds, Updates, Reconciles, Activations, and Deletes).
  - Profiling of Emby library scan durations with dynamic ETA calculation based on rolling average refresh times.
- [x] **Unified Multi-Tier Priority Queue Engine (`pkg/db`, `pkg/syncer`)**:
  - SQLite `sync_queue` ordered by `priority DESC, id ASC`.
  - `PriorityVIP (100)`: On-demand series playback activations (reference-counted VIP lock, background worker pause guard, and Emby global scan cancellation).
  - `PriorityReconcile (50)`: Daily reconciler disk upgrades & maintenance.
  - `PriorityAiring (30)`: Active series episode drops & background season chunks.
  - `PriorityBatch (10)`: Catalog ingestion backlog.
  - `PriorityMigration (5)`: Background legacy unactivated placeholder migrations (Specials ➔ Season 01).
- [x] **Direct SQLite State Daily Reconciler (`pkg/db`, `pkg/syncer`)**:
  - Reconciler queries `GetUnactivatedSeries()` directly from SQLite (`WHERE is_activated = 0`), discovering legacy `Specials/S00E99` folders across thousands of shows in $<1$s and enqueuing them at `PriorityMigration`.
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

