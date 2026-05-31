# AGENTS.md — GeoGallery

AI agent context for GeoGallery — a single-user personal media gallery and GeoGuessr-like game.

## Context

Media files (photos, videos, 360° content) are stored in MinIO (S3-compatible), metadata in PostGIS, served by a Go backend to a React/Vite frontend. No auth layer — single-user only. The ingest pipeline extracts EXIF, detects 360° content, and resolves GPS coordinates; mistakes here corrupt media metadata permanently.

## Key Paths

| Path | Purpose |
|---|---|
| `backend/cmd/server/main.go` | Entrypoint and route wiring |
| `backend/internal/config/config.go` | All env config — add new vars here |
| `backend/internal/db/db.go` | pgx pool + golang-migrate runner |
| `backend/internal/handlers/` | One file per domain: media, trips, game, ingest |
| `backend/internal/ingest/exif.go` | EXIF extraction + 360° detection + `DetectMediaType` |
| `backend/internal/geo/gps.go` | GPS interpolation (Haversine + confidence scoring) |
| `backend/internal/storage/s3.go` | S3/MinIO client wrapper |
| `backend/migrations/` | golang-migrate SQL, numbered `000001_…` |
| `frontend/src/api/client.ts` | Typed fetch wrapper — all backend calls go here |
| `frontend/src/types/index.ts` | Canonical TypeScript types shared across the app |
| `frontend/src/components/map/` | Leaflet map components |
| `frontend/src/pages/` | One file per tab: MapPage, TripsPage, GamePage, UploadPage |
| `ingest/` | Drop media here for bulk ingest via `POST /api/ingest/scan` |

## Commands

| Command | Purpose |
|---|---|
| `docker compose up db minio minio-init` | Start infrastructure only (DB + MinIO) |
| `cd backend && go run ./cmd/server` | Run backend (auto-runs migrations on startup) |
| `cd frontend && npm install && npm run dev` | Run frontend dev server (proxies `/api` → `:8080`) |
| `cd frontend && npm run build` | Production build (`tsc -b && vite build`) |
| `cd frontend && npm run lint` | ESLint |
| `docker compose up` | Run full stack in Docker |
| `curl -X POST http://localhost:8080/api/ingest/scan` | Trigger bulk ingest of `./ingest/` folder |

## API Routes

| Method | Path | Handler |
|---|---|---|
| `GET` | `/api/media` | List media (max 500, ordered by `taken_at DESC`) |
| `POST` | `/api/media/upload` | Multipart upload (field: `files[]`, `file_mtime[]` ms-since-epoch) |
| `GET` | `/api/trips` | List trips with media count + cover key |
| `PATCH` | `/api/trips/{id}` | Rename a trip (`{"name": "..."}`) |
| `GET` | `/api/game/round` | Random geolocated media for a game round |
| `POST` | `/api/game/guess` | Submit a guess, returns scores |
| `POST` | `/api/ingest/scan` | Walk `INGEST_WATCH_DIR`, ingest new files |
| `GET` | `/api/health` | `{"status":"ok"}` |

## Backend Conventions

- Handler structs hold deps (`*pgxpool.Pool`, `*storage.Client`). Construct with `New*Handler(...)`.
- New routes: register in `main.go` inside `r.Route("/api", ...)`.
- New config values: add to `config.go` with a default that works locally without editing `.env`.
- Migrations: `000003_description.up.sql` / `.down.sql` — **never edit existing migration files**.
- PostGIS geometry: `GEOGRAPHY(POINT, 4326)`, `ST_MakePoint(lng, lat)` (longitude first!), read back with `ST_Y` (lat) and `ST_X` (lng).
- S3 keys: `media/{unix_nano}/{filename}`. Serve via presigned URLs only — never expose raw MinIO URLs to the frontend.

## Frontend Conventions

- All backend calls go through `frontend/src/api/client.ts`. Add a typed helper there, never call `fetch` directly in components.
- Types shared across pages/components live in `frontend/src/types/index.ts`.
- Vite dev proxy: `/api` → `http://localhost:8080` (configured in `vite.config.ts`).
- Path alias `@` resolves to `src/` — use `@/components/...` not relative paths.
- shadcn/ui components in `src/components/ui/`. Add new ones with `npx shadcn@latest add <component>`.
- Tailwind CSS variables (colors, radius) defined in `src/index.css`.
- Map tile layer swap: change `<TileLayer>` URL in `src/components/map/MediaMap.tsx`.

## Media Type Detection

`ingest.DetectMediaType(exifData, filename, ext)` in `internal/ingest/exif.go` — do not call `detectMediaType` (removed). Detection layers:
1. **Filename keywords**: `360`, `pano`, `panorama`, `spherical` → 360° type
2. **EXIF aspect ratio**: width ≥ 3000px and ratio 1.95–2.05 → 360° type
3. **Extension**: `.mp4/.mov/.avi/.mkv` → video, else photo

XMP `ProjectionType` tag (most reliable signal) is not yet implemented — requires raw XMP scanning or `exiftool` binary.

## `taken_at` Fallback Priority

Both ingest paths follow the same order:
1. EXIF `DateTime`
2. File modification time (scan: `DirEntry.Info().ModTime()`; upload: `file_mtime[]` form field in ms-since-epoch sent by the browser)

Future: filename-based timestamp guessing (not yet implemented).

## Environment

Required env vars (only `DATABASE_URL` has no default):

```
DATABASE_URL          # required — no default
S3_ENDPOINT           # default: http://localhost:9000
S3_ACCESS_KEY         # default: minioadmin
S3_SECRET_KEY         # default: minioadmin
S3_BUCKET             # default: geogallery
S3_REGION             # default: us-east-1
SERVER_PORT           # default: 8080
INGEST_WATCH_DIR      # default: ./ingest
MIGRATIONS_PATH       # default: ./migrations (read in main.go, not config.go)
```

Copy `.env.example` to `backend/.env` for local development.

## External Dependencies

| Dependency | Purpose |
|---|---|
| PostGIS (PostgreSQL 16) | Media metadata, GPS tracks, trip groupings |
| MinIO (S3-compatible) | Media file storage; console at `:9001` |

## Database Schema

| Table | Purpose |
|---|---|
| `media` | One row per file. `location` (GEOGRAPHY), `gps_confidence`, `gps_source`, `taken_at`, `storage_key`, `width_px`, `height_px`. |
| `trips` | Auto-detected or manually named trip groupings. |
| `gps_tracks` | Raw GPS track points (Google Timeline, Strava, etc.). Indexed on `recorded_at` and location. |

GPS resolution priority: EXIF → timestamp-interpolated from `gps_tracks` → null.

## Unimplemented Work (in priority order)

1. **Test suite** — no tests exist yet. Adding coverage is a prerequisite for all subsequent work (see Testing section).
2. **GPS matcher background job** — query `gps_tracks` for bracketing points around `media.taken_at`, call `geo.Interpolate(before, after, takenAt, maxWindow)`, write result to `media.location/gps_confidence/gps_source`. Wire as `POST /api/ingest/match-gps` or auto-run at end of scan.
2. **GPS track import endpoint** — `POST /api/gps-tracks/import` accepting JSON/CSV from Google Timeline / Strava exports. Inserts into `gps_tracks`.
3. **Trip auto-detection trigger** — call `SELECT auto_detect_trips(24)` after bulk ingest. SQL function exists in migration `000002`.
4. **360° media rendering** — detect `photo_360`/`video_360` in frontend; render with Pannellum or Photo Sphere Viewer instead of `<img>`.
5. **Pagination** — `GET /api/media` currently hard-limited to 500 rows. Add `?page=`/`?limit=` with cursor-based pagination.
6. **Map clustering** — add `react-leaflet-cluster` or Supercluster when library grows large.
7. **Filename-based timestamp guessing** — parse common filename patterns (e.g. `20230729_210400.jpg`) as `taken_at` fallback after file mtime.
8. **XMP 360° detection** — parse `XMP-GPano:ProjectionType = equirectangular` for reliable 360° detection without depending on filename or aspect ratio.

## Testing

**Tests are required for all new and modified code.** The project currently has no test suite — building it out is the top priority item in Unimplemented Work.

**Backend — unit tests**
- Use the `testing` package + [testify](https://github.com/stretchr/testify) (`assert`/`require`). Test files live alongside source (`exif_test.go` next to `exif.go`).
- Pure logic (EXIF parsing, GPS interpolation, media type detection, scoring) must have unit tests with no external deps.
- Use table-driven tests (`[]struct{ input ...; want ... }`).

**Backend — integration/handler tests**
- Use a real test DB (PostGIS), not mocks. Mock/prod divergence has caused issues here before.
- Spin up via `docker compose up db` or a `TestMain` that starts a throwaway container.
- Test handler behaviour through `net/http/httptest`.

**Frontend**
- Vitest + React Testing Library.
- Test components that contain logic (score display, map marker rendering, upload flow).
- Pure utility functions in `src/lib/` get unit tests.

## Key Design Decisions

- **Single user, no auth** — no session/JWT layer. Add if multi-user becomes a requirement.
- **MinIO over LocalStack** — purpose-built S3, lighter, has a built-in UI.
- **golang-migrate over Atlas/goose** — plain SQL files, no DSL, easy to read in git history.
- **Leaflet/OSM over Google Maps** — no API key required, fully offline-capable.
- **chi over Gin/Echo** — handlers are plain `http.Handler`, zero lock-in, composes with stdlib middleware.
- **File mtime as upload fallback** — EXIF `DateTime` preferred; browser sends `file_mtime[]` (ms-since-epoch) so the server can use real file mtime instead of `time.Now()`.
