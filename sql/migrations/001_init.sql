-- +goose Up

-- Never edit this file once merged; add the next numbered migration instead.
--
-- Timestamps are unix seconds (INTEGER) so they sort correctly and need no
-- format parsing on the way back into Go. STRICT tables make SQLite reject a
-- value of the wrong type instead of silently coercing it.

CREATE TABLE users (
    id           INTEGER PRIMARY KEY,
    google_sub   TEXT NOT NULL UNIQUE, -- Google OIDC subject; stable, unlike email
    email        TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- Only the SHA-256 of the session cookie is stored, so a database leak does not
-- hand out live sessions.
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    expires_at INTEGER NOT NULL
) STRICT;

CREATE INDEX sessions_user_id ON sessions (user_id);

-- A program is an ordered rule list (design §10.2). The server validates the
-- JSON before it lands here; SQLite only guarantees it is text.
CREATE TABLE programs (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    json       TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

CREATE UNIQUE INDEX programs_user_name ON programs (user_id, name);

-- A blueprint is a legal physical configuration plus an optional default
-- program (design §5.1). Deleting the program leaves the blueprint buildable.
CREATE TABLE blueprints (
    id                 INTEGER PRIMARY KEY,
    user_id            INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    json               TEXT NOT NULL,
    default_program_id INTEGER REFERENCES programs (id) ON DELETE SET NULL
) STRICT;

CREATE UNIQUE INDEX blueprints_user_name ON blueprints (user_id, name);

-- Lobbies persist; live match state does not (AGENTS.md, "Persistence").
CREATE TABLE lobbies (
    id            INTEGER PRIMARY KEY,
    owner_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    settings_json TEXT NOT NULL,
    state         TEXT NOT NULL CHECK (state IN ('open', 'running', 'finished')),
    created_at    INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

CREATE INDEX lobbies_state ON lobbies (state);

-- +goose Down

DROP TABLE lobbies;
DROP TABLE blueprints;
DROP TABLE programs;
DROP TABLE sessions;
DROP TABLE users;
