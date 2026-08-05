package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// MatchLog is one match's replay record (E7.6): everything a restart needs that
// the lobby row does not already hold. The lobby row carries the seed and the
// settings, lobby_members carries the seats; together with the commands here
// they replay to the exact world the match was in.
//
// Commands and Summary stay as JSON, like Lobby.SettingsJSON: their shape
// belongs to internal/lobby.
type MatchLog struct {
	LobbyID     int64
	Fingerprint string
	Tick        int64
	StartedAt   time.Time
	Commands    string

	// FinishedAt is zero while the match is running, and set once it has
	// reached its duration — the flag that separates "restore this at startup"
	// from "this is history" (E9). Summary is the final standing and score
	// series alongside it, empty while running.
	FinishedAt time.Time
	Summary    string
}

// SaveMatchLog writes a match's replay record, replacing any previous one. The
// whole row is rewritten so that tick and commands are never saved apart: a
// process killed between two saves loses the ticks since the last one and
// nothing else.
func (d *DB) SaveMatchLog(ctx context.Context, l MatchLog) error {
	var finished, summary any // NULL while the match is running
	if !l.FinishedAt.IsZero() {
		finished = l.FinishedAt.Unix()
	}
	if l.Summary != "" {
		summary = l.Summary
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO match_log (lobby_id, fingerprint, tick, started_at, commands, finished_at, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (lobby_id) DO UPDATE SET
			fingerprint = excluded.fingerprint,
			tick        = excluded.tick,
			started_at  = excluded.started_at,
			commands    = excluded.commands,
			finished_at = excluded.finished_at,
			summary     = excluded.summary,
			updated_at  = unixepoch()`,
		l.LobbyID, l.Fingerprint, l.Tick, l.StartedAt.Unix(), l.Commands, finished, summary)
	if err != nil {
		return fmt.Errorf("db: save match log: %w", err)
	}
	return nil
}

const matchLogColumns = `lobby_id, fingerprint, tick, started_at, commands, finished_at, summary`

// MatchLogByID returns a match's replay record, or sql.ErrNoRows when the match
// was started by a build that did not record one.
func (d *DB) MatchLogByID(ctx context.Context, lobbyID int64) (MatchLog, error) {
	return scanMatchLog(d.QueryRowContext(ctx,
		`SELECT `+matchLogColumns+` FROM match_log WHERE lobby_id = ?`, lobbyID))
}

// ListFinishedMatchLogs returns the records of the matches that have ended,
// newest first: the history list (E9).
//
// It leaves the command log behind — the list only needs the summary, and the
// logs are the large half of the table.
//
// ponytail: no paging. The owner's call is to keep every match, and a list is
// one small row each; add a LIMIT when a real deployment has enough history for
// the response to matter.
func (d *DB) ListFinishedMatchLogs(ctx context.Context) ([]MatchLog, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT lobby_id, fingerprint, tick, started_at, '', finished_at, summary
		FROM match_log WHERE finished_at IS NOT NULL
		ORDER BY finished_at DESC, lobby_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("db: list finished match logs: %w", err)
	}
	defer rows.Close()

	var out []MatchLog
	for rows.Next() {
		l, err := scanMatchLog(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list finished match logs: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list finished match logs: %w", err)
	}
	return out, nil
}

// scanMatchLog reads one row in matchLogColumns order. sql.Row and sql.Rows
// both satisfy it.
func scanMatchLog(row interface{ Scan(...any) error }) (MatchLog, error) {
	var (
		l        MatchLog
		started  int64
		finished sql.NullInt64
		summary  sql.NullString
	)
	if err := row.Scan(&l.LobbyID, &l.Fingerprint, &l.Tick, &started, &l.Commands, &finished, &summary); err != nil {
		return MatchLog{}, err
	}
	l.StartedAt = time.Unix(started, 0).UTC()
	if finished.Valid {
		l.FinishedAt = time.Unix(finished.Int64, 0).UTC()
	}
	l.Summary = summary.String
	return l, nil
}

// DeleteMatchLog drops a record that cannot be trusted: Restore's fallback, and
// nothing else. A record with finished_at set is history and is never deleted
// (E9 owner decision: keep everything, no retention GC).
func (d *DB) DeleteMatchLog(ctx context.Context, lobbyID int64) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM match_log WHERE lobby_id = ?`, lobbyID); err != nil {
		return fmt.Errorf("db: delete match log: %w", err)
	}
	return nil
}
