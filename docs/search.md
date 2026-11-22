# Search — design

Status: draft, 2026-11-22. Nothing is built. It widens #35 (search by name, date, label and place) to search by what photos and videos show, and puts thumbnails and grids in front of it, because results need both. "Decisions" holds what was decided on 2026-11-22, and no question is left open; "To measure before building" lists the three things to test first. Sizes, times and costs are estimated for one real library (about 155 GB of video in 610 clips, plus the photos). Figures marked as measured were taken on 2 vCPUs, and prices are list prices of November 2026.

## Goal and terms

### Goal

Type "sunset over the sea", "dog in the snow" or "Kraków 2019" and see the matching photos and videos among everything the caller can read: their own and what albums share with them. Names, places and dates find every such item; what an item shows finds it only if its owner turned visual search on in Settings (opt-in). Results show on the map and in a grid, narrowed by the filters the Map and the Game already have. Queries in English and German work best; Polish and Hungarian work, less well. Labels (#34), comments (#37) and video transcripts (#52) are searched once they exist.

### Terms

- **Embedding (vector):** a list of numbers that a model computes for an image or a text, so that an image and a text describing it lie close together. Comparing a query's vector with every item's vector ranks the items by how well they match.
- **Image–text model:** two networks trained together. The **image tower** runs once per image; the **text tower** runs once per query. Both must come from the same checkpoint and preprocessing: vectors of different models cannot be compared.
- **Part:** one vector of an item: the whole photo, one perspective view of a 360° item, or one sampled video frame. An item's best part decides its rank.
- **Derivative:** a smaller file made from an original: a **tile** for grids and a **preview** for the viewer and for workers.
- **Task:** one piece of work on one item: make its derivatives, embed its image, embed its frames, later transcribe it.
- **Worker:** a program that takes tasks from the server, computes their results and sends them back. Derivatives are made by a loop inside the backend; everything else by the worker program, which runs as an AWS Lambda function and, for later GPU tasks, on a GPU PC. On AWS, a small **checker** function asks the server every minute whether tasks are waiting, and starts the worker only then.

## Decisions

| Topic | Decision |
|---|---|
| Where vectors live | **pgvector in the existing PostgreSQL.** A vector database would need its own copy of `readableMediaCond`, updated on every album change outside any transaction, and RAM the server does not have. |
| How they are searched | **An exact scan after the filters**, without an approximate index. One real library makes ~20,000–40,000 vectors; an exact scan of 100,000 took 42–68 ms on 2 CPUs. An HNSW index pays off only past ~100,000 vectors, and under a selective access filter it returned no rows at all until iterative scans were turned on. |
| What a vector encodes | **The pixels only**: one per photo, per 360° view and per sampled video frame. Never dates, places, trip or album names, labels or comments. |
| Metadata | **Filters and text search, merged by rank.** Dates and media types stay `buildMediaFilter` conditions; names, places, labels and comments are text matches; Reciprocal Rank Fusion (RRF) merges those lists with the visual one. |
| Query encoding | **On the server, so search never waits for a worker.** The model's text tower runs in int8 ONNX in a sidecar container, since the backend is built without cgo and ONNX Runtime is a native library. |
| Model | **Base size: SigLIP 2 B/16, or SigLIP 1 i18n B/16-256 if a test of real queries prefers it.** Both are Apache-2.0 and take about 350 MB on the server. Larger models need about twice that, and better Polish and Hungarian are not worth a bigger server (decided 2026-11-22). The image model is always the text tower's own. |
| Server | **One CX23 (2 vCPUs, 4 GB), as the hosted instance has today.** It encodes queries and makes thumbnails, one at a time; everything heavier runs elsewhere. At idle it has 3 GB of its 3.8 GB available (measured 2026-11-22), room for both. A CX33 is rarely orderable and a CPX32 costs €36–43 a month. A second CX23 would add nothing the batch worker does not do for less. |
| Where images and frames are embedded | **AWS Lambda in Frankfurt, on CPUs.** No GPU is needed at base size. A checker function asks the server every minute whether tasks are waiting and starts the worker only then; the worker pulls tasks over HTTPS until none are left. Both take their AWS credentials from IAM roles, so no AWS key exists outside AWS and the server never calls AWS (2026-11-22). |
| Videos | **In the first version.** Frames are sampled, 360° frames become several views, and a result opens at the matching moment. |
| What a worker receives | A photo's 2048-px preview without metadata (no GPS position, no capture time, no file name, no owner). A video's original file, read in ranges. |
| Thumbnails | **Made by the server when an item arrives:** a WebP tile and a 2048-px JPEG preview, with `vipsthumbnail` and ffmpeg run as commands. They come first: grids need them before any model exists. A tile's link stays valid for up to 7 hours, so browsers can cache it; previews and originals keep 15 minutes (2026-11-22). |
| Result display | **On the map and in a grid, with the same query and filters.** The grid has square cells with a minimum size (3 columns on a phone, 4–5 on a laptop) and scrolls to the end. Trips and Albums open their grid, and a new **Library** tab shows the whole library as one. A click opens `MediaGalleryOverlay` on the item. No similarity scores. |
| Consent | **Opt-in, per user, in Settings** (2026-11-22). Until an owner turns visual search on, nothing of theirs goes to a worker and no vector of theirs exists; turning it off deletes their vectors. Thumbnails and text search need no switch: they stay on the server. |

## Invariants

These are the rules the tests pin down.

1. **Search shows only what the caller can read.** Every ranked list starts from `buildMediaFilter`, and so from `readableMediaCond`, and applies it before ranking. Search adds no second definition of read access.
2. **A text matches only for the people who can see it.** Each source keeps its own audience. Trip names count only for the owner's items, album names only for that album's members, labels only for the owner if #34 keeps them personal, and comments for the audience #37 gives them. File names, places and transcripts count for everyone who reads the item.
3. **Vectors describe pixels.** No text, name or date enters an image vector, so a vector tells a reader nothing the item itself does not show them.
4. **Vectors never leave the server through the API.** Workers send them in; no route returns one. "Similar items" compares them inside SQL.
5. **Search never waits for a worker.** Queries are encoded on the server. An item without vectors is still found by filters and text, and the response says how many of the caller's own items visual search does not cover yet, and why (waiting, or visual search off), as the storage counts report unknown sizes instead of counting them as 0. It says nothing of other owners' settings.
6. **A worker sees only what its task needs.** A worker credential reaches `/api/worker/*` and nothing else, and user sessions and API tokens never reach `/api/worker/*`. A checker's credential reads only the counts of `GET /api/worker/pending`. A worker sees an item only through a task it leased, only for owners in its scope, and a photo only as its preview without metadata.
7. **Only an owner who opted in is analysed.** The claim, the checker's counts and the results route cover only owners who have visual search on, and turning it off deletes their tasks and vectors in the same transaction. Nobody turns it on for someone else: not an administrator, not an API token.
8. **A result counts only for its lease and its model.** The lease must still be the task's, the model must be the task's, and every vector must have the model's dimension and finite values.
9. **Derived data goes with its item.** Deleting an item deletes its derivatives (objects best-effort, like the original), vectors, texts and tasks. Turning visual search off deletes the owner's vectors and the texts workers drew from their pixels.
10. **Derivatives are read like originals.** They are served by presigned GET for readable items only, under opaque keys (`newStorageKey`'s rule: never the file name or a time).

## Architecture

```
upload / scan ─▶ registerMedia ─▶ media row ─▶ derive task ─▶ derive loop in the backend
                                                               │ vipsthumbnail, ffmpeg (one at a time)
                                                               ▼
                                Object Storage ◀── tile + preview, metadata stripped
                                                               │
                                               embed / frames task (owner opted in)
                                                               │
EventBridge Scheduler, every minute                            │
  └─▶ checker Lambda (128 MB) ── GET /api/worker/pending ──────┤
        │ starts the worker when tasks are ready               │
        ▼                                                      │
      worker Lambda (Frankfurt) ── POST /api/worker/claim ─────┘
        │ GET preview or video (presigned, 15 min)
        │ image tower on 6 vCPUs
        └── POST /api/worker/results ──▶ media_embedding (pgvector)

GET /api/search?q=… ─▶ embedder sidecar: text tower on the CPU ─▶ one SQL statement:
   buildMediaFilter + readableMediaCond ─▶ visual list ─┐
                                           text list   ├─ RRF ─▶ ranked items ─▶ map / grid
                                           place list  ─┘
```

## Step 0: Thumbnails, previews and grids

Nothing has a thumbnail today. The Map card, the gallery and the challenge results load the original, through one `GET /api/media/{id}/url` per item, so a grid of 200 items would load 200 originals: 5 MB photos and 400 MB 360° videos. This step pays off before any model exists: trips and albums gain a grid, the library gets one, and HEIC photos become viewable outside Safari.

### Derivatives

| Kind | What | Size |
|---|---|---|
| `tile` | WebP at quality 75–80, 480 px on the short side and at most 1,440 px on the long one, EXIF orientation applied, metadata stripped (`keep=none`). A 360° item's tile is a perspective view at its stored default view (ffmpeg `v360`); a video's comes from its first keyframe. | ~25–50 KB |
| `preview` | Progressive JPEG at quality 80, 2,048 px on the long side, metadata stripped. A 360° photo's preview stays equirectangular (2048×1024: 512 px per 90° view, enough for the 224–256 px the models take). A video's preview is its tile's frame at that size. | ~300 KB |

- A grid cell is square and the tile fills it (`object-fit: cover`), so the tile's short side is what must be sharp: five columns on a 2× laptop screen need about 480 pixels. The tile keeps its aspect ratio, so other views can use it uncropped.
- JPEG for the preview because every browser and every worker decodes it. AVIF took 6.3 s per preview to encode. WebP at preview size would save about a third.
- Both together come to about 3 GB for one real library, about 1.5% of its originals.
- New table `media_derivative`: `(media_id, kind)` as primary key, `storage_key` (unique, opaque), `width_px`, `height_px`, `size_bytes`, `created_at`, `ON DELETE CASCADE` from `media`.
- A **`derive` task** per item, claimed by a loop inside the backend with the queue of Step 2, one at a time. Upload and scan do not wait: the grid shows a placeholder until the tile exists. One at a time keeps the peak near 450 MB on the 4 GB server. Measured on 2 CPUs:

  | Input → output | Time | Peak memory |
  |---|---|---|
  | 72-MP JPEG → tile | 0.45 s | 22 MiB |
  | 72-MP JPEG → preview | 0.68 s | 86 MiB |
  | 48-MP HEIC without a big enough embedded thumbnail → tile | 4.2 s | 275 MiB |
  | 48-MP HEIC with a 1024-px embedded thumbnail → tile | 0.65 s | 21 MiB |
  | 8K HEVC → poster frame, keyframes only | 1.2–2.6 s | 356–442 MiB |

- The derive task also fills a missing `duration_sec` from ffprobe. In one real library, 548 of its 610 videos have none, and the grid's duration badge and the frame sampling need it.
- `server backfill-derivatives` enqueues a derive task for every item without derivatives. That library takes about a night.
- A tile is made again when the owner changes a 360° item's default view, the media type (#27) or the rotation (#51).
- The runtime image moves from `alpine:3.20` to the current Alpine and adds `vips-tools` and `vips-heif`, about 31 MB on top of ffmpeg. The backend stays `CGO_ENABLED=0`, as it already is for `ffprobe` and `ffmpeg`. A derivative's ffmpeg call keeps `-format_whitelist` like every other and adds `-map_metadata -1`; the transcode keeps its metadata, since it carries the `creation_time` ingest read.

### Serving

- `GET /api/media` gains `include=tile`: each row gets `tile: {url, width, height}` or `null`. Grids page with at most 500 rows when they ask for tiles; the Map's 5000-row pages do not ask.
- **Tile URLs stay the same for 6 hours.** A presigned URL carries its signing time, so a new signature on every request defeats the browser cache. Tiles are signed with the time rounded down to a 6-hour window and a 7-hour expiry, so a tile's link works for up to 7 hours, accepted on 2026-11-22 since a tile is small and carries no metadata. aws-sdk-go-v2's `PresignOptions.Presigner` takes the signing time, so this is a wrapper in `storage/s3.go`. Tile objects are written with `Cache-Control: private, max-age=31536000, immutable`, since a tile's key never changes. Hetzner documents neither stored `Cache-Control` nor the `response-cache-control` override: test both with `curl -I` first.
- **Previews keep the 15 minutes of originals.** The viewer loads one at a time, and a 2048-px preview is nearly the photo itself, so a URL that works for hours would leak more than a tile's.
- Follow-ups outside this step: the Map card preview and the challenge thumbnails switch to tiles; the gallery shows a flat photo's preview first and the original when zoomed in. A free Game round could show the preview instead of the original, which would stop handing the guesser the photo's EXIF position.

### Grids

- `MediaGrid`: square cells in as many columns as fit a minimum cell size, about 100 px on phones (3 columns on a 360–430 px screen) and 220 px from `sm:` up (4–5 columns on a laptop). The tile fills its cell. A badge marks 360° items and gives a video's duration; shared items keep their "Shared by" marker.
- It scrolls to the end: the next cursor page (`GET /api/media?include=tile&limit=200`) loads before the last row is reached, and only the rows near the viewport are rendered (TanStack Virtual), so 10,000 items cost what is on screen.
- Keyboard: the WAI-ARIA grid pattern. One tab stop, arrow keys between cells, Enter opens, and `aria-rowcount` and `aria-rowindex` while rows are virtualised.
- **Trips and Albums** open the grid of their trip or album (`/trips/<id>`). A click opens `MediaGalleryOverlay` on the item (`initialItemId`, `?item=`) over the same list, and closing it returns to the grid where it was.
- **The Library tab** (`/library`, named 2026-11-22) shows everything the caller can read, newest first, under the filter panel the Map uses. Step 1 adds the search field.

## Step 1: Text search

What #35 describes, without any model: the search route, the search field, and results on the map and in the grid. Step 2 adds the visual ranking.

- `GET /api/search?q=…` with the media filters of `GET /api/media` (session or API token, like `/api/media`), validated by `validateMediaFilters`.
- **Text sources:** file names (`pg_trgm`, typo-tolerant), the caller's trip names (their own items), the names of albums the caller is in, and place names.
- **Places:** an offline gazetteer, as #35 recommends. GeoNames cities (CC BY 4.0) bring alternate names in the languages users type ("Krakkó", "Krakau"); Natural Earth (public domain) brings country and region boundaries. An operator command loads both into PostGIS. A query word that names a place becomes a spatial condition when the query runs: within a radius of a city, inside a country's or region's boundary. Nothing is stored per item, so a location the matcher, a time change or the owner moves never leaves a stale place name behind. The grid shows each item's nearest place, found for the items on screen. GeoNames needs an attribution line.
- **Dates in the query:** rules turn a year, a month name or a season in English, German, Polish and Hungarian into a date filter, shown as a chip the user can remove. Rules come before any model: in one published comparison they found about as much as an LLM, and a 2-vCPU server cannot host an LLM anyway. The words they matched are removed before the rest is ranked.
- **Matching:** a text search configuration copied from `simple` with `unaccent`: no stemming, so it works for every language. PostgreSQL 16's built-in tools are enough here. The BM25 extensions need PostgreSQL 17 or newer (`pg_textsearch`), or are AGPL and need a preloaded library (ParadeDB's `pg_search`).
- **Results:** one ranked list of at most 500 items in one response, not a cursor, since a rank has no stable position to page from.
- **On the map and in the grid.** The filter panel of the Map and of the Library tab gets one search field. With a query, the Map shows the matching located items instead of everything the filters select, and the Library tab shows the matches as a grid in rank order, which can be re-sorted by date in the browser. A button in each view opens the other with the same query and filters. The query goes in the URL (`/library?q=…`); the filters stay out of it, as today. Above the results: the count, the chips for the parsed filters, and how many of the caller's own items visual search does not cover yet (Step 2).

## Step 2: Visual search for photos and videos

### Model

Recall is Immich's per-language benchmark: the mean of recall@1, @5 and @10 for text-to-image retrieval on Crossmodal-3600 ([Immich: Searching](https://docs.immich.app/features/searching)). Text tower costs are at query time, int8, on 2 cores.

| Model | Licence | EN | DE | PL | HU | Text tower at query time |
|---|---|---|---|---|---|---|
| **SigLIP 2 B/16** | Apache-2.0 | 84.9 | 84.3 | 76.5 | 58.3 | measured: ~350 MB resident, 26–47 ms |
| **SigLIP 1 i18n B/16-256** | Apache-2.0 | 79.8 | 81.2 | 76.8 | 74.0 | a tower of the same size |
| SigLIP 2 L/16-256 | Apache-2.0 | 85.0 | 86.2 | 81.3 | 70.6 | estimated 570–700 MB: too big |
| XLM-R Large ViT-H/14 | MIT | 82.4 | 84.5 | 82.0 | 81.7 | like SigLIP 2 L/16: too big |
| OpenAI CLIP ViT-B/32 (Immich's default) | MIT code; its model card rules out deployed use | 69.9 | 24.1 | — | — | measured: 65 MB, 17–30 ms |

- **The two finalists fit the server.** SigLIP 2 B/16 leads by 5 points in English and 3 in German, ties in Polish, and trails by 16 in Hungarian. It is the default, unless the test shows Hungarian queries failing where it matters.
- Left out for their licences: NLLB-CLIP (the best in Hungarian, 85.6), MetaCLIP 2 and jina-clip-v2 are non-commercial (CC-BY-NC). This instance may be non-commercial, but a published project's other users may not be (#46). Apple's MobileCLIP and DFN are for research only.
- **Test before the backfill.** About 50 real queries in English and German, and some in Polish and Hungarian, each with the items it should find. A test mode of the worker program embeds the library's previews with both finalists and prints recall@10 per language, writing nothing to the server. It runs on any PC, in minutes on one with a GPU.
- EmbeddingGemma 2 (Apache-2.0, October 2026: text, images, video and audio in one space) is not a candidate for now: its text path took 200–580 ms per query on a CPU, and it has no published per-language retrieval scores.
- `model` names the weights, the ONNX export and the preprocessing together (`siglip2-b16-224/1`). Changing any of them makes a new model.

### Query encoder

- A sidecar container, `embedder`, in compose: Python, ONNX Runtime on the CPU, the tokenizer and the text tower in int8. It listens on the internal network only (`POST /embed/text`) and reports its model; the backend turns visual search off while that differs from the model the vectors were made with. Recent query vectors are cached (Immich keeps 100).
- RAM: about 350 MB resident, with a peak of about 720 MB while it loads. The hosted instance's CX23 had 3,071 MB of 3,814 MB available at idle on 2026-11-22, with no swap in use; its four containers used 157 MiB (Postgres 131, Caddy 14, the backend 8, nginx 4). The embedder and the derive loop (up to 450 MB) fit beside them, even at both peaks at once, with about 1.9 GB left for uploads and transcodes. A compose `mem_limit` of about 1 GB keeps a leak in the embedder from taking Postgres's memory.
- The embedder and the worker are one Python package with two entry points, so model loading and preprocessing exist once and the two towers cannot drift apart.
- Loading ONNX Runtime into the backend through purego, without cgo, would save the container but is untested.
- The browser is no way out: a SigLIP 2 text tower is a 320 MB first download.

### Vectors

```sql
CREATE EXTENSION vector;  -- pgvector 0.8.7

CREATE TABLE media_embedding (
  media_id  uuid NOT NULL REFERENCES media(id) ON DELETE CASCADE,
  model     text NOT NULL,
  part      int  NOT NULL,     -- 0 = the whole image, 1… = a 360° view or a video frame
  at_s      real,              -- a video frame's time
  yaw_deg   real,              -- a 360° view's direction
  embedding halfvec NOT NULL,  -- dimension checked per model by the backend
  PRIMARY KEY (media_id, model, part)
);
ALTER TABLE media_embedding ALTER COLUMN embedding SET STORAGE PLAIN;
```

- Both finalists make 768-dimension vectors: about 1.5 KB each, and about 60 MB for 40,000.
- `halfvec` without a dimension lets an old and a new model's vectors coexist during a switch. A later HNSW index would be partial per model, on a cast to that model's dimension.
- `STORAGE PLAIN` keeps vectors in the row. At 768 dimensions they would stay there anyway; above about 1,000 PostgreSQL would move them out, and in the benchmark such scans were 7 times slower and never ran in parallel.
- The visual list is one statement: `SELECT m.id, min(e.embedding <=> $1) … FROM media m JOIN media_embedding e ON e.media_id = m.id AND e.model = $2 WHERE <buildMediaFilter's conditions> GROUP BY m.id ORDER BY 2 LIMIT 500`. The query vector is a bind parameter. The best part's `yaw_deg` or `at_s` tells the viewer where to open.
- **The database image changes first.** `postgis/postgis:16-3.4` (Debian 11, last rebuilt October 2024) can no longer install pgvector, because PGDG stopped serving Debian 11. `postgis/postgis:16-3.7` (Debian 13, PostgreSQL 16.16) installs `postgresql-16-pgvector` 0.8.7 from apt (checked). Build `FROM postgis/postgis:16-3.7` plus that package, push it to GHCR and use the one tag in compose, CI and production. It stays on PostgreSQL 16, so no `pg_upgrade` is needed. glibc moves from 2.31 to 2.41, though, which can change how text sorts, so after the switch run `REINDEX DATABASE` and `ALTER DATABASE … REFRESH COLLATION VERSION` (or dump and restore), then `ALTER EXTENSION postgis UPDATE`. This is worth doing on its own, for two years of security fixes.

### Opt-in

Visual search analyses an owner's items only after the owner turns it on (decided 2026-11-22). Thumbnails and text search need no switch: they stay on the server and use only what it already holds.

- **Settings → "Visual search"**, off until the owner turns it on. The section says what goes where: photos as 2048-px previews without position, time or file name, and videos as originals with their location tags, to AWS in Frankfurt, which sends back vectors and keeps nothing. It also says that album members can then find the owner's shared items by what they show, that the vectors identify nobody, and that turning it off deletes them.
- **Routes.** `GET /api/me/visual-search` (user) → `{enabled, since, items, analysed, waiting, failed}` for the caller's own items, so the section can show progress. `PUT /api/me/visual-search` with `{"enabled": true|false}` is session-only: an API token must never hand the owner's photos to a processor. So the switch is not a key of `/api/me/settings`, which tokens may change.
- **Record.** `users.visual_search_at` (timestamptz, `NULL` = off) holds when the owner turned it on, and `users.visual_search_notice` the version of the Settings text in force then. They show the consent later, and a change to what is sent or to whom (another processor, transcripts) can ask again.
- **On:** one transaction under `lockOwnerRow` sets the column and enqueues the owner's items that have derivatives; the others are enqueued when their derive finishes.
- **Off:** one transaction under `lockOwnerRow` clears the column, then deletes the owner's `embed` and `frames` tasks, leased ones included, and their vectors. The results route locks the owner's `users` row `FOR SHARE` before it checks a lease, and writes only while the column is set, so a result in flight is either refused or committed first and deleted with the rest.
- **Anyone can search, switch on or off.** The query is encoded on the server, so a search sends nothing out. Visual results cover the items whose owners have it on, since only those have vectors: an album member finds a friend's photos by what they show only if the friend turned it on. When the caller's own items are not covered, the results say so and link to Settings; they never say which other owners have it off.
- **Workers outside AWS**, such as an operator's own GPU machine, get tokens scoped to that operator's own items, unless the Settings text and the policy name them too.
- Transcripts (Step 3) send what people said, which is more than a picture, so they get a switch of their own when they come.

### The work queue

One table carries the backend's derive tasks and the workers' tasks. The claim stamps a lease rather than holding a transaction open:

```sql
CREATE TABLE ml_task (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  media_id     uuid NOT NULL REFERENCES media(id) ON DELETE CASCADE,
  kind         text NOT NULL,                  -- derive | embed | frames | transcribe
  model        text NOT NULL DEFAULT '',
  state        text NOT NULL DEFAULT 'ready',  -- ready | leased | done | failed
  attempts     int  NOT NULL DEFAULT 0,
  run_at       timestamptz NOT NULL DEFAULT now(),
  lease_id     uuid,
  run_id       text,                           -- the worker run holding the lease
  locked_until timestamptz,
  last_error   text,
  UNIQUE (media_id, kind, model)
);

-- claim: one statement, committed before any work starts
UPDATE ml_task SET state = 'leased', attempts = attempts + 1, run_id = $4,
       lease_id = gen_random_uuid(), locked_until = now() + interval '10 minutes'
WHERE id IN (SELECT t.id FROM ml_task t JOIN media m ON m.id = t.media_id
                                        JOIN users u ON u.id = m.owner_id
             WHERE t.kind = ANY($1) AND t.state = 'ready' AND t.run_at <= now()
               AND m.owner_id = ANY($2)              -- the worker's scope
               AND (t.kind = 'derive' OR u.visual_search_at IS NOT NULL)  -- opted in
             ORDER BY t.run_at FOR UPDATE OF t SKIP LOCKED LIMIT $3)
RETURNING id, media_id, kind, model, lease_id, locked_until;
```

- **Who enqueues.** `registerMedia` enqueues a derive task for a new row. A finished derive enqueues the item's embed task (a photo) or frames task (a video) when the owner has visual search on. Turning it on enqueues the owner's backlog (Opt-in, above). A model change enqueues every item under the new model (an operator command); search keeps the old model until the new one covers the library, then switches, and the old rows are deleted.
- **Retries.** Because `attempts` rises at claim time, a file that crashes the worker counts against itself without any error report. A sweeper returns an expired lease to `ready` after a growing delay (River's `attempts^4` seconds), and after 5 attempts parks the task as `failed` with `last_error`. The checker counts a task again once its delay has passed. The Admin page lists failed tasks with a Retry.
- **Delivery is at least once.** A result replaces the item's parts for that model in one transaction, so a repeated result changes nothing and a re-sampled video keeps no stale frames. A late result for a deleted item finds no lease and is dropped.
- River was considered for the backend's own half, but its workers run inside a River client attached to the database, so the external workers would still need this HTTP API. A plain table fits the project's hand-written SQL and golang-migrate files.

### Workers

| Route | Does |
|---|---|
| `GET /api/worker/pending` | → `{ready, running}`: how many tasks a claim with the token's scope and kinds would take now, by the claim's own conditions, and how many runs hold live leases. The checker's only route; it leases nothing. |
| `POST /api/worker/claim` | `{kinds, max, run}` (`run`: the run's ID, such as the Lambda request ID) → leased tasks, each with its lease, its lease's end, the media type, the duration of a video, the projection of a 360° item, and a presigned GET for its input: the preview of a photo, the original of a video. An empty list means there is no work. |
| `POST /api/worker/heartbeat` | Extends leases. A worker sends it about every third of a lease; video tasks get longer leases. |
| `POST /api/worker/results` | Per task: its lease and model, then either its parts (`part`, `at_s`, `yaw_deg`, the vector as base64 float16) or an error. Written in one transaction while the lease matches, else 409. |

- **Credentials.** A worker is not a user. Table `worker`: `name`, `token_hash` (sha256, like API tokens), a display hint, its scope (some owners, or every owner who has visual search on), its kinds, whether it may claim or only count, `last_seen_at`, `revoked_at`. Tokens look like `gg_wrk_…`. `server worker add --name … [--owner <id>]… [--checker]` prints a token once; a checker's token reaches `GET /api/worker/pending` alone, so a leaked one reveals counts and never a photo. `server worker list` and `server worker revoke` complete the set. A `RequireWorker` middleware guards `/api/worker/*`, and nothing else accepts the token.
- **Results are untrusted input.** The backend checks each vector's dimension and that its values are finite, normalises it, and caps the number of parts per task.
- **The worker program** lives in `worker/` in this repository: Python, ONNX Runtime and ffmpeg, built as a container image. It claims with jitter, fetches inputs in parallel, embeds in batches and posts results until a claim comes back empty or its time is nearly up. It keeps inputs in memory or a temporary directory, deletes them, and never logs a URL.
- **It runs as an AWS Lambda function in Frankfurt**, from that image in ECR: 10 GB of memory and 6 vCPUs per invocation, 15 minutes at most. A run stops claiming after about 12 minutes and leaves the rest to the next one. The worker token is an environment variable of the function. Lambda reaches the server's public `/api/worker/*` like any client: no port is opened for it.
- **A checker starts the worker.** A second function (128 MB, a few lines of Python) runs every minute from EventBridge Scheduler. It reads `GET /api/worker/pending` with a checker token and, when tasks are ready, starts the worker asynchronously: one run, or up to four while a backlog lasts (the worker function's reserved concurrency), less the runs already going. Neither function holds an AWS key. Lambda gives each the short-lived credentials of its IAM role, the checker's role may only invoke the worker, and the schedule starts the checker through a role of its own. So no AWS credential exists outside AWS, and the server never calls AWS. A new item is analysed about a minute after its derivatives exist: up to a minute until the next check, then about 10 s for the worker to start.
- **Considered and rejected.** An hourly schedule left an item waiting up to an hour, though uploads are rare. The server starting the worker itself would need a long-lived AWS key on the server, since a machine outside AWS gets no IAM role. SQS in front of the worker needs the same key, is a second queue to keep in step with `ml_task`, and is itself polled around the clock by the Lambda service.
- **Videos on Lambda.** ffmpeg reads a video through its presigned URL in ranges and decodes only the sampled keyframes, so no file has to fit on the function's disk; the largest in one real library is 6.5 GB of 360° video, and 90% of its 360° videos are under 0.85 GB. Frames per clip are capped, so the slowest clip fits one invocation (estimate: about 3 minutes for the largest). Test the largest clip before the backfill. If one does not fit, its task goes to an EC2 spot instance with a T4 GPU (g4dn.xlarge, $0.2352/h in Frankfurt), which runs the same program.
- **A GPU is not needed in this step.** At base size, CPUs do the work: Lambda embeds about 7 images a second (estimate), and decoding an 8K keyframe took 1.2–2.6 s on 2 CPUs. A GPU makes transcripts (Step 3) and captions practical, and would make larger image models fast, which the server's RAM rules out anyway. The same program, built with CUDA, runs on any GPU machine, owned or rented, and claims only the kinds given to its token.

### Videos

- Flat clips: one frame every 2 s plus scene changes, near-duplicates dropped, at most 32 frames. 360° clips: one keyframe every 3–5 s, at most 16, each as 6 views around the horizon.
- For one real library that makes roughly 20,000–40,000 vectors with the photos and the 360° photos' views (estimate; the near-duplicate rule decides how many flat frames remain).
- Videos go to the worker as originals, location tags included: the one exception to inputs without metadata.
- A video result opens at its best frame's time, and a 360° one facing its best view.

### Ranking

- **Lists:** visual (best part per item, top 500), text (Step 1's sources, later Step 3's), and places (items within the named places, nearest first). Each list starts from `buildMediaFilter`'s conditions.
- **RRF:** an item scores the sum of `weight / (60 + rank)` over the lists it appears in. The k of 60 is the original paper's ("not critical"). Weights rank machine-made text below text people wrote.
- **Where the list ends.** A visual ranking has no natural end: every item matches a little. Items below the model's score threshold, tuned with the test queries, are dropped, so a query for something the library lacks shows few results on the map rather than 500 poor ones.
- A query without visual meaning ("IMG_2041") is carried by the text list; a pure description by the visual one.
- **No scores are shown**; no comparable product shows them.
- **"Similar items"** in the gallery, on items that have vectors: `GET /api/media/{id}/similar` ranks by a readable item's own vectors, so it needs no text tower.

## Step 3: Labels, comments, transcripts

- New table `media_text`: one row per label, comment, transcript segment, caption or OCR block, with `media_id`, `source`, `audience`, `author_id`, `lang`, `body`, `at_s` and `until_s` for transcript segments, a generated `tsvector`, and an optional `embedding`. `to_tsvector` can take its configuration from `lang`, so a text in a known language gets PostgreSQL's German, Hungarian or English stemmer.
- `audience` decides who matches it (invariant 2). A comment written in an album is meant for that album's members, not for every reader of the item; #37 settles which.
- Image–text models read at most 64–77 tokens, so long text gets its own small multilingual model: multilingual-e5-small (MIT, 118M parameters, 384 dimensions, 3–5 ms per query on a CPU) in the embedder, about 120 MB more in int8 (estimate).
- Transcripts (#52) become a task for a GPU worker: whisper-large-v3-turbo (MIT, 99 languages) writes WebVTT for the players and `media_text` rows for search. faster-whisper has transcribed 13 minutes of audio in 17 s on an RTX 3070 Ti (large-v2, batched), so 5–10 hours of video take minutes. A smaller Whisper model could run on Lambda instead. Later: captions from a small vision-language model, and OCR (PP-OCRv5) for text on signs.

## Where the work runs, and what it costs

AWS prices as of 20–21 November 2026, Frankfurt.

- **The backfill on Lambda:** roughly 2–4 hours of function time at 10 GB: about 25 minutes for the photos and the 360° photos' views at ~7 a second, the rest for downloading, decoding and embedding video frames (estimates). That is 70,000–150,000 GB-seconds: inside Lambda's free monthly allowance of 400,000, or $1.20–2.50 at the list price of $0.0000166667 per GB-second.
- **A month** of a few hundred new items: minutes of the worker's time, cents at most; the worker runs only when tasks are waiting. The checker's 43,200 runs a month at 128 MB take about 1,000–2,000 GB-seconds, inside the free tier (a few cents at list price), and EventBridge Scheduler's free tier covers the schedule. The worker's image in ECR costs about $0.10 per GB a month.
- **Data transfer costs nothing in practice.** AWS charges nothing for data coming in. The vectors going back are tens of megabytes, inside the 100 GB a month AWS lets out for free. Reading the video originals (~155 GB) and the previews (~3 GB) once fits in the 1 TB of egress included in Hetzner Object Storage's base price; beyond it, a TB costs €1.

The options considered:

| Option | Backfill | Status |
|---|---|---|
| AWS Lambda, Frankfurt, CPU | free tier, or $1.20–2.50 | **chosen** |
| AWS EC2 g4dn.xlarge spot, Frankfurt (T4) | ~$0.35 | fallback for a clip Lambda cannot finish |
| An operator's own GPU machine | electricity only | optional, for later GPU tasks |
| Modal, L4, "eu" region | ~$1.92, inside its $30 monthly credit | an alternative to AWS |
| The CX23's own CPU | €0, ~2.4 h of CPU for the embeddings, plus video decoding | rejected: it competes with the app for 2 vCPUs and the RAM |
| A second CX23 | a monthly fee for minutes of work a month | rejected |
| Managed APIs (Titan Multimodal G1, Gemini Embedding 2) | $1–3 | rejected: every query leaves too, and each model retirement re-sends the library |
| Vast.ai or Salad | ~$0.14–0.37 | rejected: other people's photos on strangers' machines |

## Privacy

- **Section 2.3** of `frontend/public/privacy.html` says today: "GeoGallery does not perform face recognition or any other image analysis." Step 0 adds smaller copies of every item, which 2.3 has to list; making them is not analysis. Step 2 changes the sentence to: GeoGallery analyses your photos and videos only if you turn visual search on in Settings.
- **A new subsection on visual search** says what is sent (photos as previews without positions, times or names; videos as originals with their location tags), to whom (AWS, in Frankfurt), what comes back (vectors: a description of what an item shows, used to find it), who can search by them (the owner, and the members of albums that hold the item), that they identify nobody, and that turning visual search off deletes them. Its legal basis is consent (Art. 6(1)(a)); turning the switch off withdraws it (Art. 7(3)), and when it was given is stored (Opt-in, in Step 2).
- **Section 4** gains AWS as a processor (Amazon Web Services EMEA SARL, processing in Frankfurt), under its data processing addendum, which is part of AWS's service terms, for the users who turned visual search on.
- **Section 5**: vectors are kept while their item exists and visual search is on; backups drop them within their 30 days.
- **Section 7** says files are delivered only "through links that expire after 15 minutes". Tiles become the exception: their links expire after at most 7 hours, so the browser can keep them.
- Lambda receives nothing else, and keeps nothing after a task.
- Vectors are personal data: a picture with similar content can be generated from a vector, as DALL·E 2's decoder does from CLIP's. They are never returned by the API (invariant 4).
- This is not face recognition, and #54 stays parked: the models know no private person. A query can still describe people ("child on a swing"), and the policy should say so.

## To measure before building

1. **The model.** The test of about 50 real queries (Step 2, "Model") chooses between SigLIP 2 B/16 and SigLIP 1 i18n B/16-256.
2. **The largest video on Lambda.** The largest 360° clip (6.5 GB) must finish within one 15-minute invocation (Step 2, "Workers"), or such clips go to the EC2 spot fallback.
3. **Caching at Hetzner.** Whether Object Storage keeps a stored `Cache-Control` or honours `response-cache-control` (Step 0, "Serving"). Without either, browsers cache tiles by their own heuristics and revalidate them, which saves the download but not the request.

## Issues

- #97: "Database image with pgvector" (`16-3.7`, the reindex). Worth doing on its own.
- #98: "Thumbnails, previews and grids" (Step 0), with the Library tab.
- #35 becomes Step 1: text search, on the map and in the grid.
- #99: "Visual search for photos and videos" (Step 2): the opt-in, the queue, the Lambda worker, the embedder and ranking.
- #52 becomes a GPU worker task (Step 3); a comment there says how. #34 and #37 write `media_text` rows. #53 can group near-duplicates by the distance between vectors.
