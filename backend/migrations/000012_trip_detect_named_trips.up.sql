-- auto_detect_trips also fills the owner's named trips. An item on a day that a named
-- (user_edited) trip covers joins that trip, as a date edit of the trip would have put it there,
-- instead of starting an auto trip with the same dates. Only the rest are grouped by time gap.
CREATE OR REPLACE FUNCTION auto_detect_trips(p_owner UUID, gap_hours INT DEFAULT 24)
RETURNS void AS $$
DECLARE
    rec RECORD; prev_time TIMESTAMPTZ; cur_trip_id UUID; trip_name TEXT;
BEGIN
    DELETE FROM trips WHERE user_edited = FALSE AND owner_id = p_owner;

    -- The day is taken_at::date, in the session's zone, as in the date edit of PATCH /api/trips/{id}.
    -- A trip without both dates claims nothing (that edit moves nothing for one either). Where named
    -- trips overlap, the narrowest wins, then the latest start, then the lowest id.
    UPDATE media m
    SET trip_id = claim.trip_id
    FROM (
        SELECT DISTINCT ON (mm.id) mm.id AS media_id, t.id AS trip_id
        FROM media mm
        JOIN trips t ON t.owner_id = p_owner AND t.user_edited
                    AND mm.taken_at::date BETWEEN t.start_date AND t.end_date
        WHERE mm.owner_id = p_owner AND mm.taken_at IS NOT NULL AND mm.trip_id IS NULL
        ORDER BY mm.id, t.end_date - t.start_date, t.start_date DESC, t.id
    ) claim
    WHERE m.id = claim.media_id;

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
