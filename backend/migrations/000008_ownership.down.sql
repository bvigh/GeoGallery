-- CAVEAT: this down migration FAILS once two owners share a media file_hash, or a
-- gps_tracks (source, recorded_at) pair: the restored global UNIQUE constraints cannot be
-- built over per-owner duplicates. Manual locations ('manual' tracks at a photo's taken_at)
-- and overlapping GPS imports can cause the latter even without any shared photo.
-- Rolling back is realistic only before a second user has ingested data; otherwise remove
-- the duplicates first. A failed run leaves schema_migrations dirty (see migrate force).

DROP FUNCTION auto_detect_trips(UUID, INT);
CREATE FUNCTION auto_detect_trips(gap_hours INT DEFAULT 24)
RETURNS void AS $$
DECLARE
    rec         RECORD;
    prev_time   TIMESTAMPTZ;
    cur_trip_id UUID;
    trip_name   TEXT;
BEGIN
    -- Delete only auto-detected trips; ON DELETE SET NULL cascades to media.trip_id
    DELETE FROM trips WHERE user_edited = FALSE;

    -- Only group media that is now unassigned (user-edited trips keep their media)
    FOR rec IN
        SELECT id, taken_at
        FROM media
        WHERE taken_at IS NOT NULL AND trip_id IS NULL
        ORDER BY taken_at
    LOOP
        IF prev_time IS NULL OR
           EXTRACT(EPOCH FROM (rec.taken_at - prev_time)) / 3600 > gap_hours THEN
            trip_name := 'Trip ' || TO_CHAR(rec.taken_at, 'YYYY-MM-DD');
            INSERT INTO trips (name, start_date, user_edited)
            VALUES (trip_name, rec.taken_at::DATE, FALSE)
            RETURNING id INTO cur_trip_id;
        END IF;
        UPDATE media SET trip_id = cur_trip_id WHERE id = rec.id;
        prev_time := rec.taken_at;
    END LOOP;

    -- Update date ranges for auto-detected trips only
    UPDATE trips t
    SET start_date = (SELECT MIN(taken_at)::DATE FROM media WHERE trip_id = t.id),
        end_date   = (SELECT MAX(taken_at)::DATE FROM media WHERE trip_id = t.id)
    WHERE user_edited = FALSE;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE media DROP CONSTRAINT media_trip_owner_fkey;
ALTER TABLE trips DROP CONSTRAINT trips_id_owner_key;
ALTER TABLE media ADD CONSTRAINT media_trip_id_fkey
    FOREIGN KEY (trip_id) REFERENCES trips(id) ON DELETE SET NULL;

ALTER TABLE gps_tracks DROP CONSTRAINT gps_tracks_owner_source_recorded_at_key;
ALTER TABLE gps_tracks ADD CONSTRAINT gps_tracks_source_recorded_at_key UNIQUE (source, recorded_at);

ALTER TABLE media DROP CONSTRAINT media_owner_file_hash_key;
ALTER TABLE media ADD CONSTRAINT media_file_hash_key UNIQUE (file_hash);

DROP INDEX gps_tracks_owner_recorded_idx;
DROP INDEX trips_owner_idx;
DROP INDEX media_owner_taken_at_idx;

ALTER TABLE gps_tracks DROP COLUMN owner_id;
ALTER TABLE trips      DROP COLUMN owner_id;
ALTER TABLE media      DROP COLUMN owner_id;
