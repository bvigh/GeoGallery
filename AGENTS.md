# AGENTS.md — GeoGallery

AI agent context for GeoGallery — a single-user personal media gallery and GeoGuessr-like game.

## Context

Media files (photos, videos, 360° content) are stored in MinIO AIStore (S3-compatible), metadata in PostGIS, served by a Go backend to a React/Vite frontend. No auth layer — single-user only. The ingest pipeline extracts EXIF, detects 360° content, and resolves GPS coordinates; mistakes here corrupt media metadata permanently.

## Key Paths

| Path | Purpose |
|---|---|
| `backend/cmd/server/main.go` | Entrypoint and route wiring |
| `backend/internal/config/config.go` | All env config — add new vars here |
| `backend/internal/db/db.go` | pgx pool + golang-migrate runner |
| `backend/internal/handlers/` | One file per domain: media, trips, game, ingest, gps_tracks, gps_matcher |
| `backend/internal/handlers/register.go` | `MediaRegistrar.registerMedia` — shared ingest path for upload and scan |
| `backend/internal/handlers/gps_matcher.go` | `RunGPSMatcher` — interpolates GPS from `gps_tracks` into `media.location` |
| `backend/internal/handlers/trip_detect.go` | `RunTripDetect` — groups media into trips by time gap |
| `backend/internal/ingest/exif.go` | EXIF extraction + 360° detection + `DetectMediaType` |
| `backend/internal/geo/gps.go` | GPS interpolation (Haversine + confidence scoring) |
| `backend/internal/storage/s3.go` | S3/MinIO client wrapper |
| `backend/migrations/` | golang-migrate SQL, numbered `000001_…` |
| `frontend/src/api/client.ts` | Typed fetch wrapper — all backend calls go here |
| `frontend/src/types/index.ts` | Canonical TypeScript types shared across the app |
| `frontend/src/components/map/` | Leaflet map components |
| `frontend/src/components/viewer/` | `Panorama360.tsx`, `Video360.tsx` — 360° media viewers |
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
| `cd frontend && npm run test` | Vitest unit tests |
| `cd backend && go test ./...` | Backend unit tests |
| `docker compose up` | Run full stack in Docker |
| `curl -X POST http://localhost:8080/api/ingest/scan` | Trigger bulk ingest of `./ingest/` folder |

## API Routes

| Method | Path | Handler |
|---|---|---|
| `GET` | `/api/media` | List media (max 500, ordered by `taken_at DESC`); supports `from_date`, `to_date`, `trip_id[]`, `media_type[]` filters |
| `POST` | `/api/media/upload` | Multipart upload (field: `files[]`, `file_mtime[]` ms-since-epoch) |
| `GET` | `/api/media/{id}/url` | Get presigned URL for a media item |
| `PATCH` | `/api/media/{id}/view-settings` | Update 360° default view (`initial_yaw`, `initial_pitch`, `initial_fov`) |
| `GET` | `/api/media/{id}/keyframes` | Get video orientation keyframes |
| `PUT` | `/api/media/{id}/keyframes` | Replace all keyframes for a video |
| `GET` | `/api/trips` | List trips with media count + cover key |
| `PATCH` | `/api/trips/{id}` | Rename a trip (`{"name": "..."}`) — sets `user_edited = true` |
| `DELETE` | `/api/trips/{id}` | Delete a trip (only `user_edited = true` trips); unassigns media for re-detection |
| `GET` | `/api/game/round` | Random geolocated media; supports same filters as `/api/media` |
| `POST` | `/api/game/guess` | Submit a guess, returns scores |
| `POST` | `/api/ingest/scan` | Walk `INGEST_WATCH_DIR`, ingest new files |
| `POST` | `/api/gps-tracks/import` | Bulk-insert GPS track points (JSON array of `{recorded_at, lat, lng, source, altitude_m?, accuracy_m?, activity?}`); runs GPS matcher after insert |
| `POST` | `/api/gps-matcher/run` | Re-run GPS matcher over unmatched (or all) media |
| `GET` | `/api/health` | `{"status":"ok"}` |

## Backend Conventions

- Handler structs hold deps (`*pgxpool.Pool`, `*storage.Client`). Construct with `New*Handler(...)`.
- New routes: register in `main.go` inside `r.Route("/api", ...)`.
- New config values: add to `config.go` with a default that works locally without editing `.env`.
- Migrations: `000003_description.up.sql` / `.down.sql` — **never edit existing migration files**.
- PostGIS geometry: `GEOGRAPHY(POINT, 4326)`, `ST_MakePoint(lng, lat)` (longitude first!), read back with `ST_Y` (lat) and `ST_X` (lng).
- S3 keys: `media/{unix_nano}/{filename}`. Serve via presigned URLs only — never expose raw MinIO URLs to the frontend.
- Deduplication: `registerMedia` hashes first + last 64 KB of each file (`file_hash`). `ON CONFLICT (file_hash) DO NOTHING` — re-uploading or re-scanning the same file is always safe.
- Auto-run order after ingest: `RunGPSMatcher` → `RunTripDetect`. Both are called at the end of scan and upload.
- Trip deletion: `DELETE /api/trips/{id}` is only allowed for `user_edited = true` trips. Auto-detected trips are protected; renaming a trip flips it to `user_edited = true`.

## Frontend Conventions

- All backend calls go through `frontend/src/api/client.ts`. Add a typed helper there, never call `fetch` directly in components.
- Types shared across pages/components live in `frontend/src/types/index.ts`.
- Vite dev proxy: `/api` → `http://localhost:8080` (configured in `vite.config.ts`).
- Path alias `@` resolves to `src/` — use `@/components/...` not relative paths.
- shadcn/ui components in `src/components/ui/`. Add new ones with `npx shadcn@latest add <component>`.
- Tailwind CSS variables (colors, radius) defined in `src/index.css`.
- Map tile layer swap: change `<TileLayer>` URL in `src/components/map/MediaMap.tsx`.
- 360° media: `photo_360` renders in `Panorama360.tsx` (Photo Sphere Viewer); `video_360` renders in `Video360.tsx`. View settings (`initial_yaw/pitch/fov`) and keyframes are stored per-media on the backend.

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
DATABASE_URL              # required — no default
S3_ENDPOINT               # default: http://localhost:9000
S3_ACCESS_KEY             # default: minioadmin
S3_SECRET_KEY             # default: minioadmin
S3_BUCKET                 # default: geogallery
S3_REGION                 # default: us-east-1
SERVER_PORT               # default: 8080
INGEST_WATCH_DIR          # default: ./ingest
MIGRATIONS_PATH           # default: ./migrations (read in main.go, not config.go)
GPS_MATCH_WINDOW_HOURS    # default: 1 — max hours to look for bracketing GPS track points
TRIP_GAP_HOURS            # default: 24 — time gap (hours) that splits media into separate trips
```

Copy `.env.example` to `backend/.env` for local development.

**MinIO AIStore note:** the Docker image is `quay.io/minio/aistor/minio` and requires a license file. Set `MINIO_LICENSE_PATH` in `.env` (default: `$HOME/minio/minio.license`). The license is mounted read-only into the container.

## External Dependencies

| Dependency | Purpose |
|---|---|
| PostGIS (PostgreSQL 16) | Media metadata, GPS tracks, trip groupings |
| MinIO AIStore (S3-compatible) | Media file storage; console at `:9001` |

## Database Schema

| Table | Purpose |
|---|---|
| `media` | One row per file. `location` (GEOGRAPHY), `gps_confidence`, `gps_source`, `taken_at`, `storage_key`, `width_px`, `height_px`, `file_hash` (dedup), `initial_yaw/pitch/fov` (360° default view). |
| `trips` | Auto-detected or manually named trip groupings. `user_edited` flag: set when user renames; only `user_edited` trips can be deleted. |
| `gps_tracks` | Raw GPS track points (Google Timeline, Strava, etc.). Indexed on `recorded_at` and location. |
| `video_keyframes` | Per-video orientation keyframes (`time_seconds`, `yaw`, `pitch`, `fov`). FK → `media.id` ON DELETE CASCADE. |

GPS resolution priority: EXIF → timestamp-interpolated from `gps_tracks` → null.

## Unimplemented Work (in priority order)

1. **360° media rendering improvements** — currently renders via Pannellum/Photo Sphere Viewer. The view-settings and keyframe editors exist in the backend; the frontend 360° viewer components exist but the editor UI is not wired up end-to-end.
2. **Pagination** — `GET /api/media` currently hard-limited to 500 rows. Add `?page=`/`?limit=` with cursor-based pagination.
3. **Map clustering** — add `react-leaflet-cluster` or Supercluster when library grows large.
4. **Filename-based timestamp guessing** — parse common filename patterns (e.g. `20230729_210400.jpg`) as `taken_at` fallback after file mtime.
5. **XMP 360° detection** — parse `XMP-GPano:ProjectionType = equirectangular` for reliable 360° detection without depending on filename or aspect ratio.

## Testing

**Tests are required for all new and modified code.**

**Backend — unit tests (exist)**
- `internal/ingest/exif_test.go` — EXIF extraction and media type detection
- `internal/geo/gps_test.go` — GPS interpolation
- `internal/handlers/gps_matcher_test.go`, `gps_matcher_handler_test.go` — GPS matcher logic and handler
- `internal/handlers/gps_tracks_test.go` — GPS track import endpoint
- `internal/handlers/media_test.go`, `upload_scan_matcher_test.go` — media upload/scan/dedup flow
- `internal/handlers/exif_gps_track_test.go` — EXIF GPS track insertion
- Use `testing` + [testify](https://github.com/stretchr/testify) (`assert`/`require`). Test files live alongside source.
- Pure logic tests use no external deps. Handler tests use `net/http/httptest`.
- Use table-driven tests (`[]struct{ input ...; want ... }`).
- Integration/handler tests: use a real test DB (PostGIS), not mocks. Mock/prod divergence has caused issues here before.

**Frontend — unit tests (exist)**
- `src/pages/MapPage.test.tsx`, `TripsPage.test.tsx` — page-level tests
- `src/components/map/FilterPanel.test.tsx`, `MediaMap.test.tsx` — component tests
- Vitest + React Testing Library. Run with `npm run test`.
- Test components that contain logic; pure utility functions in `src/lib/` get unit tests.

## Key Design Decisions

- **Single user, no auth** — no session/JWT layer. Add if multi-user becomes a requirement.
- **MinIO AIStore over standard MinIO** — AIStore edition for performance; requires a license file.
- **golang-migrate over Atlas/goose** — plain SQL files, no DSL, easy to read in git history.
- **Leaflet/OSM over Google Maps** — no API key required, fully offline-capable.
- **chi over Gin/Echo** — handlers are plain `http.Handler`, zero lock-in, composes with stdlib middleware.
- **File mtime as upload fallback** — EXIF `DateTime` preferred; browser sends `file_mtime[]` (ms-since-epoch) so the server can use real file mtime instead of `time.Now()`.
- **Partial hash dedup** — first + last 64 KB + file size hashed for fast collision-resistant dedup without reading the full file.
- **Relative scoring** — game location score is relative to the geographic spread of the media collection (`max_dist_km` from `NewRound`), not a fixed global scale; GPS confidence adjusts tolerance.
