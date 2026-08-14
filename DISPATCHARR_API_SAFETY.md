# ⚠️ DISPATCHARR API KEY — READ-ONLY POLICY ⚠️

> **THIS FILE MUST BE READ BEFORE ANY DISPATCHARR API INTERACTION**

## API Key Details

- **Account**: `agy`
- **Auth Header**: `X-API-Key: <YOUR_DISPATCHARR_API_KEY>`
- **Access Level**: **Admin** (user_level 10)
- **Instance**: `https://dp.example.com`

## STRICT RULES — DO NOT VIOLATE

1. **READ ONLY**: This API key has admin privileges but MUST only be used for
   **GET requests**. Never issue POST, PUT, PATCH, or DELETE requests against
   the Dispatcharr instance.

2. **No configuration changes**: Do not modify M3U accounts, categories, user
   settings, stream profiles, or any other Dispatcharr configuration.

3. **No VOD refresh triggers**: Do not call any endpoint that triggers a VOD
   content refresh, M3U re-scan, or background task on the Dispatcharr server.
   The `provider-info` endpoint lazily fetches data on first access — this is
   acceptable as it is a normal read-path side effect, but do not call it in
   bulk/loops that would hammer the upstream XC providers.

4. **No credential extraction**: The admin API exposes M3U account credentials
   (server_url, username, password) for upstream XC providers. These MUST NOT
   be used directly. All stream URLs in `.strm` files should route through
   Dispatcharr's own output endpoints, never directly to upstream providers.

5. **Rate limiting**: When paginating large datasets (28,000+ movies, 7,900+
   series), use reasonable page sizes (100) and do not parallelize requests
   aggressively. Dispatcharr is a home server, not a CDN.

## Why Admin Access?

The `GET /api/vod/movies/` list endpoint (paginated movie enumeration) requires
admin-level access. Streamer-level keys return 403 on this endpoint. Admin access
was granted specifically to enable bulk enumeration for `.strm` file generation.

## Allowed Endpoints (GET only)

| Endpoint | Purpose |
|----------|---------|
| `GET /api/vod/movies/?page=N&page_size=100` | Paginated movie list |
| `GET /api/vod/movies/{id}/providers/` | Provider streams for a movie |
| `GET /api/vod/movies/{id}/provider-info/` | Detailed movie info (lazy fetch) |
| `GET /api/vod/series/?page=N&page_size=100` | Paginated series list |
| `GET /api/vod/series/{id}/providers/` | Provider streams for a series |
| `GET /api/vod/series/{id}/episodes/` | Episode list for a series |
| `GET /api/vod/series/{id}/provider-info/` | Detailed series info |
| `GET /api/accounts/users/me/` | Verify current auth level |

## Architecture Reminder

Dispatcharr is both an **XC client** (connects to upstream IPTV providers) and
an **XC server** (exposes `player_api.php` for media players). This project uses
Dispatcharr's **REST API** (`/api/vod/...`) for data reading only. The `.strm`
files point to Dispatcharr's XC server output for playback.

```
XCtoSTRM tool ──GET REST API──▶ Dispatcharr DB    (enumerate + metadata)
Emby player  ──.strm URL──▶ Dispatcharr XC Output ──▶ Upstream   (playback)
```
