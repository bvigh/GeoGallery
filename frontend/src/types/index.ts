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
  trip_id: string | null;
  url?: string;
  initial_yaw?: number;   // radians — from DB, used by 360° viewer
  initial_pitch?: number; // radians — from DB
  initial_fov?: number;   // degrees — from DB, default 75
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
}

export interface GameRound {
  id: string;
  media_type: string;
  url: string;
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
