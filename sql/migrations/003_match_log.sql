-- +goose Up

-- What a restart needs to bring a running match back (E7.6).
--
-- Not a state dump: the simulation is deterministic given its seed, so the
-- match is rebuilt by generating the arena from the lobby's seat list and seed
-- and replaying the player commands recorded here. That is why this table is
-- small enough to live next to the game on a small VPS.
--
-- fingerprint identifies the simulation behaviour of the build that recorded
-- the log. A log only replays correctly under a build that simulates
-- identically, so a mismatch abandons the match rather than reconstructing a
-- wrong world.
--
-- One row per match, rewritten in place: the row is always a consistent
-- (tick, commands) pair, so a crash between saves rewinds the match rather
-- than corrupting it.
CREATE TABLE match_log (
    lobby_id    INTEGER PRIMARY KEY REFERENCES lobbies (id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    tick        INTEGER NOT NULL, -- the tick the commands below replay up to
    started_at  INTEGER NOT NULL, -- unix seconds, so the restored match keeps its start time
    commands    TEXT NOT NULL,    -- JSON array of lobby.Command, in the order they applied
    updated_at  INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- +goose Down

DROP TABLE match_log;
