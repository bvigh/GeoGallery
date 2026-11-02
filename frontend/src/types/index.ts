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

export interface GpsImportResult {
  inserted: number;
  skipped: number;
  errors: { row: number; reason: string }[];
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
