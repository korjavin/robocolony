-- +goose Up

-- Design §2.1 step 3: each human player picks which of their *own* blueprints
-- the colony approves for production and which program each one runs. It is a
-- property of the seat, not of the lobby, which is why it lives here and not in
-- lobbies.settings_json alongside the match-wide settings.
--
-- The column holds a frozen *snapshot* — the parts list and the rules
-- themselves — not library ids. Two reasons, and both are correctness rather
-- than convenience:
--
--   * A restart replays a running match from its seed plus its command log
--     (internal/lobby/persist.go), and the starting robots are rebuilt from
--     this row. Storing ids would let a player edit their library mid-match and
--     have the restart resurrect the match with robots it never started with.
--   * It is the same rule lobby.Command already follows for an installed
--     program, for the same reason.
--
-- Empty means "chose nothing": the colony starts from the built-in kit, which
-- is what every colony did before this column existed.
ALTER TABLE lobby_members ADD COLUMN loadout_json TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE lobby_members DROP COLUMN loadout_json;
