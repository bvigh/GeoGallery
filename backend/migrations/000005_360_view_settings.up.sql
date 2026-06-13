ALTER TABLE media
  ADD COLUMN initial_yaw   FLOAT,
  ADD COLUMN initial_pitch FLOAT,
  ADD COLUMN initial_fov   FLOAT;

CREATE TABLE video_keyframes (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    media_id     UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    time_seconds FLOAT NOT NULL,
    yaw          FLOAT NOT NULL,
    pitch        FLOAT NOT NULL,
    fov          FLOAT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX video_keyframes_media_id_time ON video_keyframes(media_id, time_seconds);
