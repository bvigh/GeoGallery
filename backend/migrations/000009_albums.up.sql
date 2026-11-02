-- Lets album_media reference (media id, owner) so each contribution is tied to the media's owner.
ALTER TABLE media ADD CONSTRAINT media_id_owner_key UNIQUE (id, owner_id);

CREATE TABLE albums (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    owner_id   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name       TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX albums_owner_idx ON albums (owner_id);

-- The album owner also has a member row (inserted in the create transaction),
-- so readableMediaCond needs a single join.
CREATE TABLE album_members (
    album_id UUID NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    user_id  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, user_id)
);
CREATE INDEX album_members_user_idx ON album_members (user_id);

CREATE TABLE album_media (
    album_id UUID NOT NULL,
    media_id UUID NOT NULL,
    owner_id UUID NOT NULL,            -- the media's owner = the contributor
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (album_id, media_id),
    -- Only members contribute; leaving or removal deletes their contributions.
    FOREIGN KEY (album_id, owner_id) REFERENCES album_members (album_id, user_id) ON DELETE CASCADE,
    -- Only the owner can contribute an item (no re-sharing); deleting the media removes it.
    FOREIGN KEY (media_id, owner_id) REFERENCES media (id, owner_id) ON DELETE CASCADE
);
CREATE INDEX album_media_media_idx ON album_media (media_id);
