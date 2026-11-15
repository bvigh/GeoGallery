-- The owner's own correction of taken_at (#26), through PATCH /api/media/{id}/taken-at or
-- POST /api/media/shift-time. capture_evidence keeps saying how the file's time was read; time_edit
-- says that the owner overrode taken_at afterwards: the kind of change, when it was made, the time
-- before it and before the first such change, and for a time set by hand the wall clock and zone
-- entered. NULL means taken_at was never changed through these routes. server retime leaves an
-- edited row alone.
ALTER TABLE media ADD COLUMN time_edit JSONB;
ALTER TABLE media ADD CONSTRAINT media_time_edit_object
    CHECK (time_edit IS NULL OR jsonb_typeof(time_edit) = 'object');
