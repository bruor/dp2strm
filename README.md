# dp2strm - Dispatcharr to STRM Bridge, Emby Ingestion Controller & Streamcache Proxy Router

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](Dockerfile)

`dp2strm` is a high-performance, cloud-native Go application designed to bridge [Dispatcharr](https://github.com/dispatcharr) with [Emby](https://emby.media) and [streamcache](https://github.com/bruor/streamcache).

Rather than simply dumping tens of thousands of `.strm` files onto disk and overwhelming your media server, `dp2strm` acts as a complete **ingestion rate controller, playback router, and cache orchestrator**:

1. **Active Emby Ingestion Rate Control**: Paces large library ingestions into controlled micro-batches, actively monitors Emby's scheduled task status until idle, logs real-time dynamic completion ETAs, and implements reference-counted **VIP On-Demand Playback** that immediately aborts background library scans when a user starts watching.
2. **Deep [streamcache](https://github.com/bruor/streamcache) Integration**: Routes stream playback through `streamcache`, hides upstream IPTV credentials from `.strm` files on disk, and employs a boundary-aware **predictive fetch-ahead engine ("Tap & Bail")** to pre-warm upcoming episodes in the cache while watching.
3. **Lazy On-Demand Catalog Activation**: Uses a two-pass architecture (Pass 1 lightweight placeholder shells ➔ Pass 2 on-demand background hydration upon playback) to slash initial API and disk overhead by 95%+.

`dp2strm` operates strictly as a metadata coordinator and high-speed HTTP `302 Found` redirect handler—it **does not buffer, transcode, or proxy media bytes**.

---

## ✨ Key Capabilities & Architecture

### 🚦 Emby Library Ingestion Rate Control & Scan Orchestration
- **Universal TaskManager Batch Synchronization & Dynamic ETA**:
  - Ingesting 20,000–50,000+ titles at once causes Emby to peg CPU at 100% and lock its SQLite database.
  - `dp2strm` stages updates into micro-batches (`EMBY_BATCH_SIZE=25`), triggers Emby's `Scan media library` scheduled task, and halts the worker queue until Emby reports **100% Idle** before releasing the next batch.
  - Logs real-time overall progress (`Processed/Total [%]`, remaining batches) and calculates dynamic completion ETAs based on rolling average scan durations.
- **⭐ VIP On-Demand User Playback (Priority 100)**:
  - When a user presses Play on an unactivated title, `dp2strm` acquires a reference-counted VIP lock.
  - Any active global background library scan is aborted instantly via `DELETE /ScheduledTasks/Running/{Id}`, pausing background worker queues so Emby can index and stream the user's item with zero lag.
  - Concurrent user activations stack safely using reference counting.
- **Zero-Config Emby Library Auto-Discovery**:
  - Automatically queries Emby's VirtualFolders API on startup to detect TV and Movies library IDs, mount locations, and `RealtimeMonitor` status. Supports multi-library environments with optional name overrides (`EMBY_TV_LIBRARY`, `EMBY_MOVIES_LIBRARY`).
- **Targeted Online Provider ID Refresh (`POST /Items/{Id}/Refresh`)**:
  - Refreshes series and movies directly via TMDB/IMDb IDs (`AnyProviderIdEquals=tmdb.XXXXX`), rescanning only that specific item's directory in `<1s` without running global library scans.

### 🛡️ Native `streamcache` Integration & Predictive Cache Warming
- **Transparent Streamcache URL Rewriting**:
  - Natively rewrites Dispatcharr content URLs to [streamcache](https://github.com/bruor/streamcache) endpoints (`<STREAMCACHE_URL>/stream?url=<escaped_target>`).
  - Completely obfuscates upstream subscriber credentials from `.strm` files on disk and player access logs.
  - Supports external address overrides (`STREAMCACHE_DISPATCHARR_URL`) when internal Docker network hostnames differ from external reverse proxies.
- **Predictive Fetch-Ahead Engine ("Tap & Bail")**:
  - Configured via `FETCH_AHEAD_COUNT` (e.g. 1–2 episodes) and `PREFETCH_DELAY` (default: `5m`).
  - When a user plays an episode, `dp2strm` waits until playback has been stable for `PREFETCH_DELAY` before sequentially firing lightweight background requests directly to `streamcache` to warm the upcoming episodes.
  - **Boundary & Variant Aware**: Discovers valid sequential episodes in SQLite, rolls across season boundaries seamlessly (`S01E10` ➔ `S02E01`), matches the user's active playback quality variant (e.g. 4K vs 1080p Base) with fallback, and halts cleanly at season/series finales.

### 🎬 Lazy On-Demand Catalog Activation (TV & Movies)
- **On-Demand TV Series Activation Lifecycle (`Season 01/S01E01`)**:
  - **Pass 1 (Lightweight Initial Sync)**: Generates artwork and `Season 01/Show Name - S01E01.strm` accompanied by an instructional NFO (`⚡ Play to Activate Series` with `<watched>false</watched>`, `<playcount>0</playcount>`, `<runtime>1</runtime>`). Series cleanly displays as **Unplayed** in Emby and renders in the **Next Up** rail on the home screen!
  - **Pass 2 (Instant Playback Handshake)**: Clicking Play on Episode 1 writes all series episodes to disk and issues an instant **HTTP 302 Found** redirect in `<1 second` directly to the real video stream. For shows starting at Season 2+, an embedded video bumper (`activated.mp4`) is streamed to prevent watch history mismatches.
  - **Asynchronous Session-Aware Metadata Sync**: A background worker holds the VIP lock, prunes temporary instruction NFOs, verifies active playback sessions in Emby (`GET /Sessions`, configurable timeout `ACTIVATION_REFRESH_DELAY=10s`), and triggers a targeted metadata refresh (`POST /Items/{Id}/Refresh?ReplaceAllMetadata=true`) that cleanly updates episode titles without interrupting playback.
- **On-Demand Movie Quality Activation**:
  - **Pass 1**: Generates initial `Movie.strm` base file pointing to `/play/movie/{identifier}` with artwork.
  - **On Play**: `handleMoviePlay` cancels any running background library scans, resolves backing provider streams via `/api/vod/movies/{id}/providers/`, classifies Base 1080p vs 4K tiers, saves variants to SQLite, writes secondary `Movie - 4K.strm` files to disk for Emby's version dropdown, and marks movie active in `movie_state`.
  - **Subsequent Passes**: Scheduled sync passes re-verify and update all quality variant `.strm` files (`Movie.strm`, `Movie - 4K.strm`) on disk.

### 🧠 Resilient Fingerprinting, Self-Healing & State Engine
- **Resilient External ID Routing (`tmdb:` / `imdb:`)**:
  - `.strm` files output resilient, permanent external ID URLs (`/play/movie/tmdb:1739639`, `/play/series/tmdb:218510/1/1`, or `/play/movie/imdb:tt36615831`), falling back to numeric Dispatcharr IDs only for unindexed media.
- **Content Fingerprint Healing & Upstream ID Supersede**:
  - Automatically matches streams across upstream provider drop/re-add cycles using content fingerprints (`TMDB ID` or `Title + Year`).
  - When Dispatcharr issues a new internal ID for previously indexed content, `dp2strm` transfers user activation/watch state, updates SQLite, immediately purges the old obsolete directory on disk (`os.RemoveAll`), and bypasses the 7-day retention grace period.
- **Immediate Obsolete Folder Pruning on Metadata Upgrade**:
  - Distinguishes between true content deletions (safely buffered by `RETENTION_GRACE_DAYS=7`) vs in-place metadata/folder upgrades (e.g. `Movie (Year)` ➔ `Movie (Year) [tmdbid=12345]`). Old un-tagged legacy folders are pruned from disk immediately, eliminating duplicate entries in Emby.
- **In-Place STRM URL Reconciliation Fast-Path**:
  - When `.strm` files on disk require internal URL updates (e.g. upgrading from numeric ID to `tmdb:`), `dp2strm` rewrites the file in-place and preserves its original modification timestamp (`os.Chtimes`), bypassing Emby library scans.
- **Pure-Go SQLite State Engine (`modernc.org/sqlite`)**:
  - 100% CGO-free, single static Alpine binary compatible. Stores state in `/data/db/dp2strm.db` using WAL mode (`series_state`, `movie_state`, `sync_queue`, `stream_variants`).
- **Dynamic Shared-Pool Batching with Budget Rollover (`BATCH_SIZE`)**:
  - Limits total newly created media files per sync run, automatically splitting 50/50 between Movies and Series with unused quota rollover.
- **Duplicate Year & Clean NFO Title Sanitization**:
  - Strips trailing release year from NFO `<title>` and `<originaltitle>` XML tags, preventing Emby from duplicating the release year in its UI.
  - Automatically collapses duplicate consecutive year tags (e.g., `Yahoo+ (2022) (2022)` ➔ `Yahoo+ (2022)`) across all folders and filenames.
- **Timezone-Aware Cron Scheduler**:
  - Native 5-field cron scheduling (`SYNC_CRON="0 3 * * *"`) and IANA timezone awareness (`TZ="America/New_York"`).

---

## 🚀 Quick Start with Docker Compose

```yaml
services:
  dp2strm:
    build:
      context: https://github.com/bruor/dp2strm.git#main
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

      # Content Filtering Options
      - ADULT_CONTENT=disabled # Control adult content: disabled (default) or enabled
      - ADULT_GROUP_FILTER=18+,adult,porn,xxx,nutflix # Comma-separated content group (category) filters

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
| `URL` | `http://localhost:8080` | Public URL accessible by Emby players |
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
| `ADULT_CONTENT` | `disabled` | Control adult content filtering: `disabled` (default, skips and purges adult items) or `enabled` |
| `ADULT_GROUP_FILTER` | `18+,adult,porn,xxx,nutflix` | Comma-separated list of substring filters matched against content categories / groups |
| `EMBY_URL` | `""` | Optional Emby server URL for library refresh hooks |
| `EMBY_API_KEY` | `""` | Optional Emby API key |
| `EMBY_MEDIA_PATH` | `""` | Optional override: internal Emby mount path for `.strm` media folder |
| `EMBY_TV_LIBRARY` | `""` | Optional override: library name if multiple TV libraries exist (e.g. `"TV shows"`) |
| `EMBY_MOVIES_LIBRARY` | `""` | Optional override: library name if multiple Movie libraries exist (e.g. `"Movies"`) |

---

## 🏗️ Building from Source

```bash
# 1. Clone repository
git clone https://github.com/bruor/dp2strm.git
cd dp2strm

# 2. Build static Go binary
go build -o bin/dp2strm ./cmd/dp2strm

# 3. Run single sync pass
./bin/dp2strm --sync-once
```

---

## 📄 License

This project is licensed under the MIT License.
