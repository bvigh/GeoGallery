ALTER TABLE trips ADD COLUMN user_edited BOOLEAN NOT NULL DEFAULT FALSE;

-- Trips with custom names were manually created/edited
UPDATE trips SET user_edited = TRUE WHERE name NOT LIKE 'Trip %';

-- Replace auto_detect_trips to respect the user_edited flag
CREATE OR REPLACE FUNCTION auto_detect_trips(gap_hours INT DEFAULT 24)
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
