# dp2strm - Dispatcharr to STRM File Generator & Playback Handler

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](Dockerfile)

`dp2strm` is a lightweight, cloud-native Go application designed to interface with [Dispatcharr](https://github.com/dispatcharr). It generates standardized `.strm` stream pointers, `.nfo` metadata XML files, and `poster.jpg`/`fanart.jpg` artwork on disk formatted specifically for Emby, Jellyfin, and Kodi.

`dp2strm` operates strictly as a metadata generator and high-speed HTTP `302 Found` redirect handler—it **does not buffer, transcode, or proxy media bytes**.

---

## ✨ Key Features

- **Native Dispatcharr VOD Proxy Streaming**:
  - Automatically redirects playback directly to Dispatcharr's Native VOD Streaming Proxy (`/proxy/vod/movie/{uuid}` and `/proxy/vod/episode/{uuid}`).
  - Authenticates securely via `DISPATCHARR_API_KEY` without requiring separate XC subscriber usernames or passwords.
- **Resilient External ID Routing (`tmdb:` / `imdb:`)**:
  - `.strm` files output resilient, permanent external ID URLs (`/play/movie/tmdb:1739639`, `/play/series/tmdb:218510/1/1`, or `/play/movie/imdb:tt36615831`), falling back to numeric Dispatcharr IDs only for unindexed media.
- **Content Fingerprint Healing & Upstream ID Supersede**:
  - Automatically matches streams across upstream provider drop/re-add cycles using content fingerprints (`TMDB ID` or `Title + Year`).
  - When Dispatcharr issues a new internal ID for previously indexed content, `dp2strm` transfers user activation/watch state, updates SQLite, immediately purges the old obsolete directory on disk (`os.RemoveAll`), and bypasses the 7-day retention grace period.
- **Immediate Obsolete Folder Pruning on Metadata Upgrade**:
  - Distinguishes between true content deletions (safely buffered by `RETENTION_GRACE_DAYS=7`) vs in-place metadata/folder upgrades (e.g. `Movie (Year)` ➔ `Movie (Year) [tmdbid=12345]`).
  - When writing or reconciling upgraded folders with rich ID metadata, old un-tagged legacy folders are pruned from disk immediately and unregistered from `soft_deleted_items`, eliminating duplicate entries in Emby.
- **Self-Healing Legacy Playback & Dual-Key Caching**:
  - If a legacy numeric ID (e.g. `1168810`) returns a 404 upstream from Dispatcharr, `dp2strm` automatically locates the active stream using the stored TMDB ID or Title + Year fingerprint.
  - Caches the resolved stream in SQLite under both IDs for sub-millisecond future hits and enqueues a background reconciliation to correct the file on disk.
- **In-Place STRM URL Reconciliation Fast-Path**:
  - When `.strm` files on disk require internal URL updates (e.g. upgrading from numeric ID to `tmdb:`), `dp2strm` rewrites the file in-place and preserves its original modification timestamp (`os.Chtimes`).
  - In-place URL updates finish in `<1ms`, do **not** increment `EMBY_BATCH_SIZE`, and bypass Emby library scan pauses, ensuring zero playback disruption.
- **Controlled Folder Migrations (Scheduled Reconciler Only)**:
  - When an unindexed title receives a TMDB ID later, folder paths remain strictly locked during real-time playback to prevent Emby from encountering 404s during probing.
  - Folder structure renames (e.g. `Movie (Year)` ➔ `Movie (Year) [tmdbid=...]`) are handled exclusively by the scheduled worker reconciliation pass, where each rename is tracked as a structural update (`STRMCreated = true`), notified to Emby (`NotifyMediaUpdated`), and paced through `EMBY_BATCH_SIZE`.
- **On-Demand TV Series Activation Lifecycle (`Season 01/S01E01`)**:
  - **Pass 1 (Lightweight Sync)**: Generates posters and `Season 01/Show Name - S01E01.strm` accompanied by an explicit instructions NFO (`⚡ Play to Activate Series` with `<watched>false</watched>`, `<playcount>0</playcount>`, `<runtime>1</runtime>`). Series cleanly displays as **Unplayed** in Emby and renders in the **Next Up** rail on the home screen!
  - **Pass 2 (Instant Playback Handshake)**: Clicking Play on Episode 1 writes all series episodes and issues an instant **HTTP 302 Found** redirect in `<1 second` directly to the real episode video (zero bumper delay!). For shows starting at Season 2+, an embedded 5-second video bumper (`activated.mp4`) is streamed to prevent watch history mismatches.
  - **Asynchronous Session-Aware Metadata Sync**: A background worker holds the VIP lock, prunes all `*S01E01*.nfo` files, verifies the active playback session in Emby (`GET /Sessions`, configurable timeout `ACTIVATION_REFRESH_DELAY=10s`), and triggers a targeted Emby metadata refresh (`POST /Items/{Id}/Refresh?ReplaceAllMetadata=true`) that cleanly fetches TMDB/TVDB episode titles without interrupting active playback!
- **On-Demand Movie Quality Activation**:
  - **Pass 1 (Lightweight Sync)**: Generates initial `Movie.strm` base file pointing to `/play/movie/{identifier}` with artwork (and NFO metadata if enabled).
  - **On Play**: `handleMoviePlay` cancels any running background library scans, resolves backing provider streams via `/api/vod/movies/{id}/providers/`, classifies Base 1080p vs 4K tiers, saves variants to SQLite, writes secondary `Movie - 4K.strm` files to disk for Emby's version dropdown, and marks movie active in `movie_state`.
  - **Subsequent Passes**: Scheduled sync passes re-verify and update all quality variant `.strm` files (`Movie.strm`, `Movie - 4K.strm`) on disk.
- **Dedicated Staged-Queue & Worker Pipeline**:
  - **Catalog Stager (`DISPATCHARR_WORKERS`)**: Fast, lightweight catalog staging runs on a timer (default: `4h`), populating the SQLite `sync_queue` without blocking on disk I/O or Emby scans.
  - **Universal TaskManager Batch Synchronization & Dynamic ETA**: Micro-batches (configured via `EMBY_BATCH_SIZE`, default: 25) write directly to disk, trigger Emby's `Scan media library` scheduled task (`POST /Library/Refresh`), and halt until Emby returns to **100% Idle** before releasing the next batch. Completely eliminates file collisions and race conditions. Logs real-time overall queue progress (`Processed/Total [%]`, remaining items & batches) and dynamically estimates time-to-sync completion based on rolling average Emby library scan durations.
  - **⭐ VIP On-Demand Series Playback (Priority 100)**: When a user presses play on an unactivated show (`S00E99`), `dp2strm` acquires a reference-counted VIP lock; any active background global library scan is stopped instantly via `DELETE /ScheduledTasks/Running/{Id}` and background queue workers are paused so Emby can index and stream the activated item with zero delay. Secondary/concurrent activations stack safely with reference counting without interference.
- **Zero-Config Emby Library Auto-Discovery**:
  - Automatically queries Emby's VirtualFolders API on startup to detect TV and Movies library IDs, mount locations, and `RealtimeMonitor` status.
  - Supports multi-library environments with optional name targeting (`EMBY_TV_LIBRARY`, `EMBY_MOVIES_LIBRARY`).
- **Targeted Online Provider ID Refresh (`POST /Items/{Id}/Refresh`)**:
  - Refreshes series and movies directly via TMDB/IMDb IDs (`AnyProviderIdEquals=tmdb.XXXXX`), rescanning only that specific show's folder in `<1s` without running global library scans.
- **Daily Deep Disk Reconciler (`RECONCILE_INTERVAL`)**:
  - Configurable daily janitor (default: `24h` / `0 3 * * *`) that scans SQLite state (`WHERE is_activated = 0`), automatically converts legacy folder structures to the new `Specials/S00E99` architecture at `PriorityReconcile (50)`, and prunes orphaned files.
- **Pure-Go SQLite State Engine (`modernc.org/sqlite`)**: 100% CGO-free, single static Alpine binary compatible. Stores state in `/data/db/dp2strm.db` using WAL mode (`series_state`, `movie_state`, `sync_queue`, `stream_variants`).
- **Smart Series Update Scheduling Engine**:
  - Unactivated series skip fetching episode lists on scheduled sync runs (0 API calls!).
  - Activated series automatically detect new episode drops during periodic catalog staging when Dispatcharr reports newer `updated_at` timestamps or increased `episode_count`.
  - Airing episode drops are prioritized at `PriorityAiring` (`30`), jumping ahead of bulk catalog intake (`PriorityBatch` = `10`), with newly created `.strm` files naturally ingested into Emby via standard batch scans.
- **Dynamic Shared-Pool Batching with Budget Rollover (`BATCH_SIZE`)**:
  - `BATCH_SIZE` limits the total number of **newly created media files** per sync run.
  - Automatically splits the budget 50/50 between Movies and Series.
  - **Dynamic Rollover**: If one media type has fewer new items available, its unused quota automatically rolls over to the other type.
  - Keeps Emby's periodic import window predictable (e.g. `BATCH_SIZE=100` targets ~20 minutes of Emby indexing per hour).
- **Server-Side Prioritized Ordering (`DISPATCHARR_ORDERING`)**:
  - Automatically sorts API queries with `?ordering=-created_at` by default.
  - Ensures brand-new movie additions and TV series with fresh episode drops are placed at the front of the sync queue in every pass.
- **Duplicate Year & Clean NFO Title Sanitization**:
  - Strips trailing release year from NFO `<title>` and `<originaltitle>` XML tags, preventing Emby from duplicating the release year in its UI.
  - Automatically collapses duplicate consecutive year tags (e.g., `Yahoo+ (2022) (2022)` ➔ `Yahoo+ (2022)`) across all folders and filenames.
  - Strips quality, codec, and release tags from titles while preserving legitimate release years.
- **Emby Multi-Version Hierarchy Support**: Automatically classifies 1080p base default versions (`Movie.strm`) vs 4K secondary versions (`Movie - 4K.strm`) for Emby's version dropdown menu.
- **Auto-Healing URL Normalization**: Automatically appends `:PORT` to `cfg.URL` if port is omitted and non-standard (e.g. `http://dp2strm` ➜ `http://dp2strm:8080`), preventing port 80 connection errors in FFmpeg/Emby.
- **Single Unified Emby Refresh Trigger**: Emby receives a single refresh webhook at the end of each shared sync pass only when new `.strm` media files are created on disk.
- **`streamcache` Integration & Boundary-Aware Predictive Pre-Caching**:
  - Optional integration with `streamcache` transparent proxy (`<STREAMCACHE_URL>/stream?url=<escaped_target>`).
  - **Predictive Tap & Bail** (`FETCH_AHEAD_COUNT`, default `0` = disabled; `PREFETCH_DELAY`, default `5m`): When watching a series, waits `PREFETCH_DELAY` before sequentially firing lightweight background requests directly to `streamcache` with the upstream Dispatcharr target URL to warm/pre-buffer upcoming episodes without overwhelming upstream connections.
  - **Boundary & Variant Aware**: Discovers only valid sequential episodes in SQLite, seamlessly rolling over season boundaries (e.g. `S01E10` ➔ `S02E01`), matching the user's active playback variant (e.g. `4k` vs `base`) with per-episode base fallback, and stopping cleanly at season/series finales.
- **Timezone-Aware Cron Scheduler**: Native 5-field cron scheduling (`SYNC_CRON="0 3 * * *"`) and IANA timezone awareness (`TZ="America/New_York"`).

---

## 🚀 Quick Start with Docker Compose

```yaml
services:
  dp2strm:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: dp2strm
    restart: unless-stopped
    # ports:
    #   - "8080:8080"
    volumes:
      - ./strm:/data/strm
      - ./data:/data/db
    environment:
      # Required Dispatcharr Connection
      - DISPATCHARR_URL=https://your-dispatcharr-server.com
      - DISPATCHARR_API_KEY=your_dispatcharr_api_key_here

      # Path & Handler Options
      - OUTPUT_DIR=/data/strm
      - PORT=8080
      - URL=http://dp2strm:8080

      # Timezone & Scheduling
      - TZ=America/Toronto
      - SYNC_INTERVAL=4h
      - ACTIVE_SERIES_REFRESH_INTERVAL=4h # Probe interval for activated series upstream episode refresh (Default: 4h)
      - BATCH_SIZE=100 # Shared budget per run (50% movies / 50% series with rollover)
      - DISPATCHARR_ORDERING=-created_at # Server-side sorting prioritizing recent updates & additions
      - PAGE_SIZE=100 # Number of items fetched per API page request (Dispatcharr max: 100)
      - PAGE_WORKERS=5 # Number of concurrent page fetch workers
      - MAX_PROBE_CONCURRENCY=5 # Max concurrent movie processing workers

      # Streamcache Integration (Optional)
      - STREAMCACHE_URL= # e.g. http://streamcache:8080
      - STREAMCACHE_DISPATCHARR_URL= # Optional override: Dispatcharr URL streamcache should use (e.g. https://dp.example.com)
      - FETCH_AHEAD_COUNT=0 # Default: 0 (disabled). Set to 1 or 2 when STREAMCACHE_URL is set
      - PREFETCH_DELAY=5m # Sequential delay before & between next-episode taps (Default: 5m)

      # Retention & Emby Hooks
      - RETENTION_GRACE_DAYS=7
      - EMBY_URL=
      - EMBY_API_KEY=
```

---

## 🔒 Host Folder Permissions & POSIX ACL Setup Guide

When mounting a shared host directory into Docker containers, permissions can become tricky if different containers run under different User IDs (e.g. `root`, `appuser`, or `emby` `uid=1000`).

To ensure **any container or host user can read and write files** into the shared folder, follow this guide:

### 1. Pre-Create Media Folders & Configure Host POSIX Default ACLs

Before starting `dp2strm` or configuring Emby, manually pre-create the base output folder along with the `Movies` and `TV Shows` subdirectories on the host (e.g. `./strm` relative to your `docker-compose.yml`, or `/mnt/media/strm_output`), and configure POSIX Default ACLs so that newly created files automatically inherit full read/write permissions for all containers.

```bash
# Step 1: Create the base folder and required media subdirectories
# (Run this in the directory where your docker-compose.yml is located)
mkdir -p ./strm/Movies "./strm/TV Shows"

# Step 2: Ensure permissive permissions on existing directories
chmod -R 777 ./strm

# Step 3: Set POSIX Default Access Control Lists (ACLs)
# This forces ALL newly created files and subdirectories to inherit full rwx permissions automatically!
sudo setfacl -R -d -m g::rwx ./strm
sudo setfacl -R -d -m m::rwx ./strm

# Step 4: Apply ACLs to existing directories
sudo setfacl -R -m g::rwx ./strm
sudo setfacl -R -m m::rwx ./strm
```

#### **How This Works**:
- **Automatic Inheritance**: Every new `.strm`, `.nfo`, or `poster.jpg` created by `dp2strm` inherits group `rwx` permissions automatically.
- **`0666` / `0777` Creation Modes**: `dp2strm` creates files with `0666` mode and directories with `0777` mode so Linux kernel POSIX ACL inheritance works without permission masking.

#### **Adding Folders to Emby**:
Pre-creating these folders allows you to immediately browse and link them in Emby's library settings before or right after booting `dp2strm`:
- **New Libraries**: Create a "Movies" library pointing to your mounted `Movies` folder and a "TV Shows" library pointing to your mounted `TV Shows` folder.
- **Existing Multi-Path Libraries**: If you already have existing Movies or TV libraries with other paths (e.g. local NAS storage), simply open the library settings in Emby, click **Add Folder**, and add the new `Movies` / `TV Shows` path alongside your existing paths.
- **Mount Path Alignment**: If Emby mounts the folder to a different container path than `dp2strm` (e.g. `dp2strm` uses `/data/strm` while Emby uses `/media/strm`), set `EMBY_MEDIA_PATH=/media/strm` in `dp2strm`'s environment so rescan notifications target the correct Emby container path.

---

### 2. Read-Only Protection for Emby (`:ro` Mount)

Even though POSIX ACLs grant `rwx` permissions on disk, you can mount the directory as **Read-Only (`:ro`)** in Emby's container definition:

```yaml
services:
  emby:
    image: emby/embyserver:latest
    container_name: emby
    volumes:
      # Mount as Read-Only (:ro)
      - /mnt/media/strm_output:/media/strm:ro
```

#### **Why This is 100% Safe**:
- **Kernel-Level Read-Only Enforcement**: Docker and the Linux kernel strictly enforce the `:ro` flag at the container boundary.
- **Protection**: Emby can index, scan, and play `.strm` files freely, but is **100% blocked** from creating, modifying, or deleting any files inside the folder!

---

## ⚙️ Environment Variables Reference

| Variable | Default | Description |
| :--- | :--- | :--- |
| `DISPATCHARR_URL` | `http://localhost:8000` | Base URL of your Dispatcharr server (**Required**) |
| `DISPATCHARR_API_KEY` | `""` | Dispatcharr API Key (**Required**) |
| `DISPATCHARR_XC_USER` | `""` | Optional: legacy XC username (not required when using API Key) |
| `DISPATCHARR_XC_PASSWORD` | `""` | Optional: legacy XC password (not required when using API Key) |
| `OUTPUT_DIR` | `/data/strm` | Target container path for generated `.strm` tree |
| `PORT` | `8080` | Port for the embedded HTTP 302 redirect handler |
| `URL` | `http://localhost:8080` | Public URL accessible by Emby/Jellyfin players |
| `SYNC_INTERVAL` | `4h` | Background catalog staging interval (`4h`, `1h`, `0` for single run) |
| `SYNC_CRON` | `""` | Optional 5-field cron schedule expression (e.g., `0 */4 * * *`) |
| `RECONCILE_INTERVAL` | `24h` | Daily deep disk reconciliation janitor interval (Default: `24h`) |
| `RECONCILE_CRON` | `""` | Optional 5-field cron expression for reconciler (e.g., `0 3 * * *`) |
| `ACTIVE_SERIES_REFRESH_INTERVAL` | `4h` | Upstream probe interval for activated series episode updates (`4h`, `2h`, `0` to disable) |
| `DISPATCHARR_WORKERS` | `1` | Number of concurrent workers staging from Dispatcharr |
| `EMBY_WORKERS` | `1` | Number of concurrent workers processing queue tasks & Emby updates |
| `EMBY_BATCH_SIZE` | `25` | Micro-batch size of newly written media files before triggering Emby scan |
| `EMBY_PACE_DELAY` | `100ms` | Delay between items processed by Emby workers to keep CPU cool |
| `PAGE_SIZE` | `100` | Number of items fetched per API page request |
| `MAX_PROBE_CONCURRENCY` | `5` | Maximum parallel worker goroutines per page |
| `ENABLE_NFO` | `false` | Enable local NFO metadata generation for actual content (`false` = disabled by default so Emby scrapes TMDB/TVDB directly; synthetic `S01E01` instructions NFO is always generated for unactivated shows) |
| `ACTIVATION_REFRESH_DELAY` | `10s` | Maximum wait duration for active Emby playback session verification before triggering targeted metadata refresh (Default: `10s`) |
| `STREAMCACHE_URL` | `""` | Optional `streamcache` service URL (e.g. `http://streamcache:8080`) |
| `STREAMCACHE_DISPATCHARR_URL` | `""` | Optional override for Dispatcharr URL passed to `streamcache` (e.g. `https://dp.example.com` when internal Docker URL differs) |
| `FETCH_AHEAD_COUNT` | `0` | Number of upcoming TV episodes to pre-fetch for cache warming (default `0` = disabled) |
| `PREFETCH_DELAY` | `5m` | Sequential delay before and between upcoming episode pre-fetch taps (e.g. `5m`, `300s`) |
| `RETENTION_GRACE_DAYS` | `7` | Days to buffer deleted content before purging from disk |
| `TZ` | `UTC` | IANA timezone for cron evaluation (e.g., `America/New_York`) |
| `SKIP_STARTUP_SYNC` | `false` | Set to `true` to skip initial staging pass on container startup |
| `BATCH_SIZE` | `0` | Shared batch creation limit per run (`0` = Unlimited) |
| `DISPATCHARR_ORDERING` | `-created_at` | Server-side ordering parameter for Dispatcharr VOD endpoints |
| `EMBY_URL` | `""` | Optional Emby/Jellyfin server URL for library refresh hooks |
| `EMBY_API_KEY` | `""` | Optional Emby/Jellyfin API key |
| `EMBY_MEDIA_PATH` | `""` | Optional override: internal Emby mount path for `.strm` media folder |
| `EMBY_TV_LIBRARY` | `""` | Optional override: library name if multiple TV libraries exist (e.g. `"TV shows"`) |
| `EMBY_MOVIES_LIBRARY` | `""` | Optional override: library name if multiple Movie libraries exist (e.g. `"Movies"`) |

---

## 🏗️ Building from Source

```bash
# 1. Clone repository
git clone https://github.com/your-user/dp2strm.git
cd dp2strm

# 2. Build static Go binary
go build -o bin/dp2strm ./cmd/dp2strm

# 3. Run single sync pass
./bin/dp2strm --sync-once
```

---

## 📄 License

This project is licensed under the MIT License.
