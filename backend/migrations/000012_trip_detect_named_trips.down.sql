-- The 000008 auto_detect_trips: unassigned media only ever start or join auto trips. Media that
-- the up migration's version put into named trips stay there.
CREATE OR REPLACE FUNCTION auto_detect_trips(p_owner UUID, gap_hours INT DEFAULT 24)
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
