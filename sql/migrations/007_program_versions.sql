-- +goose Up

-- Program versions (design 1d/1e: the "v7 · DRAFT" badge, APPROVE v8, and the
-- VERSIONS panel). Saving a program stops overwriting one body and starts
-- appending a numbered one, and "approved" names the single version a robot is
-- given when it is installed or reprogrammed.
--
-- Two columns and one table, and the split between them is the point:
--
--   * programs.json stays the *head* — the body the editor loads and saves, and
--     what every existing reader already asks for. programs.version names it.
--   * programs.approved_version names the version an install hands to a robot.
--     It lives on the program row rather than as a flag on the version rows
--     because "exactly one version is approved" is then a fact of the schema
--     instead of an invariant a partial index has to defend.
--   * program_versions holds every saved body, head included. The head is
--     therefore stored twice; that duplication buys every reader of
--     programs.json staying exactly as it was.
CREATE TABLE program_versions (
    program_id INTEGER NOT NULL REFERENCES programs (id) ON DELETE CASCADE,
    version    INTEGER NOT NULL,
    json       TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (program_id, version)
) STRICT;

-- Defaults of 1 are what makes the existing library migrate without a backfill
-- of its own: every program already in the table becomes v1, approved.
ALTER TABLE programs ADD COLUMN version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE programs ADD COLUMN approved_version INTEGER NOT NULL DEFAULT 1;

INSERT INTO program_versions (program_id, version, json, created_at)
SELECT id, 1, json, created_at FROM programs;

-- +goose Down

DROP TABLE program_versions;
ALTER TABLE programs DROP COLUMN approved_version;
ALTER TABLE programs DROP COLUMN version;
