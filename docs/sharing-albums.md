# Sharing and albums — plan

Status: implemented on branch `feat/albums-sharing`. Replaced item 1 of "Unimplemented Work" in `CLAUDE.md` (`AGENTS.md`). Where the code differs from this plan, see "Implementation notes" at the end.

## Terms

- **Album**: a named collection of media. Its creator (the **album owner**) and its **members** contribute to it. Media from several users can sit in one album. Media keep their owner; an album only references them.
- **Member**: a user with a row in `album_members`. The album owner is a member too.
- **Contribution**: one `album_media` row, meaning "the media owner put this item into this album".
- **Shared media**: media the caller can read but does not own (it reached them through an album).
- **Trip**: unchanged and still personal. Trips group only their owner's media, and shared media never join someone else's trip.

## Decisions

| Topic | Decision |
|---|---|
| Sharing mechanism | **Albums only.** No per-media grants to individual users. To share one photo, put it in an album. |
| Roles | **Owner + members.** Every member can view, add and remove their own media. Only the owner can rename the album, manage members, delete the album, and remove other people's items. |
| Adding members | The owner picks from a **user directory** (`GET /api/users`: active, claimed users). |
| Trip → album | **One-time snapshot.** It inserts the trip's current media. Running it again is idempotent and picks up new items. |
| Leaving or removal | **That member's contributions leave with them**, enforced by a DB cascade. |
| Map/Game filters | Trips and albums together form one **"Collections"** list combined with **OR** (union). Dates, media types and owner still narrow the result with AND. |
| Owner filter | `owner=mine\|shared` on `/api/media` and `/api/game/round`. The default is everything the caller can read. |
| Ways to add media | (1) a trip, from the Trips page; (2) a single item, from the media viewer; (3) at upload time, with an optional album. |

Defaults chosen without a question (easy to change):
- **No re-sharing.** Only an item's owner can put it into an album. Members cannot forward items they see in album A into album B.
- **A disabled user** keeps their memberships and contributions, so their photos stay in the family album. They drop out of the directory and can't be added again. Albums they own stay readable, but no one can manage them (there is no ownership transfer).
- **The album owner cannot leave** (400). They delete the album instead.
- **No cross-user dedup.** If Alice and Bob each upload the same file, a shared album shows two items.

## Use cases

1. Alice creates "Iceland 2025" and adds Bob and Carol from the user list.
2. Alice adds her auto-detected trip to the album. Her Trips page is unchanged.
3. Bob adds his own trip from the same dates. It stays a separate trip in Bob's Trips page. Nobody's trips mix, and the album holds both.
4. Carol uploads her phone photos with "Add to album: Iceland 2025".
5. Bob opens the Map. He sees his own media plus every item in albums he belongs to, marked "Shared by Alice". He can filter to the album or to "Shared with me".
6. Bob plays the Game on "Shared with me" or on the album. These are photos of places he didn't take, which makes for a real game.
7. Alice adds a single photo from the viewer, or removes one. Bob's viewer shows Alice's photos read-only: no delete, no location or view editing.
8. Bob leaves. His items vanish from the album, and he loses access to Alice's and Carol's items.
9. Alice removes Carol. Carol's items leave the album, and Carol loses access.
10. Alice deletes a photo. It disappears for everyone (cascade).
11. Alice deletes the album. Everyone loses access, and no media is deleted.
12. Alice imports GPS tracks. Her interpolated locations improve, and Bob sees the updated positions. Bob's media are **never** interpolated from Alice's tracks or from the locations of Alice's shared media.

## Invariants

These are the rules the tests pin down.

1. **Sharing is by reference.** Media rows are never copied, and `media.owner_id` never changes.
2. **Read access** = `owner_id = me`, **or** the item is in an album I am a member of. There is no other path, and `readableMediaCond` stays the single place that encodes it.
3. **Only the media owner contributes, and only while a member.** Two composite FKs on `album_media` enforce this in the database (see Schema).
4. **Media writes stay owner-only**, with a literal `owner_id = $n`: location, view settings, keyframes, delete. A member touching a shared item gets 404.
5. **GPS stays per owner.** `RunGPSMatcher` and `RunTripDetect` keep their literal `owner_id` filters and must never switch to `readableMediaCond`. Shared items are not the caller's rows, so the caller's matcher can neither write to them nor read them as track points. Add a comment saying so in `gps_matcher.go`.
6. **Trips are personal.** Trip filters (including `unassigned`) match only the caller's own media. For other users' media, `trip_id` is returned as `null`.
7. **Granting access requires a session.** Member management, leaving and album deletion are session-only routes, so an API token cannot widen who sees the data.
8. **Status codes.** If the caller is not a member, everything album-scoped returns **404**. If the caller is a member attempting an owner-only action, it returns **403**. That's honest, because they can see the album; it follows the precedent of `DELETE /api/trips/{id}` on an auto trip.

## Schema — `000009_albums.up.sql`

```sql
-- Lets album_media reference (media id, owner) so each contribution is tied to the media's owner.
ALTER TABLE media ADD CONSTRAINT media_id_owner_key UNIQUE (id, owner_id);

CREATE TABLE albums (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    owner_id   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name       TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX albums_owner_idx ON albums (owner_id);

-- The album owner also has a member row (inserted in the create transaction),
-- so readableMediaCond needs a single join.
CREATE TABLE album_members (
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    user_id  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, user_id)
);
CREATE INDEX album_members_user_idx ON album_members (user_id);

CREATE TABLE album_media (
    album_id UUID NOT NULL,
    media_id UUID NOT NULL,
    owner_id UUID NOT NULL,            -- the media's owner = the contributor
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, media_id),
    -- Only members contribute; leaving or removal deletes their contributions.
    FOREIGN KEY (album_id, owner_id) REFERENCES album_members (album_id, user_id) ON DELETE CASCADE,
    -- Only the owner can contribute an item (no re-sharing); deleting the media removes it.
    FOREIGN KEY (media_id, owner_id) REFERENCES media (id, owner_id) ON DELETE CASCADE
);
CREATE INDEX album_media_media_idx ON album_media (media_id);
```

The down migration drops the three tables and `media_id_owner_key`. The app enforces two things the FKs cannot: the owner's member row is created with the album, and it cannot be deleted. Test both.

## Backend

### `readableMediaCond` (`visibility.go`)

```go
// alias is required (e.g. "m"): the EXISTS subquery must not resolve id/owner_id to album_media columns.
func readableMediaCond(alias string, n int) string {
	return fmt.Sprintf(`(%[1]s.owner_id = $%[2]d OR EXISTS (
		SELECT 1 FROM album_media am
		JOIN album_members mem ON mem.album_id = am.album_id
		WHERE am.media_id = %[1]s.id AND mem.user_id = $%[2]d))`, alias, n)
}
```

Callers that pass `""` today (`List`, `GetURL`, `NewRound`, `SubmitGuess`, `computeMaxDistKm`) move to `FROM media m`. `GetKeyframes` passes `"m."`, which becomes `"m"`.

### Shared filter builder (`filters.go`)

`List` and `NewRound` duplicate the filter code today. Extract it into `buildMediaFilter(q url.Values, userID string) (conds []string, args []any)`. `computeMaxDistKm` already reuses `conds`. New and changed parameters:

| Param | SQL |
|---|---|
| `trip_id[]` | `m.owner_id = $1 AND (m.trip_id = ANY($k) [OR m.trip_id IS NULL])`. Now owner-scoped, so `unassigned` no longer pulls in shared media |
| `album_id[]` | `EXISTS (SELECT 1 FROM album_media am JOIN album_members mem ON mem.album_id = am.album_id AND mem.user_id = $1 WHERE am.media_id = m.id AND am.album_id = ANY($j))`. The membership join makes a foreign album ID match nothing |
| both | `(<trip cond> OR <album cond>)` |
| `owner` | `mine` → `m.owner_id = $1`; `shared` → `m.owner_id <> $1`; any other value → 400 |

`validateMediaFilters` also checks `album_id` with `auth.CanonicalUUID` (returning 400) and checks the `owner` value.

### Media list response

`mediaItem` gains `owner_id` and `owner_name` (`COALESCE(u.display_name, u.email)` via `JOIN users u ON u.id = m.owner_id`). `trip_id` becomes `CASE WHEN m.owner_id = $1 THEN m.trip_id END`. The frontend derives "is mine" by comparing `owner_id` with `AuthContext`.

### New handler `albums.go` + routes

| Method | Path | Auth | Who | Behaviour |
|---|---|---|---|---|
| `GET` | `/api/albums` | user | member | Albums I belong to: `id, name, owner_id, owner_name, is_owner, member_count, media_count, start_date, end_date` (min/max `taken_at`). Every member can read every item, so the counts are the same for all members |
| `POST` | `/api/albums` | user | anyone | `{"name"}` → 201 album. Inserts the album and the owner's member row in one transaction |
| `PATCH` | `/api/albums/{id}` | user | owner | `{"name"}` |
| `DELETE` | `/api/albums/{id}` | session-only | owner | Cascades members and contributions; the media themselves are untouched |
| `GET` | `/api/albums/{id}/members` | user | member | `[{user_id, display_name, email, is_owner}]` |
| `POST` | `/api/albums/{id}/members` | session-only | owner | `{"user_id"}`; the target must be active and claimed (otherwise 404); idempotent, 204 |
| `DELETE` | `/api/albums/{id}/members/{user_id}` | session-only | owner, or self (leave) | The owner removing themselves → 400. The cascade removes that member's contributions |
| `POST` | `/api/albums/{id}/media` | user | member | `{"media_ids": [...]}` **or** `{"trip_id"}` (exactly one, else 400; at most 5000 IDs). `INSERT … SELECT … FROM media m WHERE m.owner_id = $me AND (…) ON CONFLICT DO NOTHING` → `{"added": n}`. Items the caller doesn't own are silently skipped, and a `trip_id` the caller doesn't own → 404 |
| `POST` | `/api/albums/{id}/media/remove` | user | member | Same body shape. The owner may remove any item; a member removes only their own (a foreign item → 403). `trip_id` resolves only to the caller's own trip. `{"removed": n}` |
| `GET` | `/api/media/{id}/albums` | user | — | `[{id, name}]`: albums I belong to that contain this item; `[]` if I can't read it (like keyframes). This feeds the viewer's album menu |
| `GET` | `/api/users` | session-only | anyone | Directory: `[{id, display_name, email}]` where `issuer IS NOT NULL AND disabled_at IS NULL` |

Validate every path UUID with `auth.CanonicalUUID` (malformed → 404). Register the routes in `newRouter` in the right groups and extend `TestRouter_AuthWiring` (token → 403 on the session-only ones).

### Upload into an album

- `POST /api/media/upload?album_id=<uuid>`. It is a **query param, not a form field**, so membership is checked (404) *before* `ParseMultipartForm` reads up to 4 GiB.
- `registerMedia` returns `(mediaID string, inserted bool, err error)`: `INSERT … ON CONFLICT DO NOTHING RETURNING id`, and on no row, `SELECT id … WHERE owner_id = $1 AND file_hash = $2`. `registrarDB` gains `QueryRow`, and the scan path and mocks update.
- Both newly uploaded files **and per-owner duplicates** go into the album, so re-uploading a folder "into an album" does what the user expects. Use one `INSERT … SELECT $1, unnest($2::uuid[]), $3 ON CONFLICT DO NOTHING` after the loop. The response gains `album_added`. If the insert fails, log and return 500, the same as a matcher failure.
- The frontend already splits uploads into several requests. Each request carries the param.

### Unchanged on purpose

- `RunGPSMatcher`, `bracketingPoints`, `insertExifGPSTrack`, `auto_detect_trips` and `TripHandler` keep their literal owner filters.
- `UpdateLocation`, `UpdateViewSettings`, `ReplaceKeyframes` and `Delete` keep `owner_id = $n`.

## Frontend

- **Types** (`types/index.ts`): `Album`, `AlbumMember`, `DirectoryUser`; `MediaItem.owner_id` and `owner_name`; `FilterState.albumIds: string[]` and `owner: "all" | "mine" | "shared"`.
- **Client** (`api/client.ts`): typed helpers for every route above. `upload(form, albumId?)`.
- **FilterPanel**: a "Collections" section with a "Trips" subgroup (Unassigned + trips) and an "Albums" subgroup, plus a "Show: All / Mine / Shared with me" radio. Ticked collections count as one active filter; owner ≠ all counts as one. MapPage and GamePage load `/api/albums` and send `album_id[]` and `owner`.
- **Media viewer** (MapPage popup + gallery overlay):
  - **Own item:** an "Albums" menu with a checkbox per album (from `/api/media/{id}/albums`) that adds or removes the item, plus "New album…".
  - **Shared item:** "Shared by {owner_name}". Delete, edit location, edit view and the keyframe editor are hidden. The backend returns 404 for those regardless.
- **Albums page** (new tab):
  - Album cards show name, owner, member count, item count and date range, with a "New album" button.
  - A card opens the gallery. Generalise `TripGalleryOverlay` into a `MediaGalleryOverlay` that takes a title and query params, so trips and albums share one overlay.
  - The members dialog: the owner adds users with a picker from `/api/users` (excluding existing members) and removes them. A member gets "Leave", with a confirm that says their N items will be removed.
  - Owner actions: rename, delete with confirm, remove any item.
- **Trips page**: an "Add to album…" action on each trip card opens an album picker. "New album" is prefilled with the trip name. The toast shows "Added N items".
- **Upload page**: an optional "Add to album" select (default: none).

## Tests

Tests are required by `CLAUDE.md`. SQL behaviour runs against `testdb`, not mocks.

**Integration (new `albums_integration_test.go`, extend `ownership_integration_test.go`, `migrations_integration_test.go`)**
- Migration 9 up/down round trip.
- The FKs reject an `album_media` row whose `owner_id` ≠ the media owner, and one whose contributor isn't a member. Removing a member cascades their contributions. Deleting the album cascades everything. Deleting media cascades its contributions.
- The owner's member row exists after create, and removing it → 400.
- Read paths for a member vs a non-member: list, `/url`, keyframes, game round, guess. A former member loses access immediately.
- Write paths on shared media → 404: location, view settings, keyframes PUT, DELETE.
- Filters:
  - `unassigned` and `trip_id` exclude shared media.
  - Album filter, union with trips, `owner=mine|shared`.
  - A foreign `album_id` matches nothing.
  - `trip_id` is `null` on shared rows.
- **GPS isolation:** A shares an item (EXIF location) and B imports tracks around the same time, then B's matcher runs.
  - A's item is unchanged.
  - B's unlocated item taken at the same moment stays unlocated (nothing borrowed from A's location).
  - A's matcher ignores B's tracks.
- Trip detection for B never assigns A's shared media.
- Album media API:
  - Non-owned `media_ids` are skipped.
  - Trip add is idempotent.
  - A member can't remove someone else's item (403), and the owner can.
- Upload `?album_id`: a non-member → 404 with the body unread. New files and duplicates are both added.
- `/api/users` excludes disabled and unclaimed users.

**Unit**: `buildMediaFilter` validation (table-driven), `TestRouter_AuthWiring` additions, mocks updated for the `registerMedia` signature.

**Frontend (Vitest)**:
- `FilterPanel`: albums group, owner radio, active count.
- `MapPage`: owner-only actions hidden for shared items.
- `AlbumsPage`: create, members dialog, leave confirm.
- `TripsPage`: add to album.
- `UploadPage`: sends `album_id`.
- `client.test.ts`: the new helpers.

**E2E** (`sharing.e2e.ts`):
- Alice creates an album, adds Bob and uploads `fixtures/gps.jpg` into it. That file is a duplicate of the existing spec's upload, so it exercises the dedup path.
- Bob sees it with "Shared by", has no delete button, and doesn't see it on his Trips page.
- Alice removes Bob, and Bob's map is empty again.

The existing "bob's empty library" spec has to stay valid under any spec order. Make it assert on `owner=mine`, or have the sharing spec delete its album at the end.

## Phases (one PR each)

1. **Refactor, no behaviour change.** `buildMediaFilter`, the `m` alias everywhere, `readableMediaCond` requires an alias, trip filters owner-scoped. Existing tests stay green.
2. **Schema + albums backend.** Migration 9, `albums.go`, `/api/users`, `/api/media/{id}/albums`, the new `readableMediaCond`, owner fields and the new filters on list/game, route wiring, the integration tests above.
3. **Upload into an album.** The `registerMedia` signature change and `?album_id`.
4. **Frontend.** Types and client, FilterPanel, viewer permissions and album menu, Albums page with the generalised gallery overlay, Trips "Add to album", Upload select.
5. **E2E + docs.** Update `CLAUDE.md`: routes, schema, `readableMediaCond`, the GPS-isolation rule under Backend Conventions, key paths, the test list, and drop item 1 from Unimplemented Work.

## Known effects and risks

- **Game scoring scale.** With "All", `max_dist_km` spans own plus shared media, so a friend's far-away album changes the relative scoring of your own photos. "Mine" restores today's behaviour.
- **List performance.** The `OR EXISTS` stops `media_owner_taken_at_idx` from serving `/api/media` directly. That's fine at family scale. If it gets slow, split the query into `UNION ALL` (own ∪ shared). Pagination (Unimplemented item 3) becomes more pressing because the volume grows.
- **What a share exposes.** Recipients see the item's location, `gps_confidence`, `gps_source`, `taken_at`, `filename` and the EXIF inside the file. That's intended. Legacy `media/{unix_nano}/{filename}` keys on pre-auth data now also reach recipients via presigned URLs, which is the same accepted game-hint risk.
- **Presigned URLs** (15 min) can be forwarded by a recipient. That is inherent to presigned URLs.

## Out of scope

Direct per-user shares, viewer-only roles, live trip→album links, album invite links, ownership transfer, notifications/comments, public links, album cover picking, `server scan --album`, cross-user dedup.

## Implementation notes

Where the code on `feat/albums-sharing` differs from or adds to the plan above. `CLAUDE.md` documents the current behaviour.

**Albums API**
- `GET /api/albums` rows also carry `created_at`, newest first, and `last_added_at`: the caller's latest contribution to the album (`null` without one), from which the album pickers put recently used albums first. `GET /api/albums/{id}/members` rows carry `added_at`, owner first. `PATCH /api/albums/{id}` answers 204 with no body.
- A malformed UUID in a request body (`user_id`, `media_ids`, `trip_id`) → **400**. Only path UUIDs → 404. Body checks run before the membership check, so a bad body is 400 even for a non-member.
- Removing a user who is not a member → 404. `GET /api/media/{id}/albums` with a malformed ID → `[]`, like keyframes.
- Album names are trimmed, and NUL is rejected. The album media body is capped at 1 MiB.

**Media and upload**
- Upload: a malformed `album_id` → 404, the same as a non-member. `album_added` is present only when `album_id` was given.
- The album insert on upload runs **after** `RunGPSMatcher` and `RunTripDetect`. If either fails, the request returns 500 before anything is added to the album, and the files stay in the uploader's library.
- `registerMedia` can only hash after the S3 upload. It therefore deletes the object it just uploaded when the INSERT dedups, or when the INSERT certainly stored nothing (best-effort, logged), so `registrarStorage` gained `Delete`. When a failed INSERT may already have committed, it keeps the object and logs a Warn.
- `GET /api/media` orders by `taken_at DESC NULLS LAST, id`, so OFFSET pages never overlap. A `rows.Err()` returns 500 instead of a silent `[]`.
- `buildMediaFilter` binds `trip_id` and `album_id` in canonical form, because `auth.CanonicalUUID` accepts any separator but Postgres only a dash.
- The "GPS and trips stay per owner" comment is in `trip_detect.go` as well as `gps_matcher.go`.

**Frontend**
- For a shared item, `AlbumMenu` shows a read-only list of the albums it came through.
- `FilterState` → query params lives in `src/lib/filters.ts`, and album error text in `src/lib/errors.ts`.
- The Upload page reloads the album list before sending. On a 404 it stops and marks the remaining batches failed.

**Tests**
- The integration tests are in new files instead of `ownership_integration_test.go`: `sharing_integration_test.go`, `filters_integration_test.go`, `upload_album_integration_test.go` and `internal/db/albums_schema_integration_test.go`.
- `sharing.e2e.ts` deletes its album at the end (with an API fallback). It also runs in its own Playwright project, `sharing`, which depends on `e2e`, so it runs after every other spec. If any `e2e` spec fails, it does not run.

**Known gaps**
- A duplicate uploaded into an album is still sent to S3 (and a video transcoded) once before its object is deleted. Hashing before the upload would avoid that.
- If the uploader leaves or is removed between the membership check and the album insert, the FK violation returns 500 after the files are stored.
- When an earlier item is dropped from `MediaGalleryOverlay`, the index is clamped, not moved, so the view can shift to the next item.
