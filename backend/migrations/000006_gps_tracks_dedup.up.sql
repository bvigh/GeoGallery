ALTER TABLE gps_tracks ADD CONSTRAINT gps_tracks_source_recorded_at_key UNIQUE (source, recorded_at);
