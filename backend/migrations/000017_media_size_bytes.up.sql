-- The size in bytes of a media item's object in S3 (#42), for the Admin page's storage use. Ingest
-- writes the size of what it uploads, so a video transcoded at upload counts the transcode. NULL on
-- rows stored before this column: `server backfill-sizes` fills them from the bucket (one HEAD per
-- object), and the Admin page counts what is still unknown.
ALTER TABLE media ADD COLUMN size_bytes BIGINT;
ALTER TABLE media ADD CONSTRAINT media_size_bytes_nonnegative CHECK (size_bytes >= 0);
