-- The second per-user setting (#38): whether a video the user opens full size (a gallery, the Map
-- page's view, the Game, a challenge's results) starts muted (true) or with sound (false). The
-- browser applies it when a video starts; M and the player's own button still turn the sound on
-- and off. NULL means the server's default, which GET /api/me/settings names.
ALTER TABLE users ADD COLUMN videos_start_muted BOOLEAN;
