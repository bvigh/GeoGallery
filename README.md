# GeoGallery

A personal map-based media gallery and GeoGuessr-like game for reviewing trip photos, videos, and 360° media.

- Browse media on an interactive map (Leaflet/OSM) with markers at each recording location
- GPS resolved from EXIF metadata, or interpolated from imported external tracks (Google Timeline, Strava, etc.) using timestamp-based matching with confidence scoring
- Auto-detected trips grouped by time gaps, with manual rename
- Game mode: guess where and when a photo/video was taken, scored on distance and date accuracy

## Stack

| Layer | Technology |
|---|---|
| Frontend | React 18 + Vite + TypeScript + shadcn/ui + Tailwind |
| Map | Leaflet + OpenStreetMap |
| Backend | Go 1.23 + chi router |
| Database | PostgreSQL 16 + PostGIS 3.4 |
| Object storage | MinIO (S3-compatible, local) |
| Migrations | golang-migrate |
| Orchestration | Docker Compose |

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/) and Docker Compose v2
- Go 1.23+ (local dev only)
- Node.js 22+ (local dev only)

## Quickstart (Docker)

```bash
cp .env.example .env
docker compose up --build
```

| Service | URL |
|---|---|
| Frontend | http://localhost:5173 |
| Backend API | http://localhost:8080/api |
| MinIO console | http://localhost:9001 |
| PostgreSQL | localhost:5432 |

MinIO default credentials: `minioadmin` / `minioadmin`

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
│   │   ├── handlers/       # HTTP handlers (media, trips, game, ingest)
│   │   ├── ingest/         # EXIF extraction
│   │   └── storage/        # S3/MinIO client
│   └── migrations/         # golang-migrate SQL files
├── frontend/
│   └── src/
│       ├── api/            # typed fetch wrappers
│       ├── components/     # reusable UI (map, shadcn/ui)
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
| `GET` | `/api/media` | List all media (latest 500) |
| `POST` | `/api/media/upload` | Upload files (multipart, field `files`) |
| `GET` | `/api/trips` | List auto-detected trips |
| `PATCH` | `/api/trips/{id}` | Rename a trip `{"name":"..."}` |
| `GET` | `/api/game/round` | Get a random media item for the game |
| `POST` | `/api/game/guess` | Submit a guess, receive scored result |
| `POST` | `/api/ingest/scan` | Scan `ingest/` folder and import new files |

## Bulk ingest from local folder

Copy media files into `./ingest/`, then trigger a scan:

```bash
cp /your/photos/*.jpg ./ingest/
curl -X POST http://localhost:8080/api/ingest/scan
```

The scanner reads EXIF, extracts GPS and timestamps, uploads to MinIO, and inserts into the database. Already-ingested filenames are skipped.

## Importing external GPS tracks

The `gps_tracks` table stores continuous GPS data from Google Timeline, Strava, or any other source. Schema:

```sql
-- source: 'google_timeline' | 'strava' | etc.
INSERT INTO gps_tracks (source, recorded_at, location, altitude_m, accuracy_m)
VALUES ('google_timeline', '2024-08-15T14:32:00Z',
        ST_SetSRID(ST_MakePoint(lng, lat), 4326), 120, 5);
```

After importing tracks, run the GPS matcher (not yet wired as an endpoint — see [AGENTS.md](AGENTS.md)) to interpolate locations for media that has no EXIF GPS.

## Trip auto-detection

Trips are auto-grouped by time gaps. Trigger from psql:

```sql
SELECT auto_detect_trips(24);  -- 24h gap = new trip
```

After running, trips appear in the UI with a default name (`Trip YYYY-MM-DD`). Rename them from the Trips tab.

## Scoring (game mode)

| Component | Max | Decay |
|---|---|---|
| Location | 5000 pts | Linear to 0 at 2500 km |
| Date | 5000 pts | Linear to 0 at 365 days off |
| Total | 5000 pts | Average of both components |

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
| `S3_REGION` | `us-east-1` | S3 region label (MinIO ignores it) |
| `SERVER_PORT` | `8080` | Backend HTTP port |
| `FRONTEND_PORT` | `5173` | Frontend container port |
