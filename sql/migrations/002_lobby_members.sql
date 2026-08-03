-- +goose Up

-- Who is in a lobby. 001 created the lobby row itself; membership is the piece
-- E2.3 needs to hand each member a colony when the match starts.
--
-- Colonies are assigned in join order, so joined_at is state, not decoration.
CREATE TABLE lobby_members (
    lobby_id  INTEGER NOT NULL REFERENCES lobbies (id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    joined_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (lobby_id, user_id)
) STRICT;

CREATE INDEX lobby_members_user ON lobby_members (user_id);

-- +goose Down

DROP TABLE lobby_members;
