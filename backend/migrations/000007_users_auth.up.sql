CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    issuer        TEXT,
    subject       TEXT,
    email         TEXT,          -- display/contact only; never a join key
    display_name  TEXT,
    role          TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    disabled_at   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ,
    CHECK ((issuer IS NULL) = (subject IS NULL)),
    UNIQUE (issuer, subject)
);

CREATE TABLE sessions (
    token_hash BYTEA PRIMARY KEY,            -- sha256(cookie value); the raw token is never stored
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id_token   TEXT NOT NULL,                -- raw ID token, used only as id_token_hint on logout; never sent to the browser
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE TABLE invites (
    token_hash    BYTEA PRIMARY KEY,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    claim_user_id UUID REFERENCES users(id) ON DELETE CASCADE, -- CLI-only: bind the login to an existing unclaimed user
    created_by    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL,
    used_at       TIMESTAMPTZ,
    used_by       UUID REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE api_tokens (
    id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    token_hash   BYTEA NOT NULL UNIQUE,      -- sha256(full token); the plaintext is shown once, at creation
    display_hint TEXT NOT NULL,              -- 'gg_pat_' + first 6 chars of the random part, so users can tell tokens apart
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,       -- always set; max 365 days, so a forgotten token always dies
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);

-- Unclaimed owner of all pre-auth data. It has no issuer/subject, so it cannot log in
-- until `server invite --admin` creates a claim invite for it.
INSERT INTO users (id, role, display_name)
VALUES ('00000000-0000-0000-0000-000000000001', 'admin', 'Owner');
