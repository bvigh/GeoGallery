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

// POST /api/gps-tracks/upload. skipped: points imported before; empty: files without a
// timestamped point; failed: files that could not be read.
export interface GpsUploadResult {
  inserted: number;
  skipped: number;
  empty: string[];
  failed: { file: string; reason: string }[];
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
export type ZoneRule = "gps" | "offset" | "camera";
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

// media.capture_evidence: how taken_at was derived, recorded at ingest. Every key is always present.
export interface CaptureEvidence {
  v: number;
  by: "ingest" | "backfill";
  src: CaptureSource;
  zone: ZoneRule | null;
  wall: string | null;
  gps_utc: string | null;
  wall_minus_gps_s: number | null;
  offset_tag: string | null;
  offset_ignored: boolean;
  offset_used_min: number | null;
  camera_zone: string; // the IANA name, or the literal "server zone (unset)"
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

// The zone this server applies today.
export interface ServerCameraZone {
  name: string; // "server zone (unset)" when CAMERA_TIMEZONE is unset
  set: boolean;
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

export interface OtherFormat {
  format: string; // the raw prefix, so an unknown one is shown as written
  source: string;
  t: string;
  lat: number;
  lng: number;
  gap_s: number;
  accuracy_m: number | null;
  distance_m: number | null; // from the position on the map
  used: boolean; // the row is this estimate's before or after point
}

export type WarningCode = "pair_impossible" | "pair_far" | "logger_parked" | "confidence_zero" | "point_orphan";

export interface Warning {
  code: WarningCode;
  level: "warn" | "info";
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
  };
  time: {
    recorded: "ingest" | "backfill" | "not_recorded";
    evidence: CaptureEvidence | null; // null when not_recorded
    server_camera_zone: ServerCameraZone;
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
    taken_at: string;
  } | null; // null when no capture time could be read
  delta_s: number | null; // rule_today.taken_at minus stored, in seconds
  class: "same" | "whole_offset" | "other" | "no_file_time" | "stored_has_no_time" | "unverifiable_transcoded";
  checks: { id: string; level: "ok" | "warn" | "info"; delta_s: number | null }[];
  tags: { name: string; value: string }[];
}
