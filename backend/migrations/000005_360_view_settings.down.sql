DROP TABLE video_keyframes;
ALTER TABLE media
  DROP COLUMN initial_yaw,
  DROP COLUMN initial_pitch,
  DROP COLUMN initial_fov;
