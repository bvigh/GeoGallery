# GeoGallery

A map-based media gallery and GeoGuessr-like game for reviewing trip photos, videos, and 360° media. Each user signs in through an OpenID Connect provider and has a private library.

- Browse media on an interactive map (Leaflet/OSM) with markers at each recording location
- GPS resolved from EXIF metadata, or interpolated from imported external tracks (Google Timeline, Strava, etc.) using timestamp-based matching with confidence scoring
- Auto-detected trips grouped by time gaps, with manual rename and deletion
- Delete individual media items from both the map and trips views (removes from DB and object storage)
- Game mode: guess where and when a photo/video was taken, scored on distance and date accuracy (relative to your media's geographic spread)
- Filter media by date range, trip, and media type on both the map and game pages
- 360° photo and video support with per-media default view and keyframe settings
- Invite-only accounts via any OIDC provider (Logto, Keycloak, Auth0, …), with social login configured in the provider
- Personal API tokens for scripts

## Stack

| Layer | Technology |
|---|---|
| Frontend | React 18 + Vite + TypeScript + shadcn/ui + Tailwind |
| Map | Leaflet + OpenStreetMap |
| Backend | Go 1.26 + chi router |
| Auth | OIDC (go-oidc + x/oauth2), server-side sessions |
| Database | PostgreSQL 16 + PostGIS 3.4 |
| Object storage | MinIO AIStore (S3-compatible, local) |
| Migrations | golang-migrate |
| Orchestration | Docker Compose |
| Dev/E2E identity provider | Keycloak (compose `dev` profile) |

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and Docker Compose v2
- Go 1.26+ (local dev only)
- Node.js 22+ (local dev only)
- MinIO AIStore license file (see [MinIO AIStore note](#minio-aistore-note))

## Quickstart (Docker, with the dev Keycloak)

```bash
cp .env.example .env
docker compose --profile dev up --build
```

The `dev` profile adds Keycloak with a dev realm (`deploy/keycloak/geogallery-realm.json`) and test users `alice`, `bob` and `carol`, each with a password equal to the username. The realm allows self-registration, so someone with an invite link can click **Register** on the Keycloak login page to create a new account.

Create the first admin and sign in:

```bash
docker compose exec backend ./server invite --admin
# prints http://localhost:5173/api/auth/login?invite=...
```

Open the printed link and sign in as `alice` / `alice`. That account becomes the admin and owns any media that existed before authentication was added. Invite more people from the **Admin** tab, or with `docker compose exec backend ./server invite`.

| Service | URL |
|---|---|
| Frontend | http://localhost:5173 |
| Backend API | http://localhost:8080/api |
| Keycloak (dev) | http://localhost:8180 (admin console: `admin` / `admin`) |
| MinIO console | http://localhost:9001 |
| PostgreSQL | localhost:5432 |

MinIO default credentials: `minioadmin` / `minioadmin`

The dev Keycloak is bound to `127.0.0.1` and uses known passwords. Do not use it, or its realm file, for a hosted instance.

### MinIO AIStore note

This project uses the MinIO AIStore edition (`quay.io/minio/aistor/minio`), which requires a license file. Obtain a license from [min.io](https://min.io/pricing) and set its path in `.env`:

```
MINIO_LICENSE_PATH=/path/to/minio.license
```

The default is `$HOME/minio/minio.license`.

## Local development (no Docker build)

Run the infrastructure in Docker and the backend and frontend natively:

```bash
# 1. DB, MinIO and the dev Keycloak
docker compose --profile dev up -d db minio minio-init keycloak

# 2. Backend (runs migrations automatically on startup)
cd backend
cp ../.env.example .env   # defaults target the dev Keycloak and http://localhost:5173
go run ./cmd/server

# 3. First admin (in another shell, from backend/)
go run ./cmd/server invite --admin

# 4. Frontend (proxies /api to localhost:8080; in another shell, from the repo root)
cd frontend
npm install
npm run dev
```

Open the invite link and sign in as `alice` / `alice`.

## Hosting with a managed identity provider

The hosted instance uses Logto Cloud. Any OIDC provider works. [docs/auth.md](docs/auth.md) has step-by-step setup for Logto, notes for other providers, and production Keycloak.

1. Register a confidential web app at the provider:
   - redirect URI `{PUBLIC_URL}/api/auth/callback`
   - post-logout URI `{PUBLIC_URL}/`
2. Settle the final auth domain (for example a Logto custom domain) **before** step 4. Users are keyed by the issuer URL.
3. In the root `.env`, set:
   ```
   PUBLIC_URL=https://gallery.example.com
   OIDC_ISSUER_URL=https://auth.example.com/oidc        # exactly as in the discovery document
   OIDC_DISCOVERY_URL=https://auth.example.com/oidc     # must equal OIDC_ISSUER_URL
   OIDC_CLIENT_ID=...
   OIDC_CLIENT_SECRET=...
   MINIO_ROOT_USER=...                                  # minioadmin is refused over https
   MINIO_ROOT_PASSWORD=...
   ```
4. Start **without** the dev profile, then create the first admin:
   ```bash
   docker compose up -d --build
   docker compose exec backend ./server invite --admin
   ```

`docker-compose.yml` hard-codes the backend's `S3_ENDPOINT` to `http://minio:9000`, which browsers cannot resolve. Presigned media URLs point there, so edit it to a browser-reachable S3 host before hosting (known gap).

Do not expose the database, MinIO console or backend ports publicly; see [Port hardening](docs/auth.md#port-hardening).

## Project structure

```
geogallery/
├── backend/
│   ├── cmd/server/         # entrypoint, routes, operator subcommands (invite, users, scan, rebind-issuer)
│   ├── internal/
│   │   ├── auth/           # OIDC login, sessions, API tokens, middleware
│   │   ├── config/         # env-based config
│   │   ├── db/             # pgx pool + migration runner
│   │   ├── geo/            # GPS interpolation, Haversine
│   │   ├── handlers/       # HTTP handlers (media, trips, game, ingest, gps_tracks, gps_matcher, admin)
│   │   ├── httplog/        # path-only request logger
│   │   ├── ingest/         # EXIF extraction
│   │   ├── storage/        # S3/MinIO client
│   │   └── testdb/         # throwaway PostGIS databases for integration tests
│   └── migrations/         # golang-migrate SQL files
├── deploy/keycloak/        # dev/E2E Keycloak realm (never import in production)
├── docs/auth.md            # identity provider setup
├── frontend/
│   ├── e2e/                # Playwright E2E suite
│   └── src/
│       ├── api/            # typed fetch wrappers
│       ├── components/     # reusable UI (auth, map, viewer, shadcn/ui)
│       ├── pages/          # Map, Trips, Game, Upload, Settings, Admin
│       └── types/          # shared TypeScript types
├── ingest/                 # drop media files here, then run `server scan --user <id>`
├── scripts/                # e2e-reset-db.sh
├── docker-compose.yml
└── .env.example
```

## API endpoints

Every route except those marked *public* needs a session cookie or an API token (`Authorization: Bearer gg_pat_…`). *Session* routes reject API tokens with 403. *Admin* routes are also session-only. Everything is scoped to the caller's own data; another user's IDs return 404.

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/api/health` | public | Health check |
| `GET` | `/api/auth/login` | public | Start OIDC login (`return_to`, `invite`, `prompt=login\|select_account`) |
| `GET` | `/api/auth/callback` | public | OIDC redirect target |
| `POST` | `/api/auth/logout` | public | End the session; returns `{"redirect_url": ...}` |
| `GET` | `/api/auth/me` | user | Current user `{id, email, display_name, role}` |
| `GET` | `/api/media` | user | List media (latest 500); filters: `from_date`, `to_date` (`YYYY-MM-DD`), `trip_id[]`, `media_type[]`; malformed values → 400 |
| `POST` | `/api/media/upload` | user | Upload files (multipart, field `files[]`, `file_mtime[]`) |
| `GET` | `/api/media/{id}/url` | user | Get presigned URL for a media item |
| `PATCH` | `/api/media/{id}/location` | user | Set a manual location `{"lat":..,"lng":..}` |
| `PATCH` | `/api/media/{id}/view-settings` | user | Set 360° default view (`initial_yaw`, `initial_pitch`, `initial_fov`) |
| `GET` | `/api/media/{id}/keyframes` | user | Get video orientation keyframes |
| `PUT` | `/api/media/{id}/keyframes` | user | Replace all keyframes for a video |
| `DELETE` | `/api/media/{id}` | session | Delete a media item from DB and object storage |
| `GET` | `/api/trips` | user | List trips |
| `PATCH` | `/api/trips/{id}` | user | Edit a trip `{"name"?, "start_date"?, "end_date"?}` (at least one); marks it user-edited |
| `DELETE` | `/api/trips/{id}` | session | Delete a user-edited trip (media unassigned, re-detected automatically) |
| `GET` | `/api/game/round` | user | Get a random geolocated media item; supports same filters as `/api/media`; 404 when none |
| `POST` | `/api/game/guess` | user | Submit a guess, receive scored result |
| `POST` | `/api/gps-tracks/import` | user | Bulk-insert GPS track points (JSON array) |
| `POST` | `/api/gps-tracks/upload` | user | Import track files (multipart `files`: GPX, TCX, KML/KMZ, GeoJSON, NMEA, Google Timeline, Samsung Health, …) |
| `POST` | `/api/gps-matcher/run` | user | Re-run GPS matcher over your media |
| `GET` | `/api/tokens` | session | List your API tokens |
| `POST` | `/api/tokens` | session | Create a token `{"name":"...","expires_in_days":90}`; plaintext returned once |
| `DELETE` | `/api/tokens/{id}` | session | Revoke a token |
| `GET` | `/api/admin/users` | admin | List users |
| `PATCH` | `/api/admin/users/{id}` | admin | Disable or enable a user `{"disabled":true}` |
| `POST` | `/api/admin/invites` | admin | Create an invite `{"role":"user"\|"admin"}`; returns `{"url":...}` |

## Bulk ingest from local folder

Folder ingest is an operator command, not an HTTP endpoint. It reads the server's filesystem and writes into one user's library.

1. Create an invite (`server invite --admin` for the first user), and sign in with it, so the user exists.
2. Find the user's ID:
   ```bash
   docker compose exec backend ./server users          # or: cd backend && go run ./cmd/server users
   ```
   It prints `id  role  status  email  display_name`. `status` must be `active`.
3. Copy media into `./ingest/` and scan it:
   ```bash
   cp /your/photos/*.jpg ./ingest/
   docker compose exec backend ./server scan --user <id>    # or: cd backend && go run ./cmd/server scan --user <id>
   ```

`scan` walks `INGEST_WATCH_DIR` (or `--dir <path>`). In Docker, `./ingest` is mounted at `/ingest`. The scanner reads EXIF, extracts GPS and timestamps, uploads to MinIO and inserts into the database. Then it runs the GPS matcher and trip detection for that user, and prints `ingested=N skipped=M failed=K`. It exits 1 if any file failed or the run was interrupted (Ctrl-C stops after the current file).

Files are deduplicated per user by a partial hash (first + last 64 KB), so re-scanning is always safe. It refuses unknown, disabled and unclaimed users.

### Capture times and `CAMERA_TIMEZONE`

A photo's EXIF time is a wall-clock reading without a usable timezone. The backend reads it in `CAMERA_TIMEZONE` (an IANA name such as `Europe/Warsaw`). When the photo carries a GPS timestamp that puts the camera clock within two minutes of a real UTC offset, it uses that offset instead, so phone photos taken abroad come out right. `OffsetTimeOriginal` is ignored: some cameras write a fixed offset all year. Unset, `CAMERA_TIMEZONE` falls back to the server's own zone, which is UTC in Docker; the backend warns about that at startup. Over https the backend refuses to start without `CAMERA_TIMEZONE`, so a production server cannot drift silently.

## Importing external GPS tracks

From the browser: **Upload** tab → **GPS track import**. Select any number of track files; the page sends them in batches to `POST /api/gps-tracks/upload`. The format is chosen by file extension:

| Extension | Format |
|---|---|
| `.gpx` | GPX track points (Strava, Komoot, …); points without a time are skipped |
| `.tcx` | Garmin Training Center XML (Strava export) |
| `.kml`, `.kmz` | Google Earth / Google Maps history: `gx:Track`, and placemarks with a `TimeSpan` or `TimeStamp` (a `LineString`'s vertices are spread evenly over the span) |
| `.geojson` | Google Takeout history: `Point` and `LineString` features with a `timespan` property |
| `.json` | An object: Google Maps Timeline export (`semanticSegments`). An array: Samsung Health `location_data`, or the `/import` format below |
| `.nmea` | NMEA 0183 (dashcams): RMC sentences with an active fix |
| `.log` | esoko CSV log (`time,speed,heading,latitude,longitude,altitude`, header optional) |

Any of them may be gzipped (`.gpx.gz`, `.json.gz`, …). Each point's `source` is `<format>:<file name>`, so uploading a file again adds nothing. Files that cannot be read are listed with the reason; the rest still import. One request is limited to 256 MiB.

From a script, upload files the same way (`curl -F files=@ride.gpx -F files=@Zeitachse.json …/api/gps-tracks/upload`), or post points as JSON with a personal API token (see below):

```bash
curl -X POST https://<your-site>/api/gps-tracks/import \
  -H "Authorization: Bearer $GG_TOKEN" \
  -H "Content-Type: application/json" \
  -d '[{"recorded_at":"2024-08-15T14:32:00Z","lat":47.6,"lng":19.0,"source":"google_timeline"}]'
```

Each point accepts: `recorded_at` (RFC3339, required), `lat`, `lng`, `source` (required), `altitude_m`, `accuracy_m`, `activity`. The response is `{"inserted": N, "skipped": M, "errors": [{"row": i, "reason": "..."}]}`. Duplicate points are skipped. The request body is limited to 100 MiB. The GPS matcher runs automatically after import to interpolate locations for your media that has no EXIF or manual location (previously interpolated rows are re-evaluated).

## Scripting with API tokens

Create a token under **Settings → API tokens**. It is shown once. Keep the literal token out of shell history and process lists by exporting it:

```bash
export GG_TOKEN=<the token>

curl -X POST https://<your-site>/api/gps-tracks/import \
  -H "Authorization: Bearer $GG_TOKEN" -H "Content-Type: application/json" --data @points.json

curl -X POST https://<your-site>/api/gps-matcher/run -H "Authorization: Bearer $GG_TOKEN"
```

Tokens act as you, but cannot delete media or trips, manage tokens, or use admin routes (403). They expire after at most 365 days (default 90).

Tokens are meant for curl and scripts. A web page on another origin cannot use them, because the API sends no CORS headers. See [docs/auth.md](docs/auth.md#api-tokens) for secret-scanning and reverse-proxy notes.

## Trip auto-detection

Trips are auto-grouped by time gaps, per user. Detection runs automatically at the end of every upload or scan, and after trip deletion. The gap threshold is controlled by `TRIP_GAP_HOURS` (default: 24h).

Trips appear in the UI with a default name (`Trip YYYY-MM-DD`). From the Trips tab you can:
- **Rename** a trip — marks it as user-edited so it won't be overwritten by auto-detection
- **Delete** a user-edited trip — its media is unassigned and re-grouped on the next detection run

## Scoring (game mode)

Location score is relative to the geographic spread of your media collection — the max distance is computed per round from the actual data, not a fixed global scale. GPS confidence adjusts tolerance (lower confidence = more lenient scoring).

| Component | Max | Decay |
|---|---|---|
| Location | 5000 pts | Linear to 0 at `max_dist_km` (varies per collection) |
| Date | 5000 pts | Linear to 0 at 365 days off |
| Total | 5000 pts | Average of both components |

Use the filter panel to narrow the game to a specific trip, date range, or media type.

## Switching map providers

The map tile layer is isolated in [frontend/src/components/map/MediaMap.tsx](frontend/src/components/map/MediaMap.tsx). To switch to Mapbox or Google Maps, replace the `<TileLayer>` URL and attribution string.

## Environment variables

Defaults work out of the box for local development. Copy `.env.example` to `.env` (root, read by docker compose) and/or `backend/.env` (read by the natively run backend) to customise.

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_DB` | `geogallery` | Database name |
| `POSTGRES_USER` | `geogallery` | Database user |
| `POSTGRES_PASSWORD` | `geogallery` | Database password |
| `POSTGRES_PORT` | `5432` | Host port for PostgreSQL |
| `MINIO_ROOT_USER` | `minioadmin` | MinIO access key |
| `MINIO_ROOT_PASSWORD` | `minioadmin` | MinIO secret key |
| `MINIO_API_PORT` | `9000` | MinIO S3 API port |
| `MINIO_CONSOLE_PORT` | `9001` | MinIO web console port |
| `MINIO_BUCKET` | `geogallery` | Default bucket name |
| `MINIO_LICENSE_PATH` | `$HOME/minio/minio.license` | Path to MinIO AIStore license file |
| `S3_REGION` | `us-east-1` | S3 region label (MinIO ignores it) |
| `SERVER_PORT` | `8080` | Backend HTTP port |
| `FRONTEND_PORT` | `5173` | Frontend container port |
| `PUBLIC_URL` | `http://localhost:5173` | Origin browsers use (`scheme://host[:port]`); `https` enables secure cookies |
| `OIDC_ISSUER_URL` | `http://localhost:8180/realms/geogallery` | Must equal the provider's discovery `issuer` exactly |
| `OIDC_DISCOVERY_URL` | issuer natively; `http://keycloak:8080/realms/geogallery` in compose | Set equal to `OIDC_ISSUER_URL` for any provider other than the dev Keycloak |
| `OIDC_CLIENT_ID` | `geogallery` | OIDC client ID |
| `OIDC_CLIENT_SECRET` | `geogallery-dev-secret` | OIDC client secret (dev value refused over https) |
| `OIDC_SCOPES` | `openid profile email` | Requested scopes (not passed through by `docker-compose.yml`) |
| `SESSION_TTL_HOURS` | `720` | Absolute session lifetime (not passed through by `docker-compose.yml`) |
| `KEYCLOAK_PORT` | `8180` | Dev Keycloak host port (127.0.0.1 only) |
| `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` | `admin` / `admin` | Dev Keycloak admin console login |
| `GPS_MATCH_WINDOW_HOURS` | `1` | Max hours to search for bracketing GPS track points (native only; not passed through by `docker-compose.yml`) |
| `TRIP_GAP_HOURS` | `24` | Time gap (hours) that splits media into separate trips (native only; not passed through by `docker-compose.yml`) |
| `LOG_LEVEL` | `info` | Backend log level: `debug`, `info`, `warn`, `error` (native only; not passed through by `docker-compose.yml`) |
| `LOG_FORMAT` | `text` | Backend log format: `text` (human-readable) or `json` (structured) (native only; not passed through by `docker-compose.yml`) |
| `CAMERA_TIMEZONE` | the server's zone (UTC in Docker) | IANA zone that camera clocks show, e.g. `Europe/Warsaw`; EXIF times are read in it unless the photo's GPS timestamp gives the offset |

Changing `KEYCLOAK_PORT` or `FRONTEND_PORT` requires matching redirect and post-logout URIs in `deploy/keycloak/geogallery-realm.json`, and `PUBLIC_URL` / `OIDC_ISSUER_URL` must match the new ports if you set them (compose derives them from the ports when unset). Keycloak imports the realm only when it does not exist yet, and its state lives in the `keycloak_data` volume. To apply realm edits, remove the container and that volume (`docker compose --profile dev rm -sf keycloak && docker volume rm geogallery_keycloak_data`, where the prefix is your compose project name), then start it again. The test users' IDs are pinned, so existing GeoGallery accounts keep working.
