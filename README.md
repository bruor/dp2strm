# dp2strm - Dispatcharr to STRM File Generator & Playback Handler

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](Dockerfile)

`dp2strm` is a lightweight, cloud-native Go application designed to interface with [Dispatcharr](https://github.com/dispatcharr). It generates standardized `.strm` stream pointers, `.nfo` metadata XML files, and `poster.jpg`/`fanart.jpg` artwork on disk formatted specifically for Emby, Jellyfin, and Kodi.

`dp2strm` operates strictly as a metadata generator and high-speed HTTP `302 Found` redirect handler—it **does not buffer, transcode, or proxy media bytes**.

---

## ✨ Key Features

- **Clean Extensionless Obfuscated URLs**: `.strm` files output clean extensionless URLs (`http://dp2strm:8080/play/movie/1058902`, `http://dp2strm:8080/play/series/30998/1/1`). Upstream subscriber credentials (`agy/agypassword`) are **never written to disk or exposed to Emby**.
- **On-Demand TV Series Activation Lifecycle**:
  - **Pass 1 (Lightweight Sync)**: Generates `tvshow.nfo`, posters, and `Season 01/Show Name - S01E01 - Play to Activate Series.strm` & `.nfo`. Keeps initial library indexing fast across thousands of series.
  - **Pass 2 (On Play)**: Playing Episode 1 **immediately (<50ms)** 302 redirects to playable video while asynchronously populating all real episode `.strm` files, purging placeholders, and updating Emby in the background.
- **On-Demand Movie Quality Activation**:
  - **Pass 1 (Lightweight Sync)**: Generates initial `Movie.strm` base file pointing to `/play/movie/{id}` with 100% real NFO metadata and artwork.
  - **On Play**: `handleMoviePlay` resolves backing provider streams via `/api/vod/movies/{id}/providers/`, classifies Base 1080p vs 4K tiers, saves variants to SQLite, writes secondary `Movie - 4K.strm` files to disk for Emby's version dropdown, and marks movie active in `movie_state`.
  - **Subsequent Passes**: Scheduled sync passes re-verify and update all quality variant `.strm` files (`Movie.strm`, `Movie - 4K.strm`) on disk.
- **Pure-Go SQLite State Engine (`modernc.org/sqlite`)**: 100% CGO-free, single static Alpine binary compatible. Stores state in `/data/db/dp2strm.db` (or output dir) using WAL (Write-Ahead Logging) mode (`series_state`, `movie_state`, `stream_variants`).
- **Smart Series Update Scheduling Engine**:
  - Unactivated & `Ended` series skip fetching episode lists on scheduled sync runs (0 API calls!).
  - `Continuing` series skip checks until `current_date >= next_episode_air_date`.
  - Millisecond `last_modified` checks prevent unnecessary disk writes (`os.Chtimes` preservation).
- **Emby Multi-Version Hierarchy Support**: Automatically classifies 1080p base default versions (`Movie.strm`) vs 4K secondary versions (`Movie - 4K.strm`) for Emby's version dropdown menu.
- **Auto-Healing URL Normalization**: Automatically appends `:PORT` to `cfg.URL` if port is omitted and non-standard (e.g. `http://dp2strm` ➜ `http://dp2strm:8080`), preventing port 80 connection errors in FFmpeg/Emby.
- **`streamcache` Base64 URL Rewriter**: Optional integration with `streamcache` transparent proxy using URL-safe Base64 encoded query paths (`<STREAMCACHE_URL>?url=<escaped_target>`).
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
    ports:
      - "8080:8080"
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
      - SYNC_INTERVAL=1h
      - BATCH_SIZE=0 # 0 = Unlimited full library sync
      - PAGE_SIZE=1000 # Number of items fetched per API page request
      - PAGE_WORKERS=4 # Number of concurrent page fetch workers
      - MAX_PROBE_CONCURRENCY=5 # Max concurrent movie processing workers

      # Streamcache Integration (Optional)
      - STREAMCACHE_URL= # e.g. http://streamcache:8080
      - FETCH_AHEAD_COUNT=2 # Only active when STREAMCACHE_URL is set

      # Retention & Emby Hooks
      - RETENTION_GRACE_DAYS=7
      - EMBY_URL=
      - EMBY_API_KEY=
```

---

## 🔒 Host Folder Permissions & POSIX ACL Setup Guide

When mounting a shared host directory into Docker containers, permissions can become tricky if different containers run under different User IDs (e.g. `root`, `appuser`, or `emby` `uid=1000`).

To ensure **any container or host user can read and write files** into the shared folder, follow this guide:

### 1. Configure Host POSIX Default ACLs (One-Time Host Setup)

Run the following commands on your Linux host machine targeting your output directory (e.g. `/mnt/media/strm_output`):

```bash
# Step 1: Set POSIX Default Access Control Lists (ACLs)
# This forces ALL newly created files and subdirectories to inherit full rwx permissions automatically!
sudo setfacl -R -d -m g::rwx /mnt/media/strm_output
sudo setfacl -R -d -m m::rwx /mnt/media/strm_output

# Step 2: Apply ACLs to existing files/directories
sudo setfacl -R -m g::rwx /mnt/media/strm_output
sudo setfacl -R -m m::rwx /mnt/media/strm_output
```

#### **How This Works**:
- **Automatic Inheritance**: Every new `.strm`, `.nfo`, or `poster.jpg` created by `dp2strm` inherits group `rwx` permissions automatically.
- **`0666` / `0777` Creation Modes**: `dp2strm` creates files with `0666` mode and directories with `0777` mode so Linux kernel POSIX ACL inheritance works without permission masking.

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
| `DISPATCHARR_XC_USER` | `""` | Dispatcharr XC user account username |
| `DISPATCHARR_XC_PASSWORD` | `""` | Dispatcharr XC user account password |
| `OUTPUT_DIR` | `/data/strm` | Target container path for generated `.strm` tree |
| `PORT` | `8080` | Port for the embedded HTTP 302 redirect handler |
| `URL` | `http://localhost:8080` | Public URL accessible by Emby/Jellyfin players |
| `PAGE_SIZE` | `1000` | Number of items fetched per API page request |
| `PAGE_WORKERS` | `4` | Number of concurrent page fetch workers |
| `MAX_PROBE_CONCURRENCY` | `5` | Maximum parallel worker goroutines per page |
| `STREAMCACHE_URL` | `""` | Optional `streamcache` service URL (e.g. `http://streamcache:8080`) |
| `FETCH_AHEAD_COUNT` | `2` | Number of upcoming TV episodes to pre-fetch for cache warming |
| `RETENTION_GRACE_DAYS` | `7` | Days to buffer deleted content before purging from disk |
| `SYNC_INTERVAL` | `1h` | Background sync ticker duration (`1h`, `30m`, `0` for single run) |
| `SYNC_CRON` | `""` | Optional 5-field cron schedule expression (e.g., `0 3 * * *`) |
| `TZ` | `UTC` | IANA timezone for cron evaluation (e.g., `America/New_York`) |
| `SKIP_STARTUP_SYNC` | `false` | Set to `true` to skip initial sync pass on container startup |
| `BATCH_SIZE` | `0` | Batch processing limit per sync pass (`0` = Unlimited) |
| `EMBY_URL` | `""` | Optional Emby/Jellyfin server URL for library refresh hooks |
| `EMBY_API_KEY` | `""` | Optional Emby/Jellyfin API key |

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
