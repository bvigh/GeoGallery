-- btree_gist lets the exclusion constraint below compare owner_id, make and model with =.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- A user's statement that, during a period, the clock of one of their cameras showed a fixed UTC
-- offset, which nothing in its files reveals. Ingest reads that camera's EXIF wall clock at this
-- offset (after the photo's GPS clock, before its OffsetTimeOriginal). Make and model are matched
-- against the file's EXIF without case; they are stored trimmed, with the case as entered. The
-- dates are days on the camera's own clock, because the rule is what turns that clock into UTC.
CREATE TABLE camera_clock_rules (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id       UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    make           TEXT NOT NULL CHECK (char_length(make) BETWEEN 1 AND 200 AND make = btrim(make)),
    model          TEXT NOT NULL CHECK (char_length(model) BETWEEN 1 AND 200 AND model = btrim(model)),
    utc_offset_min INT  NOT NULL CHECK (utc_offset_min BETWEEN -720 AND 840 AND utc_offset_min % 15 = 0),
    starts_on      DATE,            -- first day, on the camera's own clock; NULL = no start
    ends_on        DATE,            -- last day (inclusive), on the camera's own clock; NULL = no end
    note           TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT camera_clock_rules_period_check CHECK (starts_on IS NULL OR ends_on IS NULL OR starts_on <= ends_on),
    -- NULL is the open bound. 'infinity' would be a second spelling of it, and Go cannot read it.
    CONSTRAINT camera_clock_rules_finite_check CHECK (isfinite(starts_on) AND isfinite(ends_on)),
    -- One camera of one owner can have only one rule on any day. Its GiST index, which leads with
    -- owner_id, also serves the WHERE owner_id = $1 lookups.
    CONSTRAINT camera_clock_rules_no_overlap EXCLUDE USING gist (
        owner_id WITH =, (lower(make)) WITH =, (lower(model)) WITH =,
        daterange(starts_on, ends_on, '[]') WITH &&)
);
