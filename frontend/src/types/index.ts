export interface MediaItem {
  id: string;
  filename: string;
  media_type: "photo" | "video" | "photo_360" | "video_360";
  lat: number | null;
  lng: number | null;
  gps_confidence: number | null;
  gps_source: "exif" | "interpolated" | "manual" | "none" | null;
  taken_at: string | null;
  storage_key: string;
  trip_id: string | null; // always null for media the caller does not own
  owner_id: string;
  owner_name: string | null;
  url?: string;
  initial_yaw?: number;
  initial_pitch?: number;
  initial_fov?: number;
  keyframes?: OrientationKeyframe[];
  camera_model?: string | null; // the EXIF model; older backends omit it
}

export interface OrientationKeyframe {
  time: number;   // seconds into video
  yaw: number;    // radians
  pitch: number;  // radians
  fov: number;    // degrees (30–100)
}

export interface Trip {
  id: string;
  name: string;
  start_date: string | null;
  end_date: string | null;
  media_count: number;
  cover_key: string | null;
  user_edited: boolean;
}

export interface Album {
  id: string;
  name: string;
  owner_id: string;
  owner_name: string | null;
  is_owner: boolean;
  member_count: number;
  media_count: number;
  start_date: string | null;
  end_date: string | null;
  created_at: string;
  // When the caller last added one of their items; null when none of them is in the album.
  last_added_at: string | null;
}

export interface AlbumMember {
  user_id: string;
  display_name: string | null;
  email: string | null;
  is_owner: boolean;
  added_at: string;
}

// An entry of the user directory (GET /api/users): active, claimed users.
export interface DirectoryUser {
  id: string;
  display_name: string | null;
  email: string | null;
}

export interface FilterState {
  fromDate: string;
  toDate: string;
  // Trips and albums together are one "Collections" filter, combined with OR.
  tripIds: string[];
  albumIds: string[];
  mediaTypes: MediaItem["media_type"][];
  owner: "all" | "mine" | "shared";
}

export interface GameRound {
  id: string;
  media_type: string;
  url: string;
  initial_yaw?: number;
  initial_pitch?: number;
  initial_fov?: number;
  max_dist_km: number;
  // [south, west, north, east] of the filtered media the round was drawn from.
  bounds: [number, number, number, number] | null;
}

export interface CurrentUser {
  id: string;
  email: string | null;
  display_name: string | null;
  role: "admin" | "user";
}

export interface AdminUser extends CurrentUser {
  disabled: boolean;
  last_login_at: string | null;
  created_at: string;
}

export interface ApiToken {
  id: string;
  name: string;
  display_hint: string;
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
}

export interface CreatedApiToken extends ApiToken {
  token: string;
}

// One of the caller's cameras whose clock showed a fixed UTC offset during a period
// (/api/camera-clock-rules). Ingest matches it on the file's EXIF Make and Model, without case,
// and on the wall clock's calendar day. Days are "YYYY-MM-DD" on the camera's own clock, inclusive.
export interface CameraClockRuleInput {
  make: string;
  model: string;
  utc_offset_min: number; // what the clock showed: -720..840, a multiple of 15
  starts_on: string | null; // null: no first day
  ends_on: string | null; // null: no last day
  note: string;
}

export interface CameraClockRule extends CameraClockRuleInput {
  id: string;
  created_at: string;
  updated_at: string;
}

// GET /api/cameras: a camera in the caller's own library. Only items whose capture evidence
// recorded a make are counted.
export interface LibraryCamera {
  make: string; // the most frequent spelling
  model: string;
  items: number;
  first_taken_at: string | null;
  last_taken_at: string | null;
}

// POST /api/gps-tracks/upload. skipped: points imported before; empty: files without a
// timestamped point; failed: files that could not be read; files: each file that held points.
export interface GpsUploadResult {
  inserted: number;
  skipped: number;
  empty: string[];
  failed: { file: string; reason: string }[];
  files?: TrackFileReport[]; // older backends omit it
}

// One file of a track upload that held at least one point.
export interface TrackFileReport {
  file: string; // the uploaded file name
  format: string; // what the backend read it as: gpx, nmea, ...
  points: number; // read from the file, whether imported before or not
  first: string | null; // RFC 3339 UTC; null when JSON cannot carry the year
  last: string | null;
}

// POST /api/media/upload. Go sends an empty list as null.
export interface UploadResponse {
  uploaded: string[] | null;
  skipped?: string[] | null; // duplicates; older backends omit it
  failed?: string[] | null;
  album_added?: number; // with ?album_id only: new uploads and duplicates the album gained
  time_report?: TimeReport; // older backends omit it
}

// Who named the camera zone a wall clock was read in: the upload or scan itself ("batch"), the
// owner's setting ("user"), or CAMERA_TIMEZONE or the server's own zone ("server").
export type CameraZoneFrom = "batch" | "user" | "server";

// How the capture times of one upload request's new files were read, counted by the values their
// capture_evidence records. Duplicates and failed files are not counted.
export interface TimeReport {
  by_source: Record<CaptureSource, number>;
  by_zone: Record<ZoneRule, number>;
  camera_zone: string; // the IANA name, or the literal "server zone (unset)"
  camera_zone_from?: CameraZoneFrom; // older backends omit it: the server's
  // A file was read in the camera zone, that zone is UTC all year, and the upload did not name it.
  assumed_zone_is_utc: boolean;
  file_date_names: string[]; // the files dated by their modified date (src "mtime")
}

// GET and PATCH /api/me/settings: the caller's own settings, null where the server's default
// applies, and those defaults.
export interface UserSettings {
  camera_timezone: string | null; // the IANA zone the caller's camera clocks show
  defaults: {
    camera_timezone: string; // the server's: the IANA name, or the literal "server zone (unset)"
  };
}

// A PATCH body: the settings to change, null returning one to its default.
export type UserSettingsPatch = Partial<Pick<UserSettings, "camera_timezone">>;

// media.time_edit: the owner's last change of taken_at through PATCH /api/media/{id}/taken-at
// ("set") or POST /api/media/shift-time ("shift"). capture_evidence still says how the file's own
// time was read. Every key is always present; instants are RFC 3339 in UTC.
export interface TimeEdit {
  v: number;
  kind: "set" | "shift";
  at: string; // when the change was made
  from: string | null; // taken_at before this change; null when the item had none
  original: string | null; // taken_at before the first change
  delta_s: number | null; // the new time minus from, in seconds; null when from is null
  wall: string | null; // set only: the wall clock as entered, "YYYY-MM-DDTHH:MM:SS"
  zone: string | null; // set only: the IANA zone named
  offset_min: number | null; // set only: the UTC offset the wall clock was read at
}

// The zone a wall clock entered by hand is read in: an IANA zone, whose offset on that date
// applies, or a fixed UTC offset.
export type TakenAtZone = { zone: string } | { utcOffsetMin: number };

// PATCH /api/media/{id}/taken-at. trip_id is the item's trip after trip detection ran again;
// followup_failed says the matcher or trip detection failed after the time was saved.
export interface SetTakenAtResult {
  taken_at: string;
  trip_id: string | null;
  followup_failed: boolean;
}

// POST /api/media/shift-time. undated: items without a time, left as they were; trips: the trips
// the shifted items are in after trip detection, most items first.
export interface ShiftTimesResult {
  shifted: number;
  undated: number;
  trips: { id: string; items: number }[];
  followup_failed: boolean;
}

export interface GuessResult {
  actual_lat: number;
  actual_lng: number;
  actual_date: string | null;
  distance_km: number;
  date_diff_days: number | null;
  location_score: number;
  time_score: number | null;
  total_score: number;
}

// "Why here?": GET /api/media/{id}/provenance and /provenance/file (docs/gps-debugging.md, Step 1).
// Every instant is RFC 3339 in UTC.

export type CaptureSource = "exif_original" | "exif_ifd0" | "container" | "mtime" | "none";
export type ZoneRule = "gps" | "offset" | "clock_rule" | "camera";
export type PlacementState =
  | "current"
  | "stale"
  | "no_support"
  | "pending"
  | "no_bracket"
  | "no_time"
  | "own_position"
  | "manual";
export type BracketShape = "two_sided" | "one_sided" | "exact";

// The owner's clock rule a capture time was read with, as the rule was then: it may have been
// changed or deleted since.
export interface EvidenceClockRule {
  id: string;
  make: string;
  model: string;
  offset_min: number;
  starts_on: string | null; // "YYYY-MM-DD" on the camera's own clock
  ends_on: string | null;
}

// media.capture_evidence: how taken_at was derived, recorded at ingest, or by server retime when
// that moved taken_at. Every key is always present, except the two added with clock rules, which
// older rows lack.
export interface CaptureEvidence {
  v: number;
  by: "ingest" | "backfill" | "retime";
  retimed_from?: string | null; // taken_at before server retime moved it
  src: CaptureSource;
  zone: ZoneRule | null;
  wall: string | null;
  gps_utc: string | null;
  wall_minus_gps_s: number | null;
  offset_tag: string | null;
  offset_ignored: boolean;
  offset_used_min: number | null;
  clock_rule?: EvidenceClockRule | null; // set when zone is "clock_rule"
  camera_zone: string; // the IANA name, or the literal "server zone (unset)"
  camera_zone_from?: CameraZoneFrom; // rows recorded before per-user zones lack it: the server's
  camera_zone_utc_fallback: boolean;
  make: string | null;
  model: string | null;
  exif_partial: boolean;
  creation_time: string | null;
  file_mtime: string | null;
  duration_s: number | null;
  encoder: string | null;
  codec: string | null;
  transcoded: boolean;
}

// The camera zone the owner's uploads get today: their setting, else the server's. (The name
// predates per-user zones.)
export interface ServerCameraZone {
  name: string; // "server zone (unset)" when CAMERA_TIMEZONE is unset and the owner has none
  set: boolean;
  from?: "user" | "server"; // older backends omit it: the server's
  utc_fallback: boolean;
  offset_now_min: number;
}

export interface TrackSource {
  label: string;
  format: string; // "other" when the prefix matches no known format
  kind: "recorded" | "semantic" | "time_may_be_estimated" | "derived" | "other";
}

export interface TrackPoint {
  t: string;
  lat: number;
  lng: number;
  s: number; // index into sources
  acc: number | null;
  alt: number | null;
}

export interface TrackResponse {
  windows: { from: string; to: string }[];
  match_window_s: number;
  total_points: number;
  returned_points: number;
  bucket_s: number; // 0 when nothing was downsampled
  sources: TrackSource[];
  points: TrackPoint[];
}

export interface Bracket {
  source: string;
  format: string;
  kind: "log" | "exif" | "manual";
  t: string;
  lat: number;
  lng: number;
  accuracy_m: number | null;
  altitude_m: number | null;
  gap_s: number; // absolute distance in time from the item
  origin: {
    state: "linked" | "orphan" | "burst";
    media: { id: string; filename: string }[];
  } | null; // null for kind "log"
}

export interface Estimate {
  lat: number;
  lng: number;
  confidence: number;
  delta_m: number | null; // distance from the stored position; null when there is none
  before: Bracket | null;
  after: Bracket | null;
  arithmetic: {
    shape: BracketShape;
    pair_gap_s: number | null; // null for one_sided
    pair_km: number | null; // null for one_sided
    t: number | null; // two_sided only: 0 at before, 1 at after
    time_factor: number;
    distance_factor: number | null; // null for one_sided
    confidence: number;
    implied_kmh: number | null; // two_sided only
  };
}

export interface OutsidePoint {
  source: string;
  format: string;
  t: string;
  lat: number;
  lng: number;
  gap_s: number;
}

// One format's nearest point on one side of the item's time, within the match window.
export interface OtherPoint {
  source: string;
  t: string;
  lat: number;
  lng: number;
  gap_s: number;
  accuracy_m: number | null;
  distance_m: number | null; // from the position on the map
  used: boolean; // the estimate's before or after point
  self: boolean; // the item's own exif or manual point
}

// One format on its own: its nearest point on each side, and where those alone place the item.
export interface OtherFormat {
  format: string; // the raw prefix, so an unknown one is shown as written
  before: OtherPoint | null; // null when the format has no point on that side within the window
  after: OtherPoint | null; // the same row as before when it is at the item's exact time
  alone: {
    lat: number;
    lng: number;
    shape: BracketShape;
    confidence: number;
    distance_m: number | null; // from the position on the map
  };
  used: boolean; // one of its points is the estimate's before or after point
}

export type WarningCode = "pair_impossible" | "pair_far" | "logger_parked" | "confidence_zero" | "point_orphan";

export interface Warning {
  code: WarningCode;
  level: "warn" | "info";
}

// The trip an own item is in, so the Checks view can open the item among its neighbours.
export interface ProvenanceTrip {
  id: string;
  name: string;
}

export interface ProvenanceResponse {
  item: {
    id: string;
    filename: string;
    media_type: MediaItem["media_type"];
    taken_at: string | null;
    created_at: string; // upload time
    lat: number | null;
    lng: number | null;
    gps_source: MediaItem["gps_source"];
    gps_confidence: number | null;
    camera_model: string | null;
    width_px: number | null;
    height_px: number | null;
    duration_sec: number | null;
    trip: ProvenanceTrip | null; // the owner's trip the item is in
  };
  time: {
    recorded: "ingest" | "backfill" | "retime" | "not_recorded";
    evidence: CaptureEvidence | null; // null when not_recorded
    server_camera_zone: ServerCameraZone;
    edit?: TimeEdit | null; // null when the owner never changed the time; older backends omit it
  };
  placement: {
    state: PlacementState;
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
    others: OtherFormat[]; // never null
    warnings: Warning[]; // never null
  };
  context: TrackResponse; // empty arrays when taken_at is null
}

// GET /api/media/{id}/provenance/file: the stored file read again with today's rules.
export interface FileReread {
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
    zone_rule: ZoneRule | null;
    offset_min: number | null; // null for a container time
    zone_name: string | null; // set only when zone_rule is "camera"
    zone_from?: CameraZoneFrom | null; // who named zone_name; set only when zone_rule is "camera"
    clock_rule: EvidenceClockRule | null; // set only when zone_rule is "clock_rule"
    taken_at: string;
  } | null; // null when no capture time could be read
  delta_s: number | null; // rule_today.taken_at minus stored, in seconds
  class: "same" | "whole_offset" | "other" | "no_file_time" | "stored_has_no_time" | "unverifiable_transcoded";
  checks: { id: string; level: "ok" | "warn" | "info"; delta_s: number | null }[];
  tags: { name: string; value: string }[];
}

// Checks: GET /api/checks and /api/checks/agreement (docs/gps-debugging.md, Step 2). The caller's own
// tracks and items only. Every instant is RFC 3339 in UTC, or null when JSON cannot carry its year.

export type CheckCode =
  | "pair_impossible"
  | "future_track_points"
  | "taken_after_upload"
  | "stale_estimates"
  | "pair_far"
  | "orphan_points"
  | "time_may_be_estimated_used";

// One track file: every point with one source label, derived labels (exif, manual) included.
export interface ChecksFile {
  source: string; // the full label, e.g. "gpx:ride.gpx", "exif", "manual"
  format: string; // the raw prefix, the part before the first colon
  kind: TrackSource["kind"];
  points: number;
  first: string | null;
  last: string | null;
  items_placed: number; // own estimates whose recomputed before or after point has this label
}

// How far one log format's points sit from photos with their own position, within 5 s.
export interface ChecksResidual {
  format: string; // a log format's raw prefix
  n: number; // own-position photos with a point of this format within 5 s
  p50_m: number;
  p90_m: number;
}

export interface ChecksGroup {
  key: string;
  label: string;
  count: number;
  evidence: Record<string, string | number | null>; // keys per code (lib/checks.ts)
  media_ids: string[]; // at most 200, by taken_at then id; [] for findings about points only
}

export interface ChecksCheck {
  code: CheckCode;
  severity: "high" | "low";
  total: number; // distinct items, or points for the two point checks, across all groups
  groups: ChecksGroup[]; // count desc, then key; [] when nothing was found
}

export interface ChecksResponse {
  generated_at: string;
  window_s: number; // GPS_MATCH_WINDOW_HOURS in seconds
  files: ChecksFile[]; // by source
  residuals: ChecksResidual[];
  checks: ChecksCheck[]; // all seven, high severity first
}

// One log file compared with the other formats at up to 20 moments, and its time spacing.
export interface AgreementFile {
  source: string;
  samples: number;
  matched: number;
  median_m: number | null; // null with fewer than 5 matches
  modal_gap_share: number | null; // share of gaps equal to the file's most common one; null with fewer than 10 gaps
}

// One request of a comparison: the log files from index `from` on, in label order, as far as the
// server's time budget went. The view asks again from next_from until it is null.
export interface AgreementResponse {
  generated_at: string;
  files: AgreementFile[]; // the log files this request compared, in label order
  truncated: boolean; // true exactly when next_from is not null
  total_files: number; // log files in this request's inventory
  next_from: number | null; // the index of the first log file not compared; null once the last was
}
