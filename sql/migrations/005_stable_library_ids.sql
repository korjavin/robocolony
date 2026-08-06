-- +goose Up

-- Library ids must never be reused. A lobby approval is stored as a frozen
-- snapshot but is re-sent as an id (web/lobby.html, PUT /loadout), so an id that
-- SQLite hands to a different row silently swaps a player's approved design for
-- whatever replaced it. Plain `INTEGER PRIMARY KEY` is a rowid alias, and SQLite
-- is free to reuse the largest deleted rowid on the next insert; AUTOINCREMENT
-- makes that impossible.
--
-- AUTOINCREMENT cannot be added in place, so both tables are rebuilt. Order
-- matters: with foreign keys on, DROP TABLE runs an implicit DELETE, so dropping
-- `programs` while `blueprints` still references it would fire ON DELETE SET
-- NULL and quietly wipe every default program. Both replacements are therefore
-- created and filled *first*, with blueprints_new pointing at programs_new, and
-- the old pair is dropped only once nothing references it. Renaming
-- programs_new rewrites that reference to `programs` (SQLite >= 3.25, i.e.
-- legacy_alter_table off — the default). PRAGMA foreign_keys is a no-op inside a
-- transaction, so turning it off here would not work anyway.

CREATE TABLE programs_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    json       TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

CREATE TABLE blueprints_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    json               TEXT NOT NULL,
    default_program_id INTEGER REFERENCES programs_new (id) ON DELETE SET NULL
) STRICT;

INSERT INTO programs_new (id, user_id, name, json, created_at, updated_at)
SELECT id, user_id, name, json, created_at, updated_at FROM programs;

INSERT INTO blueprints_new (id, user_id, name, json, default_program_id)
SELECT id, user_id, name, json, default_program_id FROM blueprints;

DROP TABLE blueprints;
DROP TABLE programs;

ALTER TABLE programs_new RENAME TO programs;
ALTER TABLE blueprints_new RENAME TO blueprints;

-- The indexes went with the dropped tables.
CREATE UNIQUE INDEX programs_user_name ON programs (user_id, name);
CREATE UNIQUE INDEX blueprints_user_name ON blueprints (user_id, name);

-- Rows deleted *before* this migration left no trace in the copied tables, so
-- the sequence starts at the surviving maximum and the next few inserts would
-- walk straight back over an id an open lobby's approval still holds — the same
-- swap, once. The loadouts remember those ids, so raise each sequence past them
-- as well. sqlite_sequence is an ordinary table and has no unique index on
-- name, hence delete-then-insert rather than an upsert; a NULL maximum (both
-- tables empty, no loadouts) inserts nothing and leaves the sequence at zero.
DELETE FROM sqlite_sequence WHERE name IN ('programs', 'blueprints');

INSERT INTO sqlite_sequence (name, seq)
SELECT 'programs', high FROM (
    SELECT max(id) AS high FROM (
        SELECT id FROM programs
        UNION ALL
        SELECT json_extract(e.value, '$.program_id')
        FROM lobby_members m, json_each(m.loadout_json, '$.entries') e
        WHERE m.loadout_json <> ''
    )
) WHERE high IS NOT NULL;

INSERT INTO sqlite_sequence (name, seq)
SELECT 'blueprints', high FROM (
    SELECT max(id) AS high FROM (
        SELECT id FROM blueprints
        UNION ALL
        SELECT json_extract(e.value, '$.blueprint_id')
        FROM lobby_members m, json_each(m.loadout_json, '$.entries') e
        WHERE m.loadout_json <> ''
    )
) WHERE high IS NOT NULL;

-- +goose Down

CREATE TABLE programs_old (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    json       TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

CREATE TABLE blueprints_old (
    id                 INTEGER PRIMARY KEY,
    user_id            INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name               TEXT NOT NULL,
    json               TEXT NOT NULL,
    default_program_id INTEGER REFERENCES programs_old (id) ON DELETE SET NULL
) STRICT;

INSERT INTO programs_old (id, user_id, name, json, created_at, updated_at)
SELECT id, user_id, name, json, created_at, updated_at FROM programs;

INSERT INTO blueprints_old (id, user_id, name, json, default_program_id)
SELECT id, user_id, name, json, default_program_id FROM blueprints;

DROP TABLE blueprints;
DROP TABLE programs;

ALTER TABLE programs_old RENAME TO programs;
ALTER TABLE blueprints_old RENAME TO blueprints;

CREATE UNIQUE INDEX programs_user_name ON programs (user_id, name);
CREATE UNIQUE INDEX blueprints_user_name ON blueprints (user_id, name);
