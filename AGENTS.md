# AGENTS.md — GeoGallery

AI agent context for GeoGallery, a multi-user personal media gallery and GeoGuessr-like game. It has invite-only OIDC login, and every user has a private library.

## Context

Media files (photos, videos, 360° content) are stored in MinIO AIStore (S3-compatible) and metadata in PostGIS. A Go backend serves them to a React/Vite frontend. Users sign in through one generic OIDC issuer: Logto Cloud for the hosted instance, Keycloak for dev/E2E. Every media, trip and GPS-track row has an `owner_id`, and every query is scoped to it. Media is ingested by browser upload or by the operator command `server scan --user <id>`. The ingest pipeline extracts EXIF, detects 360° content and resolves GPS coordinates; mistakes here corrupt media metadata permanently. IdP setup: `docs/auth.md`.

## Key Paths

| Path | Purpose |
|---|---|
| `backend/cmd/server/main.go` | Entrypoint, route wiring, operator subcommands (`invite`, `rebind-issuer`, `users`, `scan`) |
| `backend/internal/config/config.go` | All env config — add new vars here |
| `backend/internal/auth/` | OIDC login/callback/logout/me (`handlers.go`), discovery (`oidc.go`), `RequireUser`/`RequireSession`/`RequireAdmin` (`middleware.go`), `Store` interface + `PGStore` (`store.go`), API token handlers (`tokens.go`), session/invite/API token generation + `CanonicalUUID` (`token.go`), invites (`invite.go`), `User`/`Method`/`MustUser` (`user.go`), `safeReturnTo` (`returnto.go`) |
| `backend/internal/auth/oidctest/` | In-process OIDC test issuer |
| `backend/internal/httplog/` | Path-only slog request logger (never logs the query string) |
| `backend/internal/db/db.go` | pgx pool + golang-migrate runner |
| `backend/internal/testdb/` | Throwaway PostGIS databases for integration tests (`New`, `NewEmpty`) |
| `backend/internal/handlers/` | One file per domain: media, trips, game, ingest, gps_tracks, gps_matcher, admin |
| `backend/internal/handlers/visibility.go` | `readableMediaCond`, the single extension point for media read access |
| `backend/internal/handlers/register.go` | `MediaRegistrar.registerMedia` — shared ingest path for upload and scan; `newStorageKey` |
| `backend/internal/handlers/ingest.go` | `RunIngestScan` + `NewIngestDeps` (called by `server scan`; no HTTP route) |
| `backend/internal/handlers/gps_matcher.go` | `RunGPSMatcher` — interpolates GPS from the owner's `gps_tracks` into `media.location` |
| `backend/internal/handlers/trip_detect.go` | `RunTripDetect` — groups one owner's media into trips by time gap |
| `backend/internal/ingest/exif.go` | EXIF extraction + 360° detection + `DetectMediaType` |
| `backend/internal/geo/gps.go` | GPS interpolation (Haversine + confidence scoring) |
| `backend/internal/storage/s3.go` | S3/MinIO client wrapper (presigned GETs, 15 min) |
| `backend/migrations/` | golang-migrate SQL, numbered `000001_…` |
| `deploy/keycloak/geogallery-realm.json` | Dev/E2E Keycloak realm (client + test users alice/bob/carol, self-registration on). Dev only |
| `docs/auth.md` | Identity provider setup (Logto, other providers, production Keycloak), API tokens, port hardening |
| `frontend/src/api/client.ts` | Typed fetch wrapper — all backend calls go here; a 401 redirects to login |
| `frontend/src/types/index.ts` | Canonical TypeScript types shared across the app |
| `frontend/src/components/auth/` | `AuthGate`, `LoginScreen`, `AuthContext` |
| `frontend/src/components/map/` | Leaflet map components |
| `frontend/src/components/viewer/` | `Panorama360.tsx`, `Video360.tsx` — 360° media viewers |
| `frontend/src/pages/` | One file per tab: MapPage, TripsPage, GamePage, UploadPage, SettingsPage (API tokens), AdminPage (users, invites) |
| `frontend/e2e/` | Playwright suite: `playwright.config.ts`, `support.ts` (`E2E_BACKEND_ENV`), `auth.setup.ts`, `*.e2e.ts` |
| `scripts/e2e-reset-db.sh` | Drops and recreates `geogallery_e2e` via `docker compose exec db` |
| `ingest/` | Drop media here, then run `server scan --user <id>` |

## Commands

| Command | Purpose |
|---|---|
| `docker compose --profile dev up -d db minio minio-init keycloak` | Start infrastructure for native dev (DB, MinIO, bucket init, dev Keycloak on `127.0.0.1:8180`) |
| `cd backend && go run ./cmd/server` | Run backend (auto-runs migrations and OIDC discovery on startup) |
| `cd backend && go run ./cmd/server invite [--admin] [--ttl 72h]` | Print a one-time invite link on stdout. `--admin` claims the unclaimed Owner (pre-auth data) if present |
| `cd backend && go run ./cmd/server users` | List users: `id  role  status(active\|disabled\|unclaimed)  email  display_name` |
| `cd backend && go run ./cmd/server scan --user <id> [--dir <path>]` | Ingest `INGEST_WATCH_DIR` (or `--dir`) into that user's library, then run matcher + trip detection. Prints `ingested=N skipped=M failed=K`; exit 1 on any failure or interrupt |
| `cd backend && go run ./cmd/server rebind-issuer --from <old> --to <new>` | Rewrite `users.issuer` after the IdP's issuer URL changed (same `sub`); prints rows changed |
| `docker compose exec backend ./server <invite\|users\|scan\|rebind-issuer> …` | The same subcommands in Docker (`./ingest` is mounted at `/ingest`) |
| `cd frontend && npm install && npm run dev` | Run frontend dev server (proxies `/api` → `VITE_API_TARGET`, default `http://localhost:8080`) |
| `cd frontend && npm run build` | Production build (`tsc -b && vite build`) |
| `cd frontend && npm run lint` | ESLint (known broken: no `eslint.config.*`; fix separately) |
| `cd frontend && npm run test` | Vitest unit tests |
| `cd frontend && npx playwright install chromium` | One-time browser install for E2E |
| `cd frontend && npm run e2e` | Playwright E2E against the dev Keycloak (preconditions below) |
| `cd backend && go test ./...` | Backend unit tests (integration tests skip) |
| `cd backend && TEST_DATABASE_URL='postgres://geogallery:geogallery@localhost:5432/postgres?sslmode=disable' go test ./...` | Backend unit + integration tests against the compose PostGIS |
| `docker compose --profile dev up` | Full stack in Docker with the dev Keycloak; then `docker compose exec backend ./server invite --admin` and sign in as `alice`/`alice` |
| `docker compose up` | Full stack without Keycloak (managed IdP via `OIDC_*` in `.env`) |

Subcommands write results to stdout and logs to stderr. Usage errors exit 2.

**E2E preconditions.**
- The dev stack must be up: `docker compose --profile dev up -d --wait db minio keycloak` plus `docker compose run --rm minio-init`.
- Run **only one E2E run at a time.** Each run rebuilds `backend/.e2e/server`, drops and recreates the `geogallery_e2e` database, and binds fixed ports: backend `8091`, Vite `5174` (`--strictPort`, `reuseExistingServer: false`).
- `E2E_BACKEND_ENV` in `frontend/e2e/support.ts` sets `OIDC_ISSUER_URL`, `OIDC_DISCOVERY_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `PUBLIC_URL`, `DATABASE_URL`, `SERVER_PORT` and `LOG_LEVEL`. Everything else (`S3_*`, `OIDC_SCOPES`, …) comes from the shell, `backend/.env` or the config defaults.

## API Routes

Auth column:
- **public:** no authentication.
- **user:** a session cookie **or** `Authorization: Bearer gg_pat_…`.
- **session-only:** a session cookie; API tokens get 403.
- **admin:** session-only, and the user must be an admin.

`http.CrossOriginProtection` runs on all of `/api`. A non-owned resource returns 404.

| Method | Path | Auth | Handler |
|---|---|---|---|
| `GET` | `/api/health` | public | `{"status":"ok"}` |
| `GET` | `/api/auth/login` | public | Start OIDC login. Query: `return_to` (validated by `safeReturnTo`), `invite`, `prompt` (`login`/`select_account`; forced to `login` with an invite) |
| `GET` | `/api/auth/callback` | public | OIDC callback. Creates the session or redirects to `/?auth_error=not_invited\|invite_invalid\|disabled` |
| `POST` | `/api/auth/logout` | public | Delete session, clear cookie, `{"redirect_url": end_session URL with id_token_hint, or "/"}` |
| `GET` | `/api/auth/me` | user | `{id, email, display_name, role}` |
| `GET` | `/api/media` | user | List the caller's media (max 500, `offset`, ordered by `taken_at DESC`); supports `from_date`, `to_date`, `trip_id[]` (incl. `unassigned`), `media_type[]` filters; 400 on a malformed date or `trip_id` |
| `POST` | `/api/media/upload` | user | Multipart upload (field: `files[]`, `file_mtime[]` ms-since-epoch); body limit 4 GiB |
| `GET` | `/api/media/{id}/url` | user | Get presigned URL for a media item |
| `PATCH` | `/api/media/{id}/location` | user | Manual location `{lat, lng}`: sets `gps_source='manual'`; if `taken_at` is known, also upserts a `manual` GPS track point and re-runs the matcher; 404 if not owned |
| `PATCH` | `/api/media/{id}/view-settings` | user | Update 360° default view (`initial_yaw`, `initial_pitch`, `initial_fov`); 404 if not owned |
| `GET` | `/api/media/{id}/keyframes` | user | Get video orientation keyframes (`[]` for missing or non-owned IDs) |
| `PUT` | `/api/media/{id}/keyframes` | user | Replace all keyframes for a video |
| `DELETE` | `/api/media/{id}` | session-only | Delete media from DB and MinIO (keyframes cascade); S3 delete is best-effort, logged on failure |
| `GET` | `/api/trips` | user | List the caller's trips with media count + cover key (`[]` when none) |
| `PATCH` | `/api/trips/{id}` | user | Edit a trip (`{"name"?, "start_date"?, "end_date"?}`, at least one) — sets `user_edited = true` |
| `DELETE` | `/api/trips/{id}` | session-only | Delete a trip (only `user_edited = true` trips); unassigns media for re-detection |
| `GET` | `/api/game/round` | user | Random geolocated media of the caller; supports same filters as `/api/media`; 404 when none, 400 on malformed filters |
| `POST` | `/api/game/guess` | user | Submit a guess, returns scores |
| `POST` | `/api/gps-tracks/import` | user | Bulk-insert GPS track points (JSON array of `{recorded_at, lat, lng, source, altitude_m?, accuracy_m?, activity?}`, body limit 100 MiB); returns `{inserted, skipped, errors}`; runs GPS matcher after insert |
| `POST` | `/api/gps-matcher/run` | user | Re-run GPS matcher over the caller's media that has no EXIF or manual location (including previously interpolated rows) |
| `GET` | `/api/tokens` | session-only | List the caller's API tokens (never hash or plaintext) |
| `POST` | `/api/tokens` | session-only | `{"name", "expires_in_days"?: 1–365, default 90}` → 201 with plaintext `token` once; max 25 active (409) |
| `DELETE` | `/api/tokens/{id}` | session-only | Revoke (soft delete); 404 for non-UUID, foreign or already revoked |
| `GET` | `/api/admin/users` | admin | List users |
| `PATCH` | `/api/admin/users/{id}` | admin | `{"disabled": bool}` only; disabling deletes sessions and revokes tokens and the user's unused invites; self-disable → 400 |
| `POST` | `/api/admin/invites` | admin | `{"role": "user"\|"admin"}` → 201 `{"url": "..."}` (72 h; never a claim invite) |

There is no HTTP ingest route. Folder scans are `server scan --user <id>` only.

## Backend Conventions

- Handler structs hold deps (`*pgxpool.Pool`, `*storage.Client`). Construct with `New*Handler(...)`.
- New routes: register in `newRouter` in `main.go`, inside `r.Route("/api", ...)`. Put them in the right group: the `RequireUser` group by default, or the nested `RequireSession` group for anything irreversible or security-sensitive, with `RequireAdmin` nested inside for admin routes.
- **Owner scoping.** Every handler behind `RequireUser` gets the caller with `auth.MustUser(r.Context())`. It panics if the route is not behind the middleware, and `Recoverer` turns that into a loud 500 instead of an unscoped query.
  - Every SQL read and write filters on the owner, and every insert writes `owner_id`.
  - A resource the caller does not own returns **404, never 403**.
  - Media **reads** use `readableMediaCond(alias, n)` from `visibility.go`, the extension point for future sharing. Write paths keep a literal `owner_id = $n`.
  - `RunGPSMatcher`, `RunTripDetect` and `RunIngestScan` take an `ownerID`. Never match one user's media against another user's tracks.
- **Auth methods.** `auth.MethodFrom(ctx)` is `MethodSession` (cookie) or `MethodToken` (Bearer `gg_pat_…`). The zero value `MethodNone` never counts as a session. Session-only routes: `DELETE /api/media/{id}`, `DELETE /api/trips/{id}`, `/api/tokens*`, `/api/admin/*`. A `Bearer` header is authoritative; other schemes fall through to the cookie.
- **Secrets in logs.** Never log tokens (session, invite, API), ID tokens, query strings or `Authorization` headers. Invite tokens and OIDC `code`/`state` travel in query strings, which is why the request logger (`internal/httplog`) logs `r.URL.Path` only. Tokens are stored as sha256 hashes.
- New config values: add to `config.go` with a default that works locally without editing `.env`. `OIDC_ISSUER_URL` is used verbatim; never normalise it.
- Logging: use `log/slog` (stdlib). `slog.SetDefault` is called in `main.go` — call `slog.Error/Warn/Info/Debug` directly anywhere in the backend. Always log before returning a 500: `slog.Error("context: what failed", "err", err)`. Per-item loops that continue on error must log at Error level, not silently skip.
- Migrations: `000003_description.up.sql` / `.down.sql` — **never edit existing migration files**.
- PostGIS geometry: `GEOGRAPHY(POINT, 4326)`, `ST_MakePoint(lng, lat)` (longitude first!), read back with `ST_Y` (lat) and `ST_X` (lng).
- **S3 keys** for new uploads: `media/{owner_id}/{32 random hex chars}{.ext}`, built by `newStorageKey`. The extension is lowercased and kept only if supported. Never put the filename or a timestamp in a key: presigned URLs expose the key and would leak game hints. The original name stays in `media.filename`. Older objects keep their legacy `media/{unix_nano}/{filename}` keys. Serve via presigned URLs only (15 min) — the bucket is private; never expose raw MinIO URLs to the frontend.
- Deduplication: `registerMedia` hashes first + last 64 KB of each file (`file_hash`). `ON CONFLICT (owner_id, file_hash) DO NOTHING` — dedup is per owner; re-uploading or re-scanning the same file is always safe.
- Auto-run order after ingest: `RunGPSMatcher` → `RunTripDetect`, for the ingesting owner. Both are called at the end of scan and upload.
- Trip deletion: `DELETE /api/trips/{id}` is only allowed for `user_edited = true` trips. Auto-detected trips are protected; renaming a trip flips it to `user_edited = true`.
- Media deletion: `DELETE /api/media/{id}` removes the DB row (cascades to `video_keyframes`) then best-effort deletes the S3 object. S3 failure is logged but does not roll back the DB transaction — the DB is the source of truth.
- New handlers should validate UUID path params with `auth.CanonicalUUID` before querying, so a malformed ID returns 404, not a Postgres 22P02 500. The admin and token routes already do; the media and trips handlers still pass the raw chi param.

## Frontend Conventions

- All backend calls go through `frontend/src/api/client.ts`. Add a typed helper there, never call `fetch` directly in components. Helpers go through the internal `request()`, which redirects to `/api/auth/login?return_to=…` on 401. `api.me()` opts out and returns `null`.
- The app is wrapped in `AuthGate`. It calls `api.me()`, and renders `LoginScreen` when signed out. Read the current user from `AuthContext`. The Admin tab renders only for `role === "admin"`. This is cosmetic: the backend enforces roles.
- Never persist an API token client-side. The plaintext lives only in SettingsPage component state and is cleared when the dialog closes.
- Types shared across pages/components live in `frontend/src/types/index.ts`.
- Vite dev proxy: `/api` → `process.env.VITE_API_TARGET ?? "http://localhost:8080"` (`vite.config.ts`). Auth relies on same-origin `/api`; the backend sends no CORS headers.
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

The filename used here is the original one (`media.filename`), never the opaque S3 key.

XMP `ProjectionType` tag (most reliable signal) is not yet implemented — requires raw XMP scanning or `exiftool` binary.

## `taken_at` Fallback Priority

Both ingest paths follow the same order:
1. EXIF `DateTime`
2. File modification time (scan: `DirEntry.Info().ModTime()`; upload: `file_mtime[]` form field in ms-since-epoch sent by the browser)

Future: filename-based timestamp guessing (not yet implemented).

## Environment

Only `DATABASE_URL` has no default:

```
DATABASE_URL              # required — no default
S3_ENDPOINT               # default: http://localhost:9000 — presigned URLs point here, so browsers must reach it
S3_ACCESS_KEY             # default: minioadmin (refused when PUBLIC_URL is https)
S3_SECRET_KEY             # default: minioadmin (refused when PUBLIC_URL is https)
S3_BUCKET                 # default: geogallery
S3_REGION                 # default: us-east-1
SERVER_PORT               # default: 8080
INGEST_WATCH_DIR          # default: ./ingest — default dir for `server scan`
MIGRATIONS_PATH           # default: ./migrations (read in main.go, not config.go)
GPS_MATCH_WINDOW_HOURS    # default: 1 — max hours to look for bracketing GPS track points
TRIP_GAP_HOURS            # default: 24 — time gap (hours) that splits media into separate trips
LOG_LEVEL                 # default: info — one of: debug, info, warn, error
LOG_FORMAT                # default: text — text (human-readable) or json (structured/machine-parseable)
PUBLIC_URL                # default: http://localhost:5173 — browser origin, scheme://host[:port]; one trailing / trimmed, any path fails startup; https → Secure + __Host- cookie
OIDC_ISSUER_URL           # default: http://localhost:8180/realms/geogallery — verbatim, must equal discovery issuer byte for byte
OIDC_DISCOVERY_URL        # default: = OIDC_ISSUER_URL (docker-compose.yml defaults it to http://keycloak:8080/realms/geogallery)
OIDC_CLIENT_ID            # default: geogallery
OIDC_CLIENT_SECRET        # default: geogallery-dev-secret (matches dev realm; refused when PUBLIC_URL is https)
OIDC_SCOPES               # default: openid profile email (space-separated)
SESSION_TTL_HOURS         # default: 720 — absolute session lifetime, no sliding refresh
```

Docker Compose only (root `.env`): `POSTGRES_*`, `MINIO_*`, `FRONTEND_PORT` (default 5173), `KEYCLOAK_PORT` (default 8180, bound to 127.0.0.1), `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` (default `admin`/`admin`). The compose backend service does **not** pass through `OIDC_SCOPES`, `SESSION_TTL_HOURS`, `GPS_MATCH_WINDOW_HOURS`, `TRIP_GAP_HOURS`, `LOG_LEVEL` or `LOG_FORMAT`. With a managed IdP, set `OIDC_DISCOVERY_URL` equal to `OIDC_ISSUER_URL`. `S3_ENDPOINT` is hard-coded to `http://minio:9000` in `docker-compose.yml`, a container-only hostname, so presigned URLs do not resolve in browsers. For a hosted instance, edit it there to a browser-reachable S3 host (known gap).

Copy `.env.example` to `backend/.env` for local development. The backend loads it with godotenv, which fills only variables that are unset.

Changing `KEYCLOAK_PORT` or `FRONTEND_PORT` requires matching `redirectUris` / `post.logout.redirect.uris` in the realm file, and `PUBLIC_URL` / `OIDC_ISSUER_URL` must match if set (compose derives them from the ports when unset; native `config.go` defaults assume 5173/8180). Keycloak imports the realm only if it does not exist yet, and its state persists in the `keycloak_data` volume, so remove that volume to re-import. User IDs are pinned, so `sub` values survive.

**MinIO AIStore note:** the Docker image is `quay.io/minio/aistor/minio` and requires a license file. Set `MINIO_LICENSE_PATH` in `.env` (default: `$HOME/minio/minio.license`). The license is mounted read-only into the container. `minio-init` uses `quay.io/minio/aistor/mc` and sets the bucket private (`mc anonymous set none`).

## External Dependencies

| Dependency | Purpose |
|---|---|
| PostGIS (PostgreSQL 16) | Users, sessions, media metadata, GPS tracks, trip groupings |
| MinIO AIStore (S3-compatible) | Media file storage (private bucket); console at `:9001` |
| OIDC provider | Login. Logto Cloud (hosted), Keycloak 26 (`dev` profile, dev/E2E), or any compliant issuer |

## Database Schema

| Table | Purpose |
|---|---|
| `users` | `id`, `issuer` + `subject` (unique pair; both NULL = unclaimed, cannot log in), `email`/`display_name` (display only, never a join key), `role` (`admin`/`user`), `disabled_at`, `last_login_at`. The legacy Owner `00000000-0000-0000-0000-000000000001` (unclaimed admin) owns all pre-auth data until `server invite --admin` claims it. |
| `sessions` | `token_hash` (sha256 of cookie, PK), `user_id` (CASCADE), `id_token` (logout hint only, never sent to the browser), `expires_at`. |
| `invites` | `token_hash` (PK), `role`, `claim_user_id` (CLI-only: bind login to an unclaimed user), `created_by`, `expires_at`, `used_at`, `used_by`. Consumed atomically in `RegisterWithInvite`. |
| `api_tokens` | `id`, `user_id` (CASCADE), `name` (1–100), `token_hash` (sha256, unique), `display_hint` (`gg_pat_` + 6 chars), `expires_at` (always set, ≤ 365 days), `last_used_at` (throttled to 5 min), `revoked_at` (soft delete). |
| `media` | One row per file. `owner_id` (NOT NULL, FK users ON DELETE RESTRICT), `location` (GEOGRAPHY), `gps_confidence`, `gps_source`, `taken_at`, `filename` (original), `storage_key`, `width_px`, `height_px`, `file_hash` (unique per owner), `trip_id`, `initial_yaw/pitch/fov` (360° default view). |
| `trips` | Auto-detected or manually named trip groupings. `owner_id` (NOT NULL), `user_edited` flag: set when user renames; only `user_edited` trips can be deleted. `UNIQUE (id, owner_id)` backs the composite FK. |
| `gps_tracks` | Raw GPS track points (Google Timeline, Strava, EXIF, manual, etc.). `owner_id` (NOT NULL), `UNIQUE (owner_id, source, recorded_at)`. Indexed on `(owner_id, recorded_at)` and location. |
| `video_keyframes` | Per-video orientation keyframes (`time_seconds`, `yaw`, `pitch`, `fov`). FK → `media.id` ON DELETE CASCADE. |

- **Composite trip FK:** `media (trip_id, owner_id) → trips (id, owner_id) ON DELETE SET NULL (trip_id)`. The database itself makes it impossible for media to sit in another owner's trip.
- **Trip detection** is the SQL function `auto_detect_trips(p_owner UUID, gap_hours INT)`. It only touches that owner's auto trips.
- Users are never deleted, only disabled (`ON DELETE RESTRICT` on owned data).
- GPS resolution priority: EXIF → timestamp-interpolated from the owner's `gps_tracks` → null.

## Unimplemented Work (in priority order)

1. **Sharing and albums** — the next plan. Intended shape: a `shares(resource_type, resource_id, grantee_user_id, permission)` table or album membership. `readableMediaCond` becomes `owner_id = $n OR EXISTS (…)`, and write paths stay owner-only.
2. **360° media rendering improvements** — currently renders via Pannellum/Photo Sphere Viewer. The view-settings and keyframe editors exist in the backend; the frontend 360° viewer components exist but the editor UI is not wired up end-to-end.
3. **Pagination** — `GET /api/media` currently hard-limited to 500 rows. Add `?page=`/`?limit=` with cursor-based pagination.
4. **Map clustering** — add `react-leaflet-cluster` or Supercluster when library grows large.
5. **Filename-based timestamp guessing** — parse common filename patterns (e.g. `20230729_210400.jpg`) as `taken_at` fallback after file mtime.
6. **XMP 360° detection** — parse `XMP-GPano:ProjectionType = equirectangular` for reliable 360° detection without depending on filename or aspect ratio.

Also out of scope so far: API token scopes, user deletion/export, multi-issuer login, re-link invites after a `sub`-changing provider migration, and CI.

## Testing

**Tests are required for all new and modified code.**

**Backend — unit tests (no external deps, always run)**
- `internal/ingest/exif_test.go`, `video_test.go` — EXIF extraction, media type detection, video probing
- `internal/geo/gps_test.go` — GPS interpolation
- `internal/config/config_test.go` — `PUBLIC_URL` normalisation, refusing dev secrets over https
- `internal/httplog/httplog_test.go` — logged line has the path, never the query
- `internal/auth/handlers_test.go` — login/callback/logout against `oidctest` + in-memory fake store (`fakestore_test.go`): state/nonce/signature, `return_to`, invites, prompt rules, userinfo fallback, session rotation, logout hint
- `internal/auth/middleware_test.go` — cookie and Bearer cases, `RequireSession`, `RequireAdmin`
- `internal/auth/tokens_test.go`, `invite_test.go`, `oidc_test.go`, `returnto_test.go`
- `cmd/server/main_test.go` — route wiring (token → 403 on session-only routes), `scan` user checks and exit behaviour, `users` table
- `internal/handlers/admin_test.go` — admin handlers
- `internal/handlers/gps_matcher_test.go`, `gps_matcher_handler_test.go` — GPS matcher logic and handler
- `internal/handlers/gps_tracks_test.go` — GPS track import endpoint
- `internal/handlers/media_test.go`, `upload_scan_matcher_test.go` — media list/upload/scan/dedup flow, owner propagation, opaque keys
- `internal/handlers/media_delete_test.go`, `media_location_test.go` — delete and manual location
- `internal/handlers/exif_gps_track_test.go` — EXIF GPS track insertion
- `internal/handlers/game_test.go` — game round/guess: no rows → 404, other DB errors → 500
- Direct handler calls need a user in context: use `withTestUser(r)` (`testuser_test.go`); without it `MustUser` panics and kills the test binary.

**Backend — integration tests (real PostGIS; skipped unless `TEST_DATABASE_URL` is set)**
- `TEST_DATABASE_URL` must be a `postgres://` URL whose user may `CREATE DATABASE` (the compose user is a superuser). `testdb.New(t)` creates a uniquely named, fully migrated database and drops it `WITH (FORCE)` on cleanup. `testdb.NewEmpty(t)` returns an unmigrated DSN.
- `internal/testdb/testdb_test.go`
- `internal/db/migrations_integration_test.go` — migrate to 6, seed, up, assert backfill, down/up round trip
- `internal/auth/store_integration_test.go` — invite consumption and claim races, `SetDisabled` (incl. creator's invites), sessions/tokens refused for inactive users, `RebindIssuer`, token SQL
- `internal/handlers/ownership_integration_test.go` — cross-user isolation on every route, matcher/trip-detection scoping, per-owner dedup, composite FK
- `internal/handlers/ingest_scan_integration_test.go` — `RunIngestScan` ownership, re-run skip, per-owner dedup, failure count
- Use `testing` + [testify](https://github.com/stretchr/testify) (`assert`/`require`). Test files live alongside source.
- Pure logic tests use no external deps. Handler tests use `net/http/httptest`.
- Use table-driven tests (`[]struct{ input ...; want ... }`).
- SQL behaviour must be tested against a real test DB (PostGIS) via `testdb`, not mocks. Mock/prod divergence has caused issues here before.

**Frontend — unit tests**
- `src/App.test.tsx`, `src/api/client.test.ts` (401 redirect; `me()` does not redirect)
- `src/components/auth/AuthGate.test.tsx`, `LoginScreen.test.tsx`
- `src/pages/MapPage.test.tsx`, `TripsPage.test.tsx`, `GamePage.test.tsx`, `UploadPage.test.tsx`, `SettingsPage.test.tsx`, `AdminPage.test.tsx`
- `src/components/map/FilterPanel.test.tsx`, `MediaMap.test.tsx`
- Vitest + React Testing Library. Run with `npm run test`.
- Test components that contain logic; pure utility functions in `src/lib/` get unit tests.

**E2E — Playwright + Keycloak (`frontend/e2e/`, local only, no CI)**
- `npm run e2e`. One-time: `npx playwright install chromium`. Preconditions: see Commands. Only one run at a time.
- Playwright starts two webServers before setup: the backend (build → `scripts/e2e-reset-db.sh` → serve on `:8091`) and Vite on `:5174`. `auth.setup.ts` then runs `./.e2e/server invite` for alice (admin) and bob, logs each in through Keycloak in a fresh browser context, and saves `e2e/.auth/{alice,bob}.json`.
- Specs: anonymous sign-in screen, carol not invited, alice uploads `fixtures/gps.jpg`, bob's empty library, admin tab and invites, logout without a Keycloak prompt (fresh context), and the API token lifecycle (bob).
- Keep `E2E_BACKEND_ENV` the single source for the E2E backend env, so invites target `geogallery_e2e` and print `:5174` links.

## Key Design Decisions

- **Invite-only multi-user with one generic OIDC issuer.** No vendor SDK in Go or React. Social login (Google, Facebook, …) is configured in the IdP. Users are keyed by (configured issuer, `sub`), so adding issuers later would not break the schema. An unknown identity without a valid invite is rejected, and the first admin comes from `server invite --admin`, never from "first login wins".
- **Logto Cloud for the hosted instance; Keycloak as the dev/E2E IdP** and the documented self-host option. The code paths are identical. Supabase Auth and Hanko are not supported (see `docs/auth.md`).
- **Backend-for-frontend sessions.** Same-origin `/api/auth/*` routes. Opaque server-side sessions in an HttpOnly, SameSite=Lax cookie, stored hashed, with a 30-day absolute TTL. Logout and disabling a user take effect immediately, and there is no signing secret to manage. No CORS; `http.CrossOriginProtection` guards state-changing (non-GET/HEAD/OPTIONS) requests.
- **Personal API tokens** (`gg_pat_…`, sha256-hashed, always expiring, ≤ 365 days) for scripts. They cannot delete, manage tokens or reach admin routes.
- **Ownership in the schema.** `owner_id NOT NULL` on media, trips and gps_tracks, a composite trip FK, and per-owner dedup. Non-owned resources return 404. `readableMediaCond` is the single hook for future sharing.
- **Folder ingest is an operator CLI** (`server scan --user`), not an HTTP endpoint: it needs server filesystem access.
- **Opaque S3 keys and a private bucket.** Presigned URLs reveal neither the filename nor the upload time. EXIF inside served files is an accepted game-hint risk.
- **MinIO AIStore over standard MinIO** — AIStore edition for performance; requires a license file.
- **golang-migrate over Atlas/goose** — plain SQL files, no DSL, easy to read in git history.
- **Leaflet/OSM over Google Maps** — no API key required, fully offline-capable.
- **chi over Gin/Echo** — handlers are plain `http.Handler`, zero lock-in, composes with stdlib middleware.
- **File mtime as upload fallback** — EXIF `DateTime` preferred; browser sends `file_mtime[]` (ms-since-epoch) so the server can use real file mtime instead of `time.Now()`.
- **Partial hash dedup** — first + last 64 KB + file size hashed for fast collision-resistant dedup without reading the full file.
- **Relative scoring** — game location score is relative to the geographic spread of the media collection (`max_dist_km` from `NewRound`), not a fixed global scale; GPS confidence adjusts tolerance.
