-- Function: auto-group media into trips by time gaps
-- A new trip starts when there's a gap > gap_hours between consecutive media items.
CREATE OR REPLACE FUNCTION auto_detect_trips(gap_hours INT DEFAULT 24)
RETURNS void AS $$
DECLARE
    rec         RECORD;
    prev_time   TIMESTAMPTZ;
    cur_trip_id UUID;
    trip_name   TEXT;
BEGIN
    -- Clear existing auto-detected trips (keeps manually named ones via a flag you can add later)
    DELETE FROM trips WHERE name LIKE 'Trip %';
    UPDATE media SET trip_id = NULL;

    FOR rec IN
        SELECT id, taken_at
        FROM media
        WHERE taken_at IS NOT NULL
        ORDER BY taken_at
    LOOP
        IF prev_time IS NULL OR EXTRACT(EPOCH FROM (rec.taken_at - prev_time)) / 3600 > gap_hours THEN
            trip_name := 'Trip ' || TO_CHAR(rec.taken_at, 'YYYY-MM-DD');
            INSERT INTO trips (name, start_date)
            VALUES (trip_name, rec.taken_at::DATE)
            RETURNING id INTO cur_trip_id;
        END IF;

        UPDATE media SET trip_id = cur_trip_id WHERE id = rec.id;
        prev_time := rec.taken_at;
    END LOOP;

    -- Update trip end dates
    UPDATE trips t SET
        end_date = (SELECT MAX(taken_at)::DATE FROM media WHERE trip_id = t.id),
        start_date = (SELECT MIN(taken_at)::DATE FROM media WHERE trip_id = t.id);
END;
$$ LANGUAGE plpgsql;
