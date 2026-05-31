# GeoGallery MLP Completion

This ExecPlan is a living document. The sections `Progress`, `Surprises & Discoveries`, `Decision Log`, and `Outcomes & Retrospective` must be kept up to date as work proceeds.

This document must be maintained in accordance with the ExecPlan requirements and guidelines in the `execution-plan` skill.

## Purpose / Big Picture

GeoGallery is a self-hosted photo/video gallery where media items appear as map markers at their GPS coordinates. The application also hosts a GeoGuessr-style memory game. Most of the infrastructure is already scaffolded; several features required for the Minimum Lovable Product (MLP) are missing. After this plan is executed a user can: import continuous GPS tracks from Google Maps Timeline or Strava, watch previously un-located media automatically gain locations after the import, see a media preview when clicking a map marker, view 360° panoramas in an interactive spherical viewer, and rely on a test suite to catch regressions.

The MLP is complete when all five milestones below pass their acceptance criteria and the test suite is green.

## Progress

- [ ] Milestone 1 — Backend unit test suite
- [ ] Milestone 2 — GPS track import + GPS matcher + trip auto-detect trigger
- [ ] Milestone 3 — Map side panel: presigned media preview, full-screen, low-confidence marker styling
- [ ] Milestone 4 — 360° rendering with Pannellum + trip date editing
- [ ] Milestone 5 — Frontend unit tests (Vitest + React Testing Library)

## Surprises & Discoveries

(Empty — to be filled as work proceeds.)

## Decision Log

- Decision: Testify is already in go.mod as an indirect dependency; promote it to direct.
  Rationale: The AGENTS.md testing section explicitly calls for testify assert/require.
  Date/Author: 2026-05-30 / ExecPlan author

- Decision: Use `@photo-sphere-viewer/core` (v5) for 360° rendering rather than Pannellum.
  Rationale: Pannellum's npm package is unmaintained and has known React SSR issues. Photo Sphere Viewer v5 ships an official React adapter and is actively maintained. See Milestone 4 for installation detail.
  Date/Author: 2026-05-30 / ExecPlan author

- Decision: GPS matcher is a synchronous function called at the end of Scan and Import, not a background goroutine.
  Rationale: The PRD says the matcher runs automatically "at the end of every ingest scan" and "at the end of every GPS track import". Synchronous execution keeps the API response as the source of truth. For very large libraries (> 10 000 media rows) a background worker could be added in Phase 3; the current scale does not require it.
  Date/Author: 2026-05-30 / ExecPlan author

- Decision: `GET /api/media` will NOT return presigned URLs inline; instead a new `GET /api/media/{id}/url` endpoint generates a presigned URL on demand when a marker is clicked.
  Rationale: Generating 500 presigned URLs on every map load adds ~500 S3 round-trips per page load. On-demand generation is one round-trip per selected item.
  Date/Author: 2026-05-30 / ExecPlan author

## Outcomes & Retrospective

(Empty — to be filled at completion.)

---

## Context and Orientation

The repository lives at the root of this working tree. Key paths:

    backend/cmd/server/main.go          — Go server entrypoint, route wiring
    backend/internal/config/config.go   — All env vars with defaults
    backend/internal/handlers/          — One file per domain: media, trips, game, ingest
    backend/internal/ingest/exif.go     — EXIF extraction, media-type detection
    backend/internal/geo/gps.go         — Interpolate(), HaversineKm(), Point, InterpolationResult
    backend/internal/storage/s3.go      — Upload(), PresignGet(), Delete()
    backend/internal/db/db.go           — pgx pool + golang-migrate runner
    backend/migrations/                 — SQL migration files, numbered 000001…
    frontend/src/api/client.ts          — All backend calls must go through this
    frontend/src/types/index.ts         — Canonical TypeScript types
    frontend/src/components/map/MediaMap.tsx — Leaflet map component
    frontend/src/pages/                 — MapPage, TripsPage, GamePage, UploadPage

**What is already working:**

- `POST /api/media/upload` and `POST /api/ingest/scan` ingest media with EXIF extraction and S3 storage.
- `GET /api/media` returns up to 500 rows with lat/lng.
- `GET /api/trips`, `PATCH /api/trips/{id}` (name only) are wired.
- `GET /api/game/round`, `POST /api/game/guess` implement the scoring game.
- `auto_detect_trips(gap_hours)` SQL function exists in migration `000002`.
- `geo.Interpolate(before, after *Point, at time.Time, maxWindow time.Duration)` is implemented in `geo/gps.go` and returns `InterpolationResult{Lat, Lng, Confidence, Source}`.

**What is missing (MLP gaps):**

1. No `POST /api/gps-tracks/import` endpoint.
2. GPS matcher (`runGPSMatcher`) is never called — media with no EXIF GPS never gets a location.
3. `auto_detect_trips(24)` is never called automatically after scan or import.
4. The map side panel shows metadata only — no media preview image or video.
5. 360° media (`photo_360`, `video_360`) is rendered as a plain `<img>` or `<video>`.
6. No test suite exists anywhere.
7. `PATCH /api/trips/{id}` only accepts `{"name":"..."}` but the PRD requires `start_date` and `end_date` fields too.
8. Low-confidence GPS markers are not visually distinguished on the map.
9. `GPS_MATCH_WINDOW_HOURS` and `TRIP_GAP_HOURS` env vars are referenced in the PRD but not in `config.go`.

**Database schema summary:**

- `gps_tracks(id, source, recorded_at, location GEOGRAPHY(POINT,4326), altitude_m, accuracy_m, created_at)` — indexed on `recorded_at` and `location`.
- `trips(id, name, start_date DATE, end_date DATE, created_at)`.
- `media(id, filename, media_type, storage_key UNIQUE, taken_at, location GEOGRAPHY, gps_confidence, gps_source, camera_model, trip_id FK, width_px, height_px, duration_sec, created_at)`.

**PostGIS geography convention used throughout this codebase:**

- Insert: `ST_SetSRID(ST_MakePoint(lng, lat), 4326)` — longitude first.
- Read lat: `ST_Y(location::geometry)`.
- Read lng: `ST_X(location::geometry)`.

---

## Plan of Work

### Milestone 1 — Backend unit test suite

Create `backend/internal/geo/gps_test.go` and `backend/internal/ingest/exif_test.go` with table-driven tests. These files must live next to the source files they test. Use `github.com/stretchr/testify/assert` for assertions. Run with `cd backend && go test ./...`.

`gps_test.go` must cover:
- Midpoint between two bracketing points 100 m apart → confidence ≥ 0.99.
- Nearest track point 55 min away with maxWindow 60 min → confidence ≤ 0.10.
- No track points within maxWindow → `Source == "none"`, Lat/Lng zero.
- Single `before` point only (nil `after`): within window → interpolated; beyond window → none.
- Single `after` point only (nil `before`): within window → interpolated; beyond window → none.
- Zero time gap between bracketing points → confidence == 1.0.
- `HaversineKm` for known coordinates (e.g. Paris→London ≈ 341 km).

`exif_test.go` must cover `DetectMediaType`. Pass synthetic `ExifData` and filename+ext combos:
- Filename containing "360" → `photo_360`.
- 4000×2000 image (ratio 2.0) with width ≥ 3000 → `photo_360`.
- 3000×1600 image (ratio 1.875, outside 1.95–2.05) → `photo`.
- `.mp4` extension → `video`.
- `.mov` extension with filename "pano.mov" → `video_360`.
- Plain `.jpg` with no 360 signals → `photo`.

### Milestone 2 — GPS track import + GPS matcher + trip auto-detect trigger

**Step 2a — Add env vars to config.go**

Add two new fields to `Config` in `backend/internal/config/config.go`:

    GPSMatchWindowHours int    // GPS_MATCH_WINDOW_HOURS, default 1
    TripGapHours        int    // TRIP_GAP_HOURS, default 24

Use `strconv.Atoi(getEnv(...))` with a fallback integer.

**Step 2b — GPS matcher function**

Create `backend/internal/handlers/gps_matcher.go` (in the `handlers` package for access to `*pgxpool.Pool`) with a single exported function:

    func RunGPSMatcher(ctx context.Context, db *pgxpool.Pool, maxWindow time.Duration) error

The function must:

1. Query all media rows where `gps_source IS NULL` and `location IS NULL`:
       SELECT id, taken_at FROM media WHERE location IS NULL AND gps_source IS NULL AND taken_at IS NOT NULL

2. For each such row, query `gps_tracks` for the nearest point before and after `taken_at` within `maxWindow`:
       SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
       FROM gps_tracks
       WHERE recorded_at <= $1
       ORDER BY recorded_at DESC LIMIT 1

   and:
       SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
       FROM gps_tracks
       WHERE recorded_at >= $1
       ORDER BY recorded_at ASC LIMIT 1

   Convert each result into a `geo.Point{Lat, Lng, At}`.

3. Call `geo.Interpolate(before, after, takenAt, maxWindow)`. If the result `Source == "none"`, skip. Otherwise update:
       UPDATE media
       SET location = ST_SetSRID(ST_MakePoint($1, $2), 4326),
           gps_confidence = $3,
           gps_source = 'interpolated'
       WHERE id = $4

   Parameters: lng ($1), lat ($2), confidence ($3), id ($4).

4. Return any DB-level error; per-row errors can be logged and skipped.

This function must not touch any row where `gps_source` is already `'exif'` or `'manual'` (the WHERE clause ensures this since those rows have `location IS NOT NULL`).

**Step 2c — GPS track import endpoint**

Create `backend/internal/handlers/gps_tracks.go`:

    type GPSTrackHandler struct {
        db         *pgxpool.Pool
        maxWindow  time.Duration
    }

    func NewGPSTrackHandler(db *pgxpool.Pool, maxWindow time.Duration) *GPSTrackHandler

Implement `POST /api/gps-tracks/import`:

    func (h *GPSTrackHandler) Import(w http.ResponseWriter, r *http.Request)

Request body is JSON: `[{"source":"google_timeline","recorded_at":"2023-07-15T14:30:00Z","lat":47.6,"lng":13.2,"altitude_m":500,"accuracy_m":10}]`.

Validation per row:
- `recorded_at` must parse with `time.Parse(time.RFC3339, ...)`.
- `lat` must be between -90 and 90; `lng` between -180 and 180.
- `source` must be non-empty.

Bulk-insert in batches of 1000 using a single `pgx` batch or a `COPY` / unnest approach. Use:

    INSERT INTO gps_tracks (source, recorded_at, location, altitude_m, accuracy_m)
    VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326), $5, $6)
    ON CONFLICT DO NOTHING

Count inserted vs. skipped via `pgconn.CommandTag.RowsAffected()`.

After inserting all rows, call `RunGPSMatcher(ctx, h.db, h.maxWindow)`.

Response: `{"inserted": N, "skipped": N, "errors": [{"row": 3, "reason": "invalid recorded_at"}]}`.

**Step 2d — Call auto_detect_trips and GPS matcher at end of scan**

In `backend/internal/handlers/ingest.go`, at the end of `Scan` (after the `filepath.WalkDir` loop and before encoding the JSON response), add:

    if ingested > 0 {
        RunGPSMatcher(r.Context(), h.db, h.maxWindow)
        h.db.Exec(r.Context(), `SELECT auto_detect_trips($1)`, h.tripGapHours)
    }

Add `maxWindow time.Duration` and `tripGapHours int` fields to `IngestHandler`. Update `NewIngestHandler` to accept them. Update `main.go` to pass `time.Duration(cfg.GPSMatchWindowHours) * time.Hour` and `cfg.TripGapHours`.

**Step 2e — Wire the new route in main.go**

In `main.go`, construct:

    gpsTrackH := handlers.NewGPSTrackHandler(pool, time.Duration(cfg.GPSMatchWindowHours)*time.Hour)

Register:

    r.Post("/gps-tracks/import", gpsTrackH.Import)

**Step 2f — Write integration-style unit tests for the matcher**

Create `backend/internal/handlers/gps_matcher_test.go`. Tests must not mock the database; they use the real `geo.Interpolate` function with hand-crafted `geo.Point` values. Because the full integration test (real PostGIS) is deferred to Phase 3, these tests call `geo.Interpolate` directly with known coordinates and verify that the SQL UPDATE statement would receive the correct parameters. This is acceptable for now. Add a TODO comment noting that handler-level integration tests against a real DB are tracked in the AGENTS.md backlog.

### Milestone 3 — Map side panel with presigned media preview

**Step 3a — New backend endpoint: GET /api/media/{id}/url**

Add to `backend/internal/handlers/media.go`:

    func (h *MediaHandler) GetURL(w http.ResponseWriter, r *http.Request)

Extract `id` with `chi.URLParam(r, "id")`. Query `storage_key` from `media` where `id = $1`. Call `h.storage.PresignGet(ctx, key)`. Return `{"url":"..."}`.

Register in `main.go`:

    r.Get("/media/{id}/url", mediaH.GetURL)

**Step 3b — Update frontend types**

In `frontend/src/types/index.ts`, no changes needed (`url?: string` is already on `MediaItem`).

**Step 3c — Update api/client.ts**

Add:

    async function getMediaURL(id: string): Promise<string> {
      const res = await get<{ url: string }>(`/media/${id}/url`);
      return res.url;
    }

Export it on the `api` object.

**Step 3d — Rewrite the MapPage side panel**

Currently `MapPage.tsx` shows only filename, taken_at, and GPS source when a marker is selected. Replace the panel with a richer component that:

1. On marker click, calls `api.getMediaURL(item.id)` and stores the presigned URL in state.
2. Shows a loading spinner while fetching the URL.
3. For `media_type === 'photo'` or `'photo_360'`: renders `<img src={url} className="w-full rounded object-cover" />`.
4. For `media_type === 'video'` or `'video_360'`: renders `<video src={url} controls className="w-full rounded" />`.
5. Shows filename, date, GPS source, confidence, and trip name (trip_id for now — trip name lookup can come later).
6. Has a "Full screen" button that opens a full-screen overlay (`position: fixed, inset-0, z-50`) with the same media. The overlay closes on Escape or a close button. 360° full-screen is handled in Milestone 4.
7. The panel is dismissible via a close button or pressing Escape.

The panel must handle the case where the presigned URL fetch fails (show an error message in the panel, do not crash).

**Step 3e — Low-confidence marker styling**

In `MediaMap.tsx`, create two `L.Icon` variants:

    const defaultIcon = L.icon({ ... }) // normal blue marker
    const lowConfidenceIcon = L.icon({ ... }) // orange or grey marker

For each marker, pass `icon={item.gps_confidence !== null && item.gps_confidence < 0.5 ? lowConfidenceIcon : defaultIcon}`.

Use SVG data URIs for the icons so there are no external URL dependencies. A simple circle SVG in two colours (e.g. `#3b82f6` blue and `#f97316` orange) works well:

    const svgIcon = (color: string) => L.divIcon({
      html: `<div style="width:12px;height:12px;border-radius:50%;background:${color};border:2px solid white;box-shadow:0 1px 3px rgba(0,0,0,.4)"></div>`,
      className: "",
      iconSize: [12, 12],
      iconAnchor: [6, 6],
    });

### Milestone 4 — 360° rendering + trip date editing

**Step 4a — Install Photo Sphere Viewer**

From the `frontend/` directory:

    npm install @photo-sphere-viewer/core

This package ships ESM and a CSS file. It does not require a special Vite plugin.

**Step 4b — Create PanoramaViewer component**

Create `frontend/src/components/PanoramaViewer.tsx`. This component wraps Photo Sphere Viewer v5's React lifecycle:

    import { useEffect, useRef } from "react";
    import { Viewer } from "@photo-sphere-viewer/core";
    import "@photo-sphere-viewer/core/index.css";

    interface Props { src: string; className?: string; }

    export function PanoramaViewer({ src, className }: Props) {
      const containerRef = useRef<HTMLDivElement>(null);
      const viewerRef = useRef<Viewer | null>(null);

      useEffect(() => {
        if (!containerRef.current) return;
        viewerRef.current = new Viewer({
          container: containerRef.current,
          panorama: src,
          navbar: false,
        });
        return () => {
          viewerRef.current?.destroy();
          viewerRef.current = null;
        };
      }, [src]);

      return <div ref={containerRef} className={className} />;
    }

**Step 4c — Use PanoramaViewer in the map side panel and full-screen overlay**

In the map side panel (updated in Milestone 3), replace the `<img>` for `photo_360` with `<PanoramaViewer src={url} className="w-full h-48 rounded" />`.

In the full-screen overlay, use `<PanoramaViewer src={url} className="w-full h-full" />` for `photo_360` and `video_360`.

For `video_360`, Photo Sphere Viewer also supports video panoramas via the `@photo-sphere-viewer/video-plugin` package; if that is not installed, fall back to a plain `<video>` element (acceptable for MLP).

**Step 4d — Trip date editing**

The PRD (F4.R1) says `PATCH /api/trips/{id}` must accept `name`, `start_date`, and `end_date`. The current `Rename` handler only reads `{"name":"..."}`.

Update `backend/internal/handlers/trips.go`:

    var body struct {
        Name      *string `json:"name"`
        StartDate *string `json:"start_date"` // YYYY-MM-DD or null
        EndDate   *string `json:"end_date"`
    }

Build a dynamic UPDATE using only the non-nil fields. The simplest correct approach for a small struct: use a fixed query with COALESCE:

    UPDATE trips
    SET name       = COALESCE($1, name),
        start_date = COALESCE($2::date, start_date),
        end_date   = COALESCE($3::date, end_date)
    WHERE id = $4

Pass nil pointers directly; pgx maps Go `nil` to SQL `NULL`, making COALESCE leave existing values unchanged.

Require that at least one of the three fields is non-nil; otherwise return HTTP 400.

Update `TripsPage.tsx` to add inline date editing fields alongside the existing name editor. The Trips tab already shows `start_date` and `end_date`; add small `<input type="date">` fields that appear when the edit button is clicked, alongside the name input. On save, call `api.patch(`/trips/${id}`, { name, start_date, end_date })`.

Update `api/client.ts`'s `patch` helper to accept partial types (it already accepts `unknown`; no change needed).

### Milestone 5 — Frontend unit tests

Install Vitest and React Testing Library if not already present. Check `frontend/package.json` for existing test configuration. If absent:

    cd frontend && npm install --save-dev vitest @vitest/ui jsdom @testing-library/react @testing-library/user-event @testing-library/jest-dom

Add to `frontend/vite.config.ts`:

    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test-setup.ts"],
    }

Create `frontend/src/test-setup.ts`:

    import "@testing-library/jest-dom";

Create `frontend/src/lib/score.ts` extracting the scoring display logic (if any) so it can be unit-tested. For MLP, test the one pure utility: the GPS confidence percentage formatting used in MapPage.

Create `frontend/src/components/map/MediaMap.test.tsx`:
- Renders without crashing when `items` is an empty array.
- Renders a marker for each item with a valid lat/lng.
- Items with `lat: null` produce no marker.

Create `frontend/src/pages/TripsPage.test.tsx`:
- Mock `api.get` to return a trip list; assert trip names appear.
- Clicking "Rename" enters edit mode.

Run with:

    cd frontend && npm run test

---

## Concrete Steps

The steps below are ordered; run them in sequence. Working directories are noted before each command.

**Milestone 1**

    cd backend
    go get github.com/stretchr/testify   # promotes to direct dependency

Create `backend/internal/geo/gps_test.go` (content described in Plan of Work above).
Create `backend/internal/ingest/exif_test.go` (content described above).

    cd backend && go test ./internal/geo/... ./internal/ingest/...
    # Expected: PASS, N tests

**Milestone 2**

Edit `backend/internal/config/config.go` — add `GPSMatchWindowHours int` and `TripGapHours int`.
Create `backend/internal/handlers/gps_matcher.go`.
Create `backend/internal/handlers/gps_tracks.go`.
Edit `backend/internal/handlers/ingest.go` — add fields, call matcher and auto_detect_trips at end of Scan.
Edit `backend/cmd/server/main.go` — construct and wire the new handler.

    cd backend && go build ./...
    # Expected: no errors

    cd backend && go test ./...
    # Expected: PASS

Manual smoke test (requires running infrastructure):

    docker compose up db minio minio-init -d
    cd backend && go run ./cmd/server &
    # Import a few synthetic track points:
    curl -X POST http://localhost:8080/api/gps-tracks/import \
      -H "Content-Type: application/json" \
      -d '[{"source":"test","recorded_at":"2023-07-15T14:00:00Z","lat":47.6,"lng":13.2}]'
    # Expected: {"inserted":1,"skipped":0,"errors":[]}

    # Trigger scan (with a file in ./ingest/ whose taken_at is 2023-07-15T14:05:00Z):
    curl -X POST http://localhost:8080/api/ingest/scan
    # Check that the media row now has a location via:
    curl http://localhost:8080/api/media | jq '.[0]'
    # Expected: lat/lng non-null, gps_source "interpolated"

**Milestone 3**

Edit `backend/internal/handlers/media.go` — add `GetURL`.
Edit `backend/cmd/server/main.go` — register `GET /api/media/{id}/url`.
Edit `frontend/src/api/client.ts` — add `getMediaURL`.
Rewrite `frontend/src/pages/MapPage.tsx` — full side panel.
Edit `frontend/src/components/map/MediaMap.tsx` — low-confidence icon styling.

    cd frontend && npm run build
    # Expected: no TypeScript errors

Manual test: start full stack, open http://localhost:5173, click a marker, verify media preview loads.

**Milestone 4**

    cd frontend && npm install @photo-sphere-viewer/core

Create `frontend/src/components/PanoramaViewer.tsx`.
Edit `MapPage.tsx` — use `PanoramaViewer` for 360° types.
Edit `backend/internal/handlers/trips.go` — extend PATCH body.
Edit `frontend/src/pages/TripsPage.tsx` — add date fields to edit mode.

    cd frontend && npm run build && npm run lint
    # Expected: clean

Manual test: upload a `photo_360` file, click its marker, verify it renders as a spherical panorama.

**Milestone 5**

    cd frontend && npm install --save-dev vitest @vitest/ui jsdom @testing-library/react @testing-library/user-event @testing-library/jest-dom

Edit `frontend/vite.config.ts` — add test block.
Create `frontend/src/test-setup.ts`.
Create `frontend/src/components/map/MediaMap.test.tsx`.
Create `frontend/src/pages/TripsPage.test.tsx`.

    cd frontend && npm test
    # Expected: all tests pass

---

## Validation and Acceptance

**Milestone 1 passes when:** `cd backend && go test ./internal/geo/... ./internal/ingest/...` prints all tests passing including the midpoint-confidence ≥ 0.99 case and the 55-min-window ≤ 0.10 case.

**Milestone 2 passes when:**
- `cd backend && go build ./...` has no errors.
- `POST /api/gps-tracks/import` with valid JSON returns `{"inserted":N,"skipped":0,"errors":[]}`.
- `POST /api/gps-tracks/import` with a duplicate row returns `{"inserted":0,"skipped":1,"errors":[]}`.
- After importing track points that bracket a media item's `taken_at`, that media row gains `gps_source = 'interpolated'` and a non-null `location`.
- After `POST /api/ingest/scan`, trips are auto-created (verify via `GET /api/trips` returning at least one trip).

**Milestone 3 passes when:**
- Clicking a map marker shows a media preview image or video in the side panel within 2 seconds.
- Items with `gps_confidence < 0.5` show an orange marker; others show a blue marker.
- The full-screen overlay opens and closes.

**Milestone 4 passes when:**
- A `photo_360` image renders as a draggable spherical panorama (not a flat image) when clicked in the side panel.
- `PATCH /api/trips/{id}` with `{"start_date":"2023-07-01"}` updates only the start date, leaving name and end_date unchanged.
- The Trips tab shows inline date inputs when editing a trip.

**Milestone 5 passes when:** `cd frontend && npm test` exits 0 with all tests passing.

---

## Idempotence and Recovery

Running `go test ./...` repeatedly is safe. All test files are additive.

The GPS matcher UPDATE uses `WHERE id = $1` against a specific media row; running it twice for the same row sets the same value — idempotent.

`auto_detect_trips` deletes only trips named `Trip %` and reassigns `trip_id`. Manually renamed trips survive (their names do not match `Trip %`). Running auto-detection twice is idempotent per the PRD.

`gps_tracks` insert uses `ON CONFLICT DO NOTHING` — re-importing the same file is safe.

---

## Interfaces and Dependencies

**New Go types and functions (all in `backend/`):**

In `internal/handlers/gps_matcher.go`:

    func RunGPSMatcher(ctx context.Context, db *pgxpool.Pool, maxWindow time.Duration) error

In `internal/handlers/gps_tracks.go`:

    type GPSTrackHandler struct { db *pgxpool.Pool; maxWindow time.Duration }
    func NewGPSTrackHandler(db *pgxpool.Pool, maxWindow time.Duration) *GPSTrackHandler
    func (h *GPSTrackHandler) Import(w http.ResponseWriter, r *http.Request)

In `internal/handlers/ingest.go` (modified):

    type IngestHandler struct { db *pgxpool.Pool; storage *storage.Client; watchDir string; maxWindow time.Duration; tripGapHours int }
    func NewIngestHandler(db *pgxpool.Pool, s *storage.Client, watchDir string, maxWindow time.Duration, tripGapHours int) *IngestHandler

In `internal/handlers/media.go` (new method):

    func (h *MediaHandler) GetURL(w http.ResponseWriter, r *http.Request)

In `internal/config/config.go` (modified struct):

    GPSMatchWindowHours int
    TripGapHours        int

**New frontend components:**

    frontend/src/components/PanoramaViewer.tsx   — wraps @photo-sphere-viewer/core Viewer
    frontend/src/components/map/MediaMap.tsx     — modified: divIcon, low-confidence styling

**New npm dependency:**

    @photo-sphere-viewer/core   ^5.x   (production)
    vitest, jsdom, @testing-library/*  (dev)

**New API routes:**

    GET  /api/media/{id}/url        → { url: string }
    POST /api/gps-tracks/import     → { inserted: int, skipped: int, errors: [{row,reason}] }

---

## Artifacts and Notes

**GPS matcher query pattern (two separate queries per media row):**

    -- Before point
    SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
    FROM gps_tracks
    WHERE recorded_at <= $takenAt
    ORDER BY recorded_at DESC
    LIMIT 1;

    -- After point
    SELECT ST_Y(location::geometry), ST_X(location::geometry), recorded_at
    FROM gps_tracks
    WHERE recorded_at >= $takenAt
    ORDER BY recorded_at ASC
    LIMIT 1;

Both queries use the `gps_tracks_recorded_at_idx` B-tree index for fast range lookup.

**Confidence acceptance test values:**

Two bracketing points 100 m apart, media at exact midpoint, maxWindow = 1 h:
- `timeConfidence(0, 1h)` = 1.0 (zero time gap between the two points for the interpolation factor; actually time gap is the span between before and after)
- If before.At = T-1min and after.At = T+1min (total span 2 min), takenAt = T (exact midpoint):
  - `timeFactor = timeConfidence(2min, 60min) = 1 - 2/60 ≈ 0.967`
  - `distFactor = distanceConfidence(0.1km) = 1 - 0.1/100 = 0.999`
  - confidence ≈ 0.967 × 0.999 ≈ 0.966
- To guarantee ≥ 0.99 the test should use a very short time span, e.g. before = T-30s, after = T+30s:
  - timeFactor = 1 - (60s/3600s) ≈ 0.983, distFactor ≈ 0.999, product ≈ 0.982
- Use before = T-5s, after = T+5s, distance 100m: timeFactor = 1 - 10/3600 ≈ 0.9972, product ≈ 0.996. Still below 0.99 threshold? No — 0.996 ≥ 0.99. Acceptable.
- For the 55-min test: before = T-55min, after = nil, maxWindow = 60min → `timeConfidence(55min, 60min) = 1 - 55/60 ≈ 0.083`. That satisfies ≤ 0.10. ✓
