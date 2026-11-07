# GeoGallery -- Product Requirement Document

*A self-hosted, map-centric media gallery with GPS-track enrichment and a GeoGuessr-style memory game, designed to help individuals and friends and explore their travel history.*

> **Status (2026-11-07).** This document records the product vision and the requirements the MLP was built against. Planned work lives in [docs/roadmap.md](docs/roadmap.md) and the Codeberg issues, not here. Where the code has moved on since the MLP, [AGENTS.md](AGENTS.md) describes the current behaviour.

---

## 1. Overview

### 1.1 Project Description

GeoGallery is a self-hosted web application that turns a personal media library — photos, videos, and 360° panoramas — into an interactive geographic experience. Media files are uploaded or bulk-ingested, stored in S3-compatible object storage (MinIO for local/self-hosted deployments), and displayed as map markers on a Leaflet/OpenStreetMap base map at the GPS coordinates where each file was recorded.

A core differentiator is GPS enrichment: many camera photos and screen-recorded videos lack embedded EXIF GPS data. GeoGallery resolves this by importing continuous GPS tracks from external sources (Google Maps Timeline, Strava, Garmin, and any SQLite-based export) and using timestamp interpolation to assign a location to every media file, even those with no EXIF location. The interpolated location is accompanied by a confidence score that decays with both the time gap between bracketing track points and the distance between them.

A secondary feature is a GeoGuessr-style memory game: the user is presented a random item from their library and must place a pin on the map for where the photo was taken, and enter a date for when they think it was recorded. Both components are scored and combined into a round total.

**System architecture (local/self-hosted):**

```
Browser (React/Vite/TypeScript)
         │
         │ HTTP /api/*
         ▼
Go backend (chi router)
    │              │
    │ SQL          │ S3 API
    ▼              ▼
PostGIS DB      MinIO
(media, trips,  (media files,
 gps_tracks)     presigned URLs)
```

For cloud deployment, MinIO is replaced by AWS S3 or equivalent. PostGIS runs on a managed PostgreSQL instance. The Go backend is containerised and deployed behind a reverse proxy.

### 1.2 Project Priorities

Ordered from must-optimise (1) to accept-as-good-enough (4):

1. **Correctness of GPS enrichment** — The primary value of GeoGallery over existing photo apps is accurate location assignment for un-tagged media. A wrong location is worse than no location. The interpolation confidence score must be prominently surfaced and low-confidence assignments must be easy to review and override.

2. **Media browsability on the map** — The map view is the main interface. It must handle thousands of markers without performance degradation (clustering required before public release) and must load media quickly via presigned S3 URLs.

3. **Game engagement** — The GeoGuessr-like game is a secondary but distinctive feature. Scoring must feel fair and the reveal (showing the actual location) must be satisfying. This can be iterated post-MLP.

4. **Operational simplicity** — The entire stack must run with `docker compose up`. No managed cloud accounts or API keys required for local use.

### 1.3 Implementation Phases

#### Phase 1 — MLP (current)

The following items complete the MLP:

| **Feature** | **Status** | **Notes** |
|---|---|---|
| Map view with media markers | **Done** | Leaflet + react-leaflet, markers wired to `/api/media`; low-confidence markers styled orange |
| Web upload + folder ingest | **Done** | `POST /api/media/upload` (streaming) + `POST /api/ingest/scan`; dedup by partial file hash |
| EXIF GPS + timestamp extraction | **Done** | `goexif` library, fallback to file mod time (browser sends `file_mtime[]` for uploads) |
| Trip auto-detection + rename + delete | **Done** | `RunTripDetect` called automatically after every ingest; rename sets `user_edited=true`; user-edited trips can be deleted via `DELETE /api/trips/{id}` |
| Game mode (location + date) | **Done** | `/api/game/round` + `/api/game/guess`; location score relative to media collection spread; filter by date/trip/type |
| GPS track import endpoint | **Done** | `POST /api/gps-tracks/import` — JSON array of track points; auto-runs GPS matcher after insert |
| GPS matcher | **Done** | `RunGPSMatcher` in `handlers/gps_matcher.go`; runs after every ingest and GPS import; also triggerable via `POST /api/gps-matcher/run` |
| 360° media rendering | **Done** | Photo Sphere Viewer (photo_360) and video viewer (video_360) in `frontend/src/components/viewer/`; per-media default view and keyframe settings |
| Test suite | **Done** | Backend: geo, exif, GPS matcher, GPS tracks, media handler tests; Frontend: Vitest + RTL for MapPage, TripsPage, FilterPanel, MediaMap |

**Phase 1 is complete.** The table describes the MLP as built. Some of it has changed since: folder ingest is the operator command `server scan --user <id>` (there is no HTTP scan route), and the 360° viewers are built on three.js.

#### Phase 2 — Sharing with Friends: done

Delivered beyond the original plan:

- Invite-only login through one OIDC issuer (Logto Cloud hosted, Keycloak for dev), instead of a shared password. Admins create invites; the first admin comes from `server invite --admin`. See [docs/auth.md](docs/auth.md).
- Albums with members instead of a read-only view. Members see everything in an album and add their own media; only the owner manages it. See [docs/sharing-albums.md](docs/sharing-albums.md).
- A layout that works on phones.

#### Phase 3 — Open-source public release: in progress

- Done: `AGENTS.md`, map marker clustering, paged `/api/media` (`limit` up to 5,000 and `offset`).
- Open: cursor pagination, README refresh and contribution guidelines, published images, CI, `/api/v1`. Tracked in [docs/roadmap.md](docs/roadmap.md) under "Public release".

#### Phase 4 — Hosted multi-user: mostly done

- Done: per-user data isolation (`owner_id` on every row, enforced in the schema), social login through the IdP, any S3-compatible object storage.
- Not planned: billing.

#### Beyond the phases

Further work is planned as milestones in [docs/roadmap.md](docs/roadmap.md): papercuts, time and place correctness, browsing and organisation, sharing and social, the public release, and parked ideas.

**API and backward compatibility:** Phase 1→2 transitions must not break stored media references (S3 keys are immutable once written). Phase 2→3 must not change the `media` or `gps_tracks` schema in a breaking way — add columns, do not remove.

## 2. Software Requirements Definition

Requirements are organized by feature group. Phase 1 MLP requirements are marked **(MLP)**.

### 2.1 Requirement Definition Details

#### F1.R1: Media Ingest — Web Upload (MLP)

**Use cases and implementation scenarios:**
- User navigates to the Upload tab, selects one or more files (photos, videos, 360° content), and clicks Upload.
- The frontend posts a `multipart/form-data` request to `POST /api/media/upload` with field name `files`.
- The backend extracts EXIF data (GPS lat/lng, timestamp, camera model) from each file, uploads the file to MinIO under an opaque key `media/{owner_id}/{random hex}{.ext}` (no filename or time, so presigned URLs give no game hints), and inserts a row into the `media` table.
- If EXIF GPS is present, `media.location` is set immediately and `gps_source = 'exif'`.
- If EXIF GPS is absent, `media.location` is NULL and `gps_source = NULL` pending the GPS matcher job (F2.R1).
- `taken_at` is set from EXIF `DateTime` if present; falls back to file modification time.

**Acceptance criteria:**
- Requests up to the 4 GiB body limit upload without timeout.
- EXIF GPS is extracted for images and videos with embedded coordinates.
- Files with no EXIF GPS are inserted with `location = NULL` (not rejected).
- Duplicate files (same partial hash: first + last 64 KB + file size) are skipped without error.
- Upload response lists each filename with success/skip status.

**Customer solution and architecture fit:**
Users with hundreds of files need bulk upload. The web UI enables this via the `multiple` attribute on the file input. For very large libraries (thousands of files), folder ingest (F1.R2) is the preferred path.

**Deployment/failure conditions:**
- If MinIO is unreachable, upload returns HTTP 500 with a descriptive error. No partial DB inserts.
- If EXIF extraction fails (corrupt file), the file is still uploaded and inserted with NULL location.

**Configuration/telemetry/management:** The request body is limited to 4 GiB. File count per request is unbounded. The Upload page hashes files in the browser first and sends only those not yet in the library.

**Security considerations:** Upload requires a signed-in user (session or API token), and every file goes into the caller's own library. File type is validated by extension and MIME type; no executable extensions accepted.

**Performance/scale:** Upload is synchronous. For > 50 concurrent uploads, consider async queue (Phase 3).

---

#### F1.R2: Media Ingest — Folder Scan (MLP)

**Use cases and implementation scenarios:**
- User copies media files to the `./ingest/` directory (mounted at `/ingest` in the backend container).
- The operator runs `server scan --user <id>` on the server (there is no HTTP route; it needs server filesystem access).
- The backend walks the directory, processes each supported file (`.jpg`, `.jpeg`, `.png`, `.heic`, `.heif`, `.webp`, `.mp4`, `.mov`, `.avi`, `.mkv`), and ingests new files into that user's library using the same pipeline as F1.R1.
- Files already in that user's library (matched by partial file hash) are skipped.

**Acceptance criteria:**
- A directory with 1000 mixed files ingests completely without timeout (async or streaming response).
- Skipped files are counted and reported in the response.
- Unsupported file types (`.txt`, `.pdf`) are silently ignored.
- A second scan of the same directory ingests 0 new files and reports all as skipped (hash-based dedup, not filename-based).

**Deployment/failure conditions:** If MinIO upload fails for one file, the scan continues and reports the failure per-file. Partial progress is not rolled back.

**Configuration/telemetry/management:** Watch directory configurable via `INGEST_WATCH_DIR` env var.

**Security considerations:** The ingest directory is a Docker volume mounted read-only by the backend. Files are not executed; they are read as binary streams.

**Performance/scale:** Scan should process at minimum 10 files/second on typical hardware (I/O bound). For libraries > 10,000 files, scan progress should be streamed or polled via a status endpoint (Phase 3).

---

#### F2.R1: GPS Enrichment — Track Import (MLP)

**Use cases and implementation scenarios:**
- User has a SQLite database or JSON/CSV export from Google Maps Timeline, Strava, or another GPS source.
- User calls `POST /api/gps-tracks/import` with a JSON payload containing an array of track points: `[{source, recorded_at, lat, lng, altitude_m, accuracy_m}]`.
- The backend bulk-inserts into the caller's `gps_tracks`. Duplicate `(owner_id, source, recorded_at)` rows are ignored (`ON CONFLICT DO NOTHING`).
- Since the MLP, track files can also be uploaded as they are (`POST /api/gps-tracks/upload`): GPX, TCX, KML/KMZ, GeoJSON, NMEA, esoko logs, Google Timeline and Samsung Health exports, optionally gzipped. No conversion script is needed.

**Acceptance criteria:**
- A batch of 100,000 track points imports in under 30 seconds.
- Duplicate points are silently deduplicated.
- `recorded_at` must be a valid ISO-8601 timestamp; malformed entries are rejected per-row with an error report.
- Response includes `{inserted: N, skipped: N, errors: [{row, reason}]}`.

**Deployment/failure conditions:** Import is transactional per batch. If the DB connection drops mid-import, the batch is rolled back. Partial imports from prior successful batches are preserved.

**Configuration/telemetry/management:** Batch size configurable (default 1000 rows per transaction).

**Security considerations:** Input is validated before insert. No PostGIS injection vectors — coordinates are parameterised, not interpolated into SQL strings.

**Performance/scale:** `gps_tracks` is indexed on `recorded_at` and `location` (GIST). Import should not require schema changes for libraries up to 10M track points.

---

#### F2.R2: GPS Enrichment — Timestamp Interpolation (MLP)

**GPS source priority (highest to lowest):**
1. `manual` — human override; never overwritten by the matcher.
2. `exif` — coordinates embedded in the file by the recording device; treated as ground truth and never overwritten.
3. `interpolated` — estimated from bracketing `gps_tracks` points by timestamp; used only when EXIF GPS is absent.

**Use cases and implementation scenarios:**
- The matcher runs in two situations: (a) automatically at the end of every ingest (upload or scan), and (b) automatically after a GPS track import (`POST /api/gps-tracks/import`). This ensures that media uploaded before GPS tracks existed gets retroactively enriched when tracks are added later.
- For each media row with `location = NULL` (and `gps_source` is NULL), the backend queries `gps_tracks` for the nearest point before and after `media.taken_at`.
- `geo.Interpolate(before, after, takenAt, maxWindow)` computes a linear interpolated lat/lng and a confidence score (0.0–1.0).
- Confidence decays with time gap (relative to the nearest bracketing point in time) and with distance between bracketing points (up to 10 km).
- The result is written back to `media.location`, `media.gps_confidence`, and `media.gps_source = 'interpolated'`.
- If no track points exist within `maxWindow`, location remains NULL.
- Media that already has a resolved location (`gps_source = 'exif'` or `'manual'`) is never re-evaluated by the matcher.

**Acceptance criteria:**
- A media file recorded at exactly the midpoint between two track points 100 meters apart gets confidence ≥ 0.99.
- A file with the nearest track point 55 minutes away (maxWindow = 60 min) gets confidence ≤ 0.10.
- A file with no track points within maxWindow has `location = NULL` and `gps_source = NULL`.
- The matcher is triggered automatically at the end of every ingest scan.
- The matcher is triggered automatically at the end of every GPS track import — previously un-located media is enriched without requiring a re-scan.
- Manually set locations (`gps_source = 'manual'`) are never overwritten by the matcher.
- EXIF-located media (`gps_source = 'exif'`) is never re-evaluated, even if track data exists for that timestamp.

**Deployment/failure conditions:** If the `gps_tracks` table is empty, the matcher exits early with no errors.

**Configuration/telemetry/management:** `maxWindow` configurable via `GPS_MATCH_WINDOW_HOURS` env var (default 1).

**Security considerations:** No user-facing inputs. Pure DB read/write.

**Performance/scale:** Matcher must handle 10,000 media rows in under 60 seconds. Uses a PostGIS spatial index and a B-tree index on `recorded_at` for range queries.

---

#### F3.R1: Map View — Media Markers (MLP)

**Use cases and implementation scenarios:**
- User opens the Map tab. All media with a resolved `location` is fetched from `GET /api/media` and rendered as markers on the Leaflet map.
- Clicking a marker opens a side panel (not an inline popup) showing a preview of the media file and its metadata. The side panel is persistent — clicking another marker replaces the current preview without closing the panel.
- The side panel displays: media preview (photo thumbnail or video player), filename, date, GPS source, confidence percentage, and trip name if assigned.
- A "Full screen" button in the side panel opens the media in a full-screen overlay. For `photo_360` and `video_360` types, the full-screen view uses the 360° renderer (three.js).
- Media without a location is excluded from the map (but accessible from the Trips tab).

**Acceptance criteria:**
- Map loads with all located media within 3 seconds for a library of 1,000 items.
- Markers are visible at all zoom levels.
- Clicking a marker opens the side panel and shows the correct media preview and metadata.
- The side panel is dismissible (close button or pressing Escape).
- Full-screen mode works for photos, videos, and 360° content.
- The full-screen overlay closes on Escape or a close button.
- Low-confidence GPS locations (< 0.9) are visually distinguished (e.g., different marker colour or icon).

**Deployment/failure conditions:** If the API returns an error, the map renders empty with a visible error banner. No crash.

**Configuration/telemetry/management:** Map tile provider is configurable by changing the `<TileLayer>` URL in `MediaMap.tsx`. Defaults to OpenStreetMap (no API key needed).

**Security considerations:** Presigned MinIO URLs are used for media previews — not raw MinIO endpoints. URLs expire after 15 minutes.

**Performance/scale:** Markers are clustered (`leaflet.markercluster`), and a filter change adds or removes only the markers that changed, in bulk. Without clustering, the Leaflet DOM degrades noticeably above ~2,000 markers.

---

#### F4.R1: Trip Management — Auto-detection and Rename (MLP)

**Use cases and implementation scenarios:**
- After ingest, `SELECT auto_detect_trips(24)` groups media with `taken_at` into trips separated by > 24-hour gaps.
- Each trip is named `Trip YYYY-MM-DD` by default (date of first media item).
- From the Trips tab, user can edit three fields inline: name, start date, and end date. All three are persisted via `PATCH /api/trips/{id}` with body `{name?, start_date?, end_date?}`.
- Editing the start or end date does not re-assign media files to other trips — it only adjusts the trip's date label. Media–trip assignment is recalculated only when auto-detection runs again.
- The date range shown in the Trips tab reflects the manually-set dates if present, falling back to the auto-detected range from media timestamps.

**Acceptance criteria:**
- A library with media from 5 separate trips (each > 24h apart) produces exactly 5 trips after auto-detection.
- Running auto-detection twice produces the same result (idempotent for trips named `Trip *`).
- Trips with manually assigned names are not overwritten by re-running auto-detection.
- Renamed trips survive a server restart.
- Manually-set start and end dates survive a server restart and are not overwritten by re-running auto-detection.
- The Trips tab shows trip name, date range, and media count.
- Editing name, start date, or end date is possible from the same inline editing UI — no separate settings page required.

**Deployment/failure conditions:** If auto-detection fails (DB error), the existing trips are preserved unchanged.

**Configuration/telemetry/management:** Gap threshold configurable via `TRIP_GAP_HOURS` env var (default 24). `auto_detect_trips` SQL function accepts the threshold as a parameter.

**Performance/scale:** Auto-detection on 10,000 media items completes in under 10 seconds (single SQL pass).

---

#### F5.R1: Game Mode — Round and Scoring (MLP)

**Use cases and implementation scenarios:**
- User opens the Game tab and clicks "Start Round".
- `GET /api/game/round` returns a random media item with a resolved location, a presigned URL, and `max_dist_km` (the 95th-percentile pairwise distance across the media collection, computed per round).
- The game can be filtered by date range, trip, album, owner, and media type via query parameters — the same filter set as `GET /api/media`. Media shared through albums can appear in rounds.
- The left half of the screen shows the photo or video. The right half shows a world map (Leaflet).
- User clicks the map to place their location guess, and optionally enters a date guess.
- User clicks "Submit Guess". `POST /api/game/guess` returns the actual location, actual date, distance error (km), date error (days), and component scores.
- The result screen shows the total score, component breakdown, and reveals the actual location on the map.
- User clicks "Next Round" to repeat.

**Acceptance criteria:**
- A guess of exactly the correct location and date scores 5,000 points total.
- Location score decays linearly to 0 at `max_dist_km` (relative to the media collection's geographic spread — not a fixed global constant). GPS confidence adjusts tolerance: lower confidence = more lenient scoring.
- If no date is entered, only the location component counts (total = location score, not averaged).
- Game is unplayable if no media has a resolved location; the start button shows a descriptive message.

**Deployment/failure conditions:** If the API returns no media (empty library or filter matches nothing), the game displays "Upload and locate some media first."

**Configuration/telemetry/management:** Date decay constant (365 days) is hardcoded. Location decay (`max_dist_km`) is computed dynamically from the collection.

**Security considerations:** The game endpoint does not reveal the actual location until after the guess is submitted. The `GET /api/game/round` response contains only the media URL and type — no coordinates.

**Performance/scale:** No special requirements. Each user plays on their own; shared challenges are planned (see the roadmap).

---

#### F6.R1: Trip GPS Track Visualisation (low priority, post-MLP)

**Use cases and implementation scenarios:**
- Some trips have continuous GPS data in `gps_tracks` that corresponds to the trip's time range.
- When a trip is selected (from the Trips tab or the Map tab filtered to a trip), the user can optionally toggle on a GPS track overlay on the map.
- The backend provides `GET /api/trips/{id}/track` which returns the ordered sequence of `gps_tracks` points for the trip's `[start_date, end_date]` time range as a GeoJSON LineString.
- The frontend renders the track as a polyline on the Leaflet map, visually connecting the route taken during the trip.
- The track overlay is optional — toggled by the user, defaulting to off — because not all trips have GPS data.

**Acceptance criteria:**
- Trips with no GPS data in the corresponding time range show no track (the toggle is hidden or disabled, with a tooltip explaining why).
- Trips with GPS data render a polyline connecting all track points in chronological order.
- The track overlay does not interfere with marker click behaviour.
- The track is styled distinctly from markers (e.g., a coloured semi-transparent line).
- Toggling the track off removes the polyline from the map without a page reload.

**Deployment/failure conditions:** If the track query times out or returns too many points (> 50,000), the backend downsamples using a configurable stride or the Ramer-Douglas-Peucker algorithm before returning.

**Configuration/telemetry/management:** Max track points per response configurable via env var (default 10,000). Downsampling strategy is configurable (stride or RDP).

**Security considerations:** No new attack surface. Track data is internal, read from `gps_tracks` table with a time-range filter.

**Performance/scale:** A full-day GPS track at 1-second resolution is ~86,400 points. Downsampling to ≤ 10,000 points keeps the Leaflet polyline responsive. The PostGIS `recorded_at` index makes the time-range query fast even against 10M-row tables.

---

## 3. Customer and User Success

### 3.1 Definition of Use Cases Against Deployment Scenarios

**Deployment scenario:** A self-hosted instance for one person or a group of friends. All services run via `docker compose up` on a personal workstation, home server or small VPS. Users sign in through an OIDC provider; locally, the dev Keycloak (`docker compose --profile dev up`) needs no internet access.

---

**UC1: Bulk-import a travel photo archive**

*Precondition:* User has a directory of thousands of photos and videos from a past trip. Some have EXIF GPS; most do not.

1. User copies files into `./ingest/`
2. The operator runs `server scan --user <id>` (or the user uploads the files on the Upload tab)
3. Backend processes each file: extracts EXIF, uploads to MinIO, inserts into that user's `media`
4. Backend triggers the GPS matcher; media with EXIF GPS shows immediately on the map
5. Media without EXIF GPS shows as unlocated (grey, absent from map)

*Success state:* All files accessible in the gallery; EXIF-located files visible on map within minutes.

---

**UC2: Enrich un-located media with external GPS tracks**

*Precondition:* User has exported their Google Maps Timeline as JSON. Most media from UC1 has no EXIF GPS but was recorded during periods covered by the timeline.

1. User uploads the Timeline export on the Upload tab (`POST /api/gps-tracks/upload`; no conversion needed)
2. Backend parses it and inserts the points into the user's `gps_tracks`
3. GPS matcher runs: for each un-located media row, finds bracketing track points, interpolates, writes location + confidence
4. Previously-grey markers appear on the map with their confidence shown in the popup

*Success state:* > 80% of un-located media gains a location. Remaining un-located items are those recorded outside any tracked period.

---

**UC3: Browse a trip on the map**

*Precondition:* Media has been ingested and located. Auto-detection has grouped it into trips.

1. User opens the Map tab
2. Markers appear at recording locations; user zooms to a country visited on a past trip
3. User clicks a marker; the side panel opens on the right with a media preview and metadata
4. User clicks "Full screen" to view the photo or video at full size (or in 360° mode for panoramas)
5. User closes full screen, clicks a nearby marker to move through the day's route
6. (Optional, if GPS data exists for the trip) User toggles on the GPS track overlay to see the exact route taken as a polyline connecting all track points

*Success state:* User can follow their route through a destination by clicking markers in sequence, previewing media in the side panel, and optionally tracing the exact route via the GPS track overlay.

---

**UC4: Play a memory game round**

*Precondition:* At least one media item has a resolved location.

1. User opens the Game tab and clicks "Start Round"
2. A random photo or video appears on the left; a blank world map appears on the right
3. User places a pin on the map for their location guess, enters a date
4. User clicks "Submit Guess"
5. Result screen shows distance error, date error, scores, and the actual location pinned on the map

*Success state:* User receives a score, sees where they were wrong, and can immediately start another round.

---

**UC5: Edit auto-detected trips**

*Precondition:* `auto_detect_trips(24)` has run after a bulk ingest.

1. User opens the Trips tab; sees trips named `Trip 2023-07-15`, `Trip 2024-02-03`, etc.
2. User recognises a trip, clicks to edit it inline
3. Types a new name ("Iceland 2023"), adjusts the start date to one day earlier (to include travel day photos), and confirms
4. Name and dates update immediately and persist

*Success state:* All trips have meaningful names and correct date ranges within 10 minutes of reviewing the list.

---

### 3.2 Management Strategy

**Admin interface:** The Admin tab (admins only) lists users, disables and re-enables them, and creates invite links. Server configuration is done through environment variables (`.env` file) before startup; changes require a restart.

**APIs:** All functionality is exposed via the REST API at `/api/*`, with a session cookie or a personal API token (`Authorization: Bearer gg_pat_…`, created on the Settings tab). The route list is in [AGENTS.md](AGENTS.md). The API is not versioned yet; `/api/v1/` is planned before a public release.

**CLI tools:** The server binary has operator subcommands: `invite`, `users`, `scan` and `rebind-issuer`. Scripts use the REST API with an API token.

**Monitoring:** No built-in metrics or alerting. The backend logs every request path (never the query string) through `log/slog`, as text or JSON (`LOG_FORMAT`).

**Operational runbooks:**

| **Operation** | **Command** |
|---|---|
| Start all services | `docker compose up` (add `--profile dev` for the dev Keycloak) |
| Create the first admin | `docker compose exec backend ./server invite --admin`, then open the printed link |
| Folder ingest for a user | `docker compose exec backend ./server scan --user <id>` (files in `./ingest`) |
| Import GPS tracks | Upload the track files on the Upload tab, or `POST /api/gps-tracks/upload` with an API token |
| Re-run GPS matcher | `POST /api/gps-matcher/run` (per user) |
| Run trip auto-detection | Runs automatically after every ingest and import. No manual step needed. |
| Reset all data | `docker compose down -v && docker compose up` |
| Run DB migrations manually | `cd backend && go run ./cmd/server` (auto-runs on startup) |

**Backup:** Docker volume `db_data` (PostGIS) and `minio_data` (media files) should be backed up regularly. No backup tooling is included.

---

## 4. References

| **Document** | **Location** |
|---|---|
| README.md | [README.md](README.md) — quickstart, API reference, env variables |
| AGENTS.md | [AGENTS.md](AGENTS.md) — developer/agent guide: current behaviour, routes, conventions |
| Roadmap | [docs/roadmap.md](docs/roadmap.md) — planned work, ranked, with issue numbers |
| Authentication | [docs/auth.md](docs/auth.md) — identity provider setup, API tokens |
| Sharing | [docs/sharing-albums.md](docs/sharing-albums.md) — albums design |
| Deployment | [docs/deployment-hetzner.md](docs/deployment-hetzner.md) — a hosted instance |
| docker-compose.yml | [docker-compose.yml](docker-compose.yml) |
| DB schema (migration 1) | [backend/migrations/000001_init.up.sql](backend/migrations/000001_init.up.sql) |
| Trip auto-detection SQL | [backend/migrations/000002_trip_autodetect.up.sql](backend/migrations/000002_trip_autodetect.up.sql) |
| GPS interpolation logic | [backend/internal/geo/gps.go](backend/internal/geo/gps.go) |
| PostGIS documentation | https://postgis.net/documentation/ |
| Leaflet documentation | https://leafletjs.com/reference.html |
| golang-migrate | https://github.com/golang-migrate/migrate |
