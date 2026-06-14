# GeoGallery

A personal map-based media gallery and GeoGuessr-like game for reviewing trip photos, videos, and 360° media.

- Browse media on an interactive map (Leaflet/OSM) with markers at each recording location
- GPS resolved from EXIF metadata, or interpolated from imported external tracks (Google Timeline, Strava, etc.) using timestamp-based matching with confidence scoring
- Auto-detected trips grouped by time gaps, with manual rename and deletion
- Game mode: guess where and when a photo/video was taken, scored on distance and date accuracy (relative to your media's geographic spread)
- Filter media by date range, trip, and media type on both the map and game pages
- 360° photo and video support with per-media default view and keyframe settings

## Stack

| Layer | Technology |
|---|---|
| Frontend | React 18 + Vite + TypeScript + shadcn/ui + Tailwind |
| Map | Leaflet + OpenStreetMap |
| Backend | Go 1.23 + chi router |
| Database | PostgreSQL 16 + PostGIS 3.4 |
| Object storage | MinIO AIStore (S3-compatible, local) |
| Migrations | golang-migrate |
| Orchestration | Docker Compose |

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and Docker Compose v2
- Go 1.23+ (local dev only)
- Node.js 22+ (local dev only)
- MinIO AIStore license file (see [MinIO AIStore note](#minio-aistore-note))

## Quickstart (Docker)

```bash
cp .env.example backend/.env
docker compose up --build
```

| Service | URL |
|---|---|
| Frontend | http://localhost:5173 |
| Backend API | http://localhost:8080/api |
| MinIO console | http://localhost:9001 |
| PostgreSQL | localhost:5432 |

MinIO default credentials: `minioadmin` / `minioadmin`

### MinIO AIStore note

This project uses the MinIO AIStore edition (`quay.io/minio/aistor/minio`), which requires a license file. Obtain a license from [min.io](https://min.io/pricing) and set its path in `.env`:

```
MINIO_LICENSE_PATH=/path/to/minio.license
```

The default is `$HOME/minio/minio.license`.

## Local development (no Docker build)

Run only the infrastructure services in Docker, then start the backend and frontend natively for faster iteration:

```bash
# 1. Start DB + MinIO
docker compose up db minio minio-init

# 2. Backend (runs migrations automatically on startup)
cd backend
cp ../.env.example .env   # or set DATABASE_URL etc. in shell
go run ./cmd/server

# 3. Frontend (proxies /api to localhost:8080)
cd frontend
npm install
npm run dev
```

## Project structure

```
geogallery/
├── backend/
│   ├── cmd/server/         # main entrypoint
│   ├── internal/
│   │   ├── config/         # env-based config
│   │   ├── db/             # pgx pool + migration runner
│   │   ├── geo/            # GPS interpolation, Haversine
│   │   ├── handlers/       # HTTP handlers (media, trips, game, ingest, gps_tracks, gps_matcher)
│   │   ├── ingest/         # EXIF extraction
│   │   └── storage/        # S3/MinIO client
│   └── migrations/         # golang-migrate SQL files
├── frontend/
│   └── src/
│       ├── api/            # typed fetch wrappers
│       ├── components/     # reusable UI (map, viewer, shadcn/ui)
│       ├── pages/          # MapPage, TripsPage, GamePage, UploadPage
│       └── types/          # shared TypeScript types
├── ingest/                 # drop media files here for bulk ingest
├── docker-compose.yml
└── .env.example
```

## API endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/health` | Health check |
| `GET` | `/api/media` | List media (latest 500); filters: `from_date`, `to_date`, `trip_id[]`, `media_type[]` |
| `POST` | `/api/media/upload` | Upload files (multipart, field `files[]`, `file_mtime[]`) |
| `GET` | `/api/media/{id}/url` | Get presigned URL for a media item |
| `PATCH` | `/api/media/{id}/view-settings` | Set 360° default view (`initial_yaw`, `initial_pitch`, `initial_fov`) |
| `GET` | `/api/media/{id}/keyframes` | Get video orientation keyframes |
| `PUT` | `/api/media/{id}/keyframes` | Replace all keyframes for a video |
| `GET` | `/api/trips` | List trips |
| `PATCH` | `/api/trips/{id}` | Rename a trip `{"name":"..."}` |
| `DELETE` | `/api/trips/{id}` | Delete a user-edited trip (media unassigned, re-detected automatically) |
| `GET` | `/api/game/round` | Get a random geolocated media item; supports same filters as `/api/media` |
| `POST` | `/api/game/guess` | Submit a guess, receive scored result |
| `POST` | `/api/ingest/scan` | Scan `ingest/` folder and import new files |
| `POST` | `/api/gps-tracks/import` | Bulk-insert GPS track points (JSON array) |
| `POST` | `/api/gps-matcher/run` | Re-run GPS matcher over unmatched media |

## Bulk ingest from local folder

Copy media files into `./ingest/`, then trigger a scan:

```bash
cp /your/photos/*.jpg ./ingest/
curl -X POST http://localhost:8080/api/ingest/scan
```

The scanner reads EXIF, extracts GPS and timestamps, uploads to MinIO, and inserts into the database. Files are deduplicated by a partial hash (first + last 64 KB), so re-scanning the same files is always safe.

## Importing external GPS tracks

Post a JSON array to `/api/gps-tracks/import`:

```bash
curl -X POST http://localhost:8080/api/gps-tracks/import \
  -H "Content-Type: application/json" \
  -d '[{"recorded_at":"2024-08-15T14:32:00Z","lat":47.6,"lng":19.0,"source":"google_timeline"}]'
```

Each point accepts: `recorded_at` (RFC3339, required), `lat`, `lng`, `source` (required), `altitude_m`, `accuracy_m`, `activity`. Duplicate points are silently skipped. The GPS matcher runs automatically after import to interpolate locations for media without EXIF GPS.

## Trip auto-detection

Trips are auto-grouped by time gaps and run automatically at the end of every ingest or GPS track import. The gap threshold is controlled by `TRIP_GAP_HOURS` (default: 24h).

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

All variables have defaults that work out of the box. Copy `.env.example` to `.env` to customise.

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
| `GPS_MATCH_WINDOW_HOURS` | `1` | Max hours to search for bracketing GPS track points |
| `TRIP_GAP_HOURS` | `24` | Time gap (hours) that splits media into separate trips |
