ALTER TABLE media      ADD COLUMN owner_id UUID REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE trips      ADD COLUMN owner_id UUID REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE gps_tracks ADD COLUMN owner_id UUID REFERENCES users(id) ON DELETE RESTRICT;

UPDATE media      SET owner_id = '00000000-0000-0000-0000-000000000001';
UPDATE trips      SET owner_id = '00000000-0000-0000-0000-000000000001';
UPDATE gps_tracks SET owner_id = '00000000-0000-0000-0000-000000000001';

ALTER TABLE media      ALTER COLUMN owner_id SET NOT NULL;
ALTER TABLE trips      ALTER COLUMN owner_id SET NOT NULL;
ALTER TABLE gps_tracks ALTER COLUMN owner_id SET NOT NULL;

-- Dedup becomes per owner: two users uploading the same photo each get their own row.
ALTER TABLE media DROP CONSTRAINT media_file_hash_key;
ALTER TABLE media ADD CONSTRAINT media_owner_file_hash_key UNIQUE (owner_id, file_hash);

ALTER TABLE gps_tracks DROP CONSTRAINT gps_tracks_source_recorded_at_key;
ALTER TABLE gps_tracks ADD CONSTRAINT gps_tracks_owner_source_recorded_at_key
    UNIQUE (owner_id, source, recorded_at);

-- DB-level guarantee: media can only belong to a trip of the same owner.
ALTER TABLE trips ADD CONSTRAINT trips_id_owner_key UNIQUE (id, owner_id);
ALTER TABLE media DROP CONSTRAINT media_trip_id_fkey;
ALTER TABLE media ADD CONSTRAINT media_trip_owner_fkey
    FOREIGN KEY (trip_id, owner_id) REFERENCES trips (id, owner_id)
    ON DELETE SET NULL (trip_id);   -- PG15+; plain SET NULL would also null owner_id

CREATE INDEX media_owner_taken_at_idx       ON media (owner_id, taken_at);
CREATE INDEX trips_owner_idx                ON trips (owner_id);
CREATE INDEX gps_tracks_owner_recorded_idx  ON gps_tracks (owner_id, recorded_at);

-- Trip detection is per owner. The old signature deleted *every* auto trip globally.
DROP FUNCTION auto_detect_trips(INT);
CREATE FUNCTION auto_detect_trips(p_owner UUID, gap_hours INT DEFAULT 24)
RETURNS void AS $$
DECLARE
    rec RECORD; prev_time TIMESTAMPTZ; cur_trip_id UUID; trip_name TEXT;
BEGIN
    DELETE FROM trips WHERE user_edited = FALSE AND owner_id = p_owner;

    FOR rec IN
        SELECT id, taken_at FROM media
        WHERE owner_id = p_owner AND taken_at IS NOT NULL AND trip_id IS NULL
        ORDER BY taken_at
    LOOP
        IF prev_time IS NULL OR
           EXTRACT(EPOCH FROM (rec.taken_at - prev_time)) / 3600 > gap_hours THEN
            trip_name := 'Trip ' || TO_CHAR(rec.taken_at, 'YYYY-MM-DD');
            INSERT INTO trips (owner_id, name, start_date, user_edited)
            VALUES (p_owner, trip_name, rec.taken_at::DATE, FALSE)
            RETURNING id INTO cur_trip_id;
        END IF;
        UPDATE media SET trip_id = cur_trip_id WHERE id = rec.id;
        prev_time := rec.taken_at;
    END LOOP;

    UPDATE trips t
    SET start_date = (SELECT MIN(taken_at)::DATE FROM media WHERE trip_id = t.id),
        end_date   = (SELECT MAX(taken_at)::DATE FROM media WHERE trip_id = t.id)
    WHERE user_edited = FALSE AND owner_id = p_owner;
END;
$$ LANGUAGE plpgsql;
