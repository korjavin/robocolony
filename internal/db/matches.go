package db

import (
	"context"
	"fmt"
	"time"
)

// MatchLog is one running match's replay record (E7.6): everything a restart
// needs that the lobby row does not already hold. The lobby row carries the
// seed and the settings, lobby_members carries the seats; together with the
// commands here they replay to the exact world the match was in.
//
// Commands stays as JSON, like Lobby.SettingsJSON: the shape of a command
// belongs to internal/lobby.
type MatchLog struct {
	LobbyID     int64
	Fingerprint string
	Tick        int64
	StartedAt   time.Time
	Commands    string
}

// SaveMatchLog writes a match's replay record, replacing any previous one. The
// whole row is rewritten so that tick and commands are never saved apart: a
// process killed between two saves loses the ticks since the last one and
// nothing else.
func (d *DB) SaveMatchLog(ctx context.Context, l MatchLog) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO match_log (lobby_id, fingerprint, tick, started_at, commands)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (lobby_id) DO UPDATE SET
			fingerprint = excluded.fingerprint,
			tick        = excluded.tick,
			started_at  = excluded.started_at,
			commands    = excluded.commands,
			updated_at  = unixepoch()`,
		l.LobbyID, l.Fingerprint, l.Tick, l.StartedAt.Unix(), l.Commands)
	if err != nil {
		return fmt.Errorf("db: save match log: %w", err)
	}
	return nil
}

// MatchLogByID returns a match's replay record, or sql.ErrNoRows when the match
// was started by a build that did not record one.
func (d *DB) MatchLogByID(ctx context.Context, lobbyID int64) (MatchLog, error) {
	var (
		l       MatchLog
		started int64
	)
	err := d.QueryRowContext(ctx,
		`SELECT lobby_id, fingerprint, tick, started_at, commands FROM match_log WHERE lobby_id = ?`,
		lobbyID).Scan(&l.LobbyID, &l.Fingerprint, &l.Tick, &started, &l.Commands)
	if err != nil {
		return MatchLog{}, err
	}
	l.StartedAt = time.Unix(started, 0).UTC()
	return l, nil
}

// DeleteMatchLog drops a record whose match is over or unreplayable. Nothing
// deletes lobbies, so without this the logs would accumulate for the life of
// the volume.
func (d *DB) DeleteMatchLog(ctx context.Context, lobbyID int64) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM match_log WHERE lobby_id = ?`, lobbyID); err != nil {
		return fmt.Errorf("db: delete match log: %w", err)
	}
	return nil
}
