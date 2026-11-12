# GPS and capture-time debugging — design

Status: Step 1 is built (#61), and "Implementation notes" at its end lists where the code differs from this specification. Steps 2, 3 and 4 are specified well enough to build later. "Not handled now" keeps everything else that came up, so that none of it has to be rediscovered.

## Goal and terms

### Goal

An item's position is usually an estimate, and its capture time is usually one of several possible readings of a clock. Today the only surviving trace is a line such as "GPS: interpolated (73%)". The owner of a library should be able to answer these questions without writing SQL:

- Which track points placed this item, from which files, and does that still hold?
- Why does this item have no location?
- Where did this capture time come from, and in which zone was it read?
- Which track files, formats or cameras in my library are suspect?
- What GPS data exists for this trip or album, by type, and what does each point say?

The work has four steps, each usable alone:

1. **"Why here?" for one item.** Recording starts, and every time shown names its zone.
2. **Checks page.** Finds problems without the owner having to suspect a particular item.
3. **Trip and album tracks** (#32).
4. **Clock check.** One item's reading, per-camera verdicts, and a backfill of existing rows.

### Terms

- **Item:** one `media` row. An **own item** has `owner_id` equal to the caller. A **shared item** reached the caller through an album.
- **Track point:** one `gps_tracks` row.
- **Source:** the `gps_tracks.source` label. An uploaded file's points carry `<format>:<file name>`. A photo's position is stored as `exif` and a hand-placed position as `manual`. A GeoGallery JSON row keeps the label it came with.
- **Track file:** all points of one owner with one source label.
- **Format:** the part of the source before the first colon, `split_part(source, ':', 1)`. Known formats are the `gpstrack` constants (`gpx`, `tcx`, `kml`, `kmz`, `geojson`, `nmea`, `esoko`, `google_timeline`, `samsung_health`, `geogallery`) and `exif` and `manual`. Anything else is reported as `other`.
- **Log point:** a track point from any format other than `exif` and `manual`.
- **Derived point:** a track point with source `exif` or `manual`. It was written from one of the owner's own items, so it is not independent evidence.
- **Own position:** an item with `gps_source = 'exif'`. That value also covers a video's container location tag.
- **Estimate:** a position the matcher computed, `gps_source = 'interpolated'`.
- **Match window**, W: `GPS_MATCH_WINDOW_HOURS`, one hour by default.
- **Bracket:** the nearest track point at or before an item's time and the nearest at or after it, each within W. A bracket has a **before** point, an **after** point, or both.
- **Shape:** how a bracket produced a position. `two_sided` interpolates between two points, `one_sided` copies the only point, and `exact` means a point at the same instant.
- **Recompute:** running the matcher's own bracket query and arithmetic for one item now, without writing anything, and comparing the result with the stored row.
- **Capture evidence:** the JSON object in `media.capture_evidence` that records how `taken_at` was derived.
- **Wall clock:** a time as a camera wrote it, with no zone.
- **Camera zone:** the zone wall clocks are assumed to show: `CAMERA_TIMEZONE`, or the server's own zone when that is unset.
- **Reading:** a statement about one item, such as "closest at +1 h". A **verdict** is a statement about a group of items. Only groups get verdicts.
- **Filename family:** items whose file names follow one pattern, such as one prefix followed by a date. It is a proxy for a camera until `camera_model` is filled.
- **Evidence view:** the full-screen "Why here?" view of one item.

### How to read the numbers

Unless a sentence says otherwise, every number here is a read-only aggregate from one real library, the **measured library**, holding 8,619 items and 6,246,703 track points in 4,466 track files, with a one-hour match window. Where a number covers only the largest owner of that library, it says so; that owner held 4,956 of the library's 4,962 estimates. One library, with a handful of known problems, cannot show that a threshold generalises. The hosted instance was not checked at all.

"The replay" means this design walked through 11 past data problems on the measured library, asking whether the design would have shown each one.

Sizes are XS, S, M and L: relative effort estimates from the design work, with a duration where one was given. None was tested against a build. The durations are restated for the four steps, because the design's first phases divided the work differently. A deferred entry with no size was never estimated.

## Decisions

| Topic | Decision | Reason |
|---|---|---|
| Order of work | Why here?, then Checks, then Tracks, then Clock check. | The replay found the design a good explainer and a weak detector: most problems surfaced only for someone already looking at the right item. The Checks page needs no backfill, so it comes straight after the explainer. |
| Explanations | **Recomputed on request. No stored match snapshot.** | A recompute reproduced 4,962 of 4,962 stored estimates, to within 1 m and 0.001 confidence. Every production caller of the matcher passes `reprocess=true`, so the next run would overwrite a snapshot anyway. |
| Schema | One migration: `media.capture_evidence JSONB`, nullable, no default. | The rule used at upload, the file's modified time and the raw wall clock are computed at ingest and thrown away. They cannot be recovered later. |
| Existing rows | No backfill to explain one item. The file is re-read on request, and the view says it used today's rules. | One ranged read per photo costs little. A backfill is only needed for per-camera grouping (Step 4). |
| Reading order | Time comes first in the evidence view. | A wrong clock still interpolates neatly. Correct points at a wrong time give a tidy answer, so bracket data alone can never reveal a clock error. |
| Verdicts | Single items get a reading. Only groups get a verdict. | Eight of 15 testable camera-and-period groups had a non-zero best shift, and only one was a real clock error. |
| Access | Owner only. Shared items keep today's one-line GPS note. | Bracket points are the owner's location history, and tracks are never shared through albums. |
| Authentication | Every new route that returns track points or evidence is session-only. | Today only the matcher reads `gps_tracks`, so no API token can read location history. This feature keeps it that way. |
| Tie-break | `bracketingPoints` becomes deterministic now, sorting ties by source. | 538 of 4,962 estimates have equal-time candidates at different positions, so today's result depends on physical row order, and a recompute could show false differences. Upper bound of the movement across those rows: median 0 m, 90th percentile 5 m, 4 rows at or above 100 m, maximum 705 m. It sorts on source rather than `id` because re-imports replace ids. |
| Confidence | Shown last, with its parts. Never described as accuracy. | Re-estimating 1,606 photos that have their own position, from logs alone, gave a median error of 234 m at confidence 1.0 (n=696) and 92 m below 0.40 (n=104). That comparison is confounded by format, but the percentage still must not be the headline. |
| Bracket warnings | Warn on the bracket pair itself. | 924 of 4,956 estimates (19%) interpolate across two formats. A narrower banner, requiring two formats each close on both sides, would have caught 1 of 47 pairs over 1 km apart and none of the 8 impossible ones. |
| Time kinds | Format-level wording only ("time may be estimated", "may be a visit midpoint", "cannot be told apart"). | Stored rows do not carry a kind. A visit midpoint and a path point are identical rows, and KML holds both real and spread times. |
| Filename clock | Not in Step 1. Moves to Step 4, worded as a camera clock reading. | In the replay, 94% of the videos it flagged as non-zero were correct: the filename is the camera's clock, and that clock was what was off. |
| Vehicle loggers | A vehicle logger's stationary points are excluded from clock tests. | A logger that stands still says nothing about where a photo was taken. Step 4 applies this to formats on a short configured list. |
| Downsampling | Time buckets that return real rows. | Every returned point keeps its true time, accuracy and source, so a click needs no second lookup and never lands on an invented vertex. |
| Libraries | Hand-written SVG and `Intl`; no chart, date or timezone library. | A hand-written chart is estimated at about 2 KB gzip, against 23 KB for uPlot and 116 KB for Recharts, on a single 291 KB chunk with no code splitting. |
| Navigation | No eighth tab, no debug mode, no setting. | The header is sized for seven tabs on a 360 px phone. Owner-only entry points on existing surfaces are enough. |
| Writes | These views change nothing except through the existing matcher and the existing manual location route. | Shifting times belongs to #24 and #26, with their safeguards. A dip in a chart is a suggestion. |

## Invariants

These are the rules the tests pin down.

1. **Literal owner scoping.** Every new query on `media` or `gps_tracks` uses a literal `owner_id = $n` and never `readableMediaCond`. This extends "GPS stays per owner" in `CLAUDE.md`.
2. **Session only.** Every new route that returns track points or evidence sits in the `RequireSession` group. An API token gets 403.
3. **Foreign and shared items are 404** on every provenance route, including for an album member. Hiding the entry point in the UI is cosmetic.
4. **No second matcher.** `bracketingPoints` stays the only bracket code and `geo.Explain` the only arithmetic. Nothing interpolates in TypeScript. Step 4's nearest-point distance test measures a distance and never produces a position.
5. **`taken_at` is never moved** by recording evidence, re-reading a file, or the backfill.
6. **UTC on the wire.** New routes serialise instants with `.UTC()`, because pgx scans into the process's local zone. Tests pin this with a non-UTC process zone.
7. **No bare time on an evidence surface.** Every time names its clock. Only the item's capture time has a camera clock, which comes only from the backend; the browser never derives one by applying an offset. Every other instant shows UTC and the browser zone.
8. **Evidence stays out of shared payloads.** `capture_evidence` never appears in `GET /api/media` or in the Game.
9. **Source labels hold uploaded file names.** They render as text (React or `textContent`), never as HTML. They stay out of logs, so a request that carries one uses a POST body or a route with `access_log off`.
10. **Presigned URLs are never logged.** That includes the URL given to `ffprobe`.
11. **A track can never appear in the Game.** It would reveal the answer.
12. **Wording.** Today's rules agreeing with a stored time is "today's rules agree", never "correct". 100% is only explained as a same-instant point when the shape is `exact`. A hop between estimates is "estimates jumped", not a clock signal.
13. **Anything copied out stays aggregate-only**, with no coordinates, dates or file names, if a copy or report feature is ever added.
14. **A debug field can never fail an upload.** Text read from a file goes through `ingest.CleanTag` before it reaches SQL, and a value that cannot be parsed is recorded as null.

## Step 1: "Why here?" for one item

Estimated size: L, about two weeks.

### Scope

- Recording at ingest: `capture_evidence`, plus `camera_model`, `width_px`, `height_px` and `duration_sec`, which exist and are never written.
- A transcoded video keeps its `creation_time`.
- The camera zone is recorded honestly.
- The matcher's bracket query returns more, takes exclusions, and is deterministic.
- `geo.Explain` exposes the arithmetic.
- Two owner-only routes: `GET /api/media/{id}/provenance` and `GET /api/media/{id}/provenance/file`.
- Zone labels on evidence surfaces and on the Map card and gallery time labels.
- The evidence view, opened from own items only.
- `POST /api/gps-matcher/run` gets its first frontend caller, and nginx a longer timeout for that route.
- `frontend/public/privacy.html` is updated.

### Schema: `000010_capture_evidence`

```sql
-- up
ALTER TABLE media ADD COLUMN capture_evidence JSONB;
-- down
ALTER TABLE media DROP COLUMN capture_evidence;
```

The column is nullable with no default. Existing rows stay `NULL`. The four other columns already exist in `000001_init`.

`capture_evidence` has this shape. All keys are always present.

| Field | Type | Meaning |
|---|---|---|
| `v` | number | Format version, `1`. |
| `by` | string | `ingest`. Step 4's backfill writes `backfill`. |
| `src` | string | What produced `taken_at`: `exif_original`, `exif_ifd0`, `container`, `mtime` or `none`. Also `none` when the source gave a time whose UTC year JSON cannot carry (outside 1 to 9998): `taken_at` is then `NULL`, the next source is not tried, `zone` and `offset_used_min` are null, and `wall`, `creation_time` and `file_mtime` still show the inputs. |
| `zone` | string or null | `gps`, `offset` or `camera` (`ExifData.TakenAtZone`). Null unless `src` is an EXIF value. |
| `wall` | string or null | The EXIF wall clock as written, after `CleanTag`, with no zone. Kept even when it cannot be parsed. |
| `gps_utc` | RFC 3339 UTC or null | `GPSDateStamp` + `GPSTimeStamp`, recorded whenever the stamp parses, whether or not it was accepted and whether or not the file has a wall clock. |
| `wall_minus_gps_s` | number or null | Wall clock minus `gps_utc`, before rounding. Null unless both parse. |
| `offset_tag` | string or null | `OffsetTimeOriginal` as written, after `CleanTag`, even when it was ignored. |
| `offset_ignored` | boolean | The tag exists and the make is in `offsetIgnoredMakes`. |
| `offset_used_min` | number or null | The UTC offset actually applied, in minutes, under whichever of the three rules applied. Null exactly when `zone` is null. |
| `camera_zone` | string | The zone in effect at ingest: the IANA name when `CAMERA_TIMEZONE` is set, otherwise the literal `server zone (unset)`. Written for every file. |
| `camera_zone_utc_fallback` | boolean | `CAMERA_TIMEZONE` was unset and the server's zone is UTC all year. |
| `make`, `model` | string or null | EXIF `Make` and `Model`, after `CleanTag`. |
| `exif_partial` | boolean | The EXIF decoder stopped at an error after reading some of the tags `ExtractExif` uses, and those were kept. |
| `creation_time` | RFC 3339 UTC or null | The container's `creation_time`, even when EXIF won. |
| `file_mtime` | RFC 3339 UTC or null | The fallback time the caller supplied, even when an earlier source won. |
| `duration_s` | number or null | ffprobe `format.duration`. |
| `encoder` | string or null | ffprobe `format.tags.encoder`, after `CleanTag`. |
| `codec` | string or null | `VideoProbeResult.CodecName`, after `CleanTag`. |
| `transcoded` | boolean | `probe.NeedsTranscode()` was true, so the stored object is a re-encode. |

The size of this object was not measured. It is expected to be a few hundred bytes per row.

### Recording at ingest

**`ingest/exif.go`.** `ExifData` gains a `Raw` struct: `WallClock string`, `WallTag string` (`DateTimeOriginal` or `DateTime`), `GPSStamp *time.Time`, `WallMinusGPS *time.Duration`, `OffsetTag string`, `OffsetIgnored bool`, `Make string`, `Partial bool`. `CameraModel` stays where it is.

`ExtractExif` fills `Raw` through the helpers `takenAt` uses, whether or not `takenAt` succeeds. `WallClock` keeps the text even when it does not parse, `GPSStamp` is set whenever the stamp parses, and `WallMinusGPS` is set only when both parse. A `wallClock` that finds `DateTimeOriginal` but cannot parse it still does not fall back to `DateTime`, so `WallTag` then says `DateTimeOriginal`. `WallTag` uses EXIF's names: imagemeta calls IFD0's `DateTime` `ModifyDate`.

The applied offset needs no new field. `register.go` reads it from `ExifData.TakenAt.Zone()`, which holds for all three rules because `takenAt` keeps the location it applied.

`raw` reads `OffsetTimeOriginal` whatever the make, so an ignored value is recorded, while `offsetTimeOriginal` still applies the exemption before it uses the tag. `takenAt`'s result must not change. A table test pins that.

Never call imagemeta's `Tags.GetDateTime`, which applies its own zone rules.

**Text from files.** Today none of these raw strings reaches SQL, so an optional debug field must not be able to fail an upload that works today. imagemeta drops non-printable characters, NUL among them, but writes U+FFFD in place of invalid UTF-8, and ffprobe's tags arrive as the file wrote them. PostgreSQL rejects invalid UTF-8 in a `TEXT` parameter, and an embedded NUL serialises as `\u0000`, which `jsonb` refuses. The `INSERT` would then fail, and `registerMedia` would delete the object and report the file as failed. One helper handles every such value:

```go
// CleanTag removes invalid UTF-8, U+FFFD and NUL bytes, trims surrounding space and cuts the result
// to 200 runes. An empty result means the value is absent: NULL in a column, null in
// capture_evidence.
func CleanTag(s string) string
```

It is applied to `Raw.Make`, `CameraModel`, `Raw.WallClock`, `Raw.OffsetTag`, `Encoder` and the codec name written to evidence. The 200-rune cap is a judgement.

**`ingest/video.go`.** `parseProbeOutput` decodes `format.duration` and `format.tags.encoder` into `VideoProbeResult.DurationS *float64` and `Encoder string`. ffprobe emits `format.duration` as a JSON **string** (`"1.000000"` on ffprobe 6.1.1), so the struct field is a `string`, parsed with `strconv.ParseFloat` after the `Unmarshal`. A missing, unparseable, NaN, infinite or non-positive value gives `DurationS` nil and never fails the parse. A field typed `float64` would make the `Unmarshal` fail, and `parseProbeOutput` returns an empty result on any `Unmarshal` error, so every video would silently lose its dimensions, `creation_time`, location tag and 360° detection. `TranscodeH264` gains `-map_metadata 0` before the output path. On ffmpeg 6.1.1 with synthetic clips, the current flags drop `creation_time` and `-map_metadata 0` keeps it. This flag is the only change in Step 1 that prevents further permanent loss.

**Camera zone.** `time.Local`'s name is `Local`, which would hide exactly the failure this column exists to expose: a container running in UTC. A new `ingest.CameraZone` carries what ingest needs:

```go
type CameraZone struct {
	Loc         *time.Location // CAMERA_TIMEZONE, or time.Local when unset
	Set         bool           // CAMERA_TIMEZONE was set
	UTCFallback bool           // unset, and the server's zone is UTC all year
}

// Name returns the IANA name when Set, otherwise "server zone (unset)".
func (z CameraZone) Name() string
```

`main.go` builds it from `cfg.CameraTimezone`, `cfg.CameraTimezoneSet` and `cfg.CameraTimezoneFallsBackToUTC()`. It replaces the `*time.Location` argument of `NewMediaHandler` and `NewIngestDeps` and the `cameraZone` field of `MediaRegistrar`; the callers in `cmd/server` and its tests change with them, as does `handlers/register_exif_time_test.go`, which sets the registrar's zone directly and passes nil zones. A zero `CameraZone` reads as UTC, as a nil zone does today. `ExtractExif` keeps its signature and receives `z.Loc`.

**`handlers/register.go`.** The `INSERT` also writes `camera_model`, `width_px`, `height_px`, `duration_sec` and `capture_evidence`. Width and height come from EXIF, else from the probe, where 0 means unknown and is stored as `NULL`. Only a new row gets evidence; the duplicate path returns before any of this. A row's `camera_model` is `NULL` when EXIF has none. Reading a video's model from container tags is not part of Step 1. `src` is decided in the same order as `taken_at`: EXIF only when `ExifData.TakenAt` is non-nil (`exif_original` or `exif_ifd0`, which `WallTag` selects only then), then `container`, then `mtime`, then `none`. A wall tag that exists but does not parse therefore records the origin that actually applied, while `wall` still shows the text. `offset_used_min` is the offset of `ExifData.TakenAt.Zone()` in minutes.

### Matcher and arithmetic

**`geo.Point`** gains `Source string`, `AccuracyM *float64` and `AltitudeM *float64`.

**`bracketingPoints`** becomes:

```go
type bracketOptions struct {
	excludeFormats []string // compared with split_part(source, ':', 1)
}

func bracketingPoints(ctx context.Context, db matcherDB, ownerID string, at time.Time,
	maxWindow time.Duration, opts bracketOptions) (before, after *geo.Point, err error)
```

Both queries select `ST_Y`, `ST_X`, `recorded_at`, `source`, `accuracy_m` and `altitude_m`, and add:

```sql
AND split_part(source, ':', 1) <> ALL($4::text[])
```

The before query uses `ORDER BY recorded_at DESC, source ASC LIMIT 1`. The after query uses `ORDER BY recorded_at ASC, source ASC LIMIT 1`. `UNIQUE (owner_id, source, recorded_at)` makes that a total order, so a point at the exact instant is the same row on both sides. `bracketingPoints` normalises a nil `excludeFormats` to `[]string{}` itself, because `x <> ALL(NULL)` is `NULL` and would match nothing, and no caller should have to remember that. A planner check on one real library found an incremental sort on the existing index, 0.03 ms.

`RunGPSMatcher` passes `bracketOptions{}`. The literal `owner_id` and the comment that forbids `readableMediaCond` stay. The fake rows in `gps_matcher_test.go` change with the column list.

**`geo.Explain`** is new, and `geo.Interpolate` becomes a wrapper over it, so the panel and the matcher cannot drift:

```go
type Shape string // "two_sided", "one_sided", "exact"

type Explanation struct {
	OK             bool    // false where Interpolate returns Source "none"
	Lat, Lng       float64
	Shape          Shape
	PairGapS       float64
	PairKm         float64
	T              *float64 // position along the pair, two_sided only
	TimeFactor     float64
	DistanceFactor *float64 // nil for one_sided: there is no distance term
	Confidence     float64
	ImpliedKmh     *float64 // two_sided only
}

func Explain(before, after *Point, at time.Time, window time.Duration) Explanation
```

The arithmetic is exactly today's. `exact` is the `total == 0` case: confidence 1.0, `PairGapS` 0, `PairKm` 0, time and distance factors 1. `one_sided` copies the point, has confidence `timeConfidence(gap, window)`, and has no distance factor. `two_sided` has confidence `timeConfidence(pair gap, window) * distanceConfidence(pair km)`. `ImpliedKmh` is `PairKm / (PairGapS / 3600)`.

### Track loader

`handlers/track.go` holds `loadTrack` and `TrackResponse`. Step 1 uses them for the context track. Step 3 adds routes on top of them.

```go
func loadTrack(ctx context.Context, db matcherDB, ownerID string,
	windows []timeWindow, maxPoints int) (TrackResponse, error)
```

It first counts log points, distinct log formats and derived points in the windows, with one range scan per window. If the log points are within `maxPoints`, it returns every row and `bucket_s` is 0. Otherwise `bucket_s = ceil(span * formats / maxPoints)`, where `span` is the sum of the window lengths in seconds and `formats` counts log formats only, and each log format keeps one real row per bucket:

```sql
SELECT DISTINCT ON (split_part(source, ':', 1), floor(extract(epoch FROM recorded_at - $from) / $bucket))
       source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m, altitude_m
FROM gps_tracks
WHERE owner_id = $1 AND recorded_at BETWEEN $from AND $to
  AND split_part(source, ':', 1) NOT IN ('exif', 'manual')
ORDER BY split_part(source, ':', 1), floor(extract(epoch FROM recorded_at - $from) / $bucket), recorded_at, source
```

`exif` and `manual` rows are never bucketed, because derived points are what this view most needs to show. They come from a second query, whole and on top of the budget:

```sql
SELECT source, recorded_at, ST_Y(location::geometry), ST_X(location::geometry), accuracy_m, altitude_m
FROM gps_tracks
WHERE owner_id = $1 AND recorded_at BETWEEN $from AND $to
  AND split_part(source, ':', 1) IN ('exif', 'manual')
```

The range is inclusive, so a row at exactly `to` can land in one extra bucket per format. The bound a test may assert is `returned_points <= maxPoints + log formats + derived rows`. `total_points` counts log and derived rows. Points are returned in time order, sorted by parsed time and never by string.

The format is computed after the time-range scan. **Never filter a format with `LIKE` or a prefix range on `source`.** A range test on one real database, under its `en_US.utf8` collation, returned 0 rows. `source = ANY($1)` is fine for one whole label.

The context track of one item covers a single window, `taken_at` plus or minus W. `provenanceContextMaxPoints` is 2,000. On 300 sampled estimates, the median context within W either side was 1,555 points, the 95th percentile 10,747 and the maximum 14,508, so a budget is needed for every open and every arrow step. Bracket points are not guaranteed to survive downsampling. The evidence view draws them from its own response.

```ts
interface TrackResponse {
  windows: { from: string; to: string }[];
  match_window_s: number;
  total_points: number;
  returned_points: number;
  bucket_s: number;                 // 0 when nothing was downsampled
  sources: {
    label: string;
    format: string;                 // "other" when the prefix matches no known format
    kind: "recorded" | "semantic" | "time_may_be_estimated" | "derived" | "other";
  }[];
  points: {
    t: string;                      // RFC 3339, UTC
    lat: number;
    lng: number;
    s: number;                      // index into sources
    acc: number | null;
    alt: number | null;
  }[];
}
```

`kind` is decided by format only: `recorded` for `gpx`, `tcx`, `nmea`, `esoko`, `samsung_health`; `semantic` for `google_timeline`; `time_may_be_estimated` for `kml`, `kmz`, `geojson`; `derived` for `exif`, `manual`; `other` for the rest. It describes the format. It is not a claim about any one row.

### `GET /api/media/{id}/provenance`

Session-only and compressed with `middleware.Compress(5, "application/json")`. There are no query parameters. The handler validates the path with `auth.CanonicalUUID` (malformed is 404), takes the caller from `auth.MustUser`, and reads the item `WHERE m.id = $1 AND m.owner_id = $2`, so a foreign or shared item is 404. It carries no secrets, so compression respects the rule in `main.go`.

```ts
interface ProvenanceResponse {
  item: {
    id: string;
    filename: string;
    media_type: "photo" | "video" | "photo_360" | "video_360";
    taken_at: string | null;        // RFC 3339, UTC
    created_at: string;             // upload time, RFC 3339, UTC
    lat: number | null;
    lng: number | null;
    gps_source: "exif" | "interpolated" | "manual" | "none" | null;
    gps_confidence: number | null;
    camera_model: string | null;
    width_px: number | null;
    height_px: number | null;
    duration_sec: number | null;
  };
  time: {
    recorded: "ingest" | "backfill" | "not_recorded";
    evidence: CaptureEvidence | null;       // null when not_recorded
    server_camera_zone: {                   // the zone this server applies today
      name: string;                         // "server zone (unset)" when CAMERA_TIMEZONE is unset
      set: boolean;
      utc_fallback: boolean;
      offset_now_min: number;
    };
  };
  placement: {
    state: "current" | "stale" | "no_support" | "pending" | "no_bracket"
         | "no_time" | "own_position" | "manual";
    window_s: number;
    now: Estimate | null;
    independent: {
      lat: number;
      lng: number;
      confidence: number;
      moved_m: number | null;
      before: Bracket | null;
      after: Bracket | null;
    } | null;
    nearest_outside: { before: OutsidePoint | null; after: OutsidePoint | null } | null;
    others: OtherFormat[];              // never null
    warnings: Warning[];                // never null
  };
  context: TrackResponse;               // empty arrays when taken_at is null
}

interface Estimate {
  lat: number;
  lng: number;
  confidence: number;
  delta_m: number | null;               // distance from the stored position; null when there is none
  before: Bracket | null;
  after: Bracket | null;
  arithmetic: {
    shape: "two_sided" | "one_sided" | "exact";
    pair_gap_s: number | null;          // null for one_sided
    pair_km: number | null;             // null for one_sided
    t: number | null;                   // two_sided only: 0 at before, 1 at after
    time_factor: number;
    distance_factor: number | null;     // null for one_sided
    confidence: number;
    implied_kmh: number | null;         // two_sided only
  };
}

interface Bracket {
  source: string;
  format: string;
  kind: "log" | "exif" | "manual";
  t: string;                            // RFC 3339, UTC
  lat: number;
  lng: number;
  accuracy_m: number | null;
  altitude_m: number | null;
  gap_s: number;                        // absolute distance in time from the item
  origin: {
    state: "linked" | "orphan" | "burst";
    media: { id: string; filename: string }[];
  } | null;                             // null for kind "log"
}

interface OutsidePoint {
  source: string;
  format: string;
  t: string;
  lat: number;
  lng: number;
  gap_s: number;
}

interface OtherFormat {
  format: string;                       // the raw prefix, so an unknown one is shown as written
  before: OtherPoint | null;            // this format's nearest point at or before the item's time, within W
  after: OtherPoint | null;             // the same row as before when it is at the exact time
  alone: {                              // where this format's points alone place the item (geo.Explain)
    lat: number;
    lng: number;
    shape: "two_sided" | "one_sided" | "exact";
    confidence: number;
    distance_m: number | null;          // from the position on the map
  };
  used: boolean;                        // one of its points is the estimate's before or after point
}

interface OtherPoint {
  source: string;
  t: string;
  lat: number;
  lng: number;
  gap_s: number;
  accuracy_m: number | null;
  distance_m: number | null;            // from the position on the map
  used: boolean;                        // the estimate's before or after point
  self: boolean;                        // the item's own exif or manual point
}

interface Warning {
  code: "pair_impossible" | "pair_far" | "logger_parked" | "confidence_zero" | "point_orphan";
  level: "warn" | "info";
}
```

`CaptureEvidence` is the object in the schema section.

#### Placement states

`placementState` is a pure function, tested by table. The first matching row wins. "Bracket" means a recompute with no exclusions found at least one point within W.

| Order | State | Exact condition | Actions offered |
|---|---|---|---|
| 1 | `manual` | `gps_source = 'manual'` | Move by hand |
| 2 | `own_position` | `gps_source = 'exif'` | Place by hand |
| 3 | `no_time` | `taken_at` is null | Place by hand |
| 4 | `pending` | `location` is null, and a bracket exists | Re-match, Place by hand |
| 5 | `no_bracket` | `location` is null, and no bracket exists | Place by hand |
| 6 | `no_support` | `location` is set, and no bracket exists | Place by hand, **no Re-match** |
| 7 | `current` | `location` is set, a bracket exists, `delta_m <= 1`, and `abs(stored confidence - recomputed confidence) <= 0.001` | Place by hand |
| 8 | `stale` | Everything else, including a null stored confidence on an estimate | Re-match, Place by hand |

The thresholds of 1 m and 0.001 are the tolerances at which the recompute reproduced 4,962 of 4,962 stored rows.

A `gps_source` of `none` is treated as null for this table. The database `CHECK` allows that value and no code writes it (0 rows on the measured library). Such a row is offered Place by hand only, because the matcher's query excludes that value and Re-match could never place it.

Why `no_support` exists: the matcher skips an item with no bracket and never clears its location, so the old value stays forever and Re-match cannot change it. Measured today, 0 of 4,956 estimates are in this state. It is expected after track files are cleaned up.

Why `stale` can appear: a track row was deleted, moved, or inserted by SQL or the import script without a matcher run; `GPS_MATCH_WINDOW_HOURS` differs between processes (compose does not pass it); the algorithm changed; a run was aborted (the matcher has no transaction and stops at its first error); two runs overlapped for one owner (there is no lock); or `taken_at` changed without a re-run. The response returns `window_s` so that a window mismatch is visible.

#### Computing the fields

- **`now`** is the recompute, with no exclusions. It is non-null for `current`, `stale` and `pending`, and null otherwise. An item in `own_position` or `manual` would never be re-matched, so "what the matcher would do now" does not apply to it.
- **`independent`** re-runs `bracketingPoints` with `excludeFormats: ["exif", "manual"]` whenever `taken_at` is set, and is null when that finds nothing. `moved_m` is measured from the stored position, or from `now` when there is no stored position. For an own position it answers how far the logs were at that moment.
- **`nearest_outside`** is set for `no_bracket` and `no_support`, and null otherwise. It is the nearest point earlier than `taken_at - W` and the nearest later than `taken_at + W`, by two indexed `LIMIT 1` lookups with the same tie-break. Of 1,667 unlocated items, 171 missed the window by under an hour, so this line has real use.
- **`others`** has one row per raw format prefix with a point in the window, so that formats the matcher passed over show beside the ones it used. Two unknown prefixes stay two rows, each shown as written rather than as `other`. One scan of the window lists the prefixes, nearest to the item's time first. Each row's `before` and `after` come from `bracketingPoints` with every other prefix in the window excluded, so they are the matcher's own query on that format alone, and `alone` is `geo.Explain` on them. `self` marks the item's own `exif` or `manual` point: the item's own derived format, at its exact time and within 1 m of its stored position. `distance_m` is measured from the stored position when there is one, otherwise from `now`; for an own position it is that format's error at that moment. Rows are ordered by how closely each format surrounds the item: formats with a point on each side first, by the longer of the two gaps, then formats with one side, by that gap, then by prefix. At most 16 rows are returned, the nearest formats kept, because each costs two bracket lookups; a format beyond the cap is still excluded from the others' brackets. On one real library this cost a median 1.5 ms and a 95th percentile of 14 ms per item, against 2.1 ms and 19 ms for the single nearest-point query it replaced.

  ```sql
  SELECT prefix FROM (
    SELECT DISTINCT ON (split_part(source, ':', 1))
           split_part(source, ':', 1) AS prefix, abs(extract(epoch FROM recorded_at - $4::timestamptz)) AS gap_s
    FROM gps_tracks
    WHERE owner_id = $1 AND recorded_at BETWEEN $2 AND $3
    ORDER BY split_part(source, ':', 1), gap_s
  ) formats
  ORDER BY gap_s, prefix
  ```

- **`origin`** for an `exif` or `manual` bracket point joins `media` on `owner_id`, `taken_at = recorded_at` and `gps_source IN ('exif', 'manual')`, capped at 6 rows. Zero rows is `orphan`, one is `linked`, more is `burst`. There is no foreign key, so `orphan` is an inference. On the measured library, 18% of estimates rest on at least one derived point and 12% rest only on derived points.
- **Times** are serialised with `.UTC()`.

#### Warnings

Every warning is computed from `now`. The thresholds are stated with what was measured.

| Code | Level | Exact condition |
|---|---|---|
| `pair_impossible` | warn | `two_sided`, the two formats differ, `implied_kmh > 150`, and `pair_km > 0.1` |
| `pair_far` | warn | `two_sided`, the two formats differ, `pair_km > 1`, and `pair_impossible` does not apply |
| `logger_parked` | info | `two_sided`, both points `kind = "log"` with the same `source` label, `pair_gap_s >= 120`, and `implied_kmh < 2` |
| `confidence_zero` | info | `confidence == 0` |
| `point_orphan` | warn | `before` or `after` has `origin.state = "orphan"` |

Of 4,956 estimates, 924 cross formats, 47 of those pairs are over 1 km apart, and 8 of the 47 exceed 150 km/h. The 0.1 km floor on `pair_impossible` is a judgement that removes jitter between near-simultaneous points. Its yield was not measured, because the replay only counted pairs over 1 km. Check it in the PR.

`logger_parked` needs one `source` label because its sentence says "this logger", and two labels are two files or two devices. The 120 s floor is a judgement, like the 0.1 km floor: without it, a 1 Hz log during the few seconds someone stands still would qualify. The cost is that a 1 Hz logger parked for hours does not fire it either, because its pair is one second apart; only Step 4's stationary test looks beyond the pair. Its yield was not measured. Count it in the PR.

Confidence stays high for these pairs, because the formula has no notion of a format: a 5 km pair 30 s apart scores about 94%.

Two formats alone are not a warning. Of 3,290 two-sided estimates without ties, 737 (22%) mix formats, and those pairs are no farther apart than single-format ones: median 162 m against 182 m, over 1 km in 6% against 18%. Only the distance or the speed makes a mixed pair worth flagging.

### `GET /api/media/{id}/provenance/file`

Session-only, owner only, `auth.CanonicalUUID`, literal `owner_id`, 404 for shared items, a 15 s context. It is read-only and never moves `taken_at`.

Photos use `storage.ReadHead(ctx, key, mediaHeadSize)`, one ranged GET of at most 1 MiB, then `ExtractExif` with today's zone. Photos are never transcoded, so the stored object is the original. Videos use `ingest.ProbeURL` on a URL from `storage.PresignGetInternal`, signed for `S3_ENDPOINT`: the backend fetches it itself, and `S3_PUBLIC_ENDPOINT`, which `PresignGet` signs for, may be a host only browsers reach. Never use `ProbeVideo`, which copies the whole file. Both helpers are ported from branch `fix/exif-offset-video-location`, commit `f2afa0d`. A head-only read cannot work for a video, because `moov` is often at the end. Over HTTP with `Range`, a synthetic 60 MB moov-at-end file needed 3 requests and about 4.4 MB.

An S3 or ffprobe failure is logged and returns 502. A timeout returns 504.

A video starts one `ffprobe` process per request. Every existing row is `not_recorded`, so without a limit, arrow-stepping through old videos would start one process per item. Three things bound it. The frontend reads only after an item has been current for 400 ms. The request carries an `AbortSignal`, so leaving an item cancels `r.Context()`, and `ProbeURL`'s `exec.CommandContext` then kills the process. The handler holds a semaphore of 4 concurrent `ffprobe` runs and waits for a slot inside the same 15 s context, so an exhausted wait is the ordinary 504. The limit of 4 is a judgement and was not measured.

```ts
interface FileReread {
  basis: "reread_now";
  stored_taken_at: string | null;
  uploaded_at: string;
  file: {
    wall_clock: string | null;
    wall_tag: "DateTimeOriginal" | "DateTime" | null;
    offset_tag: string | null;
    offset_ignored: boolean;
    gps_stamp: string | null;
    wall_minus_gps_s: number | null;
    make: string | null;
    model: string | null;
    video: {
      creation_time: string | null;
      duration_s: number | null;
      encoder: string | null;
      codec: string | null;
    } | null;
  };
  rule_today: {
    source: "exif_original" | "exif_ifd0" | "container";
    zone_rule: "gps" | "offset" | "camera" | null;
    offset_min: number | null;    // null for a container time
    zone_name: string | null;     // set only when zone_rule is "camera"
    taken_at: string;
  } | null;                       // null when no capture time could be read
  delta_s: number | null;         // rule_today.taken_at minus stored, in seconds
  class: "same" | "whole_offset" | "other" | "no_file_time" | "stored_has_no_time"
       | "unverifiable_transcoded";
  checks: { id: string; level: "ok" | "warn" | "info"; delta_s: number | null }[];
  tags: { name: string; value: string }[];
}
```

`class` is the first that matches:

| Class | Condition | Wording |
|---|---|---|
| `unverifiable_transcoded` | `rule_today` is null, and the item is a video that was re-encoded (defined below) | With evidence: "This video was re-encoded at upload, so its time cannot be checked against the file." By the heuristic: "This video may have been re-encoded at upload, so its time may not be checkable against the file." |
| `no_file_time` | `rule_today` is null | Photo: "No capture time could be read from this file. This reader understands EXIF in JPEG and TIFF only." Video: "No capture time could be read from this video's container." When `capture_evidence.src` is `mtime` and only then, it adds: "The stored time is the file's modified date." |
| `stored_has_no_time` | `stored_taken_at` is null | "The file holds a capture time, but this item has none stored. Re-reading does not change that." |
| `same` | `abs(delta_s) < 1` | "Today's rules agree with the stored time." Never "correct": it is exactly what a repeatable clock error would show. |
| `whole_offset` | `delta_s` is a multiple of 900 and `abs(delta_s) <= 50400` | "Differs by exactly {delta}. An older rule or a changed setting would explain it." |
| `other` | anything else | "Today's rules differ by {delta}." |

"Re-encoded" has one exact definition. When `capture_evidence` is present, only `capture_evidence.transcoded` counts. When it is `NULL`, the heuristic is: the item is a video, `rule_today` is null, `video.codec` is `h264`, and `video.encoder` starts with `Lavf`. On ffmpeg 6.1.1 a transcode carries that encoder tag with or without `-map_metadata 0`. An H.264 file exported by desktop software with no `creation_time` also matches, which is why the heuristic's wording says "may". A missing container time must never be labelled a modified time unless `src` says so.

`checks` in Step 1:

| `id` | Level | Condition |
|---|---|---|
| `taken_after_upload` | warn | `stored_taken_at > uploaded_at` |
| `ifd0_may_be_edit_time` | info | `wall_tag = "DateTime"` |
| `offset_ignored` | info | `offset_ignored` is true |
| `gps_clock_rejected` | info | `gps_stamp` is present and `zone_rule` is not `gps` |
| `video_time_may_be_end` | info | a video whose time came from the container |

`delta_s` on a check is set for `taken_after_upload` (`stored_taken_at` minus `uploaded_at`) and `gps_clock_rejected` (`wall_minus_gps_s`), and is null for the others.

`tags` feeds a disclosure. `ExtractExif` cannot supply it, because it keeps only the tags it uses.

- **Photos.** A separate read-only `ingest.ReadTags(head []byte) []Tag` decodes the head within `ExtractExif`'s limits and lists every tag of IFD0 and the Exif and GPS IFDs under imagemeta's names, which are ExifTool's (`ModifyDate` for EXIF's `DateTime`, `ExifImageWidth`, `ISO`). An id imagemeta has no name for is shown in hex. The Interoperability IFD and IFD1 are left out, and so are the pointers between IFDs. imagemeta reports no tag types, so a binary value shows its printable characters as text and one with none is dropped. A value over 256 bytes is left out whole. Values go through `CleanTag`.
- **Videos.** `VideoProbeResult` gains `RawTags map[string]string`: ffprobe's `format.tags` and the first video stream's tags, prefixed `format.` and `stream.`. They are decoded by a separate `json.Unmarshal` whose failure is ignored, so an odd tag can never lose the probe result.
- **Both.** The list is sorted by name, values longer than 200 characters are cut, and it is capped at 200 entries.

### Wiring

```go
// inside the RequireSession group in newRouter
r.With(middleware.Compress(5, "application/json")).Get("/media/{id}/provenance", h.provenance.Get)
r.Get("/media/{id}/provenance/file", h.provenance.File)
```

`NewProvenanceHandler(pool, s3Client, matchWindow, cameraZone)` holds the dependencies. Its `probeURL` field is a function so tests can stub it. `routeHandlers` gains `provenance`. `POST /api/gps-matcher/run` is unchanged.

nginx gives up on an upstream after 60 s by default. That closes the upstream connection, cancels `r.Context()`, and stops the matcher at its next query, with no transaction. Add a longer timeout for this one route to `frontend/nginx.conf`:

```nginx
location = /api/gps-matcher/run {
    proxy_read_timeout 900s;
    proxy_pass http://backend:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
}
```

### Frontend

#### Times: `frontend/src/lib/time.ts`

`Intl` with a required `timeZone` argument and the zone name always in the output: `formatInstant(iso, timeZone)`, `browserTimeZone()`, `formatDuration(seconds)`. There is no shared time helper today and no code names a zone.

`frontend/eslint.config.js` gains `no-restricted-properties` for `toLocaleString`, `toLocaleTimeString` and `toLocaleDateString`, on `src/components/evidence/**` and `src/lib/evidence.ts`, excluding tests. A failure here fails CI, because any warning fails lint.

**The item's capture time** is shown as three labelled clocks:

- **UTC**, always shown.
- **Camera**, the wall clock as written, from `capture_evidence` or the file re-read, with its offset and one word for how the offset was obtained: `measured` (GPS clock), `declared` (offset tag), `assumed` (camera zone) or `file date`. Never colour alone. A video container stores UTC, so the row reads that no wall clock was written. With `src` `mtime` it reads "No wall clock. This is the file's modified date." With `src` `none` it reads "No wall clock." With neither evidence nor a re-read it reads "not recorded".
- **Your browser**, with the zone named.

**Every other instant** (a track point, the upload time, `gps_utc`, `file_mtime`, `creation_time`) has no camera clock. It shows UTC and the browser zone, each labelled, and the camera row is left out.

**The Map card and the gallery label** gain the zone name beside the browser time, through `lib/time.ts`. They cannot show the camera clock, which is not in `/api/media` and must not be derived in the browser, so it stays one click away in the evidence view.

#### Entry points, own items only

- **Map card.** The `GPS: …` line becomes a button that keeps its text and adds "Why here?", with `touchRow` for the 44 px rule. A shared item keeps the plain line.
- **Gallery.** The position label (date, count and file name) becomes a button for own items. It needs `pointer-events-auto`, because the label sits inside a `pointer-events-none` column, plus `touch:min-h-11`, an `aria-label` containing "Why here?", and a small info icon so the label shows that it opens something. The toolbar gains nothing: on a 360 px phone it already holds up to seven 44 px buttons for an own 360° album item, and by arithmetic an eighth pushes the row to 388 px against 344 px available, clipping the close button. That arithmetic was not tested on a device. The gallery is the only route to unlocated items, because the Map always sends `has_location=true`.
- Shared items keep today's text and no button.

#### `EvidenceView`

`components/evidence/EvidenceView.tsx` is one code path for both entries and both layouts:

```ts
interface EvidenceViewProps {
  mediaId: string;
  isOwn: boolean;
  onClose: () => void;
  onPrev?: () => void;   // absent when fewer than two own items can be stepped; both parents wrap
  onNext?: () => void;   // the same
  position?: string;     // "3 / 12", counted among own items
  onLocationSaved: (id: string, lat: number, lng: number) => void;
  onRematched: () => void;
}
```

Root: `fixed inset-0 z-[1100] flex flex-col`, with `useCloseOnBack(onClose)`. Do not put `data-app-dialog` on it: `keyBelongsToDialog` would then make the view ignore its own keys.

**Layout.**

- Header: title, truncated file name, previous, position, next, close. On a coarse pointer every control is at least 44 px.
- Desktop (`md:` and up): map on the left in a `flex-1 min-w-0` box, facts on the right in a 420 px column that scrolls.
- Phone upright: map on top at `h-[40vh] supports-[height:100dvh]:h-[40dvh]`, facts below in a `min-h-0 flex-1` scroller.
- Phone sideways (`short:`): map and facts side by side.

**Map** (`EvidenceMap.tsx`). `isolate` on `MapContainer`. The OSM credit link opens in a new tab. A green unclustered marker for the item with `zIndexOffset={TOP_MARKER_Z_INDEX_OFFSET}`, at the stored position, else at `now`, else no marker. The bracket points of `now` and the straight segment between them, drawn thick. An `exact` bracket draws one point, because both sides are the same row. In `stale` that segment did not produce the stored position, so it is labelled "what a re-match would use". A small dot per format from `others`. The context track through `components/map/TrackLayer.tsx`, one line per source label, coloured and styled by format, with `splitSegments` applied per source: recorded logs solid, `semantic` lighter and dashed, `time_may_be_estimated` dashed, `derived` as dots with no line. Lines are `interactive: false` on the default SVG renderer, in a custom pane at z 450 so markers stay clickable, and they break at time gaps. Dots are one polyline of zero-length segments with a round cap, which redraws fastest (see the rejected canvas renderer). The fit covers the marker, the bracket points and the `others` dots. With none of those it uses the `nearest_outside` points, then the context track, and otherwise a world view at zoom 2, as `EditLocationModal` does. It copies the gallery's size guard, because `fitBounds` on a 0×0 map sets the zoom to infinity. A map click calls a pure `nearestPoint` with a 22 px radius on a coarse pointer and 8 px with a mouse, and opens `PointCard`.

Downsampling keeps one row per format, so a per-source line in a downsampled context may show gaps between files of one format. Drawing per format instead would zigzag between overlapping files and invent movement in a debugging view.

`PointCard` renders as a sibling of `MapContainer` in the map's `relative` wrapper, anchored to the bottom edge with `max-h-full overflow-y-auto`, so it neither moves the facts nor sits inside Leaflet's stacking context. It closes with its own 44 px button, a map click away from any point, a step to another item, or Escape. For a context point it has only the format-level kind, "log" or "another photo or pin", because `TrackResponse` carries no origin.

**Facts, top to bottom.**

1. **Lead:** a state badge and one plain sentence (below).
2. **Time** (`TimeCard.tsx`): the capture time's three clocks; a time origin chip; a zone chip; for a video its duration and that its time can be the end of the clip; the checks; a disclosure with the file's tags. With `recorded = "not_recorded"`, the file re-read loads once the item has been current for 400 ms, and the card reads "Read now with today's rules. The rule used at upload was not recorded." With recorded facts, the re-read is behind a button.
3. **Points** (`PlacementCard.tsx`, `PointCard.tsx`): the bracket points of `now`, headed "What a re-match would use" in `stale`. An `exact` bracket lists its one point once. For each point its format, file, UTC and browser time, gap, accuracy or "not recorded", altitude, a bracket kind chip, and the format's time note. Then span, distance, position along the pair, time factor, distance factor, and confidence last.
4. **Others:** one row per format, "Each format on its own": the gap to its nearest point before and after, and how far its points alone would place the item (Alone), the used rows marked and each row in its format's map colour. On the map, each format's own points are dots joined by a thin dashed line, with a diamond at its own estimate. Below the rows comes one independence line (table below), when one applies.
5. **Actions:** Re-match my library, Place by hand.

**Zone chip wording.** `gps`: the GPS clock, with its residual. `offset`: `OffsetTimeOriginal`. `camera`: the zone name and "nothing in the file confirms this". With `offset_ignored`: "The file declares {tag}, which is ignored for this make." With `camera_zone` unset: "the server's own zone, because `CAMERA_TIMEZONE` is unset", and with `camera_zone_utc_fallback`: a warning that this time was read as UTC.

**Time origin chip.** `exif_original`: "EXIF capture time". `exif_ifd0`: "EXIF general date, may be an edit time". `container`: "Video container time". `mtime`: "File modified time, may be wrong", with the note "A modified time is often a copy or download time." `none`: "No capture time". With no evidence, the chip follows the re-read's `rule_today.source`, and until that loads it reads "not recorded".

**Bracket kind chip.** A log point: "Log". An `exif` point with `origin.state` `linked`: "Your photo {filename}". A `manual` point that is `linked`: "Your pin on {filename}". `burst`: "{n} items at this second". `orphan`: "A deleted item".

**Jumping to the item behind a point.** A `linked` or `burst` chip is a button, because a photo placed from a wrong-clock photo is only explained by reading that photo's own time. `linked` opens its item in the same view. `burst` expands to its up to 6 items, each a button. `EvidenceView` holds `jumpId` state, and the item it shows is `jumpId ?? mediaId`; the loading effect and its stale-response guard key on that id. While jumped, the header swaps previous, position and next for "Back to {filename}", because the parent's stepping is relative to `mediaId`. That button, Escape and the back gesture (`useCloseOnBack` a second time, active only while jumped) return to `mediaId`, and a second Escape then closes the view. One level only: a further jump replaces `jumpId`, and Back always returns to `mediaId`. A change of `mediaId` clears `jumpId`. `onLocationSaved` reports the id of the item on screen, which is why the parents compare ids. `origin.media` comes from a literal `owner_id` join, so a jump can never reach a foreign item.

**State badge.** `current`: "Up to date". `stale`: "Would change". `no_support`: "No support". `pending`: "Not placed yet". `no_bracket`: "No track nearby". `no_time`: "No capture time". `own_position`: "From the file". `manual`: "Placed by hand".

**Sentences** live in the pure `lib/evidence.ts`:

| Case | Sentence |
|---|---|
| `exact` | "A track point at the same second." |
| `one_sided` | "Copied from one point {gap} away, with no distance check." |
| `two_sided`, one format | "Between two points {a} and {b} away." |
| `two_sided`, two formats | "Between two points from different sources ({f1} and {f2}), {a} and {b} away." |
| `stale` | "Re-matching would move this {delta}. Your tracks, this item's time or the match window changed." |
| `stale`, `delta_m <= 1` | "Re-matching would keep this position and change its confidence from {a} to {b}. Your tracks, this item's time or the match window changed." |
| `no_support` | "No track point supports this estimate any more. The matcher never clears a location, so it stays until you place it by hand." |
| `pending` | "A track point exists within the window, but the matcher has not placed this item yet." |
| `no_bracket`, both nearest points | "No track point within {W}. The nearest is earlier by {x} and later by {y}." |
| `no_bracket`, one nearest point | "No track point within {W}. The nearest is earlier by {x}, and there is none later." The later case mirrors it. |
| `no_bracket`, none | "You have no track points at all." |
| `no_time` | "This item has no capture time, so it cannot be matched." |
| `own_position` | "The file gave this position." For a video: "The video's location tag gave this position. It can be the phone's last known fix." |
| `manual` | "You placed this by hand." |
| `pair_impossible` | "These two points come from different formats and would need {v} km/h to connect, so at least one is unreliable for this moment." |
| `pair_far` | "These two points come from different formats and are {d} apart, so the position between them is uncertain." |
| `point_orphan` | "No item exists at this point's time, so the photo or pin that wrote it was probably deleted. The matcher still uses it." |
| `taken_after_upload` | "The stored time is {delta} after the upload. A correct clock cannot produce that." |
| `ifd0_may_be_edit_time` | "This time comes from the general date tag, which editing software can rewrite." |
| `offset_ignored` | "The file declares {tag}, which is ignored for this make." |
| `gps_clock_rejected` | "The photo's GPS clock was not used: it is {delta} from the camera clock, which is not within 2 minutes of a real UTC offset." |
| `video_time_may_be_end` | "On some cameras a video's time is when the recording ended." |
| Re-match failed | "Re-match did not finish; some items may have changed." |

The independence line:

| Case | Sentence |
|---|---|
| `now` uses a derived point, `independent` present | "Without photos and pins this would move {d}." |
| `now` uses a derived point, `independent` null | "No log supports this. It rests only on other photos or pins." |
| `own_position` or `manual`, `independent` present | "The logs at that moment are {d} from this position." |
| `own_position` or `manual`, `independent` null | "No log point is within {W} of this item's time." |

**Confidence wording.**

- Never say 100% means exact unless the shape is `exact`, where it reads: "100% means a track point at the same second. It says nothing about whether the position or the clock is right."
- Below `exact`, the view shows one decimal, floored and capped at 99.9%. The Map card rounds, so any confidence of 0.995 or more displays there as 100%. Of the 1,567 rows that do, 430 (27%) are not same-instant matches. For a row with `shape != "exact"` and `confidence >= 0.995`: "Very close points, not a correct position."
- Confidence 0 gets a sentence: "Confidence 0: the two points are a full window or more apart, or at least 100 km apart. The item is placed anyway, because the matcher has no lower limit." On the measured library, 142 rows have confidence 0.
- A standing line always appears under the points: "A wrong clock looks equally consistent here."
- `logger_parked` reads: "This logger barely moved between these points. It may have been parked."
- Format notes are format-level only. `kml`, `kmz`, `geojson`: "This format's times may be estimated by the exporter. One point cannot be told apart from a recorded one." `google_timeline`: "This point may be a visit midpoint, which can be hours from any real presence, at a place snapped to a venue that can sit far from the device. It cannot be told apart from a path point."

**Keys.** The view's `window` listener starts with `keyBelongsToDialog(e)`, then a **child-open guard**: while the location editor is open, every key is ignored, because `EditLocationModal` handles its own Escape. Without that guard, an arrow key steps the item underneath and Save writes the old picked position to the new item. `keyBelongsToDialog` does not see that modal, which is a plain `div`. Text fields keep their keys. Otherwise Escape closes an open `PointCard` and, with none open, the view, and the left and right arrows step.

**The location editor** is hosted by `EvidenceView`, at z-[1200]. When it opens, the view copies the fields it needs from `response.item` into editor state, and the editor is keyed by that snapshot's `id`, so a late response cannot swap the item under it. `EditLocationModal`'s `item` prop narrows to `Pick<MediaItem, "id" | "filename" | "lat" | "lng">`. Saving bumps a local reload counter and calls `onLocationSaved`.

**Stepping** goes through own items only. Map cluster peers and album galleries mix owners (2 of 18 albums on the measured library have more than one media owner), and provenance returns 404 for the others. The parent computes `onPrev` and `onNext` from its own items. As a safety net, `isOwn === false` renders a calm "Only the owner can see how this item was placed" with **no request sent**.

**Loading.** One effect keyed on `mediaId` and the reload counter, with a stale-response guard. The file re-read has the same guard and aborts its request when the item changes. On the Map tab this must not be `handleSelect`, because `navigatePeer` bypasses it. The previous content stays, dimmed, until the next response arrives. While `response.item.id !== mediaId`, the actions and the map are inert. Otherwise Place by hand, pressed in that gap, would open on the old item and remount onto the new one pre-filled with its estimate, and Save would pin that estimate by hand and feed it to neighbouring estimates. A 404 reads "This item is no longer available." Other errors offer a retry.

**Re-match** appears for `stale` and `pending` only, and never for a `gps_source` of `none`. It calls `POST /api/gps-matcher/run` through a new `api.runMatcher()`, shows a busy state, then refetches and calls `onRematched`. If the call fails or the connection drops, the view shows the failure sentence and still refetches and calls `onRematched`, because the matcher has no transaction and may have written some rows. It runs over the whole library. Its duration was not measured, so measuring it on the largest library is a task of this step's PR.

**Parents.**

- `MapPage`: `evidenceOpen` state, joined to `covered` so its own arrow listener is off; the listener's dependency list gains it. `navigateOwnPeer` wraps as `navigatePeer` does, skipping foreign peers. `onLocationSaved` updates `items`, updates `selected` only when the ids match (the existing save path patches it unconditionally), and bumps `mediaReload`, so re-matched neighbours are not stale. Today only the selected item changes there. `onRematched` bumps `mediaReload` too. The reloaded list also refreshes `selected` and `clusterPeers` by id, because today a reload replaces only `items`, and the card would contradict the view.
- `MediaGalleryOverlay`: a separate `evidenceOpen` boolean, not a new `editing` kind. The key effect returns early while it is open. `useVideoKeys(videoRef, !editorOpen && !evidenceOpen)`. `renderMedia` returns `null` while it is open, so video sound and the WebGL context stop. `onLocationSaved` follows `handleLocationSaved`, and `onRematched` refetches the list the same way.

**Client and types.** `api.mediaProvenance(id)`, `api.mediaProvenanceFile(id, signal)`, `api.runMatcher()`. The existing helpers take no signal, so the file helper passes `{ signal }` to `request()` itself. The response types go in `types/index.ts`.

**Privacy page.** `frontend/public/privacy.html`: the metadata list gains the camera make and model, dimensions, duration and how the capture time was derived (including the photo's GPS timestamp and the file's modified time); the location section says imported tracks are also shown to you, and only you, to explain an estimate.

### Files

**New, backend:** `migrations/000010_capture_evidence.{up,down}.sql`, `internal/handlers/provenance.go`, `internal/handlers/provenance_file.go`, `internal/handlers/track.go`, and their tests.

**Changed, backend:** `internal/geo/gps.go`, `internal/ingest/exif.go`, `internal/ingest/video.go`, `internal/storage/s3.go`, `internal/handlers/gps_matcher.go`, `internal/handlers/register.go`, `internal/handlers/media.go`, `internal/handlers/ingest.go`, `internal/ingest/exiftest/exiftest.go`, `cmd/server/main.go`.

**New, frontend:** `src/components/evidence/{EvidenceView,EvidenceMap,PlacementCard,TimeCard,PointCard}.tsx`, `src/components/map/TrackLayer.tsx`, `src/lib/evidence.ts`, `src/lib/time.ts`, `src/lib/track.ts` (`splitSegments`, `nearestPoint`).

`splitSegments` breaks a line where the gap exceeds `max(300 s, 3 * bucket_s)`, or 7,200 s for `google_timeline`. On the measured library, 1 Hz formats have 0.03–0.05% of gaps over 5 minutes and Timeline has 37%.

**Changed, frontend:** `src/pages/MapPage.tsx`, `src/components/MediaGalleryOverlay.tsx`, `src/components/EditLocationModal.tsx`, `src/api/client.ts`, `src/types/index.ts`, `eslint.config.js`, `public/privacy.html`, `nginx.conf`.

**Docs:** `CLAUDE.md` gains the two routes, the note that `/api/media` is no longer the only compressed route, the z-[1100] layer's second user, key paths and test lists.

### Tests

**Backend, unit.**

- `internal/geo/gps_test.go`: `Explain` equals `Interpolate`, table-driven, every shape.
- `internal/ingest/exif_test.go`: the raw values; `offsetTimeOriginal` reads an ignored tag without changing the result; a wall tag that does not parse; a GPS stamp with no wall clock; `CleanTag` by table.
- `ReadTags`: `OffsetTimeOriginal` is listed, the order is stable, unnamed ids appear in hex, a binary value with nothing printable is dropped, the Interoperability IFD is not listed, and the decoder's limits hold.
- `internal/ingest/video_test.go`: duration and encoder decoded from a fixture copied from real ffprobe output; a missing, unparseable, NaN or non-positive duration gives nil and loses nothing else; the raw tag maps.
- `ReadHead` and `ProbeURL`: the tests in `internal/storage/s3_test.go` and `video_test.go` on branch `fix/exif-offset-video-location` are ported with the helpers.
- A real-ffmpeg test, skipped without ffmpeg, that a transcoded file keeps `creation_time`.
- `internal/handlers/register_exif_time_test.go`: `capture_evidence` for every origin and zone, with `CAMERA_TIMEZONE` set, unset, and unset with a UTC server; a zero `CameraZone` reads as UTC; a JPEG with invalid bytes in `Model` still ingests (`exiftest.Photo` gains a `Model` field); a probe width of 0 is stored as `NULL`.
- `internal/handlers/gps_matcher_test.go`: the tie-break and the exclusions.
- `internal/handlers/provenance_test.go`: `placementState` by table, including `gps_source` `none`; the warnings by table, including `logger_parked` with two labels and below the 120 s floor; every `class` of the re-read using `exiftest`, including `stored_has_no_time` and both branches of the re-encode rule.
- `cmd/server/main_test.go`: rows for token → 403 on both new routes, and a session with a malformed ID → 404, which shows the route is wired.

**Backend, integration** (`testdb`).

- `internal/handlers/provenance_integration_test.go`: every placement state; `others` one row per format, each with its own bracket and estimate, other formats skipped, and the cap; exclusions; `origin` linked, orphan and burst; the context budget; a non-UTC process still sends UTC; 404 on a foreign item.
- Beside `TestSharing_GPSIsolation` in `sharing_integration_test.go`: an album member gets 404 and no track data on both new routes.
- An integration test that `GET /api/media` and the Game payload never return `capture_evidence`.
- `internal/db/migrations_integration_test.go`: migration 10 up, down and up again.
- `cmd/server/compress_integration_test.go`: provenance is gzipped. That test authenticates by token today, so `compressStore` needs a session lookup.
- Tied points pick one row on both sides.
- `RunGPSMatcher` with `bracketOptions{}` places items, which pins the normalised nil exclusion list. The mock tests ignore arguments and cannot see this.
- The context budget: derived rows are never thinned, and `returned_points <= maxPoints + log formats + derived rows`.
- The file re-read never moves `taken_at`.

**Manual.** The ranged GET is a new kind of S3 call, so check it against MinIO, SeaweedFS and the object store the hosted instance uses, as `CLAUDE.md` requires. Also check `ffprobe` by presigned URL against all three, including over https.

**Frontend (Vitest).**

- `src/lib/time.test.ts` with explicit zones, because CI runners are UTC, including a daylight-saving boundary.
- `src/lib/evidence.test.ts`: every state, shape, warning, check and chip; the confidence wording, including 0, near 100, and exact; `no_bracket` with zero, one and two nearest points; `stale` with only the confidence changed.
- `src/lib/track.test.ts`: `splitSegments`, `nearestPoint`.
- `EvidenceView.test.tsx`, with a mocked map: keys; an arrow press with the editor open does not change the item; Escape with the editor open closes only the editor; Escape with a `PointCard` open closes only the card; `useCloseOnBack`; no request for a non-owner; the stale-response guard. A step, then Place by hand before the response, then the response: the editor's item did not change, or the editor could not open. A failed Re-match shows its sentence and still refetches. A `linked` chip opens its item and Back returns to `mediaId`; a `burst` chip lists its items; previous and next are hidden while jumped; Escape and the back gesture return to `mediaId` before closing the view; a change of `mediaId` clears the jump.
- `TimeCard.test.tsx`: the camera clock reads "not recorded" when absent; `src` `mtime` and `none` wording; the re-read loads after 400 ms for `not_recorded`, is aborted when the item changes, and waits for the button when evidence exists.
- `PlacementCard.test.tsx`: `exact` lists one point; the `stale` heading; each independence sentence.
- `MapPage.test.tsx`: no entry on a shared item; the page's own arrow listener is off while the view is open; `onLocationSaved` for another item leaves `selected` alone; a reload refreshes `selected` and `clusterPeers`; `navigateOwnPeer` skips foreign cluster peers and wraps; position counts own items only; `onPrev` and `onNext` are absent with fewer than two own items.
- `MediaGalleryOverlay.test.tsx`: the toolbar has the same number of buttons as before; the viewer is unmounted while the view is open; keys do not reach the gallery; stepping passes through own items only and wraps; position counts own items only; `onPrev` and `onNext` are absent with fewer than two own items.
- `TrackLayer.test.tsx` and `EvidenceMap.test.tsx` with real Leaflet, through `map.eachLayer`: line breaks at gaps, the green marker's z-index offset, and the OSM credit opening in a new tab.
- `src/api/client.test.ts`: the new helpers.
- The gallery test mocks `EvidenceView`, because that test's Leaflet mock is narrow.

No Playwright spec is required in Step 1.

### Pull requests and rollout

The plan was four pull requests: recording (migration, ingest, `-map_metadata 0`, camera zone); time labels (`lib/time.ts`, the Map card and gallery labels, the lint rule); the file re-read route with the S3 check; then the matcher, `geo.Explain`, `loadTrack`, provenance and the evidence view. Step 1 ships as one pull request instead, because the four parts share files: `exif.go`, `video.go`, `main.go`, the Map page and the gallery. Its first three commits are Step 1 (backend, frontend, docs). The last two give each format its own points and estimate in the others table, as "Computing the fields" describes, and add the measured reliability entry under deferred work.

After deploying the matcher change, run the re-match once per owner, so a tie that flips does not read as `stale`. The route acts only on the caller, so an operator runs it with `server scan --user <id> --dir <empty dir>`, which runs the matcher after an empty walk; alternatively the owner presses Re-match or uploads something, and stale rows are expected until then. Measure the tie aggregate on the hosted instance first, since it has been measured on only one library.

### Implementation notes

Where the code differs from the specification above, or settles something it left open.

**Backend.**

- `ffprobe` and `ffmpeg` read only the containers ingest accepts (`-format_whitelist`). Detecting the format from the bytes would let a manifest under a video extension make the server fetch what it names. This covers upload, the file re-read and transcoding.
- At most 4 photo heads are fetched at once, separately from the 4 `ffprobe` runs, and each is then decoded in one of the 4 EXIF decode slots uploads use, because decoding cannot be cancelled. A numeric tag of more than 100 values is listed as its length.
- An EXIF width or height that is 0, negative or above 2^31 − 1 counts as absent, and a probe's too. Such a value would make the `INT` column refuse the row and fail the upload.
- `TranscodeH264` receives the time ingest read and writes it with `-metadata creation_time=`, or clears the tag when ingest read none. `-map_metadata 0` alone would carry over a time ingest refused, such as an AVI's zone-less `IDIT` time, which `ffmpeg` then writes as an instant in the server's own zone. A transcoded object's container time is therefore exactly what ingest read or absent. A transcode with none has `rule_today` null and class `unverifiable_transcoded`.
- A year outside 0 to 9999 is `null` in evidence and on both routes, because JSON cannot carry it. A stored `taken_at` like that reads as no time. So does one whose match window leaves that range: `item.taken_at` is `null`, the state is `no_time` and the context is empty. The file re-read still reports that stored time, because it uses no window. `nearest_outside.before` or `.after` is `null` when the nearest row on that side is out of range, since every row further out is too.
- `HaversineKm` clamps its haversine term to at most 1. Exact antipodes give half the circumference, with distance factor and confidence 0, never `NaN`.
- `capture_evidence` is `NULL` if it cannot be encoded at all. `parseProbeOutput` never produces such a value.
- `offset_ignored` is true for a blank offset tag on an exempt make, and `offset_tag` is then `null`.
- `others` was first one row per format with its single nearest point. A follow-up gave each format both sides and its own estimate, as "Computing the fields" now describes.
- `ReadTags` lists only IFD0 and the Exif and GPS IFDs, because imagemeta names the Interoperability IFD's tags 1 and 2 `GPSLatitudeRef` and `GPSLatitude`.
- EXIF is read through imagemeta, which replaced goexif (#66) after this note was written. On one real library of 14,188 objects, `ExtractExif` and `Raw` agreed with the goexif version on all but 13, and each of those gained a value. 3 files are partial, all of them files goexif could not read at all.
- A `by` other than `ingest` or `backfill` counts as not recorded.
- Pair warnings compare raw source prefixes, so two unknown prefixes count as two formats, although both report as `other`.
- `trackBucketSeconds` has a minimum of 1, and the provenance handler sets `MatchWindowS`.
- `returned_points <= maxPoints + log formats + derived rows` holds for one window. With several windows, which Step 3 needs, the middle term is log formats times windows.

**Frontend.**

- The view moves the focus in when it opens, keeps Tab inside, and gives the focus back. Its map has Leaflet's keyboard handler off, and its item marker is not in the tab order.
- Lone points of a dashed source are drawn as a second, solid line, because Chromium paints no zero-length subpath in a dashed path with several subpaths.
- After a failed step or jump the view shows neither the previous item's name nor its map.
- `nearest_outside` points are drawn as hollow dots and can be picked. The bracket has its own pane at z 460.
- Each format's own points are filled dots in its colour, joined by a thin dashed line, with a diamond where that format alone would place the item; points the estimate used, and the item's own point, are not drawn twice. The table gives each row the same colour. A picked point says which side of the capture time it is on.
- `independenceLine` says nothing for `own_position` or `manual` with no time, since no comparison ran.
- `burst` shows at most 6 items, because the backend caps the list.
- `formatInstant` takes an optional third argument, `{ seconds?, locale? }`, and appends the IANA name without a numeric offset. In the repeated autumn hour, two instants an hour apart can print the same. The evidence view shows UTC beside it, the Map card and the gallery label do not.
- The Map card's date now has a short month, seconds and the zone.
- `splitSegments` uses a flat 7,200 s for Google Timeline. A Step 3 bucket over 2,400 s would break that line at every point.

## Step 2: Checks page

Estimated size: M, about one week.

Finds problems without the owner suspecting a particular item. None of it depends on the Step 4 backfill.

### Routes

`GET /api/checks` and `GET /api/checks/agreement`, both session-only and compressed, literal `owner_id`, 30 s and 60 s contexts respectively.

Bracket recomputes go through a new `bracketingPointsMany`, which sends the same two SQL constants as `bracketingPoints` through a `pgx.Batch`. The SQL text is shared, so this is not a second copy of the bracket query.

```ts
interface ChecksResponse {
  generated_at: string;
  window_s: number;
  files: {
    source: string;
    format: string;
    kind: string;
    points: number;
    first: string;
    last: string;
    items_placed: number;
    modal_gap_share: number | null;   // share of consecutive gaps equal to the file's most common gap
  }[];
  residuals: { format: string; n: number; p50_m: number; p90_m: number }[];
  checks: {
    code: string;
    severity: "high" | "low";
    total: number;
    groups: {
      key: string;
      label: string;
      count: number;
      evidence: Record<string, string | number | null>;
      media_ids: string[];            // capped at 200
    }[];
  }[];
}

interface AgreementResponse {
  generated_at: string;
  files: { source: string; samples: number; matched: number; median_m: number | null }[];
  truncated: boolean;
}
```

### Per-file inventory

One row per track file: format, points, first and last time, items placed, and the median distance to other formats at the same moment. It is sorted by any column.

Sorting exposes two past problem classes: logs dated far in the future, by `last`, and files recording a device that was somewhere else, by the median distance. On the measured library, 64 files sat more than 100 km from the other formats at the same moment, by the replay's test.

`items_placed` counts own estimates whose recomputed before or after point has that label.

`GET /api/checks/agreement` is separate because its cost was not measured at library scale. The method: for each file, take up to 20 evenly spaced samples at least 30 s apart; for each sample, find the nearest point of a different format, excluding `exif` and `manual`, within 5 minutes; report the median distance, null with fewer than 5 matches. If it takes more than a few seconds on the hosted server, move it behind a button.

`modal_gap_share` exists so that synthetic time spacing can be decided from data. On the measured library, one value accounted for 94% of KMZ gaps, 23% of KML, 37% of GeoJSON and 83% of TCX. So KMZ looks like TCX, and its time spacing was never checked.

### Per-format residuals against photos

For each own-position photo (`media_type IN ('photo', 'photo_360')`), the nearest point of each format within 5 s, and the median and 90th-percentile distance. `exif` and `manual` are excluded. This is the single most informative table found. On the measured library, the medians were: `samsung_health` 10 m, `gpx` 11 m, `nmea` 32 m, `google_timeline` 69 m, and `esoko` 340 m (n=552), while `esoko` agreed with `nmea` to 3.2 m at the same second (n=1,967).

The label says: typical distance between this format's points and photos with their own position. It measures disagreement. It does not say which side is right, and photos with wrong clocks contaminate it slightly. Agreement with another format to 3 m also makes a constant coordinate bias in that format unlikely.

### Library-wide rules

| Code | Severity | Condition | On the measured library |
|---|---|---|---|
| `pair_impossible` | high | The Step 1 warning, over all estimates, grouped by format pair. | 8 pairs over 150 km/h and over 1 km. |
| `pair_far` | low | The Step 1 warning, grouped the same way. | 39 further pairs over 1 km. |
| `future_track_points` | high | `recorded_at > now() + interval '1 day'`, grouped by file. | 27,558 points. |
| `taken_after_upload` | high | `taken_at > created_at`. | 6 items, all more than a year ahead, none located. |
| `orphan_points` | low | An `exif` or `manual` point with no own item at that instant, with the count of estimates using it. | 34 points (32 `exif`, 2 `manual`), used by 6 estimates. |
| `stale_estimates` | high | Own estimates in state `stale` or `no_support`. | 0 today; expected after track cleanups. |
| `time_may_be_estimated_used` | low | An estimate using a `kml` or `geojson` point, while a recompute excluding those formats still finds a bracket. Reports how far each would move. `kmz` is listed separately as unproven. | 287 estimates use such a point, 154 have a real point in the window, 78 would move over 100 m and 30 over 1 km (maximum 152 km). |

A bracket-pair check counts on the pair's own distance and speed, so it reaches cases the owner would never open. The format pair is the group key, so that one bad file shows as one row.

### Upload receipts

**Media.** `POST /api/media/upload` gains an additive `time_report`:

```ts
interface TimeReport {
  by_source: Record<string, number>;   // exif_original, exif_ifd0, container, mtime, none
  by_zone: Record<string, number>;     // gps, offset, camera
  camera_zone: string;                 // the zone name, or "server zone (unset)"
  assumed_zone_is_utc: boolean;        // any file used rule "camera" in a zone that is UTC all year
  file_date_names: string[];
}
```

The Upload page shows one line per outcome, including the zone name, for example "38 from EXIF (10 GPS clock, 20 photo offset, 8 assumed camera zone), 2 from file date". The names in `file_date_names` are expandable under that line. A warning appears when **any** file used an assumed zone that is UTC. The all-year test avoids a false alarm for a zone that is at UTC only part of the year. `registerMedia` returns the provenance values to its callers. `server scan` prints the same counts on stdout. Files skipped by the browser's hash check were never read and are not reported.

A wrong but non-UTC zone gets no warning. Over https the backend already refuses to start without `CAMERA_TIMEZONE`, so this warning matters mainly on an http server or when the zone is explicitly UTC.

**Tracks.** `trackUploadResponse` gains `files: [{ file, format, points, first, last }]`, so that a span reaching decades ahead is obvious on the day of the import. A span with `last` more than a day ahead is shown as a warning. Each first and last time shows UTC and the browser zone, each labelled.

### Frontend

`components/checks/ChecksView.tsx`, full screen at z-[1000], `useCloseOnBack`, `keyBelongsToDialog`. A "Checks" button in the Trips page header opens it. Nothing loads before that, and there is no count badge, because a badge would compute on every visit. Sections: track files (with `SearchInput` and sortable columns, the agreement column filling in when ready), formats against photos, findings. Each finding is a sentence with its evidence; opening one starts `EvidenceView` stepping through its `media_ids`. High-severity findings come first and low-severity findings start collapsed. On a phone, rows stack.

`generated_at` and each file's first and last time go through `lib/time.ts` and show UTC and the browser zone, each labelled. `eslint.config.js` extends the bare-`toLocale*` rule to `src/components/checks/**` and `src/pages/UploadPage.tsx`, which holds no such call today.

### Tests

One seeded integration test per rule. Another user's tracks and shared media never enter a result. Token → 403 on both routes. A compression test. `ChecksView.test.tsx` for keys, the back gesture, low severity starting collapsed, and opening `EvidenceView` through `media_ids`. `UploadPage.test.tsx` for both receipts, including the expandable names and labelled first and last times. The upload and scan handler tests for `time_report`. A test with an assumed UTC zone that warns on a single file, and one with a zone at UTC for only part of the year that does not.

## Step 3: Trip and album tracks

Estimated size: M, about one week.

Implements #32, which must be re-scoped first: real points rather than a GeoJSON LineString, because a line carries neither times nor gaps; time buckets rather than stride or Ramer-Douglas-Peucker, because every point must stay real; albums as well as trips; session-only rather than any authenticated caller.

### Routes

`GET /api/trips/{id}/track` and `GET /api/albums/{id}/track`, session-only, `Compress(5, "application/json")`, `auth.CanonicalUUID`, literal `owner_id` on media and `gps_tracks`.

Query: `max_points` 100–10000, default 5000; `pad_h` 0–12, default the match window; `from` and `to`, optional and always intersected with the computed windows; `hide`, repeatable, whole source labels.

Response: `TrackResponse`, with one addition: a derived point carries `orphan: true` when no own item exists at that instant, so it can be drawn hollow.

**Hard cap.** `max_points` limits what is returned, not what is scanned, and editing a trip's dates can add windows. Both routes therefore run in a 30 s context, like the Checks route. A query that runs out of it is logged and returns 504 with `{"error":"track_query_timeout"}`, and the map says "This track is too large to load." A test with an already-cancelled context pins the 504. The 30 s is a judgement and was not measured on the hosted server.

### Windows

- **Trip:** a foreign trip is 404. Windows are the merged union of `[taken_at - pad, taken_at + pad]` over the trip's own items with a time. **There is no widening to the trip's `DATE` columns.** Those columns have day resolution, are derived in the database session zone, and can be edited to any value by `PATCH /api/trips/{id}`, which also pulls matching media into the trip.
- **Album:** a non-member is 404. Windows are the same merged union over `WHERE m.owner_id = $me`, because other members' items were placed from their owners' tracks. No own items gives 200 with empty arrays. A first-to-last window is not an option: album spans on the measured library reach about 16 years, which would cover nearly all 6 million points.
- **Lines never cross a window boundary.** Merged windows are separate polylines. `splitSegments` also takes the window list and breaks between two points in different windows, whatever the gap. Two merged windows closer together than its time threshold would otherwise be joined by a straight line across time nobody asked for, which looks like a real route.
- **Limits, stated honestly.** The response cannot exceed the sum of per-item windows, so no request walks arbitrary history. A stolen session could still widen those by editing a trip's dates, because that pulls in more items. A test pins that a widened trip's response stays within its items' windows.

### Downsampling

`span` is the **sum of the merged window lengths**. Read as first to last item, the worst album on the measured library gets 3.6-day buckets, against 173 s with the sum, at 3 formats and 5,000 points.

Buckets are aligned within each window: the `DISTINCT ON` key includes the window index, and the bucket counts from that window's start.

**Per-window range scans, never a multirange containment test.** On the largest album (924 items, 16 merged windows), `recorded_at <@ multirange` took 890 ms with a sequential scan of 6.2 million rows, and `unnest` plus a per-window range scan took 105 ms for the same 82,926 rows. Pin it with a comment and a test.

With a non-empty `hide`, buckets are per source rather than per format and `formats` in the formula becomes the count of remaining log sources. Otherwise a second file's points in a bucket are never sent and hiding the first leaves false gaps.

### Logging

`from`, `to` and `hide` hold times and file names, and the backend's request logger records the path only, but the hosted nginx logs the full request line. Add to `frontend/nginx.conf`:

```nginx
location ~ ^/api/(trips|albums)/[^/]+/track$ {
    access_log off;
    proxy_pass http://backend:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
}
```

### Which items did this point place

`POST /api/gps-tracks/usage`, session-only, body `{"source": "<label>", "recorded_at": "<RFC 3339>"}`. It is a POST so the file label stays out of the access log.

It is recomputed. There is no stored snapshot. Candidates are the owner's own items with `taken_at` within W of the point and `gps_source = 'interpolated'` or no location, capped at 1,000. Each goes through `bracketingPoints` and `geo.Explain`, and the item counts when its before or after point is that point. Compare `recorded_at` as `timestamptz`, not as text.

```ts
interface UsageResponse {
  total: number;
  placed: {                         // capped at 200
    media_id: string;
    filename: string;
    taken_at: string;
    role: "before" | "after";
    weight: number;                 // 1 - t for before, t for after, 1 for one_sided and exact
    lat: number;
    lng: number;
    confidence: number;
    current: boolean;               // the stored row matches this recompute
  }[];
}
```

On the measured library, 2,774 points were used by a bracket: median 1 item, 95th percentile 11, maximum 248. For derived points: 274 used, median 2, maximum 76.

### Frontend

- `MapPage` shows a Track button, off by default, only when exactly one trip or one album is ticked. `trackScope(filters)` in `lib/filters.ts` returns one trip when `albumIds` is empty and `tripIds` holds one id other than the `unassigned` sentinel, and one album when `tripIds` is empty and `albumIds` holds one id. `owner` and `mediaTypes` do not affect the scope. Whether `fromDate` and `toDate` clip the track is an open question; until it is decided, they do not.
- Toggle state lives in `MapPage`, outside `FilterState`: a new filters object refetches every page.
- `FilterPanel` gets an optional prop that only `MapPage` passes, so the Game can never show a track. `GamePage` renders the same `FilterPanel`; a test pins that no control appears.
- `MediaMap` gains a `children` slot.
- `TrackLegend.tsx` at z-[900]: one row per format with colour, line style and count, plus "sampled every N s" when downsampled.
- `PointCard` shows UTC and browser time, accuracy or "not recorded", altitude, file, speed to its neighbours, the format's time note, and "Placed N items". On a downsampled track it says so next to More detail here: "This track shows one point every {bucket_s} s here." Expanding "Placed N items" lists each item with its role (before or after), weight and position from `UsageResponse.placed`, and selecting a row opens that item's evidence view.
- In the evidence view, the Points section of an `own_position` or `manual` item gains one line from the same route: "This photo's point also placed N others", or "This pin also placed N others".
- **Hide this file** must say it changes only this view: "Hides this file on this map. Estimates still use it."
- **More detail here** is a fixed span of 30 minutes either side of the clicked point, clamped to the windows. A 5,000-point budget reaches 1 Hz at about a one-hour window.
- A 404 on a trip reloads the trip list instead of showing an error: auto trip ids are recreated on every detection.
- A coarse pointer uses a 22 px radius, a mouse 8 px.

**Photo trail.** A checkbox in the legend draws a thin line through located items in capture order, sorted by parsed epoch. It is **drawn per owner**, or two people's photos in a shared album would produce false hops, and only hops within one owner count. A hop over 300 km/h and over 5 km is red and worded "estimates jumped"; it is not a clock signal, because a camera shifted by a constant keeps its order and shows nothing on the trail. The 5 km floor removes GPS jitter between near-simultaneous items. The legend counts the red hops. Of the hops on two trips that held a known clock error, none touched the affected photos. It also gives a route for trips with no track points (65 of 141 on the measured library's largest owner).

**Gallery.** A Route toggle in the mini map of a trip or album gallery: `<TrackLayer fit={false} interactive={false}>` as a `MiniMapOverlay` child. The gallery derives the scope from the `{trip_id}` or `{album_id}` params it already reads, with no new props. It is hidden for an album with none of the caller's items.

Size guard, `isolate`, OSM credit and 44 px controls as in Step 1. `eslint.config.js` adds `TrackLegend.tsx` to the bare-`toLocale*` rule.

### Tests

Integration: owner scoping; an album returns only the caller's points; a window comes from instants, not `DATE`; two windows years apart; a widened trip does not widen the response; buckets are real rows; `from` and `to` are clamped; a hidden file leaves no false gaps; usage keeps two owners with identical labels separate, and a shared item never appears. Token → 403 on all three routes. Extended compression test. Real-Leaflet `TrackLayer` tests through `map.eachLayer`. `filters.test.ts` for `trackScope`. `photoTrail.test.ts` for ordering, per-owner trails and hops. A `GamePage` test that no track control appears. `track.test.ts`: two merged windows closer than the gap threshold produce two lines.

Test setup: `MediaGalleryOverlay.test` mocks `TrackLayer` for the Route toggle, because its Leaflet mock is narrow, and `MapPage.test`'s `MediaMap` mock must render its children, or the layer is invisible there. Real tap accuracy can only be checked in a browser, and no e2e spec clicks a map today, so a Playwright spec stubs the track route, with `video-keys.e2e.ts` as the template, and clicks a point.

Sizing from the measured library: the largest trip window held 260,645 points, 150–260 ms for all rows and 643 ms bucketed, about 45 KB gzip for 2,861 returned points.

## Step 4: Clock check

Estimated size: M to L, about one and a half weeks.

Bracket data cannot show a clock error, so this step tests clocks directly. It needs the backfill for per-camera grouping.

**Rows whose time came from a file date never test a clock.** A modified time is often a copy or download time and says nothing about a camera. A row is left out of every test in this step when `capture_evidence.src` is `mtime` or `none`, or, with no evidence yet, when `taken_at` has a fractional second. That proxy is a lower bound, because a whole-second file time cannot be told apart. The rule covers the own-position reading, the group verdicts, the cross-camera histogram and `video_outlier`, and rows left out count under "Could not be tested".

### One item: `GET /api/media/{id}/provenance/clock?span_h=3|12`

Session-only, `auth.CanonicalUUID`, literal `owner_id`, 404 otherwise.

```ts
interface ClockCheck {
  mode: "own_position" | "estimate_only";
  step_s: number;                  // 900 at span_h=3, 1800 at span_h=12
  points: {
    shift_s: number;
    lat: number | null;
    lng: number | null;
    confidence: number | null;
    residual_m: number | null;     // own_position only
    moved_m: number | null;        // estimate_only only
    source: string | null;
    supported: boolean;
  }[];
  best: { shift_s: number; residual_m: number } | null;
  at_zero_m: number | null;
  reading: "dip" | "no_dip" | "no_usable_reference" | "time_matters" | "time_does_not_matter";
  sensitivity: { per_10min_m: number | null; per_1h_m: number | null };
}
```

**`own_position` mode** applies to `gps_source = 'exif'` with `media_type IN ('photo', 'photo_360')`. **Videos are excluded**, because a video's location tag can be a last known fix and its time the end of the recording. They get `estimate_only`.

A reference point is the nearest point within 5 minutes of the shifted time, from a recorded format: not `exif`, `manual`, `google_timeline`, `kml`, `kmz` or `geojson`. Every test in this step uses this definition, with its own time limit.

**A vehicle logger's stationary points are excluded.** Formats on `stationaryIgnoredFormats` are ignored while the logger is stationary. Stored points hold no speed, so speed comes from the two neighbouring rows of the same source, each at most 120 s away: under 2 km/h counts as stationary, as does a point with no such neighbours. The 120 s limit is a judgement and was not measured. The lookup takes up to 5 nearest candidates and returns the first that is not excluded.

| Reading | Exact condition |
|---|---|
| `dip` | `best.residual_m < 100`, and `at_zero_m >= 4 * best.residual_m`, and `at_zero_m >= 100`, and `best.shift_s != 0` |
| `no_usable_reference` | Shift 0 is unscored, or fewer than 2 steps are scored, or the item's time came from a file date |
| `no_dip` | Otherwise |

Replayed by nearest point within 5 minutes, those `dip` thresholds label 29 of 29 photos known to be off and 5 of 836 others. With one log format removed entirely the figures are 18 of 18 and 1 of 230. The rule adopted here ignores a listed format only while it is stationary, so it sits between those two results, and it was not replayed. An earlier rule (best under 50 m and stored at or above 500 m) labelled only 10 of the 29, because the known median was 425 m. The other 19 read as "flat", the opposite of the truth.

`no_usable_reference` exists because 615 of the 865 photos with any recorded format within 5 minutes (71%) had only one log format in reach, so a plain test cannot work on most of its population.

**`estimate_only` mode** loops `bracketingPoints` and `geo.Explain` at each shift, so there is no second matcher. `moved_m` is the distance from the estimate at shift 0.

| Reading | Exact condition |
|---|---|
| `no_usable_reference` | Either of the ±1 h steps is unsupported |
| `time_does_not_matter` | `per_1h_m < 100` |
| `time_matters` | Otherwise |

On 514 sampled estimates, a one-hour shift moved the estimate a median 2.9 km, at least 10 km for 31% and under 100 m for 11%. The cost is about 50 indexed lookups per request; it was not measured as a Go loop.

**Wording** stays "closest at +1 h", never "wrong". The check is a section of the evidence view, after the time section: one chart, shift from −3 h to +3 h in 15-minute steps, with a toggle that widens it to 12 h, and whole-hour ticks. Nothing is written. `components/evidence/ClockCheck.tsx`, hand-written SVG, snapping only to response points, pointer events shared by mouse and touch.

What the owner is told matters more than the chart. In the replay, "flat" read as the opposite of the truth, so each state has its own sentence:

| Case | Sentence |
|---|---|
| `dip` | "Closest at {shift}: {best} against {stored} as stored." |
| `no_dip` | "No shift of this item's time brings the logs closer." |
| `no_usable_reference`, own position | "This cannot be tested: no recorded log is in reach at this item's time, or its time came from a file date. That does not mean the time is right." |
| `time_matters` | "A 1 h error would move this {per_1h}, a 10 min error {per_10min}." |
| `time_does_not_matter` | "Time does not matter here: a 1 h error would move this only {per_1h}." |
| `no_usable_reference`, estimate only | "This cannot be tested: there is no track an hour either side of this item's time, so the effect of a clock error is unknown." |

### Groups: `GET /api/checks/clocks`

Session-only, compressed, literal `owner_id`, a 60 s context. The route's cost on a small server was not measured.

```ts
interface ClockChecksResponse {
  generated_at: string;
  groups: {
    key: string;                       // camera model and quarter
    camera: string;
    quarter: string;                   // "2024-Q3"
    verdict: "clock_off" | "no_finding" | "no_usable_reference";
    severity: "high" | "low";          // high for clock_off, low otherwise
    n: number;                         // own-position photos tested
    delta_s: number | null;            // the best whole-hour shift; the body of Copy shift
    best_median_m: number | null;
    stored_median_m: number | null;
    tested_ids: string[];              // capped at 200
    untested_ids: string[];            // the same camera and quarter, capped at 200
    untested_total: number;
  }[];
  cross_camera: { camera: string; pairs: number; bins: { offset_min: number; n: number }[] }[];
  video_outliers: { key: string; media_id: string; r_s: number; mode_s: number }[];
  not_testable: { camera: string; n: number }[];
}
```

The result is a Clocks section of `ChecksView`, loaded by its own button because of the 60 s context. `clock_off` groups are listed first and `no_finding` groups start collapsed, like the low-severity findings of Step 2. Each `cross_camera` row is a small hand-written SVG histogram, and "Could not be tested: N items" follows its camera's rows.

**What a verdict covers.** Only own-position photos are tested, yet the clock error applies to the camera's other items in that period too: an estimate or an unlocated photo from the same camera can only be judged through this verdict, and the one known group on the measured library held more photos (42) than were tested (31). The sentence therefore counts both: "This camera in this period: 31 photos tested look 1 h early (2 m against 430 m). 11 others from this camera could not be tested." Open lists `tested_ids` first and then `untested_ids`, under separate headings. Copy shift uses `tested_ids` by default, with a tick to add the untested ones, because a quarter can hold a clock change the test cannot see in them.

**How the verdicts are computed, per camera and period.** For own-position photos, group by `camera_model` and calendar quarter, and compute server-side, on exact rows, the median distance to a reference point within 2 minutes at each whole-hour shift from −3 to +3. The reference point is the one defined above, including the stationary exclusion, with a 2-minute limit. A group is `clock_off` when the best median is under 100 m, the stored median is at least 4 times that and at least 100 m, the best shift is non-zero, and n is at least 5. On the measured library this flagged one group of 15 testable: n=31, +1 h, 2 m against 430 m, in 3 s of SQL. Those figures predate the stationary exclusion, used every recorded point, and were not re-run. A verdict reads as a sentence: "This camera in this period: 31 photos look 1 h early (2 m against 430 m)", with Open and Copy shift. Copy shift produces the `{media_ids, delta_seconds}` body #26 specifies. The fix waits for #24 and #26.

Exact rows matter. Client-side buckets had 0 of 36 relevant photos usable at the default point budget: a long trip gets 816 s buckets against a 2-minute test.

**A cross-camera timing test for cameras with no GPS.** For a camera with no own-position photos, pair each item with the nearest photo from another camera whose offset is measured (`zone = 'gps'`, or `zone = 'offset'` with `offset_ignored = false`) within ±3 h, and report a histogram of the difference in 15-minute bins. It is a reading, worded "timing against other cameras suggests", and would not stand on its own. On the measured library, a camera on the ignored-offset list held 2,302 items by filename pattern (27%), none with its own position, and 398 had a located photo from another camera within 30 minutes.

**"Could not be tested: N items"** is stated explicitly per camera, so that silence is not read as a pass.

**A tag from an ignored make is not "measured".** It is `declared`, and it can never make a group look healthy or wrong.

**`video_outlier`.** For videos with a parsable filename clock, compute `r = filename clock - taken_at - camera zone offset on that date`, rounded to 15 minutes, per filename family and quarter. A video is an outlier when its `r` is at least 1 hour from the mode of `r` in a group of at least 5. The seasonal subtraction matters, because a daylight-saving change can fall inside a quarter: without it, a correct video on the minority side of the change reads as 1 h from the mode. Wording: "This video's stored time is 1 h away from the other videos of this camera; which side is wrong is inferred, not measured." On the measured library, a rule of this form without the seasonal subtraction flagged 12 rows, 11 of them exactly 1 h off. The seasonal form was not replayed.

A filename family is only a proxy. Two cameras sharing one naming pattern merge into one group, and photo and video names differ, so a family rarely spans both.

**Filename clock.** `ingest.FilenameClock(filename)` reads the patterns that embed a clock. The difference is reported as a **camera clock reading** ("this camera's clock showed UTC+1"), never as an error in the stored time, and compared with the same camera's neighbours, which is what isolates the odd ones. Photos are labelled "same clock as EXIF, proves nothing", since 3,669 of 3,670 agree by construction. For a video from a phone that stamps the end of the recording, show `stored - filename - duration`, which should be about 2 s; without that it is a warning on nearly every such video. On the measured library, 70 such videos sat a median 18 s after the filename clock, 61 of them more than 5 s. The effect on positions is small: only 18 of the 70 are estimates, and 51 use their own location tag.

The reading appears as one line under the chart in `ClockCheck.tsx`, and `ClockCheck` gains `filename: { clock: string; family: string; kind: "photo" | "video"; offset_min: number | null; after_duration_s: number | null; end_stamped: boolean } | null`. `clock` is the reading as written, with no zone. `after_duration_s` is `stored - filename - duration`, null without a duration. A video's model is often null, so end-stamping is not recognised by device: a filename family is `end_stamped` when its median `after_duration_s` is within 10 s of zero with n at least 5. The 10 s and the 5 are judgements and were not measured. Table tests cover both sides of each. `ClockCheck.test.tsx` covers the photo label, an end-stamped video and a video with no duration.

### Backfill: `server backfill-evidence --user <id> [--apply]`

Dry run by default, modelled on `RunRetime`. One ranged read per photo, `ffprobe` by URL per video. Prints counts on stdout: consistent, differs by a whole offset, other, unverifiable, per make. With `--apply` it writes `capture_evidence` with `by: "backfill"` and fills `camera_model`, `width_px`, `height_px` and `duration_sec` where null. It never touches `taken_at` or `location`. A file with no readable capture time is written with `src: "none"`, which the clock tests leave out. "Consistent" means only that today's rules agree. `TimeCard` shows "recovered later, using today's rules" for those rows. `file_mtime`, the rule used at ingest and the zone then in force cannot be recovered. Estimated cost on the measured library: 8,070 ranged GETs of at most 1 MiB each (at most about 7.9 GiB read inside the deployment) and 549 `ffprobe` runs, not measured on the real bucket.

### Tests

Integration with a seeded shifted track; the item's own point excluded; a stationary vehicle-logger point excluded and a moving one kept; each reading by table; a video falls to `estimate_only`; group verdict scoping; another user's tracks and shared media never enter. A row with `src` `mtime`, or with a fractional-second time and no evidence, enters none of the four tests. `video_outlier` by table across a daylight-saving change, where a correct video on the minority side is not flagged. Backfill: a dry run writes nothing, apply fills only nulls and never moves `taken_at`, a second run is a no-op, and usage errors and exit codes in `main_test.go`. Token → 403 on both routes. An album member gets 404 and no track data on `GET /api/media/{id}/provenance/clock`, beside `TestSharing_GPSIsolation`. A compression test for `GET /api/checks/clocks`. `ClockCheck.test.tsx`, including each sentence. Table tests for `FilenameClock`. `ChecksView.test.tsx`: the verdict sentence counts tested and untested items, Open lists both under their headings, Copy shift defaults to `tested_ids` and adds the others only when ticked, `no_finding` starts collapsed, and the histogram and "Could not be tested" render.

## Risks and known effects

- **These are the first routes to return raw GPS history over HTTP.** Mitigations are in the invariants. The real limit on a stolen session is stated under Step 3.
- **False reassurance.** Bracket data looks perfect while the clock is wrong. The standing line exists for that.
- **False clock suggestions.** Returning to the same place makes a real-looking dip. Earlier testing found scattered non-zero per-item results that were noise. Wording stays "closest at", and verdicts are per group.
- **Explanations describe the present.** A recompute shows what the matcher would do now, not what placed an item originally. A `stale` or `no_support` item can say only that, and which rows were tied at the original run cannot be recovered.
- **The tie-break moves rows when it ships.** The bound is small on the measured library and unmeasured on the hosted instance.
- **`stale` can be wrong** if `GPS_MATCH_WINDOW_HOURS` differs between processes on one database. `window_s` makes that visible.
- **Rows changed outside the matcher.** Off-main repair tools write rows without updating evidence. They read as `stale`, which is correct but needs care in wording.
- **`capture_evidence` decided none of the 11 replayed incidents**, because every affected row predates it. It is forward-looking insurance.
- **The re-read depends on** ranged GET and `ffprobe` by presigned URL. Both passed on MinIO and SeaweedFS, but the hosted object store and https could not be reached.
- **One `ffprobe` process per video request.** Stepping through old videos is the worst case. The 400 ms delay, the aborted request and the semaphore under Step 1 bound it, but the limit of 4 was not measured.
- **A filename family is a weak proxy for a camera.** Two cameras sharing one naming pattern merge into one group, and photo and video names differ, so a family rarely spans both. `video_outlier` and the other family-based counts inherit this.
- **Library-wide queries on a small server.** The library shift grid took 3 s and the coverage query 2.7 s on the measured library, and either could be much slower on the hosted server.
- **Phone density.** There is a lot of information in one view. Sticky section chips, the mitigation first proposed, are deferred, so the first version relies on the layout alone. Real-device use has not been tested.
- **Thresholds were tuned on one library and one known incident.** The group rule's zero false alarms show it is promising, not that it generalises.
- **A filename clock is a measurement only if the camera wrote the name.** Renamed or edited files look like clock errors, and transcoded videos can lack `creation_time`, so both are labelled, not flagged.
- **Zone labels are hygiene, not analysis.** All 11 replayed incidents were in stored data or ingest, and none was a display bug.
- **Scope.** Each step must stand alone. If only Step 1 ships, the owner still gets the bracket explanation, the time origin for new uploads, and honest zones. Use each step on real problems for a while before building the next.

## Not handled now

Everything below came up during the design and is deliberately left out. Each entry has enough detail to act on later.

### Deferred features

**Explaining one item further**

- **What-if exclusions.** Untick a format, a file, or photos and pins, and see how far the estimate would move, with nothing saved. Needs an exclusion parameter on provenance in a POST body or behind `access_log off`, because labels hold file names, and an `excludeLabels` field beside `excludeFormats`. One format scan cost about 15 ms per item. Worthwhile just before #22 changes the matcher, because it previews that rule on real items. Size: S.
- **Typical error from the owner's own photos.** `GET /api/gps-tracks/calibration`, returning `{generated_at, rows: [{format, shape, n, p50_m, p90_m}]}`, private cache of one hour, one line in `PlacementCard`. Method: each own-position photo goes through `bracketingPoints` with `exif` and `manual` excluded, then `geo.Explain`, and the distance from the photo's own position is grouped by the formats used and the shape. A pair of two formats groups under both, in alphabetical order, as `a+b`. It is valid only where photos carry their own position. Photos with a wrong clock contaminate it, which the medians limit. The test uses a seeded log with a known offset. On the measured library, by span: at most 1 minute median 24 m (n=110), 1–5 minutes 195 m (n=273), exact second 234 m (n=696), one-sided 108 m (n=87). Format is confounded with span, so it must read as typical error for a source, not an error bar. Worthwhile after Step 2's residual table has been used. The SQL equivalent took 82 ms over 1,606 photos; a Go loop was not measured. Size: S.
- **Neighbours list with measured offsets.** Five items each side, with implied speed from the previous item, and a line comparing this camera's offset with nearby photos' measured offsets, for example "The 6 phone photos around this one were at UTC+3; this camera was read as UTC+2". It replaces a timezone-boundary dataset. Needs `capture_evidence` on neighbours, so it comes after the backfill. On the measured library, 20 consecutive own or manual positions imply over 900 km/h within 5 minutes. Size: S.
- **Hour ticks on the evidence map.** Labelled ticks from −3 h to +3 h on the track where the item would land, as a `shift_probe` array of `{shift_s, lat, lng, mode, residual_m}`, computed with `exif` and `manual` points excluded; 12 indexed lookups. For an own-position item, the facts line names the closest tick: "Your photo sits 60 m from the +1 h tick and 2.4 km from now". The photo preview stays beside the facts, so the scene can be compared with the map, and reuses the parent's presigned URL through a prop. Nothing in Step 1 shows the picture in the evidence view. Worthwhile if Step 4's chart proves too abstract. Size: XS.
- **Camera roll-up inside one item's clock section.** "Other photos from this camera within 30 days: 38 of 42 dip at +1 h", with a histogram; `GET /api/media/{id}/provenance/camera`. After the backfill. The design estimated M, including that backfill.
- **Copy report.** One button that copies everything loaded for an item as JSON, on explicit click, with a line saying it contains locations and times, hidden when the clipboard is unavailable. It replaces a manual step when an investigation goes to an issue. It places coordinates on the clipboard, so the privacy trade-off needs a decision. Size: XS.
- **Privacy-safe summary.** A Markdown table of counts, medians and shifts, with no coordinates, dates or file names and camera labels only if ticked, with a test asserting none survive. Any time. Size: XS.
- **A confidence circle on the map.** A lightweight visual companion to the arithmetic. It needs nothing new from the backend. Worthwhile if the numbers alone prove hard to read.
- **A pin's error measurement.** "This pin replaced an estimate 640 m away" is a free accuracy measurement, but it needs the replaced estimate stored at save time, which no longer happens without a snapshot. Worthwhile if it is wanted enough to add a field.
- **Speed in the Step 1 `PointCard`.** Added in Step 3, where neighbours are loaded.
- **Per-format estimates and support ranking in the others table.** Partly built after Step 1: each row of `others` now has its format's own before and after points and its own estimate, the map draws them all, and rows are ordered by how closely each format surrounds the item (see "Computing the fields"). Still open from this entry: tapping a row to draw only that estimate, rows within 5 m collapsing into "same as …", support classes, and what-if exclusions. The original entry follows. A route, first proposed as `GET /api/media/{id}/provenance/alternatives?exclude=`, would give each format its own before and after points and its own estimate through `geo.Explain`, the gap before and the gap after, a support class (`dense`, `sparse` or `one_sided`), and ticks on the formats actually chosen. Tapping a row draws that estimate on the map. Any exclusion by label must follow invariant 9, so it needs a POST body or `access_log off`. Rows within 5 m collapse into "same as …", and rows are ordered by how closely each format surrounds the item. On 514 sampled estimates, 353 had at least two log formats in the window and the widest alternative was a median 618 m away. With two well-supported formats (66 items) the median disagreement was 0 m, 10 exceeded 100 m and 2 exceeded 500 m. Worthwhile if the plain table reads as constant false alarms. Size: S.
- **Section chips on a phone.** Sticky chips (Time, Points, Others, Clock, Near) that jump between sections, where tapping a section also fits the map to that section's geometry. It was the first proposed answer to phone density. Worthwhile if real use shows the scroll is too long.
- **A zone-at-location chip.** Compare the offset actually used with the legal offset at the item's position on that date, worded for example "read as UTC+1, local zone at this place UTC+2", as a chip on the item. It needs a location-to-zone source: a new dependency, or Timeline's per-point offsets where they exist (open question 11). The neighbours entry above is a different signal, from other photos' measured offsets rather than from the place. Worthwhile if the neighbours line proves too sparse.
- **A video's clip span on the evidence map.** For a video with a duration, draw the track between `taken_at` minus the duration and `taken_at`, with the note that `taken_at` can be the end of the clip. Step 1 states the duration in text only. Needs `duration_sec`, so it applies to new rows until the backfill.
- **The two legs of the evidence line coloured by source.** Two segments, from the item to each bracket point, coloured by format, so that a mixed pair is visible on the map. Step 1 draws one segment. Mixing is not an error in itself, so this is a reading aid.

**Library-wide views**

- **Clock board.** A row per camera on a shared time axis, in the Checks view: y is camera clock minus UTC, a grey staircase is the configured zone's legal offset, hollow marks are the assumed offset and filled marks the measured one. Shape gives the kind of measurement: a circle for a GPS clock, a square for an own position against an independent log, a triangle for a video's filename clock. No shape was chosen for an offset tag. A healthy camera has filled on hollow on the staircase. Segment bands read "measured UTC+1, read as UTC+2, 31 items, 2 m against 430 m", and selecting one lists its items. On a phone, rows are 96 px tall in one scroll.

  `GET /api/clocks` is session-only and compressed; the first proposal's `RequireUser` would break invariant 2. It returns `camera_zone`, `zone_steps` of `{from, offset_min}`, and `families` of `{key, label, items, testable, cells, segments}`. A cell is `{week, kind, assumed_min, measured_min, n, median_m_at_stored, median_m_at_measured}`, where `kind` is `gps_clock`, `offset_tag`, `exif_track`, `video_filename` or `filename_assumed`. A segment is `{from, to, n, assumed_min, measured_min, delta_s, agreement, verdict, media_ids}`, where `verdict` is `ok`, `wrong_clock`, `mixed` or `untestable`. A pure Go function builds segments.

  A "Same camera as..." gesture would merge two rows through `PUT /api/clocks/aliases`, body `{family_key, camera_label}`, because on the measured library one camera's videos hold no model. It needs the alias table rejected below. In the replay the board repeated the group verdicts for two problems and, for a make on the ignored-offset list, either passed the bad case or called the largest camera wrong. Build only if the group list proves insufficient, and treat a tag from an ignored make as not measured. A compression test for `GET /api/clocks` goes with it. Size: M.
- **A one-line per-camera offset summary.** The replay's smaller replacement for the time strip and the board: one line per camera saying which UTC offsets its items were read at and which were measured. Worthwhile if the Step 4 group list is too sparse to read.
- **Checks scoped to one trip.** `GET /api/checks?scope=library|trip:<id>`, proposed so that one trip can be checked alone. Step 2 is library-wide only. Worthwhile if the library-wide list grows too long to work through.
- **Coverage calendar.** A year grid with one strip per format, plus a marker where media exists with no track. `GET /api/coverage` took 2.7 s for 5,065 cells with no rollup, private cache of 300 s. Days must be labelled as UTC days. Useful context, but the future-dated rule already catches the main case. Size: S.
- **Zone and daylight bands.** The UTC offset at the track's position over time, and day and night shading. `tz-lookup` at 29 KB gzip behind `import()` would be the app's first code split, and `suncalc` is 4 KB. The bands need a track or an item position. Sun position alone cannot detect a 1 h error, and comparing it with EXIF exposure values was not tested. Mostly a reading aid; worthwhile only if multi-hour errors become a recurring problem. Size: S.
- **Flagged-only filter in the trip gallery.** `GET /api/trips/{id}/evidence-flags` with a reason per item: stale, time from file date, copied from a pin, impossible hop, or clock suggests a shift, shown as small badges on gallery items. The thresholds need tuning on real data. It was first designed to read stored snapshots, so without them every flag needs a per-item recompute. Worthwhile once the chain is trusted. Size: M.
- **A Trips page badge counting high-severity findings.** Compute on request or cache per owner; do not load on every visit.
- **Further rules.** `impossible_hop` over 300 km/h and over 5 km (3 of 1,808 consecutive pairs; 7 of 6,816 in another population). `time_from_file_mtime` (54 by a fractional-second proxy; exact from `capture_evidence`, 42 of 48 AVI files). `video_clock_off_zone` by family and quarter, n at least 5 (4 groups, 152 items; these videos are correct but warn that the camera's photos are suspect). `photo_predicted_by_videos` (5 flagged, all 5 in the known set, recall only 5 of 42 until families are merged). `counter_inversion` (13 of 1,171 adjacent pairs, 1 near a whole hour). These last two were only ever named and counted: no condition was written down, so each must be defined before it can be built. `file_time_jump`, a large jump between consecutive points of one file, not measured; it is the only one of the two misdating signals that catches a misdating into the past, because `future_track_points` looks only ahead. `taken_far_from_trip`, a capture time far from its trip neighbours, not measured; it would catch a wrong time before the upload date, such as a camera's default date, which `taken_after_upload` cannot.
- **What rests on a flagged clock.** When a group verdict fires, list the estimates resting on those photos' points. In the replay, 4 estimates rested on the affected photos' points and would move a median 646 m. Exposure in general is wide: 893 estimates of the largest owner rest on a derived point. Today that needs one lookup per point, and the what-if preview is deferred to #26.
- **A `retime` dry run as a library-wide audit.** Port `server retime` with #24 and add one summary line per make to its existing consistent, whole-offset and unverifiable counts. It overlaps with the Step 4 backfill's dry run, so build only one. Size: S.
- **A "needs attention" list** for items with impossible times, which never appear on a map because they have no location. Step 2's `taken_after_upload` covers the main case; a list on the Trips and Map pages is worthwhile if those items keep getting missed.
- **A scatter of wall clock minus GPS clock per item**, with allowed offsets as bands. It would show stale fixes and slow clock drift. Needs `capture_evidence` on those rows, so it comes after the backfill, and is worthwhile if a drifting camera turns up.
- **A per-source speed sparkline** to spot corrupted sentences. Computable from stored points. Plausible, not seen, so worthwhile only once such a file is found.
- **Colour markers by source.** Extend the icon cache key in `MediaMap` with a `colorBy` prop; the cluster diff must rebuild icons. Spots patches of estimates but explains nothing. Any time. Size: XS.
- **A filter or list token for how a time was derived.** One short `taken_at_source` token on `/api/media`, or a column, once a list filter is actually wanted.
- **A "UTC day" note** on Trips (dates and auto-generated names), Albums, the date filters and the Game's date. No response carries the database session zone today, so it needs `SHOW timezone` in a session-only response, or pinning the server to UTC.

**Track data**

- **A matcher-run report.** `POST /api/gps-matcher/run` returns only `{"ok":true}`. Return moved count, median and maximum. Worthwhile before any track cleanup and before #22; one planned cleanup on the measured library touches 87 estimates.
- **A real way to exclude or delete a bad track file.** Today it is SQL only. "Hide this file" is view-only and the matcher still uses the file. Needs a delete or exclude route, a confirmation, a matcher run, and a decision on derived points. Worthwhile once the Step 2 inventory has named files to act on.
- **A whole-file "what did this file place" view.** The point-level version is in Step 3. This one is the same usage route with `recorded_at` omitted, returning `{points, first, last, total, placed}`, and a legend row reading "This file placed N items". With no snapshot it would be recomputed: candidates are the owner's estimates and unlocated items within W of the file's first-to-last span, each run through `bracketingPoints`, counted when its before or after point has that label. Step 2's `items_placed` is the same count per file; this view adds the list. It reports 0 for files that matter only in theory, so it sizes damage rather than finding it. Size: S.
- **Per-format layers in the Step 3 legend.** A checkbox per format row that hides that format client-side. A `formats=` query parameter was also proposed, filtered with `split_part` after the time scan; it is unnecessary if the toggle stays client-side, at the cost that hiding a format adds no detail to the others.
- **Drill-down by file in the Step 3 legend.** Formats can hold thousands of files (2,847 for one format and 1,301 for another on the measured library), so the legend layers by format. A per-format list of files, with a checkbox each, is worthwhile once "Hide this file" is used often.
- **Residuals split by track speed.** Step 2's per-format table has one median and one 90th percentile. Splitting each by the track's speed at that moment, derived from consecutive points of the same source, would separate a format's stationary and moving cases, which a single median mixes. Worthwhile if Step 2's table raises a question that the single figures cannot answer.
- **Server what-if preview with blast radius.** `POST /api/clocks/preview`, read-only and session-only; the first proposal's `RequireUser` would break invariant 2. Body `{media_ids, deltas_s, exclude_self_points}`, with 1 to 500 `media_ids`, 1 to 49 `deltas_s`, and items times deltas at most 6,000. A malformed UUID is 400. Foreign and shared IDs are dropped and counted, so nothing about them leaks. It returns summaries, per-item positions and dependents. 13,188 range lookups took 1.5 s on the measured library. A test pins that no row in `media` or `gps_tracks` changes.

  The UI is a curve of error against shift, ghost markers joined to the current positions by a thin line, and a "Would also move" list of the estimates resting on a shifted photo's point. Mouse dragging snaps to 15 minutes. A phone gets 44 px steppers instead, because dragging fights scrolling. Shifted items' own `exif` points are excluded and not re-added at the new time, so ghost positions for neighbours are an approximation until dependents are computed fully. Worthwhile only alongside #26, when an Apply button exists. Size: M.
- **Anchor sync.** "I was here when I took this": tap the track to infer a clock offset for an item no automated test can reach, or enter a known true time with an explicit zone. It reads out "this means the clock was N min behind" and offers the shift preview for the camera group, so it depends on the deferred server preview and on Step 3's points. Writes nothing. Needs a track near the moment, the owner recognising the place, and attention item by item. Anchors could later be stored as a further clock board mark. Size: S.
- **SQL recipes** for rare library-wide questions, as an appendix to this note: future-dated points by format, orphan points, estimates with no point left in the window, rows dated after upload, equal-time ties, per-source residuals, the per-camera shift test. Queries only. Size: S.
- **A flag when a dense log disagrees with a video's location tag.** The matcher leaves `exif` rows alone. 51 videos on the measured library use their own tag.

- **Choosing points by measured source reliability.** The matcher takes the nearest point on each side in time, whatever its format, so a coarse source can win over a precise one a few seconds further away. A leave-one-out run on one real library estimated the 1,606 photos that have their own position and a log point within the hour from the logs alone, and measured the distance from each photo's own position under several rules:

  | Rule | p50 | p75 | p90 | p95 | Better / worse than today by over 10 m |
  |---|---|---|---|---|---|
  | Today: the nearest point of any log format | 148 m | 483 m | 1.3 km | 2.5 km | |
  | The format that surrounds the photo most tightly | 146 m | 470 m | 1.2 km | 2.3 km | 34 / 59 |
  | Today, without sparse formats (one side only, or a side over 30 minutes) when a dense one exists | 147 m | 485 m | 1.3 km | 2.5 km | 4 / 16 |
  | #22: KML, KMZ and GeoJSON only when no other format is in the window | 137 m | 470 m | 1.2 km | 1.9 km | 83 / 18 |
  | Google Timeline only when no other log format is in the window | 200 m | 630 m | 1.5 km | 2.8 km | 156 / 340 |
  | The format with the lowest typical error for its coverage, learnt on the other half of the photos | 117 m | 349 m | 1.2 km | 2.1 km | 364 / 267 |

  Each format alone, with a point within a minute on each side: `gpx` and `samsung_health` a median of 10 m, `nmea` 31 m, `google_timeline` 130 m, `kml` 234 m, `geojson` 310 m, `esoko` 339 m. With a point on one side only, every format did several times worse, `google_timeline` worst: a median of 1.2 km and 37 km at the 90th percentile. A long gap on each side did not: `google_timeline` with a side over 30 minutes still had a median of 137 m, most likely because its long gaps are visits, during which nothing moved. A Timeline point snapped to a visited place can still be the nearest in time while the photo was taken elsewhere, and rows carry no kind that would tell a visit from a path point (see "A per-row time kind" below).

  So the signal is the format and whether it surrounds the item, not how sparse the format is overall. A design that follows: learn each format's typical error by coverage (both sides within 1, 5 or 30 minutes, longer, one side only) from the owner's own photos, as in the calibration entry above, and choose per item the format or pair with the lowest expected error, falling back to today's rule where too few photos back a cell. Show the expected error beside each row of the others table first, so that the owner can judge it before the matcher acts on it. Caveats: photos with their own position are mostly phone photos, taken where phone logs exist; a photo with a wrong clock counts against every format; and the learnt rule made 267 photos worse while improving 364. #22 is simpler and best at the 95th percentile, so it comes first, and this is measured again after it. Size: M.

**Richer input data**

- **Parser enrichment.** Timeline's per-point UTC offsets are direct ground truth for what the phone's clock showed; also `rawSignals` with accuracy, activity type, and the visit's real span, lost at parse. NMEA GGA HDOP and satellites, and RMC speed and heading. `esoko` speed and heading. The GPX parser reads only time and elevation, and general time parsing discards any offset, not just Timeline's. All of it needs a migration, because `gps_tracks` has no column for speed, heading, HDOP, satellites or a per-point UTC offset (only `altitude_m`, `accuracy_m` and `activity`), and a delete-and-re-import, because the unique key skips a plain re-upload.
- **A per-row time kind.** A parser marker for synthetic times and visit midpoints, so the wording can stop being format-level. Same re-import decision.
- **EXIF tags not read today.** `SubSecTimeOriginal`, `GPSDOP`, `GPSSpeed`, `GPSImgDirection` and `GPSProcessingMethod`, which say how far to trust the photo's own position. A position rejected by `IsValidGPS` is also computed and discarded. On the measured library, `exif` points have `accuracy_m` on 0 of 1,838 rows.
- **Container tags not read today.** The QuickTime `creationdate` tag, and a video's model tags.
- **A HEIC and HEIF parser.** Those files yield no EXIF, so they get a file time and no position of their own, and phone uploads are affected by default. The signal is the file extension against `capture_evidence.src`, which Step 1 starts recording. The recommendation from the code reading was to fix the parser ahead of the visual work. No issue or roadmap row tracks it yet, so file one; see #29 under "Dependencies and overlaps with issues".

### Ideas considered and rejected

| Idea | Why not | What would reopen it |
|---|---|---|
| A stored match snapshot (`media.gps_evidence`) | A recompute reproduced 4,962 of 4,962. Every production caller reprocesses, so a snapshot is overwritten. It would survive a cleanup only when no bracket remains, and it would need a retention sentence and a clear-on-delete path. | `no_support` rows appearing after real cleanups, with "what produced this" actually being asked. It would then need real history, by value, with `.UTC()` times and `timestamptz` comparison. |
| A separate audit table or per-run history | Far more machinery for a question asked rarely. | Repeated incidents that need history across matcher runs. |
| Storing `gps_tracks` ids or a foreign key | Re-imports replace ids, and a foreign key would wipe the explanation exactly when it matters. | Nothing foreseeable. |
| No migration at all | The rule used at upload and the file's modified time are lost for good on each new upload. | Nothing. |
| Separate columns (`taken_at_source`, `taken_at_zone`, `taken_at_offset_min`, `camera_make`) | One JSONB is enough while nothing filters on them. | A list filter on how a time was derived. |
| Interpolation in TypeScript | It would be a second matcher. | Nothing. |
| A narrow per-item conflict banner | It requires two formats each close on both sides, so it caught 1 of 47 pairs over 1 km and none of the 8 impossible ones. | Nothing. The bracket-pair warning replaced it. |
| Per-item clock verdicts | 8 of 15 groups had a non-zero best shift and 1 was real. | Nothing; readings only. |
| A filename-against-EXIF flag on photos | 3,669 of 3,670 agree by construction, including photos known to be wrong. | Nothing for photos. |
| A filename check in Step 1 | 94% of the videos it flagged as non-zero were correct. | Reworded and moved to Step 4. |
| A repeated daylight-saving hour warning | 0 items in that hour. | Items actually appearing in that hour. |
| Sun altitude checks | Catches only multi-hour errors, which other views show. | A real multi-hour error that nothing else catches. |
| Manual pins as clock evidence | Only 7 of 106 pins have an independent point within 3 h: pins are placed where logs are missing. | A library where that differs. |
| Hour-of-day histogram per camera | Of 26 class-year groups, only 7 peaked at zero shift and 5 peaked over 3 h off. Noise at this size. | A much larger library. |
| A per-file whole-hour lag rule | 0 of 111 sampled files with overlap showed one. | A file actually found with such a lag. |
| Time strip of sources against cameras | One hour is 0.6% of a long trip's width, about 7 px on a desktop and 2 px on a phone. None of the 11 replayed incidents depends on it. Video bars need `duration_sec`, null on all 8,619 rows, and need a direction, because some phones stamp the end of a clip and other cameras the start. The media list holds no `camera_model` or duration, and the Map loads only located items (1,666 of the largest owner's 8,559 are not), so it would need own-item capture facts added to `TrackResponse`. The replay's smaller alternative, a one-line per-camera offset summary, is deferred under "Library-wide views". | A real question the checks and the trail cannot answer. |
| A source disagreement lane in that strip | It lives on the strip. The Step 2 per-file distance answers the same question. Defined below. | The strip being built. |
| A full-screen timeline view with camera lanes | The largest piece, and it finds a long-running error less directly than a group verdict. Lanes for other members' items were also proposed. Defined below. | As above. |
| A per-item time ribbon, or a one-line bracket strip | It mostly repeats what the facts column says in words. | As above. |
| A trip-wide shift slider with whiskers | 0 of 36 relevant photos usable at the default budget, 9 of 36 at the maximum, 31 of 36 with all rows. Replaced by server-computed residuals on exact rows. | Real server-side residuals plus a demonstrated need for a visual check. |
| A draggable ghost marker on the clock chart | Interaction polish on a check whose wording had to be fixed first. | After Step 4 is in use. |
| Red hops as a clock signal | None of the 3 red hops on the two relevant trips touched the affected group. | Nothing; they are "estimates jumped". |
| A Dismiss button on findings (`PUT /api/checks/dismissed/{key}`, table `check_dismissals(owner_id, check_key, dismissed_at)`), or a camera alias table | Not needed in a first version. | Findings that recur after being checked, or camera families that prove wrong after the backfill. |
| A library shift grid as UI: a heatmap of camera and period against shift, with one bright non-zero column per bad clock, or a per-item shift histogram for one trip | An aggregate question asked rarely, and the group list answers it in words. The shift grid exists only as the SQL behind the group verdicts, the median residual for each camera-and-quarter at each whole-hour shift (105 cells, 3 s). | The group list proving too hard to read. |
| A coverage rollup table | 2.7 s on demand does not justify keeping one in sync. | Slow on the hosted server. |
| `ORDER BY recorded_at, id` as tie-break | Re-imports replace ids. | Nothing. |
| Reporting ties and picking the pair that reproduces the stored value | A deterministic order is simpler and makes staleness reliable. | Nothing. |
| A guarantee that the downsampler keeps bracket points | The evidence view draws them from its own response. | Tracks being used to judge a specific item's bracket. |
| An item-level track route | Replaced by `context` in the provenance response. | Nothing. |
| Storing alternatives, clock sweeps, neighbours or calibration | They are cheap to recompute and would go stale. The decision against a snapshot covers match explanations only, so this states the same for these. The one-hour private cache on the calibration route is an HTTP cache, not storage. | A measured cost that a recompute cannot meet. |
| Automatic re-query on pan and zoom, or a free `from`/`to` route | No arbitrary range keeps the history from being walked. | Nothing for free ranges. |
| Canvas rendering or `preferCanvas` | On a synthetic 10,000-point track in one headless run, one-path SVG dots redrew in a median 19 ms, against 56 ms for canvas circle markers and 73 ms for SVG circle markers. Canvas also needs a `getContext` stub in jsdom. | Real phones showing SVG as too slow. |
| RDP, a GeoJSON LineString, or a custom binary encoding | Time buckets of real rows give about 45 KB gzip for the largest trip. `ST_SimplifyPreserveTopology` would also lose the timestamp. | Payload becoming a real problem. |
| Chart, date or timezone libraries | Bundle sizes in the decisions table. | Pan and zoom on dense data, for which uPlot could be lazy-loaded. |
| A global debug mode, an eighth tab, a Settings entry or `localStorage` preferences | Owner-gated buttons are enough. Persisting would be the app's first `localStorage` use. | Real use showing the entry points are not enough. |
| A timezone-boundary dataset | Neighbours' measured offsets answer the same question for free. | A real need that those do not meet. |
| Replacing the confidence formula in this work | Its parts must be shown and measured first, and it changes marker colours and Game tolerance. | The calibration data in hand. |
| Any repair action in these views | Shifting times belongs to #24 and #26. Applying from a dip would be reckless. | #26 shipping an Apply. |
| Fixing the day boundaries | It needs a per-photo zone that does not exist. | A per-photo zone being stored. |
| Evidence or tracks for shared items | Tracks stay personal. | See open questions. |
| API token access to track data | No token can read location history today. | Nothing foreseeable. |

Some of the rejected views need defining, so that the reopen triggers mean something.

- **The full-screen timeline view.** Routes `GET /api/trips/{id}/timeline` and `GET /api/albums/{id}/timeline`. `source_lanes` hold one lane per format (`format`, `kind`, `points`, `files`, accuracy median, `spans`), expandable to files. `camera_lanes` hold item ticks, hollow when interpolated, and a bar per video anchored per make, dashed when the anchor is unknown. A playhead shows one dot per source on the map at that instant. The axis has a zone switch (home, UTC, browser, place). On a phone, dragging pans, a pinch zooms and a tap picks the nearest tick. Size: L.
- **The source disagreement lane.** One lane under the sources. Its height is the largest distance between formats at each moment on a log scale, and it is empty where only one source exists. A pure function computes it client-side from loaded points, so downsampled points can miss a brief spike. Clicking a spike highlights the file responsible. Size: S.
- **The time strip.** A strip under the map while Track is on, built from the track response with no new route. One lane per format with covered spans and gaps, one lane per camera with ticks hollow when interpolated, a playhead with one dot per format, and an axis switching between UTC and the browser zone. Hover and wheel on a desktop, drag and a 22 px tap on a phone. Hidden while the item sheet is open. The full-screen timeline above is its larger variant.
- **The per-item time ribbon.** A strip under the evidence map with one row of ticks per format, a row of other items, a cursor at the claimed time and a bar for a video. A tap on a tick opens the point card. Its purpose is showing, for example, a log that stops minutes before the item.
- **The one-line bracket strip.** One axis with the before point, the item and the after point, the gaps labelled in seconds.
- **The trip clock check with whiskers.** A thin whisker from each own-position photo to where the track says it was at `taken_at` plus the slider. Long parallel whiskers read as a wrong clock, and whiskers fanning from one parked point as a logger left behind. A slider from −3 h to +3 h in 15-minute steps, with the median distance per whole-hour shift and an "N of M usable" readout, and a per-camera selector. Shown only with at least 3 own-position items. A point counts only within 2 minutes, with a note when `bucket_s` exceeds 60.

### Problems found in the existing code

**Hostile or corrupt input**

- `registerMedia` no longer stores a `taken_at` outside the years 1 to 9998, but a row written earlier can still hold one outside 0 to 9999. `GET /api/media` then answers 200 with an empty body, because `time.Time` refuses to marshal it, and only logs the error. The provenance routes read such a time as none.
- A video in a container `ffprobe` refuses is stored unread, but its file re-read answers 502 every time, which the view words as "try again". `ProbeURL` runs with `-v quiet`, so a refusal looks the same as a failure to fetch the object.
- `POST /api/gps-tracks/import` accepts any source label, including the `exif:` and `manual:` prefixes that only the server should write. Such rows count as derived points, which are never thinned, so the context budget does not bound them. Only the caller's own data is affected.
- `ffprobe`'s output is read whole, so a very large metadata value costs that much memory at upload and on every re-read.

**Stale or leftover data**

- **Deleted items leave their track points behind, and the matcher still uses them.** `gps_tracks` has no reference to `media`, and nothing deletes a point when an item goes. On the measured library, 32 of 1,838 `exif` points and 2 of 110 `manual` points have no item at that instant, and 6 estimates rest on such a point. Re-uploading a deleted item without a position copies its own leftover point back at confidence 1.0, the one way an item can rest on itself. A cleanup needs care, because several items can share one point (41 instants have more than one item), and a re-import of track files must never delete the `exif` and `manual` labels, which are GeoGallery's own points.
- **The matcher never clears a location.** An estimate with no bracket left keeps its position forever. Hence `no_support`.
- **Same-second collisions.** `ON CONFLICT DO NOTHING` keeps the first photo's position (29 of 1,833 own-position photos share an owner and second with another), and a manual save overwrites a point at that second.
- **Off-main writers leave no trace.** `retime`, `video-locations` and hand SQL change rows, and there is no `updated_at` or audit column.

**Matcher**

- The matcher has no transaction, returns at the first error, stops when a request context is cancelled, has no `ORDER BY`, and has no per-owner lock against overlapping runs. `UpdateLocation` only logs a matcher failure.
- `GPS_MATCH_WINDOW_HOURS` is not passed by compose, so a native process and a container can differ on one database. Shrinking the window leaves old values in place.
- It treats derived points as ordinary points. A photo with a wrong clock therefore teaches the matcher a wrong fact: 899 of 4,962 estimates (18%) rest on at least one derived point, 584 (12%) only on those, and 411 of those are plain copies of another item's position.
- It treats a logger that stands still as the photographer's position, because it has no notion of whether the logger moved with the camera.
- It uses a track file from a device that was somewhere else like any other. None of the 64 such files on the measured library has an item within an hour today, and only one falls inside a trip's window, but a future photo with no other point nearby would be placed from it. Nothing but SQL removes such a file.
- A two-sided estimate is a straight line in degrees, so it ignores roads and stops. Pair spans: median 550 s, 90th percentile 2,438 s, 142 over an hour. Pair distances: median 178 m, 90th percentile 1,163 m, 504 over 1 km, 53 over 10 km.
- A hand-placed point feeds neighbours like a log point, so a pin dropped at a wrong moment teaches the matcher a wrong fact. 398 of 4,962 estimates use a `manual` point, and 362 of the 535 one-sided estimates copy one.
- There is no antimeridian handling. Interpolation is linear in degrees, so a pair across ±180° lands on the far side of the globe. Plausible, not seen. The signal would be a longitude difference over 180° between the two bracket points, shown as a low-priority one-line warning in the evidence view. The warning table has no such code today.

**Confidence**

- It is not one scale. Two-sided and one-sided use different formulas, one-sided has no distance term, and a same-instant hit is hard-coded to 1.0 even if the clock is wrong. 1,126 of the 1,137 rows at 1.0 come from logs, where a 1 Hz log hits whole-second EXIF times.
- 142 rows have confidence 0 and are placed anyway.
- 60 estimates have confidence at or above 0.9 with brackets over 1 km apart.
- The Map card rounds, so 430 rows that are not same-instant matches display as 100%. Step 1 fixes this in the evidence view only.

**Capture time**

- A stale GPS fix keeps its position even when its timestamp is rejected, and is written as a track point at `taken_at`.
- A stale GPS timestamp can be accepted when its age is near a whole hour. About 7% of random ages pass, so it should be rare. A camera clock that drifts over 2 minutes loses its fresh GPS time for the same reason.
- A photo with an EXIF position but no EXIF time puts its `exif` point at the fallback time.
- IFD0 `DateTime` is used when `DateTimeOriginal` is missing, and editors rewrite it to the edit time.
- HEIC, HEIF, PNG and WebP are read through imagemeta, but only from the first 1 MiB, and no real phone file has been tried (#29).
- `TranscodeH264` drops `creation_time`. Step 1 fixes new files. How many stored videos were transcoded is unknown.
- One `CAMERA_TIMEZONE` applies to every user (#25). A camera whose clock was changed by hand is misread, with no GPS tag or offset to catch it. A wall clock in the repeated daylight-saving hour is ambiguous, since Go does not specify which occurrence `time.Date` picks.
- A make with a wrong offset tag goes unnoticed until someone adds it to `offsetIgnoredMakes`.
- A video's `creation_time` is parsed as RFC 3339 even when the file holds a wall clock labelled as UTC, and such a video is off by the camera's offset.
- A video's `taken_at` is sometimes the end of the recording. No correction exists.
- Items can be dated in the future; there is no guard at ingest. 6 such items exist, all without a location.

**Track parsers**

- `parseISO8601` reads a time with no offset as UTC (#23).
- NMEA checksums are not verified and only RMC is read, so a corrupted sentence with a plausible value is stored.
- Far-future points are stored, and the duplicate check then skips a corrected re-upload (#23). An item near such a log shows only that the nearest point is hours away, with no hint that a log is misdated.
- `Activity` is set only by the GeoGallery format, so `activity` is empty on every row.
- Timeline's points and KML's are not all measurements: a visit is one place at the stay's midpoint, at a position snapped to a venue that can sit far from the device, and a LineString's times are spread evenly.

**Trips and time display**

- `PATCH /api/trips/{id}` accepts any date range and pulls unassigned and auto-trip media in that range into the trip.
- Trip dates and auto-generated trip names, album ranges, date filters, and the Game's actual date and date score use the database session's day, which no code sets. The date score is the one place where the day boundary changes a computed result rather than a label, which bears on open question 4. A photo near midnight can show one date on the Map card and another on its trip. Read from code and not reproduced on data. On the measured library, 9 of 142 trips have a start or end whose UTC date differs from the date in a stand-in non-UTC zone, and 3 of 46 user-edited trips differ from their media.
- Item times are formatted in the browser's zone with no label. Step 1 fixes the Map card and the gallery; the popup still shows a bare date.
- The gallery sorts by comparing raw strings, which can misorder across daylight-saving changes on a non-UTC server, because `/api/media` sends the server's local offset.
- Manual saves on the Map tab do not refresh re-matched neighbours until the next reload. Step 1 fixes this.

**Logging**

- The hosted nginx access log records the full request line for `/api`, and only `/api/auth/` has `access_log off`. Existing filter parameters land there too, and the "query strings stay out of logs" rule is only met by the backend's own logger.

### Dependencies and overlaps with issues

- **#22, matcher source preference.** Step 1's options struct and returned source are what #22 builds on. The `others` table and the `independent` line show on real items what it would change. The tie-break ships here rather than in #22. When #22 lands, estimates change and read as `stale` until the next run. Step 2's `time_may_be_estimated_used` matches #22's two formats. #22 keeps KMZ among the preferred sources, because its time spacing was never checked; Step 2's `modal_gap_share` is what can settle that.
- **#23, parser guards.** Independent. Until it lands, Step 2's `future_track_points` reports what it would have refused, and #23's one-day limit is the threshold used. It stops new rows but does not surface existing ones. The track upload receipt shows a bad span on the day of import.
- **#24, per-camera clock rule and retime port.** Step 1 ports `storage.ReadHead` and `ingest.ProbeURL`, which #24 also needs. #24's clock rule becomes a new `capture_evidence.zone` value. The ported `retime` must update `capture_evidence`, so that a repaired row is not identical to an untouched one. The backfill follows `RunRetime`'s pattern. The Step 4 group verdict finds what #24's rule would fix.
- **#25, per-user camera zone.** `capture_evidence.camera_zone` records the zone actually applied to each file, so per-user and per-batch zones stay explainable. `server_camera_zone` must then become the effective zone for that user.
- **#26, edit and shift times.** Depends on #24. Step 4's Copy shift produces the `{media_ids, delta_seconds}` body it specifies. Apply buttons belong to #26. An edit must record itself in `capture_evidence`.
- **#29, phone upload spike.** The spike decides what a phone upload actually delivers, HEIC or JPEG, and whether position survives. It sets the priority of the HEIC parser entry above, and Step 1's `capture_evidence.src` shows the symptom on real uploads.
- **#30, capture time from the filename.** Step 4 adds `ingest.FilenameClock`, which #30 can reuse. A filename can fill in a missing time but cannot validate a photo's existing EXIF time. `src` would gain a `filename` value.
- **#32, trip GPS track overlay.** Implemented by Step 3, after the issue is edited as described there.
- **Roadmap upkeep.** One GitHub issue per step, the `docs/roadmap.md` table kept in step, and `CLAUDE.md` updated for new routes, z-layers, compressed routes and test lists, as each step ships.

### What was not verified

- **The hosted instance was not checked at all:** whether a recompute reproduces its rows, its tie counts, its future-dated or orphan points, or any time-related count.
- **All numbers come from one library**, with a one-hour window that was inferred from unset environment files. The 4,962-of-4,962 match supports that inference.
- **Real phones and other browsers.** The evidence view was driven in headless Chromium against the real backend, at desktop size and with touch emulated at 360×740 and 740×360: every placement state, the re-read of photos and videos, keys and the back gesture, and 44 px controls. Nothing was run on a phone, or in Firefox or Safari. Tap accuracy on a real screen is unmeasured, and so is the track layer beyond 5,000 points in that view.
- **The hosted object store and https.** Ranged GET and `ffprobe` by presigned URL passed on MinIO and on SeaweedFS as CI runs it, in every accepted container and with `moov` at either end of the file, on synthetic files. Real `moov` sizes and wall time on a real bucket were not measured.
- **imagemeta on a real HEIC file.** The library has none.
- **How many stored videos were transcoded.** 54 AVI and 3GP files are candidates; `mp4v` inside `.mp4` is not counted.
- **The re-encode heuristic on real files.** How often an H.264 file with no container time and a `Lavf` encoder tag was not in fact re-encoded at upload is unknown.
- **Go-loop costs:** the clock sweep's roughly 50 lookups per request, the calibration loop, and the matcher's duration through Re-match.
- **The agreement query's cost** at library scale.
- **Library-wide queries on a small server.** The shift grid's 3 s and the coverage query's 2.7 s are from the measured library only.
- **The size of `capture_evidence`.**
- **The 0.1 km floor on `pair_impossible`, the 120 s floor on `logger_parked`, the 120 s neighbour limit for a stationary logger, and the semaphore of 4 on the file re-read.** All are judgements.
- **The stationary rule was never replayed.** The clock thresholds were replayed with every recorded point (29 of 29 known photos and 5 of 836 others) and with one log format removed entirely (18 of 18 and 1 of 230). The rule adopted here ignores a listed format only while it is stationary, and sits between those. The group verdict's 1 of 15 and its 3 s also predate it.
- **Group thresholds generalising.** They were tuned on one library with one known incident and 15 testable groups.
- **The replay's limits.** One incident could not be replayed, because the rows had already been repaired. `video_outlier` had no rule when it was replayed, so 12 is the design's own count, and its seasonal form was not replayed at all. The ignored-offset camera group is a filename-pattern proxy, because make is not stored. Filename families can merge two cameras that share one naming pattern.
- **The gallery toolbar overflow** is arithmetic and was not tested on a device.
- **Bundle sizes** are from esbuild, not the project's Vite build.
- **KMZ time spacing.**
- **Google Timeline field details** beyond those parsed were not checked against files in this work.
- **A match-window mismatch between two processes** has not been reproduced. Reading it as `stale` is inferred from the code.

### Open questions

1. Should album members see how a shared item's time was derived? The served file already exposes its EXIF, and no track data would be included. The current answer is owner only.
2. After Step 3, is a library-wide clock board worth building, or is the group list enough? Decide after using Steps 1 to 4.
3. Is richer track data worth a delete-and-re-import? Timeline's per-point offsets would be direct ground truth. Decide once the clock checks show whether they would settle anything.
4. Should the inconsistent day boundaries be fixed or only labelled? Fixing them needs each photo's zone.
5. Should the track toggle, and per-format layer choices if those are added, persist across reloads? That would be the app's first `localStorage` use.
6. Should the confidence score be replaced by an estimated error in metres, or only explained better?
7. Should `retime` and `video-locations` be merged to `main`, and record that they ran? Today a repaired row looks identical to an untouched one.
8. Should ingest also read the zero-cost extras: the GPS error tag and sub-second times?
9. Should `Re-match` be narrowed to one item? Today it can only run over the whole library.
10. Is the tie-break's effect on the hosted instance as small as it was on the measured library? Measure before deploying it.
11. Which offset should a future zone-at-location chip (see "Explaining one item further") use: a new dependency, or Timeline's offsets where they exist?
12. If Step 1 has to shrink, should the first version be desktop-first? The spec uses one code path for desktop and phone, and a full phone layout is the larger part of the gallery work.
13. Should the Map's `fromDate` and `toDate` filters clip the Step 3 track through `from` and `to`? Step 3 leaves the track unclipped until this is decided.
