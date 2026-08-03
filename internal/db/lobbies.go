package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Lobby states, matching the CHECK constraint in 001_init.sql.
const (
	LobbyOpen     = "open"
	LobbyRunning  = "running"
	LobbyFinished = "finished"
)

// Why a join was refused. A missing lobby is still sql.ErrNoRows, like every
// other lookup here.
var (
	ErrLobbyFull    = errors.New("db: lobby is full")
	ErrLobbyNotOpen = errors.New("db: lobby is not open")
)

// Lobby is a pre-match room. Settings stay as JSON: the schema of the settings
// belongs to internal/lobby, which validates them before they get here.
type Lobby struct {
	ID           int64
	OwnerID      int64
	Name         string
	SettingsJSON string
	State        string
	CreatedAt    time.Time
	MemberCount  int // populated by ListLobbies only
}

// Member is one seat in a lobby, in join order. It carries JSON tags because
// it is handed to the client verbatim; nothing else in this package is.
type Member struct {
	UserID      int64  `json:"user_id"`
	DisplayName string `json:"display_name"`
}

const lobbyColumns = `id, owner_id, name, settings_json, state, created_at`

// CreateLobby opens a lobby with its owner already seated. Both writes are one
// transaction: an owner-less lobby could never be started or cleaned up.
func (d *DB) CreateLobby(ctx context.Context, ownerID int64, name, settingsJSON string) (Lobby, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Lobby{}, fmt.Errorf("db: create lobby: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lobby, err := scanLobby(tx.QueryRowContext(ctx, `
		INSERT INTO lobbies (owner_id, name, settings_json, state) VALUES (?, ?, ?, 'open')
		RETURNING `+lobbyColumns, ownerID, name, settingsJSON))
	if err != nil {
		return Lobby{}, fmt.Errorf("db: create lobby: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO lobby_members (lobby_id, user_id) VALUES (?, ?)`, lobby.ID, ownerID); err != nil {
		return Lobby{}, fmt.Errorf("db: create lobby: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Lobby{}, fmt.Errorf("db: create lobby: %w", err)
	}
	return lobby, nil
}

// ListLobbies returns every lobby in one of the given states, newest first,
// with its member count.
func (d *DB) ListLobbies(ctx context.Context, state string) ([]Lobby, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT `+lobbyColumns+`, (SELECT count(*) FROM lobby_members m WHERE m.lobby_id = lobbies.id)
		FROM lobbies WHERE state = ? ORDER BY created_at DESC, id DESC`, state)
	if err != nil {
		return nil, fmt.Errorf("db: list lobbies: %w", err)
	}
	defer rows.Close()

	var out []Lobby
	for rows.Next() {
		var (
			l       Lobby
			created int64
		)
		if err := rows.Scan(&l.ID, &l.OwnerID, &l.Name, &l.SettingsJSON, &l.State, &created, &l.MemberCount); err != nil {
			return nil, fmt.Errorf("db: list lobbies: %w", err)
		}
		l.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list lobbies: %w", err)
	}
	return out, nil
}

// LobbyByID returns a lobby, or sql.ErrNoRows. Lobbies are public within the
// server, so this one is not scoped to a user; mutations below are.
func (d *DB) LobbyByID(ctx context.Context, id int64) (Lobby, error) {
	return scanLobby(d.QueryRowContext(ctx, `SELECT `+lobbyColumns+` FROM lobbies WHERE id = ?`, id))
}

// LobbyMembers returns the seats in join order — the order colonies are
// assigned in, so it must stay deterministic. joined_at has one-second
// resolution, so insertion order (rowid) breaks the ties two joins in the same
// second would otherwise leave to chance.
func (d *DB) LobbyMembers(ctx context.Context, lobbyID int64) ([]Member, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT u.id, u.display_name FROM lobby_members m JOIN users u ON u.id = m.user_id
		WHERE m.lobby_id = ? ORDER BY m.joined_at, m.rowid`, lobbyID)
	if err != nil {
		return nil, fmt.Errorf("db: lobby members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName); err != nil {
			return nil, fmt.Errorf("db: lobby members: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: lobby members: %w", err)
	}
	return out, nil
}

// JoinLobby seats a user, and is idempotent for someone already seated.
//
// The state check and the seat count live inside the INSERT ... SELECT rather
// than in Go, so two players racing for the last seat cannot both read "one
// free" and both write. The follow-up read only explains a refusal.
func (d *DB) JoinLobby(ctx context.Context, lobbyID, userID int64, maxPlayers int) error {
	res, err := d.ExecContext(ctx, `
		INSERT INTO lobby_members (lobby_id, user_id)
		SELECT l.id, ? FROM lobbies l
		WHERE l.id = ? AND l.state = 'open'
		  AND (SELECT count(*) FROM lobby_members m WHERE m.lobby_id = l.id) < ?
		ON CONFLICT DO NOTHING`, userID, lobbyID, maxPlayers)
	if err != nil {
		return fmt.Errorf("db: join lobby: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: join lobby: %w", err)
	}
	if n > 0 {
		return nil
	}

	var (
		state  string
		seats  int
		member bool
	)
	err = d.QueryRowContext(ctx, `
		SELECT l.state,
		       (SELECT count(*) FROM lobby_members m WHERE m.lobby_id = l.id),
		       EXISTS (SELECT 1 FROM lobby_members m WHERE m.lobby_id = l.id AND m.user_id = ?)
		FROM lobbies l WHERE l.id = ?`, userID, lobbyID).Scan(&state, &seats, &member)
	switch {
	case err != nil:
		return err // sql.ErrNoRows for a lobby that does not exist
	case member:
		return nil // already in: joining twice is not an error
	case state != LobbyOpen:
		return ErrLobbyNotOpen
	default:
		return ErrLobbyFull
	}
}

// LeaveLobby frees a seat in an open lobby. It returns sql.ErrNoRows when the
// user had no seat there, or the lobby has already started.
func (d *DB) LeaveLobby(ctx context.Context, lobbyID, userID int64) error {
	res, err := d.ExecContext(ctx, `
		DELETE FROM lobby_members WHERE lobby_id = ? AND user_id = ?
		  AND (SELECT state FROM lobbies WHERE id = ?) = 'open'`, lobbyID, userID, lobbyID)
	if err != nil {
		return fmt.Errorf("db: leave lobby: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: leave lobby: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// StartLobby flips an open lobby to running, and only for its owner. Ownership
// and the "no start twice" rule are both in the WHERE clause, so two concurrent
// start requests cannot both win. It returns sql.ErrNoRows when nothing
// matched; the caller reads the row back to tell "not yours" from "already
// started".
func (d *DB) StartLobby(ctx context.Context, lobbyID, ownerID int64) error {
	res, err := d.ExecContext(ctx,
		`UPDATE lobbies SET state = 'running' WHERE id = ? AND owner_id = ? AND state = 'open'`,
		lobbyID, ownerID)
	if err != nil {
		return fmt.Errorf("db: start lobby: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: start lobby: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetLobbyState is how a match end (or a failed start) moves the row on.
func (d *DB) SetLobbyState(ctx context.Context, lobbyID int64, state string) error {
	if _, err := d.ExecContext(ctx, `UPDATE lobbies SET state = ? WHERE id = ?`, state, lobbyID); err != nil {
		return fmt.Errorf("db: set lobby state: %w", err)
	}
	return nil
}

func scanLobby(s scannable) (Lobby, error) {
	var (
		l       Lobby
		created int64
	)
	if err := s.Scan(&l.ID, &l.OwnerID, &l.Name, &l.SettingsJSON, &l.State, &created); err != nil {
		return Lobby{}, err
	}
	l.CreatedAt = time.Unix(created, 0).UTC()
	return l, nil
}
