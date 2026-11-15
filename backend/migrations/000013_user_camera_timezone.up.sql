-- The first per-user setting (#25): the IANA zone the user's camera clocks show. Ingest reads an
-- EXIF wall clock in it when the file itself gives no offset (no usable GPS clock, no clock rule,
-- no trusted OffsetTimeOriginal), unless an upload or a scan names a zone of its own. NULL means the
-- server's CAMERA_TIMEZONE. The name is checked with time.LoadLocation when it is set; the length
-- check only keeps the column honest.
ALTER TABLE users
    ADD COLUMN camera_timezone TEXT
        CONSTRAINT users_camera_timezone_length CHECK (char_length(camera_timezone) BETWEEN 1 AND 64);
