# AGENTS.md — GeoGallery

Guidance for AI agents and developers working in this repository.

## Project overview

GeoGallery is a single-user personal media gallery and GeoGuessr-like game. Media files (photos, videos, 360° content) are stored in MinIO (S3-compatible), metadata in PostGIS, and served by a Go backend to a React/Vite frontend.

## Repository layout

```
backend/cmd/server/main.go          # entrypoint, router wiring
backend/internal/config/            # env config — add new vars here
backend/internal/db/                # pgx pool + golang-migrate runner
backend/internal/geo/gps.go         # GPS interpolation + Haversine
backend/internal/handlers/          # one file per domain (media, trips, game, ingest)
backend/internal/ingest/exif.go     # EXIF extraction (goexif)
backend/internal/storage/s3.go      # S3/MinIO client wrapper
backend/migrations/                 # golang-migrate SQL, numbered 000001_…
frontend/src/api/client.ts          # typed fetch wrapper — all backend calls go here
frontend/src/types/index.ts         # canonical TypeScript types shared across the app
frontend/src/components/map/        # Leaflet map components
frontend/src/pages/                 # one file per tab (MapPage, TripsPage, GamePage, UploadPage)
ingest/                             # drop media here for bulk ingest via POST /api/ingest/scan
```

## How to run locally

```bash
# Infrastructure only (DB + MinIO)
docker compose up db minio minio-init

# Backend
cd backend && go run ./cmd/server

# Frontend
cd frontend && npm install && npm run dev
```

Backend auto-runs migrations on startup. No manual schema setup needed.

## Backend conventions

- **Handlers** live in `backend/internal/handlers/`. Each handler struct holds its deps (`*pgxpool.Pool`, `*storage.Client`). Construct with `New*Handler(...)`.
- **New routes** are registered in `backend/cmd/server/main.go` inside the `r.Route("/api", ...)` block.
- **New config values** go in `backend/internal/config/config.go` and must have a default that works locally without editing `.env`.
- **Migrations** follow `golang-migrate` naming: `000003_description.up.sql` / `000003_description.down.sql`. Never edit existing migration files — add new ones.
- **PostGIS geometry**: all coordinates stored as `GEOGRAPHY(POINT, 4326)` (lon, lat order in `ST_MakePoint`). Read back with `ST_Y` (lat) and `ST_X` (lng).
- **S3 keys** use the pattern `media/{unix_nano}/{filename}`. Presigned URLs are used for serving — do not expose raw MinIO URLs to the frontend.

## Frontend conventions

- All backend calls go through `frontend/src/api/client.ts`. Add a typed helper there rather than calling `fetch` directly in components.
- Types shared between pages/components live in `frontend/src/types/index.ts`.
- Map tile layer is in `frontend/src/components/map/MediaMap.tsx`. Swap the `<TileLayer>` URL there to change providers (OSM → Mapbox → Google Maps).
- shadcn/ui components belong in `frontend/src/components/ui/`. Run `npx shadcn@latest add <component>` to add new ones.
- Tailwind CSS variables (colors, radius) are defined in `frontend/src/index.css`.

## Database schema (summary)

| Table | Purpose |
|---|---|
| `media` | One row per media file. Holds resolved `location`, raw `exif_location`, `gps_confidence`, `gps_source`, `taken_at`, `storage_key`. |
| `trips` | Auto-detected or manually created trip groupings. |
| `gps_tracks` | Raw GPS track points from external sources (Google Timeline, Strava, etc.). Indexed on `recorded_at` and location. |

GPS resolution priority: EXIF → timestamp-interpolated from `gps_tracks` → null.

## Unimplemented work (known gaps)

These are intentionally deferred — implement in order:

1. **GPS matcher background job** — after ingest, query `gps_tracks` for the two bracketing points around `media.taken_at`, call `geo.Interpolate(before, after, takenAt, maxWindow)`, and write the result back to `media.location`, `media.gps_confidence`, `media.gps_source`. Wire as `POST /api/ingest/match-gps` or run automatically at end of scan.

2. **GPS track import endpoint** — `POST /api/gps-tracks/import` accepting a JSON or CSV payload from Google Timeline / Strava exports. Maps to inserts into `gps_tracks`.

3. **Trip auto-detection trigger** — call `SELECT auto_detect_trips(24)` after bulk ingest completes. The SQL function exists in migration `000002`.

4. **360° media rendering** — detect `photo_360` / `video_360` media types in the frontend and render with [Pannellum](https://pannellum.org/) or [Photo Sphere Viewer](https://photo-sphere-viewer.js.org/) instead of a plain `<img>`.

5. **Pagination** — `GET /api/media` currently returns 500 rows. Add `?page=` / `?limit=` query params and cursor-based pagination for large libraries.

6. **Map clustering** — thousands of markers will be slow. Add `react-leaflet-cluster` or `Supercluster` when the library grows.

## Testing

No test suite exists yet. When adding tests:

- Backend: use `testing` + `pgxmock` or a real test DB (preferred — see project decision to avoid mock/prod divergence).
- Frontend: Vitest + React Testing Library.

## Key design decisions

- **Single user, no auth** — no session or JWT layer. Add if multi-user becomes a requirement.
- **MinIO over LocalStack** — MinIO is purpose-built S3, lighter, and has a built-in UI. LocalStack would be overkill.
- **golang-migrate over Atlas/goose** — plain SQL files, no DSL, easy to read in git history.
- **Leaflet/OSM over Google Maps** — no API key required, fully offline-capable, easy tile-layer swap.
- **chi over Gin/Echo** — handlers are plain `http.Handler`, zero lock-in, composes naturally with stdlib middleware.
- **File modification time as fallback timestamp** — EXIF `DateTime` is preferred; if absent, `os.FileInfo.ModTime()` is used as the `taken_at` value for GPS matching.
