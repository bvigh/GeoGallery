CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- GPS track points imported from external sources (Google Timeline, Strava, etc.)
CREATE TABLE gps_tracks (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    source      TEXT NOT NULL,            -- 'google_timeline', 'strava', etc.
    recorded_at TIMESTAMPTZ NOT NULL,
    location    GEOGRAPHY(POINT, 4326) NOT NULL,
    altitude_m  FLOAT,
    accuracy_m  FLOAT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    activity    TEXT                      -- 'WALKING', 'IN_VEHICLE', etc.
);
CREATE INDEX gps_tracks_recorded_at_idx ON gps_tracks (recorded_at);
CREATE INDEX gps_tracks_location_idx ON gps_tracks USING GIST (location);

-- Auto-detected trips
CREATE TABLE trips (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        TEXT NOT NULL,
    start_date  DATE,
    end_date    DATE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Media files (photo, video, 360° panorama/video)
CREATE TABLE media (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    filename      TEXT NOT NULL,
    media_type    TEXT NOT NULL CHECK (media_type IN ('photo', 'video', 'photo_360', 'video_360')),
    storage_key   TEXT NOT NULL UNIQUE,   -- S3/MinIO object key
    taken_at      TIMESTAMPTZ,
    location      GEOGRAPHY(POINT, 4326), -- resolved GPS location
    gps_confidence FLOAT CHECK (gps_confidence BETWEEN 0 AND 1),
    gps_source    TEXT CHECK (gps_source IN ('exif', 'interpolated', 'manual', 'none')),
    camera_model  TEXT,
    trip_id       UUID REFERENCES trips(id) ON DELETE SET NULL,
    width_px      INT,
    height_px     INT,
    duration_sec  FLOAT,                  -- for video
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX media_taken_at_idx ON media (taken_at);
CREATE INDEX media_location_idx ON media USING GIST (location);
CREATE INDEX media_trip_id_idx ON media (trip_id);
