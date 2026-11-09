# AGENTS.md — GeoGallery

AI agent context for GeoGallery, a multi-user personal media gallery and GeoGuessr-like game. It has invite-only OIDC login. Every user has a private library and can share items with other users through albums.

## Context

Media files (photos, videos, 360° content) are stored in MinIO AIStore (S3-compatible) and metadata in PostGIS. A Go backend serves them to a React/Vite frontend. Users sign in through one generic OIDC issuer: Logto Cloud for the hosted instance, Keycloak for dev/E2E. Every media, trip and GPS-track row has an `owner_id`, and every query is scoped to it. The one exception is media reads: a user can also read items that sit in an album they are a member of (`readableMediaCond`). Sharing is by reference, so media rows are never copied and `owner_id` never changes. Media is ingested by browser upload or by the operator command `server scan --user <id>`. The ingest pipeline extracts EXIF, detects 360° content and resolves GPS coordinates; mistakes here corrupt media metadata permanently. IdP setup: `docs/auth.md`.

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
| `backend/internal/handlers/` | One file per domain: media, trips, game, ingest, gps_tracks, gps_matcher, albums, admin |
| `backend/internal/handlers/visibility.go` | `readableMediaCond`, the single place that encodes media read access (own, or in an album the caller is a member of) |
| `backend/internal/handlers/filters.go` | `validateMediaFilters` + `buildMediaFilter`, the filters shared by `/api/media` and `/api/game/round` (dates, media types, trips, albums, owner) |
| `backend/internal/handlers/albums.go` | `AlbumHandler`: albums, members, album media, `GET /api/media/{id}/albums`, the `GET /api/users` directory; `authorize` (non-member 404, member on an owner-only action 403) |
| `backend/internal/handlers/register.go` | `MediaRegistrar.registerMedia` — shared ingest path for upload and scan, returns `(mediaID, inserted, err)`; `mediaHash` (the frozen `file_hash` definition); `newStorageKey` |
| `backend/internal/handlers/ingest.go` | `RunIngestScan` + `NewIngestDeps` (called by `server scan`; no HTTP route) |
| `backend/internal/handlers/gps_matcher.go` | `RunGPSMatcher` — interpolates GPS from the owner's `gps_tracks` into `media.location` |
| `backend/internal/handlers/trip_detect.go` | `RunTripDetect` — groups one owner's media into trips by time gap |
| `backend/internal/ingest/exif.go` | EXIF extraction (capture time: see "`taken_at` Fallback Priority") + 360° detection + `DetectMediaType` |
| `backend/internal/goexif/` | Copy of `github.com/rwcarlsen/goexif` (`exif`, `tiff`, its `LICENSE`) with limits on what decoding can allocate and follow (`tiff/budget.go`); `README.md` lists every difference from upstream. `goexiftest/` builds TIFF structures for the limit tests |
| `backend/internal/ingest/exiftest/` | Test helper: builds small JPEGs with chosen EXIF tags (make, capture time, offset, GPS position and timestamp) and optionally an Exif IFD goexif rejects |
| `backend/internal/gpstrack/` | Track file parsers for `POST /api/gps-tracks/upload`: `Parse(name, r)` picks GPX, TCX, KML/KMZ, GeoJSON, NMEA, esoko `.log` or a `.json` format (Google Timeline object; Samsung Health or GeoGallery array) by extension, any of them `.gz`. Unreadable and out-of-range points are skipped |
| `backend/internal/geo/gps.go` | GPS interpolation (Haversine + confidence scoring) |
| `backend/internal/storage/s3.go` | S3/MinIO client wrapper (presigned GETs, 15 min) |
| `backend/migrations/` | golang-migrate SQL, numbered `000001_…`; `000009_albums` adds `albums`, `album_members`, `album_media` and `media_id_owner_key` |
| `deploy/keycloak/geogallery-realm.json` | Dev/E2E Keycloak realm (client + test users alice/bob/carol, self-registration on). Dev only |
| `docs/auth.md` | Identity provider setup (Logto, other providers, production Keycloak), API tokens, port hardening |
| `docs/sharing-albums.md` | Sharing/albums design: decisions, invariants, schema, routes; "Implementation notes" lists where the code differs |
| `docs/roadmap.md` | Planned work: milestones, ranking, dependencies, with a GitHub issue per item |
| `frontend/src/api/client.ts` | Typed fetch wrapper — all backend calls go here; a 401 redirects to login |
| `frontend/src/types/index.ts` | Canonical TypeScript types shared across the app |
| `frontend/src/components/auth/` | `AuthGate`, `LoginScreen`, `AuthContext` |
| `frontend/src/components/map/` | Leaflet map components. `MediaMap` drives a `leaflet.markercluster` group directly (no `<Marker>` per item): an items change diffs by id and adds/removes only what changed, in bulk. `MiniMapOverlay`: the hidden/small/large corner map of the Game and the gallery (a mouse enlarges it on hover) |
| `frontend/src/components/viewer/` | `Panorama360.tsx`, `Video360.tsx` — 360° media viewers (three.js); `sphereControls.ts` (`createSphereView`: renderer, controls, context loss; returns `null` without WebGL), `sphereMath.ts` (FOV across the viewport's shorter side), `gestures.ts` (pinch and tap detection; `tapSlop` shared with `ZoomableImage`), `ViewerHeader.tsx`, `WebGLNotice.tsx` (WebGL unavailable or context lost); `EditViewModal.tsx` (the owner's 360° default view and video keyframe editor, opened from the Map page's item panel and from `MediaGalleryOverlay`); `ZoomableImage.tsx` (pinch, double-tap and wheel zoom, swipe and tap callbacks; maths in `src/lib/zoom.ts`); `VideoDecodeNotice` (the "can't decode, most likely HEVC" box linking `/video-playback.html`) |
| `frontend/src/components/ErrorBoundary.tsx` | Fallback with "Try again" instead of a blank app when rendering throws; `App` wraps the map page, the other tab pages (keyed by tab) and the whole app |
| `frontend/src/lib/useCloseOnBack.ts` | The system back button closes the innermost fullscreen view (one history entry per open view) |
| `frontend/src/lib/dialogKeys.ts` | `keyBelongsToDialog`: a `window` key listener behind a dialog ignores the key (a dialog is open, or the key was already handled: Radix's closing Escape) |
| `frontend/src/components/albums/` | `AlbumMenu` (the viewer's per-item album checkboxes + "New album…"), `AlbumPickerDialog` (Trips "Add to album…", the Upload page's album choice) |
| `frontend/src/components/SearchInput.tsx` | Filter field for long lists (clear button; Enter does nothing, so it never submits a form) |
| `frontend/src/components/MediaGalleryOverlay.tsx` | Full-screen gallery for a title + media query params; shared by the Trips and Albums pages (replaces `TripGalleryOverlay`). Own items get the location editor and, for 360° items, the view editor; while either is open the gallery's keys do nothing but Escape (closes the editor) |
| `frontend/src/lib/filters.ts` | `emptyFilters`, `filterParams` (`FilterState` → `/api/media` and `/api/game/round` params), `activeFilterCount` |
| `frontend/src/lib/errors.ts` | `describeError` — album-aware error text from an `ApiError` status (404 / 403) |
| `frontend/src/lib/albums.ts` | `byRecentUse`: the album pickers' order (`last_added_at`, else `created_at` for own albums, never-used last) |
| `frontend/src/lib/search.ts` | `matchesSearch` (case- and accent-insensitive, every word must match a field) and `SEARCH_MIN_ITEMS` |
| `frontend/src/lib/mediaHash.ts` | `mediaHash`: the backend's `file_hash` computed in the browser (Web Crypto), so the Upload page skips files already in the library; `canHashFiles` |
| `frontend/src/lib/trips.ts` | `groupTripsByYear`: the filter panel's year groups (newest first, undated last, server order inside) |
| `frontend/src/lib/video.ts` | `videoDecodeFailed` (data loaded but a 0×0 picture, or a decode/unsupported media error) and `useVideoDecodeCheck` (handlers for a `<video>`, keyed by src) |
| `frontend/src/lib/videoKeys.ts` | Space (play/pause) and M (mute) in every full-size player: `listenVideoKeys` (`Video360`) and `useVideoKeys` (a `<video controls>`) |
| `frontend/src/pages/` | One file per tab: MapPage, TripsPage ("Add to album…" per trip), AlbumsPage (album cards, members dialog with a searchable user list and leave, gallery), GamePage, UploadPage (duplicate pre-check by hash, optional "Add to album" through `AlbumPickerDialog`; GPS track files sent in batches of ≤ 32 MiB and ≤ 500 files), SettingsPage (API tokens), AdminPage (users, invites) |
| `frontend/public/privacy.html`, `terms.html` | Static privacy policy and terms of service (public, no login; linked from `LoginScreen`, required for Google sign-in). Update them when the data the app stores, its providers or retention change |
| `frontend/public/video-playback.html` | Static guide for browsers that cannot decode HEVC (Chromium on Linux needs GPU decoding through VA-API, or use Firefox ≥ 137); linked from `VideoDecodeNotice` |
| `frontend/e2e/` | Playwright suite: `playwright.config.ts`, `support.ts` (`E2E_BACKEND_ENV`), `auth.setup.ts`, `*.e2e.ts`; `sharing.e2e.ts` runs in its own `sharing` project after all others |
| `scripts/e2e-reset-db.sh` | Drops and recreates `geogallery_e2e` via `docker compose exec db` |
| `.github/workflows/ci.yml` | GitHub Actions on every pull request and push to `main`: the `Backend`, `Frontend` and `E2E` jobs |
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
| `cd frontend && npm run lint` | ESLint (`eslint.config.js`: TypeScript, the classic `react-hooks` rules, `react-refresh`); any warning fails it |
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

`http.CrossOriginProtection` runs on all of `/api`. A non-owned resource returns 404. Media in an album the caller is a member of is **readable** (list, `/url`, keyframes GET, game), but every media write stays owner-only and returns 404 on shared media. Album routes return 404 to a non-member and 403 to a member attempting an owner-only action. A malformed UUID in an album path → 404; in an album request body (`user_id`, `media_ids`, `trip_id`) → 400. Body checks run before the membership check, so a bad body is 400 even for a non-member.

| Method | Path | Auth | Handler |
|---|---|---|---|
| `GET` | `/api/health` | public | `{"status":"ok"}` |
| `GET` | `/api/auth/login` | public | Start OIDC login. Query: `return_to` (validated by `safeReturnTo`), `invite`, `prompt` (`login`/`select_account`; forced to `login` with an invite) |
| `GET` | `/api/auth/callback` | public | OIDC callback. Creates the session or redirects to `/?auth_error=not_invited\|invite_invalid\|disabled` |
| `POST` | `/api/auth/logout` | public | Delete session, clear cookie, `{"redirect_url": end_session URL with id_token_hint, or "/"}` |
| `GET` | `/api/auth/me` | user | `{id, email, display_name, role}` |
| `GET` | `/api/media` | user | List the media the caller can read: their own plus items in albums they are a member of (`limit` 1–5000, default 500; `offset`; ordered by `taken_at DESC NULLS LAST, id`). Gzipped when the client accepts it (the only compressed route). Filters: `from_date`, `to_date`, `media_type[]`, `trip_id[]` (incl. `unassigned`; matches only the caller's own media), `album_id[]` (only albums the caller is a member of; a foreign ID matches nothing), `owner=mine\|shared` (default: everything readable), `has_location=true` (located items only; the map sends it). Ticked trips and albums are ORed together, then ANDed with the rest. Rows carry `owner_id` and `owner_name` (display name, else email); `trip_id` is `null` on rows the caller does not own. 400 on a malformed date, `trip_id`, `album_id`, `owner`, `has_location` or `limit` |
| `POST` | `/api/media/upload` | user | Multipart upload (field: `files[]`, `file_mtime[]` ms-since-epoch); body limit 4 GiB; returns `{uploaded, skipped, failed}` filename lists (`skipped` = per-owner duplicates). Optional query param `?album_id=<uuid>`: a malformed ID or a non-member gets 404 **before** the body is read; new files **and** duplicates are added to the album after the matcher and trip detection, and the response gains `album_added` (rows newly added; present only with `album_id`) |
| `POST` | `/api/media/known-hashes` | user | `{"hashes": [...]}` (1–5000 `file_hash` values, 64 lower-case hex each, else 400) → `{"known": {hash: media_id}}` for the caller's **own** media only (shared media never count). The Upload page's duplicate pre-check |
| `GET` | `/api/media/{id}/url` | user | Get presigned URL for a readable media item (own or shared through an album); 404 otherwise |
| `PATCH` | `/api/media/{id}/location` | user | Manual location `{lat, lng}`: sets `gps_source='manual'`; if `taken_at` is known, also upserts a `manual` GPS track point and re-runs the matcher; 404 if not owned (shared media included) |
| `PATCH` | `/api/media/{id}/view-settings` | user | Update 360° default view (`initial_yaw`, `initial_pitch`, `initial_fov`); 404 if not owned (shared media included) |
| `GET` | `/api/media/{id}/keyframes` | user | Get video orientation keyframes of a readable item (`[]` for missing or unreadable IDs) |
| `PUT` | `/api/media/{id}/keyframes` | user | Replace all keyframes for a video; owner only, 404 on shared media |
| `GET` | `/api/media/{id}/albums` | user | `[{id, name}]`: albums the caller is a member of that contain this item; `[]` for an unreadable or malformed ID (feeds the viewer's album menu) |
| `DELETE` | `/api/media/{id}` | session-only | Delete media from DB and MinIO (keyframes and album contributions cascade, so it disappears for every album member); owner only, 404 on shared media; S3 delete is best-effort, logged on failure |
| `GET` | `/api/trips` | user | List the caller's trips with media count + cover key (`[]` when none) |
| `PATCH` | `/api/trips/{id}` | user | Edit a trip (`{"name"?, "start_date"?, "end_date"?}`, at least one) — sets `user_edited = true` |
| `DELETE` | `/api/trips/{id}` | session-only | Delete a trip (only `user_edited = true` trips); unassigns media for re-detection |
| `GET` | `/api/albums` | user | Albums the caller is a member of: `[{id, name, owner_id, owner_name, is_owner, member_count, media_count, start_date, end_date, created_at, last_added_at}]` (dates = min/max `taken_at`, `null` without dated media; `last_added_at` = the caller's latest contribution, `null` without one), newest first |
| `POST` | `/api/albums` | user | `{"name"}` (trimmed, 1–200 chars) → 201 album; inserts the album and the owner's member row in one transaction |
| `PATCH` | `/api/albums/{id}` | user | Rename `{"name"}` → 204; owner only (member 403, non-member 404) |
| `DELETE` | `/api/albums/{id}` | session-only | Delete the album → 204; owner only; members and contributions cascade, the media themselves stay |
| `GET` | `/api/albums/{id}/members` | user | Member only: `[{user_id, display_name, email, is_owner, added_at}]`, owner first |
| `POST` | `/api/albums/{id}/members` | session-only | Owner only: `{"user_id"}` → 204, idempotent; the target must be an active, claimed user (else 404) |
| `DELETE` | `/api/albums/{id}/members/{user_id}` | session-only | The owner removes another member, or a member removes themselves (leave) → 204. The owner removing themselves → 400; a member removing someone else → 403; a target who is not a member → 404. The cascade removes that member's contributions |
| `POST` | `/api/albums/{id}/media` | user | Member only: exactly one of `{"media_ids": [...]}` (1–5000) or `{"trip_id"}` (a one-time snapshot of the caller's own trip; a foreign trip → 404), else 400 → `{"added": n}`. Only the caller's own media are added; other IDs and items already in the album are skipped silently (no re-sharing) |
| `POST` | `/api/albums/{id}/media/remove` | user | Same body (a `trip_id` resolves only to the caller's own trip) → `{"removed": n}`; the media stay. The owner may remove any item; a member only their own (a request naming someone else's item → 403 for the whole request) |
| `GET` | `/api/game/round` | user | Random geolocated readable media (own + shared); supports the same filters as `/api/media`, including `album_id[]` and `owner`; `max_dist_km` and `bounds` (`[south, west, north, east]`, rounded out to a 0.5° grid; `null` if not measured) describe the same filtered set, never the item; 404 when none, 400 on malformed filters |
| `POST` | `/api/game/guess` | user | Submit a guess on a readable item, returns scores; 404 if the item is no longer readable |
| `POST` | `/api/gps-tracks/import` | user | Bulk-insert GPS track points (JSON array of `{recorded_at, lat, lng, source, altitude_m?, accuracy_m?, activity?}`, body limit 100 MiB); returns `{inserted, skipped, errors}`; runs GPS matcher after insert |
| `POST` | `/api/gps-tracks/upload` | user | Import track files (multipart `files`, body limit 256 MiB) through `gpstrack.Parse`; each point's source is `<format>:<file name>` (a GeoGallery JSON row keeps its own), so a re-upload is skipped. Returns `{inserted, skipped, empty, failed: [{file, reason}]}`: an unreadable file is listed in `failed` and the others still import. Runs the GPS matcher once if any file had points |
| `POST` | `/api/gps-matcher/run` | user | Re-run GPS matcher over the caller's media that has no EXIF or manual location (including previously interpolated rows) |
| `GET` | `/api/tokens` | session-only | List the caller's API tokens (never hash or plaintext) |
| `POST` | `/api/tokens` | session-only | `{"name", "expires_in_days"?: 1–365, default 90}` → 201 with plaintext `token` once; max 25 active (409) |
| `DELETE` | `/api/tokens/{id}` | session-only | Revoke (soft delete); 404 for non-UUID, foreign or already revoked |
| `GET` | `/api/users` | session-only | User directory for the album member picker: `[{id, display_name, email}]` of active, claimed users (`issuer IS NOT NULL AND disabled_at IS NULL`) |
| `GET` | `/api/admin/users` | admin | List users |
| `PATCH` | `/api/admin/users/{id}` | admin | `{"disabled": bool}` only; disabling deletes sessions and revokes tokens and the user's unused invites; self-disable → 400 |
| `POST` | `/api/admin/invites` | admin | `{"role": "user"\|"admin"}` → 201 `{"url": "..."}` (72 h; never a claim invite) |

There is no HTTP ingest route. Folder scans are `server scan --user <id>` only.

## Backend Conventions

- Handler structs hold deps (`*pgxpool.Pool`, `*storage.Client`). Construct with `New*Handler(...)`.
- New routes: register in `newRouter` in `main.go`, inside `r.Route("/api", ...)`. Put them in the right group: the `RequireUser` group by default, or the nested `RequireSession` group for anything irreversible or security-sensitive, with `RequireAdmin` nested inside for admin routes.
- **Owner scoping.** Every handler behind `RequireUser` gets the caller with `auth.MustUser(r.Context())`. It panics if the route is not behind the middleware, and `Recoverer` turns that into a loud 500 instead of an unscoped query.
  - Every SQL read and write filters on the owner, and every insert writes `owner_id`. The exceptions are media reads and album routes, which are scoped by album membership instead (next bullets).
  - A resource the caller does not own returns **404, never 403**. Albums refine this: a non-member gets 404 on everything album-scoped, and a member attempting an owner-only action (rename, delete, manage members, remove someone else's item) gets **403**, because they can see the album. `AlbumHandler.authorize` encodes both.
  - Media **reads** use `readableMediaCond(alias, n)` from `visibility.go`: `(alias.owner_id = $n OR EXISTS (SELECT 1 FROM album_media am JOIN album_members mem ON mem.album_id = am.album_id WHERE am.media_id = alias.id AND mem.user_id = $n))`. It is the single place that encodes read access; never add a second path. The alias is **required** (use `FROM media m` and `"m"`): unqualified, `id`/`owner_id` would resolve to `album_media` columns inside the `EXISTS`. `/api/media` and `/api/game/round` get it through `buildMediaFilter` (`filters.go`), which also builds the shared filters; run `validateMediaFilters` first.
  - Media **writes** keep a literal `owner_id = $n` and never use `readableMediaCond`: location, view settings, keyframes PUT, delete. A member touching a shared item gets 404.
  - Trips are personal: trip filters (including `unassigned`) are ANDed with `m.owner_id = $1`, and `trip_id` is returned as `NULL` for media the caller does not own.
  - Only an item's owner contributes it to an album, and only while a member: album media inserts select `FROM media m WHERE m.owner_id = $me`, and the composite FKs on `album_media` enforce it in the database.
  - `RunGPSMatcher`, `RunTripDetect` and `RunIngestScan` take an `ownerID`. Never match one user's media against another user's tracks.
- **GPS stays per owner.** `RunGPSMatcher` (its media query and `bracketingPoints`), `insertExifGPSTrack` and `auto_detect_trips`/`RunTripDetect` keep their literal `owner_id` filters and must **never** use `readableMediaCond`. Media shared with a user through an album are not that user's rows: their matcher must neither write to them nor read their locations as track points, and their trip detection must never assign them. Comments in `gps_matcher.go` and `trip_detect.go` say so; `TestSharing_GPSIsolation` and `TestSharing_TripDetectIgnoresSharedMedia` pin it.
- **Auth methods.** `auth.MethodFrom(ctx)` is `MethodSession` (cookie) or `MethodToken` (Bearer `gg_pat_…`). The zero value `MethodNone` never counts as a session. Session-only routes: `DELETE /api/media/{id}`, `DELETE /api/trips/{id}`, `DELETE /api/albums/{id}`, `POST /api/albums/{id}/members`, `DELETE /api/albums/{id}/members/{user_id}`, `GET /api/users`, `/api/tokens*`, `/api/admin/*`. Granting or revoking album access and listing users' emails need a session, so an API token can never widen who sees the data. A `Bearer` header is authoritative; other schemes fall through to the cookie.
- **Secrets in logs.** Never log tokens (session, invite, API), ID tokens, query strings or `Authorization` headers. Invite tokens and OIDC `code`/`state` travel in query strings, which is why the request logger (`internal/httplog`) logs `r.URL.Path` only. Tokens are stored as sha256 hashes.
- New config values: add to `config.go` with a default that works locally without editing `.env`. `OIDC_ISSUER_URL` is used verbatim; never normalise it.
- Logging: use `log/slog` (stdlib). `slog.SetDefault` is called in `main.go` — call `slog.Error/Warn/Info/Debug` directly anywhere in the backend. Always log before returning a 500: `slog.Error("context: what failed", "err", err)`. Per-item loops that continue on error must log at Error level, not silently skip.
- Migrations: `000003_description.up.sql` / `.down.sql` — **never edit existing migration files**.
- PostGIS geometry: `GEOGRAPHY(POINT, 4326)`, `ST_MakePoint(lng, lat)` (longitude first!), read back with `ST_Y` (lat) and `ST_X` (lng).
- **S3 keys** for new uploads: `media/{owner_id}/{32 random hex chars}{.ext}`, built by `newStorageKey`. The extension is lowercased and kept only if supported. Never put the filename or a timestamp in a key: presigned URLs expose the key and would leak game hints. The original name stays in `media.filename`. Older objects keep their legacy `media/{unix_nano}/{filename}` keys. Serve via presigned URLs only (15 min) — the bucket is private; never expose raw MinIO URLs to the frontend.
- **EXIF is decoded only through `internal/goexif`,** never the upstream module. Upstream is dormant, and the copy keeps what decoding a file can allocate and follow proportional to the size of its data. Running out of that budget is an ordinary decode error: critical in IFD0 and its chain, non-critical in a sub-IFD. Keep changes to the copy small, and list them in its `README.md`. `registerMedia` decodes in one of `exifDecodeSlots` (4 per process; `server scan` is a process of its own), so the total memory and CPU decoding can take in a process follows that number and not the number of uploads running; anything else that starts decoding uploaded files takes a slot too.
- Deduplication: `registerMedia` takes an `io.ReaderAt` (multipart files are spooled, scan opens an `os.File`), computes `file_hash` with `mediaHash` from the head and tail, and looks up `(owner_id, file_hash)` **before** probing, transcoding or uploading: a duplicate returns `(existingID, inserted=false, nil)` and never touches S3, so upload can still add it to an album. Two concurrent uploads of one file can both miss the lookup; `ON CONFLICT (owner_id, file_hash) DO NOTHING RETURNING id` settles it and the loser's object is deleted again (as is the object of an INSERT that certainly stored nothing; best-effort, logged). Dedup is per owner (no cross-user dedup: the same file from two users is two items); re-uploading or re-scanning the same file is always safe. An empty file is refused.
- **`mediaHash` is frozen.** It is not quite "size + first and last 64 KB": it reproduces what the original streaming code hashed (photos between 128 KB and 1 MB hash only the size and the first 64 KB; probed videos hash only their first 1 MB). Changing it would make every existing row miss its re-upload. `frontend/src/lib/mediaHash.ts` copies it, and both check `frontend/src/lib/mediaHash.vectors.json` (inside `frontend/` because the frontend image is built from that directory alone and `tsc -b` checks tests; regenerate only with `go test ./internal/handlers -run TestMediaHash_Vectors -update-hash-vectors`, which must never change an existing vector).
- Auto-run order after ingest: `RunGPSMatcher` → `RunTripDetect`, for the ingesting owner. Both are called at the end of scan and upload. An upload with `?album_id` then adds the request's new and duplicate media IDs in one `INSERT … SELECT $1, unnest($2::uuid[]), $3 ON CONFLICT DO NOTHING`. If the matcher, trip detection or that insert fails, the request returns 500 and the files stay in the uploader's library without being added to the album.
- Trip deletion: `DELETE /api/trips/{id}` is only allowed for `user_edited = true` trips. Auto-detected trips are protected; renaming a trip flips it to `user_edited = true`.
- Albums: the album owner always has an `album_members` row, inserted in the create transaction, and it can never be deleted (the owner cannot leave: 400; they delete the album instead). A disabled user keeps their memberships and contributions, drops out of `GET /api/users` and cannot be added again; albums they own stay readable but unmanageable (no ownership transfer).
- Media deletion: `DELETE /api/media/{id}` removes the DB row (cascades to `video_keyframes` and `album_media`) then best-effort deletes the S3 object. S3 failure is logged but does not roll back the DB transaction — the DB is the source of truth.
- New handlers should validate UUID path params with `auth.CanonicalUUID` before querying, so a malformed ID returns 404, not a Postgres 22P02 500. The admin, token and album routes already do (and `buildMediaFilter` binds trip and album IDs in canonical form); the media and trips handlers still pass the raw chi param.

## Frontend Conventions

- All backend calls go through `frontend/src/api/client.ts`. Add a typed helper there, never call `fetch` directly in components. Helpers go through the internal `request()`, which redirects to `/api/auth/login?return_to=…` on 401. `api.me()` opts out and returns `null`.
- The app is wrapped in `AuthGate`. It calls `api.me()`, and renders `LoginScreen` when signed out. Read the current user from `AuthContext`. The Admin tab renders only for `role === "admin"`. This is cosmetic: the backend enforces roles.
- Never persist an API token client-side. The plaintext lives only in SettingsPage component state and is cleared when the dialog closes.
- Types shared across pages/components live in `frontend/src/types/index.ts`.
- Shared media: the UI decides "is mine" by comparing `MediaItem.owner_id` with the `AuthContext` user. A shared item shows "Shared by {owner_name}" and hides delete, edit location and edit view. `AlbumMenu` gives an own item add/remove checkboxes and "New album…", and a shared item only a read-only list of the albums it came through (no re-sharing). This is cosmetic too: the backend returns 404 for writes on shared media.
- The Map tab stays mounted while another tab is open (`App` hides it; `MapPage` gets `active`), so its markers and viewport survive tab switches. Shown again, it reloads trips, albums and the media list in the background and swaps the list in when its last page arrives; changing the filters clears the map and fills it page by page (5000 per request).
- Map and Game filters: `FilterState` (`albumIds`, `owner: "all" | "mine" | "shared"` beside dates, trips and media types) becomes query params only through `filterParams` in `src/lib/filters.ts`. FilterPanel's "Collections" (trips + albums) count as one active filter, and the backend ORs them.
- `AlbumMenu` and `AlbumPickerDialog` order albums with `byRecentUse` once per load (`AlbumMenu` puts the item's albums first), so rows never move under the pointer. The picker never preselects the most recent album: on the Upload page that would silently share the uploads. The Albums page and the filter panel keep the server order (newest album first).
- Upload pre-check: before sending, UploadPage hashes every file with `mediaHash` (reads at most 128 KB each) and asks `api.knownHashes`. Known files are reported as skipped and never sent; with an album chosen, their media IDs go to `api.addToAlbum` first (a 404 there stops the whole upload as "album gone"). Without Web Crypto (plain http off localhost) or when the check fails, every file is sent and the server dedups as before.
- Long lists (filter panel collections, `AlbumMenu`, `AlbumPickerDialog`, the members dialog's user list) get a `SearchInput` once they reach `SEARCH_MIN_ITEMS` and filter with `matchesSearch`. Ticked or chosen items stay listed while searching, so they can always be unticked.
- The filter panel puts trips under collapsible year headers (`groupTripsByYear`) once they span more than one year; "Unassigned" stays above them and albums stay flat. Only the newest year starts open, and the header counts ticked trips. A search opens every year with a visible trip and hides the rest without changing the user's own expansion, which returns when the query is cleared. Both reset when the panel closes.
- Vite dev proxy: `/api` → `process.env.VITE_API_TARGET ?? "http://localhost:8080"` (`vite.config.ts`). Auth relies on same-origin `/api`; the backend sends no CORS headers.
- Lint (`eslint.config.js`) enables only `react-hooks/rules-of-hooks` and `exhaustive-deps`, not the plugin's React Compiler rules (`refs`, `set-state-in-effect`, …): the app does not use the compiler, and those rules flag deliberate patterns like `ref.current = prop` during render. Where an effect deliberately omits a dependency, add `// eslint-disable-line react-hooks/exhaustive-deps` with a comment saying why. A parameter named `_x` may go unused.
- Path alias `@` resolves to `src/` — use `@/components/...` not relative paths.
- shadcn/ui components in `src/components/ui/`. Add new ones with `npx shadcn@latest add <component>`.
- Tailwind CSS variables (colors, radius) defined in `src/index.css`.
- Custom Tailwind variants (`tailwind.config.js`): `touch:` = `(pointer: coarse)`, `mouse:` = `(hover: hover) and (pointer: fine)`, `short:` = a raw screen for phones held sideways (`(max-height: 500px) and (min-width: 560px)`). A raw screen turns off Tailwind's `max-*`, `min-[…]` and `max-[…]` screen variants for the whole project (`max-sm:` etc. produce no CSS and no warning): use the default + `sm:…` pattern. `short` must stay a screen, because a plugin variant sorts before `sm:`/`md:`/`lg:`. Use `overflow-clip-safe` (`index.css`: `overflow: hidden`, then `clip`) instead of `overflow-hidden` where script must not scroll a box, and give such a box `min-h-0` / `min-w-0` as a flex item.
- Phones: the document never scrolls (`index.css`). The shell is one viewport tall and each page scrolls inside `<main>`, so the header stays put and fixed overlays open in place. On a coarse pointer every control is at least 44 px (`touch:min-h-11 touch:min-w-11`); Leaflet's zoom and popup close buttons get theirs from one `(pointer: coarse)` rule in `index.css`, on every map. Branch on the input with `touch:`/`mouse:` or `PointerEvent.pointerType`, never on the user agent.
- The green marker a map is about (the gallery's current item, the location editor's picked position, the Game's actual location) gets `zIndexOffset={TOP_MARKER_Z_INDEX_OFFSET}` (`src/components/map/markers.ts`): Leaflet stacks markers by screen row, so a marker just below would otherwise cover it.
- Every `MapContainer` gets `isolate`, so Leaflet's z-index 1000 panes and controls stay inside the map. App layers: the Map page's item panel and status line `z-[800]`, overlays above them (filter panel, mini map, the Game's score card, the Trips page's status bar) `z-[900]`, fullscreen views and 360° viewers `z-[1000]`, the view editor `z-[1100]`, `EditLocationModal` `z-[1200]`, dialogs `z-[1300]`. Each fullscreen view calls `useCloseOnBack`, so the phone's back gesture closes it instead of leaving the app.
- Every full-size video player takes Space and M from `src/lib/videoKeys.ts`; do not add another Space handler. The listener is on `window` in the capture phase and also stops the event, because with focus on a `<video controls>` Chromium's controls toggle on Space even after a bubble listener called `preventDefault`. With focus inside those controls (play, mute, timeline) Chromium shows the page no key events at all, not even in capture, so `useVideoKeys` blurs the video after a `play`, `pause`, `seeked` or `volumechange` while the mouse is in use. It waits for those media events because clicks inside the controls never reach the page either, and a control that changes nothing must keep focus (blurring the ⋮ menu closes it). After a Tab, focus stays until the mouse moves, so a keyboard user can keep seeking on the timeline. Shortcuts are off in text fields but stay on a range input. Regressions here only show with real native controls: `e2e/video-keys.e2e.ts`.
- Fullscreen views listen for keys on `window`, so every such listener starts with `if (keyBelongsToDialog(e)) return;` (`src/lib/dialogKeys.ts`). Do not rely on `stopPropagation` in the dialog: focus falls back to `<body>` when the focused control is disabled (a ticked album checkbox while its request runs), and then keys skip the dialog. Radix closes a dialog on Escape from a capture listener on `document` and calls `preventDefault`; in a browser React commits that close before the event reaches `window`, so the `defaultPrevented` check is what keeps that Escape from also closing the view. `DialogContent` (`components/ui/dialog.tsx`) carries the `data-app-dialog` marker the helper looks for.
- `dvh` sizes need a `vh` fallback, or they set nothing in browsers without `dvh`: `max-h-[90vh] supports-[height:100dvh]:max-h-[90dvh]`. The `supports-` variant keeps the CSS order fixed (before `sm:` and `short:`); two plain arbitrary classes have no guaranteed order.
- Map tile layer swap: change `<TileLayer>` URL in `src/components/map/MediaMap.tsx`. Every OSM credit link opens in a new tab (`target="_blank" rel="noopener noreferrer"`), so a mis-tap never navigates the app away.
- 360° media: `photo_360` renders in `Panorama360.tsx`, `video_360` in `Video360.tsx`, both three.js through `createSphereView`. View settings (`initial_yaw/pitch/fov`, the FOV measured across the viewport's shorter side) and keyframes are stored per-media on the backend. `EditViewModal` starts from `item.keyframes`, so open it on a `video_360` only once they are loaded (saving from an empty list deletes the stored ones); the gallery unmounts its own viewer while the editor is open, so only one WebGL context and one video run. When WebGL is unavailable, the viewers show `WebGLNotice` instead of throwing; the same notice appears while the context is lost. A tap or click on `Video360` plays or pauses unless `tapToPlay` is `false` (the view and keyframe editor, where it would move the time the next keyframe stores).
- HEVC is stored as recorded (every `video_360` is HEVC 5760×2880, and so are some flat videos), so browsers without an HEVC decoder play only the audio. Every full-size video player (`Video360`, the gallery overlay, the Map fullscreen view, the Game round) runs `videoDecodeFailed` on `loadeddata`, `timeupdate` and `error`, and shows `VideoDecodeNotice` over the player. `Video360` never sets `texture.needsUpdate` itself: `VideoTexture` uploads each presented frame through `requestVideoFrameCallback`, so a paused or undecodable video uploads nothing.
- The gallery keeps each media URL together with its item id and renders a viewer only with the current item's URL: a URL left over from the previous item for one commit would make a 360° viewer build a renderer and load the old texture again.

## Media Type Detection

`ingest.DetectMediaType(exifData, filename, ext)` in `internal/ingest/exif.go` — do not call `detectMediaType` (removed). Detection layers:
1. **Filename keywords**: `360`, `pano`, `panorama`, `spherical` → 360° type
2. **EXIF aspect ratio**: width ≥ 3000px and ratio 1.95–2.05 → 360° type
3. **Extension**: `.mp4/.mov/.avi/.mkv` → video, else photo

The filename used here is the original one (`media.filename`), never the opaque S3 key.

XMP `ProjectionType` tag (most reliable signal) is not yet implemented — requires raw XMP scanning or `exiftool` binary. Planned in #28; an owner override for stored items in #27.

## `taken_at` Fallback Priority

Both ingest paths follow the same order:
1. EXIF `DateTimeOriginal` (IFD0 `DateTime` only when that tag is missing), read by `ingest.ExtractExif(r, cameraZone)`
2. The video container's `creation_time` (ffprobe, UTC)
3. File modification time (scan: `DirEntry.Info().ModTime()`; upload: `file_mtime[]` form field in ms-since-epoch sent by the browser)

**The EXIF time is a wall clock without a zone.** `takenAt` in `exif.go` picks the offset and never uses the server's own zone:
- The photo's GPS clock: `GPSDateStamp` + `GPSTimeStamp` (UTC). It is used when wall minus GPS lands within `gpsClockTolerance` (2 min) of a real UTC offset (whole hours, or the fractional offsets in `fractionalOffsets`). A stale fix (minutes or days old, 1970-01-01) falls through.
- Otherwise `OffsetTimeOriginal`, trusted unless the photo's make is in `offsetIgnoredMakes`. It follows a phone abroad, and many phones (the Galaxy S24 on every photo) write no GPS timestamp. The Panasonic DC-TZ200 is exempt: it writes `+01:00` all year, even while its clock shows summer time. The value alone cannot separate it from a phone in London, so the exemption goes by make. An offset that is blank or not in real use (`isRealOffset`) falls through.
- Otherwise `CAMERA_TIMEZONE` (`MediaRegistrar.cameraZone`; nil reads UTC), with that zone's DST on that date.

`ExifData.TakenAtZone` says which one applied (`ingest.ZoneGPS`, `ingest.ZoneOffset`, `ingest.ZoneCamera`). Never call goexif's `x.DateTime()`, which falls back to `time.Local`. Before `CAMERA_TIMEZONE`, a Docker server read every EXIF time as UTC.

`ExtractExif` keeps what it could read after a non-critical goexif error: old Sony cameras write an Exif IFD goexif rejects, and their IFD0 `DateTime` is still right. A critical error (say, a corrupt IFD0) leaves no EXIF at all, so the file time is used.

A rule change applies to new ingests only; stored rows keep their times. The existing deployments were repaired once with the operator commands `server retime` and `server video-locations`, which are deliberately not in `main`: they live in commit `f2afa0d` (branch `fix/exif-offset-video-location`). Port them from there if a later rule change needs another repair.

For a video, `taken_at` is the container's `creation_time`. On Samsung phones that is when the recording ended (about 2 s after start + duration), so it sits later than the time in the filename. Insta360 files do not behave this way, so it is not corrected.

Future: filename-based timestamp guessing (#30).

## Video location tag

`ingest.parseProbeOutput` reads a video's position from the container's `location`, `location-eng` or `com.apple.quicktime.location.ISO6709` tag: ISO 6709 in decimal degrees. The degrees-and-minutes forms and `+00.0000+000.0000/` (no fix) are refused. `registerMedia` stores it with `gps_source = 'exif'`, which covers everything read from the file itself. It makes **no track point**: the tag can be the phone's last known fix and `taken_at` is the end of the recording, so the pair is too loose to place other media.

Known limit: a video keeps its tag even when a dense GPS log covers the clip, because the matcher leaves `exif` rows alone. On a real library, tags from 2022 on agreed with such a log to within 20 m; 4 of 16 tags from 2017 were 400 m to 4 km off it.

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
CAMERA_TIMEZONE           # IANA zone camera clocks show, e.g. Europe/Warsaw. Required when PUBLIC_URL is https (the backend refuses to start without it). Over http, unset means the server's zone (TZ or /etc/localtime; UTC in Docker) and a UTC server logs a warning. An unknown name fails startup
```

Docker Compose only (root `.env`): `POSTGRES_*`, `MINIO_*`, `FRONTEND_PORT` (default 5173), `KEYCLOAK_PORT` (default 8180, bound to 127.0.0.1), `KEYCLOAK_ADMIN` / `KEYCLOAK_ADMIN_PASSWORD` (default `admin`/`admin`). The compose backend service does **not** pass through `OIDC_SCOPES`, `SESSION_TTL_HOURS`, `GPS_MATCH_WINDOW_HOURS`, `TRIP_GAP_HOURS`, `LOG_LEVEL` or `LOG_FORMAT`. It passes `CAMERA_TIMEZONE`, which is empty (so unset) unless the root `.env` sets it. A production compose file must pass it too: over https the backend refuses to start without it. With a managed IdP, set `OIDC_DISCOVERY_URL` equal to `OIDC_ISSUER_URL`. `S3_ENDPOINT` is hard-coded to `http://minio:9000` in `docker-compose.yml`, a container-only hostname, so presigned URLs do not resolve in browsers. For a hosted instance, edit it there to a browser-reachable S3 host (known gap). The `db` service sets `shm_size: 256mb`: Docker's default 64 MB `/dev/shm` is too small for a parallel `VACUUM` of `gps_tracks` at the default 64 MB `maintenance_work_mem`.

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
| `albums` | `id`, `owner_id` (the album owner; FK users ON DELETE RESTRICT), `name` (1–200 chars), `created_at`. Indexed on `owner_id`. No ownership transfer. |
| `album_members` | PK `(album_id, user_id)`; `album_id` → albums ON DELETE CASCADE, `user_id` → users ON DELETE RESTRICT, `added_at`. The album owner has a row too (created with the album, never deleted), so `readableMediaCond` needs a single join. Indexed on `user_id`. |
| `album_media` | One contribution: PK `(album_id, media_id)`, `owner_id` (the media's owner = the contributor), `added_at`. Indexed on `media_id`. |

- **Composite trip FK:** `media (trip_id, owner_id) → trips (id, owner_id) ON DELETE SET NULL (trip_id)`. The database itself makes it impossible for media to sit in another owner's trip.
- **Album FKs (migration 9):** `media_id_owner_key UNIQUE (id, owner_id)` on `media` backs two composite FKs on `album_media`:
  - `(album_id, owner_id) → album_members (album_id, user_id) ON DELETE CASCADE`: only members contribute, and leaving or removal takes that member's contributions with them.
  - `(media_id, owner_id) → media (id, owner_id) ON DELETE CASCADE`: only the media owner can contribute an item (no re-sharing), and deleting the media removes it from every album.
  - Deleting an album cascades its members and contributions; the media rows are never touched.
- **Trip detection** is the SQL function `auto_detect_trips(p_owner UUID, gap_hours INT)`. It only touches that owner's auto trips.
- Users are never deleted, only disabled (`ON DELETE RESTRICT` on owned data).
- GPS resolution priority: EXIF (for a video, its container's location tag) → timestamp-interpolated from the owner's `gps_tracks` → null. Never from another user's tracks or shared media.

## Planned Work

`docs/roadmap.md` ranks the planned work into milestones, and every item has a GitHub issue with the details. Read the issue before starting on an item, and keep the roadmap's issue table in step when an item is split, dropped or added. `PRD.md` is the original product vision and MLP requirements; where it and the code differ, this file describes the code.

Also out of scope so far: API token scopes, user deletion/export, multi-issuer login, and re-link invites after a `sub`-changing provider migration. For sharing (design: `docs/sharing-albums.md`): direct per-user shares, viewer-only roles, live trip→album links, album invite links, ownership transfer, public links, album cover picking, `server scan --album` and cross-user dedup.

## Testing

**Tests are required for all new and modified code.**

**Backend — unit tests (no external deps, always run)**
- `internal/ingest/exif_test.go`, `video_test.go` — EXIF extraction, capture time (camera zone with DST, the GPS clock abroad and at fractional offsets, stale and 1970 GPS fixes rejected, `OffsetTimeOriginal` trusted except for exempt makes, a broken Exif IFD, malformed structures turned down early, IFD0 kept when only the Exif IFD is over its budget, the result independent of the process's own zone), media type detection, video probing, ISO 6709 location tags
- `internal/geo/gps_test.go` — GPS interpolation, `IsValidGPS` refusing NaN and infinities
- `internal/goexif/tiff/limits_test.go`, `internal/goexif/exif/limits_test.go` — decoding malformed structures stays within time and allocation limits, the budget boundary (sized and unsized readers), a value kept in a slice of exactly its length, a sub-IFD over its budget costs only that sub-IFD, `Rat` and `JpegThumbnail` return errors on bad values, `FuzzDecode` (plain `go test` runs only its seeds)
- `internal/gpstrack/*_test.go` — every track format, extension dispatch, `.gz`, the expansion limit, the Google Timeline activity start/end times, headerless esoko logs
- `internal/config/config_test.go` — `PUBLIC_URL` normalisation, refusing dev secrets over https, `CAMERA_TIMEZONE` (unknown zone fails, the UTC fallback check, refusal over https without it)
- `internal/httplog/httplog_test.go` — logged line has the path, never the query
- `internal/auth/handlers_test.go` — login/callback/logout against `oidctest` + in-memory fake store (`fakestore_test.go`): state/nonce/signature, `return_to`, invites, prompt rules, userinfo fallback, session rotation, logout hint
- `internal/auth/middleware_test.go` — cookie and Bearer cases, `RequireSession`, `RequireAdmin`
- `internal/auth/tokens_test.go`, `invite_test.go`, `oidc_test.go`, `returnto_test.go`
- `cmd/server/main_test.go` — route wiring (token → 403 on session-only routes, album routes included), `scan` user checks and exit behaviour, `users` table
- `cmd/server/compress_integration_test.go` — `GET /api/media` is gzipped through the real router, other routes are not (needs `TEST_DATABASE_URL`)
- `internal/handlers/admin_test.go` — admin handlers
- `internal/handlers/gps_matcher_test.go`, `gps_matcher_handler_test.go` — GPS matcher logic and handler
- `internal/handlers/gps_tracks_test.go` — GPS track import and upload endpoints (per-file source, failed and empty files, one matcher run)
- `internal/handlers/register_exif_time_test.go` — `registerMedia` stores the EXIF time in the camera zone (or by the GPS clock or `OffsetTimeOriginal`), on the row and on the `exif` track point
- `internal/handlers/register_video_location_test.go` — `registerMedia` with real ffmpeg and ffprobe: a video's location tag becomes its location, zeros and no tag leave none, never a track point
- `internal/handlers/media_test.go`, `upload_scan_matcher_test.go` — media list/upload/scan/dedup flow, owner propagation, opaque keys, owner fields and `trip_id` only on own rows, `rows.Err()` → 500, upload `?album_id` (refused before the body, duplicates added), `registerMedia` looking up the hash before uploading (a duplicate is never uploaded; a concurrent one is deleted again) and waiting for an EXIF decode slot (the wait ends when the request is cancelled), `KnownHashes` validation and owner scoping
- `internal/handlers/media_hash_test.go` — `mediaHash` equals the original streaming hash at every size edge, tail read errors, and the shared vectors in `frontend/src/lib/mediaHash.vectors.json`
- `internal/handlers/media_delete_test.go`, `media_location_test.go` — delete and manual location
- `internal/handlers/exif_gps_track_test.go` — EXIF GPS track insertion
- `internal/handlers/game_test.go` — game round/guess: no rows → 404, other DB errors → 500, round and the set's distance and bounds share `buildMediaFilter`, `guessMapBounds` rounding, a failed measurement still plays, bad `album_id`/`owner` → 400
- `internal/handlers/visibility_test.go` — `readableMediaCond` SQL for several aliases and arg numbers
- `internal/handlers/filters_test.go` — `validateMediaFilters` and `buildMediaFilter` (table-driven: trips/albums union, owner, canonical ID binding)
- `internal/handlers/albums_test.go` — album name validation, `media_ids`/`trip_id` body parsing
- Direct handler calls need a user in context: use `withTestUser(r)` (`testuser_test.go`); without it `MustUser` panics and kills the test binary.

**Backend — integration tests (real PostGIS; skipped unless `TEST_DATABASE_URL` is set)**
- `TEST_DATABASE_URL` must be a `postgres://` URL whose user may `CREATE DATABASE` (the compose user is a superuser). `testdb.New(t)` creates a uniquely named, fully migrated database and drops it `WITH (FORCE)` on cleanup. `testdb.NewEmpty(t)` returns an unmigrated DSN.
- `internal/testdb/testdb_test.go`
- `internal/db/migrations_integration_test.go` — migrate to 6, seed, up, assert backfill, down/up round trip; migration 9 up/down with populated album tables
- `internal/db/albums_schema_integration_test.go` — the album FKs and cascades (contributor must be a member and the media owner; member removal, album and media deletion)
- `internal/auth/store_integration_test.go` — invite consumption and claim races, `SetDisabled` (incl. creator's invites), sessions/tokens refused for inactive users, `RebindIssuer`, token SQL
- `internal/handlers/ownership_integration_test.go` — cross-user isolation on every route (GPS track upload included: owner, coordinates, re-upload skipped, activity start and end both stored), matcher/trip-detection scoping, per-owner dedup, `known-hashes` finds only the caller's own media (not shared ones), composite FK
- `internal/handlers/ingest_scan_integration_test.go` — `RunIngestScan` ownership, re-run skip (nothing re-uploaded), per-owner dedup, failure count, no orphan objects
- `internal/handlers/albums_integration_test.go` — every album route: create/list (incl. the caller's `last_added_at`), rename, delete keeps media, members, remove/leave, disabled users keep memberships, album media add/remove, `/api/media/{id}/albums`, `/api/users`
- `internal/handlers/sharing_integration_test.go` — read access follows membership, members cannot write shared media, filters on shared rows, round bounds follow the owner and album filters, owner fields, GPS isolation, trip detection ignores shared media
- `internal/handlers/filters_integration_test.go` — `/api/media` and `/api/game/round` filters, canonical ID binding, OFFSET pages hold every item once
- `internal/handlers/upload_album_integration_test.go` — upload `?album_id` refused before the body is read, new files and duplicates added (duplicates never reach S3), `registerMedia` returns the existing ID
- Use `testing` + [testify](https://github.com/stretchr/testify) (`assert`/`require`). Test files live alongside source.
- Pure logic tests use no external deps. Handler tests use `net/http/httptest`.
- Use table-driven tests (`[]struct{ input ...; want ... }`).
- SQL behaviour must be tested against a real test DB (PostGIS) via `testdb`, not mocks. Mock/prod divergence has caused issues here before.

**Frontend — unit tests**
- `src/App.test.tsx` (incl. the error boundaries), `src/api/client.test.ts` (401 redirect; `me()` does not redirect)
- `tailwind.config.test.ts` (variant order, the `max-*` limitation, `overflow-clip-safe`, the 44 px Leaflet buttons on touch), `tailwind.screens.test.ts` (the `short` query against real phone sizes)
- `src/components/auth/AuthGate.test.tsx`, `LoginScreen.test.tsx`
- `src/pages/MapPage.test.tsx`, `TripsPage.test.tsx`, `AlbumsPage.test.tsx`, `GamePage.test.tsx`, `UploadPage.test.tsx`, `SettingsPage.test.tsx`, `AdminPage.test.tsx`
- `src/components/map/FilterPanel.test.tsx`, `MediaMap.test.tsx` (real Leaflet + markercluster in jsdom; spies on the group's bulk calls, including a removal during a chunked add), `MiniMapOverlay.test.tsx` (real Leaflet)
- `src/components/MediaGalleryOverlay.test.tsx` (incl. the view editor: own 360° items only, waits for keyframes, saves through the API helpers, keys and back; the album dialog keeps the gallery's keys with focus inside it and on `<body>`), `EditLocationModal.test.tsx`, `CopyField.test.tsx`, `SearchInput.test.tsx`, `src/components/ui/dialog.test.tsx`, `src/components/albums/AlbumMenu.test.tsx`, `AlbumPickerDialog.test.tsx`
- `src/components/viewer/Video360.test.tsx` (three.js with a stub `WebGLRenderer`; the decode notice, `tapToPlay`), `Panorama360.test.tsx`, `sphereControls.test.ts` (dispose order, WebGL failure, context loss), `gestures.test.ts`, `sphereMath.test.ts`, `ZoomableImage.test.tsx`, `VideoDecodeNotice.test.tsx`; shared fakes in `viewerTestUtils.ts`
- `src/lib/video.test.ts`, `videoKeys.test.tsx` (Space and M, typing targets, held keys, modifiers, dialogs, focus release after mouse use and not after Tab), `filters.test.ts`, `errors.test.ts`, `search.test.ts`, `albums.test.ts`, `trips.test.ts`, `uploadBatches.test.ts`, `zoom.test.ts`, `useCloseOnBack.test.tsx`, `dialogKeys.test.tsx` (real Radix dialog: open state, the closing Escape marked handled with focus on `<body>`), `mediaHash.test.ts` (Node environment; the shared backend vectors)
- Vitest + React Testing Library. Run with `npm run test`. Limits are sized for a busy machine: 20 s per test (`vite.config.ts`) and 5 s for `findBy*`/`waitFor` (`src/test-setup.ts`). To record what renders after a user action, clear the record and fire the action in one synchronous block (`fireEvent`): `userEvent` yields between its pointer events, and renders from before the action would land in the record.
- Test components that contain logic; pure utility functions in `src/lib/` get unit tests.

**CI — GitHub Actions (`.github/workflows/ci.yml`)**
- Runs on every pull request and every push to `main`. It uses no secrets, so pull requests from forks can run it too.
- `Backend`: `go test ./...` against a PostGIS service with `TEST_DATABASE_URL`. It installs ffmpeg, because the video tests skip without it.
- `Frontend`: `npm run lint`, `npm run test` and `npm run build` on Node 22.
- `E2E`: `npm run e2e` against the compose `db` and `keycloak`. S3 is SeaweedFS (`chrislusf/seaweedfs`, pinned), because the AIStor image needs a license file. It listens on `localhost:9000` with the default `S3_*` credentials and bucket, so the backend runs unchanged. The only S3 calls are `HeadBucket`, upload (multipart included), presigned `GET` and `DeleteObject`; a new kind of call must be checked against both servers. Playwright traces are uploaded when the job fails.
- Runner clocks are UTC, and there is no `backend/.env`: a test must not depend on either.

**E2E — Playwright + Keycloak (`frontend/e2e/`, locally and in CI)**
- `npm run e2e`. One-time: `npx playwright install chromium`. Preconditions: see Commands. Only one run at a time.
- Playwright starts two webServers before setup: the backend (build → `scripts/e2e-reset-db.sh` → serve on `:8091`) and Vite on `:5174`. `auth.setup.ts` then runs `./.e2e/server invite` for alice (admin) and bob, logs each in through Keycloak in a fresh browser context, and saves `e2e/.auth/{alice,bob}.json`.
- Specs: anonymous sign-in screen, carol not invited, alice uploads `fixtures/gps.jpg`, bob's empty library, admin tab and invites, logout without a Keycloak prompt (fresh context), the API token lifecycle (bob), Space and M on a video against Chromium's real native controls (`video-keys.e2e.ts`: a stubbed Game round playing `fixtures/clip.webm`; the play button is clicked by position, so a Playwright upgrade that moves Chromium's control bar shows up here), and sharing (`sharing.e2e.ts`: alice creates an album, adds bob from the members dialog's user list, picks the album on the Upload page, uploads the duplicate `gps.jpg` into it (the page finds it by hash and sends no upload request); bob sees it "Shared by" with no delete button, not on his Trips page, and the album on his Albums page; alice removes bob, bob's map is empty, and alice deletes the album, with an API fallback in a `finally`).
- `sharing.e2e.ts` is its own `sharing` project that depends on the `e2e` project, so it runs after every other spec: it would otherwise turn the upload spec's new file into a duplicate and show bob's empty-library spec a shared photo. If any `e2e` spec fails, the sharing spec does not run.
- Keep `E2E_BACKEND_ENV` the single source for the E2E backend env, so invites target `geogallery_e2e` and print `:5174` links.

## Key Design Decisions

- **Invite-only multi-user with one generic OIDC issuer.** No vendor SDK in Go or React. Social login (Google, Facebook, …) is configured in the IdP. Users are keyed by (configured issuer, `sub`), so adding issuers later would not break the schema. An unknown identity without a valid invite is rejected, and the first admin comes from `server invite --admin`, never from "first login wins".
- **Logto Cloud for the hosted instance; Keycloak as the dev/E2E IdP** and the documented self-host option. The code paths are identical. Supabase Auth and Hanko are not supported (see `docs/auth.md`).
- **Backend-for-frontend sessions.** Same-origin `/api/auth/*` routes. Opaque server-side sessions in an HttpOnly, SameSite=Lax cookie, stored hashed, with a 30-day absolute TTL. Logout and disabling a user take effect immediately, and there is no signing secret to manage. No CORS; `http.CrossOriginProtection` guards state-changing (non-GET/HEAD/OPTIONS) requests.
- **Personal API tokens** (`gg_pat_…`, sha256-hashed, always expiring, ≤ 365 days) for scripts. They cannot delete, manage tokens or album members, list users or reach admin routes.
- **Ownership in the schema.** `owner_id NOT NULL` on media, trips and gps_tracks, a composite trip FK, and per-owner dedup. Non-owned resources return 404. `readableMediaCond` is the single place that encodes media read access.
- **Sharing through albums only, by reference.** No per-media grants: to share one photo, put it in an album. Owner + members; every member views everything and adds or removes their own media, and only the owner renames, manages members, deletes the album or removes other people's items. No re-sharing: only an item's owner puts it into an album, and only while a member. Contributions leave with the member: composite FKs tie each contribution to the media owner and to a membership, so leaving or removal takes that member's items out of the album. Trips and GPS stay personal. A share exposes the item's location, `gps_confidence`, `gps_source`, `taken_at`, `filename` and its EXIF, which is intended. Design: `docs/sharing-albums.md`.
- **Folder ingest is an operator CLI** (`server scan --user`), not an HTTP endpoint: it needs server filesystem access.
- **Opaque S3 keys and a private bucket.** Presigned URLs reveal neither the filename nor the upload time. EXIF inside served files is an accepted game-hint risk.
- **MinIO AIStore over standard MinIO** — AIStore edition for performance; requires a license file.
- **golang-migrate over Atlas/goose** — plain SQL files, no DSL, easy to read in git history.
- **Leaflet/OSM over Google Maps** — no API key required, fully offline-capable.
- **chi over Gin/Echo** — handlers are plain `http.Handler`, zero lock-in, composes with stdlib middleware.
- **File mtime as upload fallback** — EXIF `DateTime` preferred; browser sends `file_mtime[]` (ms-since-epoch) so the server can use real file mtime instead of `time.Now()`.
- **Capture times in a configured camera zone, not the server's.** EXIF times have no reliable zone. `CAMERA_TIMEZONE` decides unless the photo's own GPS clock or `OffsetTimeOriginal` gives the offset. The offset is trusted by default, because a phone's is right even abroad, and exempt by make, because a camera with daylight saving switched off writes the wrong one. On a real library, the GPS clock agreed with the camera zone on every accepted reading, the 2-minute tolerance rejected every stale fix, and all Samsung offsets were right.
- **Partial hash dedup** — the size plus (roughly) the first and last 64 KB are hashed, so dedup never reads the full file; see `mediaHash` for the exact, frozen definition. The browser computes the same hash and asks `POST /api/media/known-hashes` first, so a re-upload sends only new files; the server still hashes everything it receives, so a wrong browser hash costs only bandwidth.
- **Relative scoring** — game location score is relative to the geographic spread of the media collection (`max_dist_km` from `NewRound`), not a fixed global scale; GPS confidence adjusts tolerance. With `owner` unset the spread includes shared media, so a friend's far-away album changes the scale; `owner=mine` restores own-only scoring.
