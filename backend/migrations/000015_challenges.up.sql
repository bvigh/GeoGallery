-- Game challenges (#39): one user draws five located items that every chosen player can already
-- read, the players each guess them once, and a high-score table compares them. A challenge
-- grants no access: an item is shown to a player only while readableMediaCond lets them read it.

CREATE TABLE challenges (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    owner_id     UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name         TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    -- The scale every guess is scored on and the area the guess map opens on, measured once over
    -- the set the items were drawn from, so every player is scored alike.
    max_dist_km  DOUBLE PRECISION NOT NULL CHECK (max_dist_km >= 1),
    bounds_south DOUBLE PRECISION NOT NULL,
    bounds_west  DOUBLE PRECISION NOT NULL,
    bounds_north DOUBLE PRECISION NOT NULL,
    bounds_east  DOUBLE PRECISION NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX challenges_owner_idx ON challenges (owner_id);

-- The players. The challenge owner has a row too (inserted in the create transaction), as an
-- album's owner has in album_members.
CREATE TABLE challenge_members (
    challenge_id UUID NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    added_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (challenge_id, user_id)
);
CREATE INDEX challenge_members_user_idx ON challenge_members (user_id);

-- The rounds. location, taken_at and gps_confidence are the item's own at the time the challenge
-- was made: every player is scored against the same answer, whatever its owner corrects later.
-- Deleting the media takes its round, and the guesses on it, out of the challenge.
CREATE TABLE challenge_items (
    challenge_id   UUID NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    position       SMALLINT NOT NULL CHECK (position BETWEEN 1 AND 5),
    media_id       UUID NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    location       GEOGRAPHY(POINT, 4326) NOT NULL,
    taken_at       TIMESTAMPTZ,
    gps_confidence DOUBLE PRECISION NOT NULL CHECK (gps_confidence BETWEEN 0 AND 1),
    PRIMARY KEY (challenge_id, position),
    UNIQUE (challenge_id, media_id)
);
CREATE INDEX challenge_items_media_idx ON challenge_items (media_id);

-- One scored guess per player and round: the primary key is what makes the attempt single.
CREATE TABLE challenge_scores (
    challenge_id   UUID NOT NULL,
    position       SMALLINT NOT NULL,
    user_id        UUID NOT NULL,
    guess          GEOGRAPHY(POINT, 4326) NOT NULL,
    guess_date     DATE,
    distance_km    DOUBLE PRECISION NOT NULL,
    date_diff_days DOUBLE PRECISION,
    location_score DOUBLE PRECISION NOT NULL,
    time_score     DOUBLE PRECISION,
    total_score    DOUBLE PRECISION NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (challenge_id, position, user_id),
    -- A round that goes (its media deleted) takes its guesses along.
    FOREIGN KEY (challenge_id, position) REFERENCES challenge_items (challenge_id, position) ON DELETE CASCADE,
    -- Only players guess; a player who leaves takes their guesses along.
    FOREIGN KEY (challenge_id, user_id) REFERENCES challenge_members (challenge_id, user_id) ON DELETE CASCADE
);
CREATE INDEX challenge_scores_member_idx ON challenge_scores (challenge_id, user_id);
