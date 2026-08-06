-- +goose Up

-- A finished match stops disappearing (E9). Until now the driver deleted its
-- match_log row the moment the match ended, so the only trace of it was the
-- in-memory registry and a redeploy erased it.
--
-- finished_at is what separates the two lives of this table: NULL means "a
-- process was running this, restore it at startup", set means "this is
-- history". Restore filters on it, and nothing deletes a row that has it.
ALTER TABLE match_log ADD COLUMN finished_at INTEGER; -- unix seconds, NULL while running

-- summary is the final standing (lobby.Info) and the sampled score series
-- (lobby.History), written once when the match ends.
--
-- It is redundant with the command log — replaying the log rebuilds both — and
-- it is stored anyway, because the log only replays under a build that
-- simulates identically (the fingerprint column above). This project deploys on
-- every push, so a balance change invalidates every stored log; without this
-- column that redeploy would empty the history page. Bounded by historyCap at
-- about 60 KB in the worst case, and it does not grow with match length.
ALTER TABLE match_log ADD COLUMN summary TEXT; -- JSON {info, history}, NULL while running

-- +goose Down

ALTER TABLE match_log DROP COLUMN summary;
ALTER TABLE match_log DROP COLUMN finished_at;
