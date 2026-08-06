package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Every query below is parameterised. Never build SQL by concatenating values.
//
// Rows that belong to a user are always looked up by (user_id, id): the
// ownership check lives in the WHERE clause, so a wrong id reads as "not
// found" instead of leaking another player's library. Missing rows come back
// as sql.ErrNoRows for the caller to map onto a 404.

// User is a player account, keyed on the Google OIDC subject.
type User struct {
	ID          int64
	GoogleSub   string
	Email       string
	DisplayName string
	CreatedAt   time.Time
}

// Program is a saved, ordered rule list from a player's library (design §10.2).
// JSON is stored verbatim; validation happens before it gets here.
//
// JSON is the *head*: the body the editor last saved, which is Version. What a
// robot is given on install is ApprovedVersion, whose body lives in
// program_versions and is read with ProgramVersion — the two are the same
// number until a save leaves a draft ahead of the approval.
type Program struct {
	ID              int64
	UserID          int64
	Name            string
	JSON            string
	Version         int
	ApprovedVersion int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

const userColumns = `id, google_sub, email, display_name, created_at`

// UpsertUser creates the account on first login and refreshes the profile
// fields on every later one; google_sub is the stable identity, email is not.
func (d *DB) UpsertUser(ctx context.Context, googleSub, email, displayName string) (User, error) {
	row := d.QueryRowContext(ctx, `
		INSERT INTO users (google_sub, email, display_name) VALUES (?, ?, ?)
		ON CONFLICT (google_sub) DO UPDATE SET email = excluded.email, display_name = excluded.display_name
		RETURNING `+userColumns, googleSub, email, displayName)
	return scanUser(row)
}

// UserByID returns the account, or sql.ErrNoRows.
func (d *DB) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(d.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// CreateSession stores a session keyed on the SHA-256 of the cookie token. The
// raw token is never persisted.
func (d *DB) CreateSession(ctx context.Context, tokenHash string, userID int64, expiresAt time.Time) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, userID, expiresAt.Unix())
	if err != nil {
		return fmt.Errorf("db: create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token hash to its user, treating an expired
// session as absent (sql.ErrNoRows).
func (d *DB) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	return scanUser(d.QueryRowContext(ctx, `
		SELECT u.id, u.google_sub, u.email, u.display_name, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > unixepoch()`, tokenHash))
}

// RefreshSession slides a live session's expiry to now+ttl, but only once it
// is past its half life, so an active browser stays logged in without one
// write per request. It reports whether the row was actually extended.
func (d *DB) RefreshSession(ctx context.Context, tokenHash string, ttl time.Duration) (bool, error) {
	secs := int64(ttl.Seconds())
	res, err := d.ExecContext(ctx, `
		UPDATE sessions SET expires_at = unixepoch() + ?
		WHERE token_hash = ? AND expires_at > unixepoch() AND expires_at < unixepoch() + ?`,
		secs, tokenHash, secs/2)
	if err != nil {
		return false, fmt.Errorf("db: refresh session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("db: refresh session: %w", err)
	}
	return n > 0, nil
}

// DeleteSession logs a session out. Deleting an unknown session is not an error.
func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("db: delete session: %w", err)
	}
	return nil
}

const programColumns = `id, user_id, name, json, version, approved_version, created_at, updated_at`

// CreateProgram saves a new program as its v1, approved. A duplicate name for
// the same user violates a unique index and returns an error.
func (d *DB) CreateProgram(ctx context.Context, userID int64, name, programJSON string) (Program, error) {
	return d.writeVersion(ctx, func(tx *sql.Tx) (Program, error) {
		return scanProgram(tx.QueryRowContext(ctx,
			`INSERT INTO programs (user_id, name, json) VALUES (?, ?, ?) RETURNING `+programColumns,
			userID, name, programJSON))
	})
}

// ProgramByID returns one of the user's own programs, or sql.ErrNoRows.
func (d *DB) ProgramByID(ctx context.Context, userID, id int64) (Program, error) {
	return scanProgram(d.QueryRowContext(ctx,
		`SELECT `+programColumns+` FROM programs WHERE user_id = ? AND id = ?`, userID, id))
}

// ListPrograms returns the user's library, newest first.
func (d *DB) ListPrograms(ctx context.Context, userID int64) ([]Program, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+programColumns+` FROM programs WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list programs: %w", err)
	}
	defer rows.Close()

	var out []Program
	for rows.Next() {
		p, err := scanProgram(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list programs: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list programs: %w", err)
	}
	return out, nil
}

// UpdateProgram saves a new version of one of the user's own programs and
// returns the row as it now stands, or sql.ErrNoRows if the program does not
// exist or belongs to someone else.
//
// The head always moves; approve says whether the approval moves with it. A
// save that does not approve is the design's draft: the editor shows "v8 ·
// DRAFT" over an approved v7, and robots keep being handed v7 until somebody
// presses APPROVE.
func (d *DB) UpdateProgram(ctx context.Context, userID, id int64, name, programJSON string, approve bool) (Program, error) {
	return d.writeVersion(ctx, func(tx *sql.Tx) (Program, error) {
		return scanProgram(tx.QueryRowContext(ctx, `
			UPDATE programs SET
				name = ?, json = ?, version = version + 1, updated_at = unixepoch(),
				approved_version = CASE WHEN ? THEN version + 1 ELSE approved_version END
			WHERE user_id = ? AND id = ?
			RETURNING `+programColumns, name, programJSON, approve, userID, id))
	})
}

// writeVersion runs a write that moves a program's head and files the resulting
// body in program_versions, in one transaction: a head with no body behind it
// would be a program that reads back and cannot be installed.
func (d *DB) writeVersion(ctx context.Context, write func(*sql.Tx) (Program, error)) (Program, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return Program{}, fmt.Errorf("db: save program: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	p, err := write(tx)
	if err != nil {
		return Program{}, err // sql.ErrNoRows and the unique-name violation, untouched
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO program_versions (program_id, version, json) VALUES (?, ?, ?)`,
		p.ID, p.Version, p.JSON); err != nil {
		return Program{}, fmt.Errorf("db: save program version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Program{}, fmt.Errorf("db: save program: %w", err)
	}
	return p, nil
}

// ApproveProgram marks version as the one robots are given, and returns the row
// as it now stands. sql.ErrNoRows covers all three ways this can miss: no such
// program, somebody else's program, or a version that was never saved.
func (d *DB) ApproveProgram(ctx context.Context, userID, id int64, version int) (Program, error) {
	return scanProgram(d.QueryRowContext(ctx, `
		UPDATE programs SET approved_version = ?
		WHERE user_id = ? AND id = ?
		  AND EXISTS (SELECT 1 FROM program_versions WHERE program_id = ? AND version = ?)
		RETURNING `+programColumns, version, userID, id, id, version))
}

// ProgramVersion returns one stored version's body. Scoped through the program
// row, so another user's version is sql.ErrNoRows like everything else.
func (d *DB) ProgramVersion(ctx context.Context, userID, id int64, version int) (string, error) {
	var body string
	err := d.QueryRowContext(ctx, `
		SELECT v.json FROM program_versions v JOIN programs p ON p.id = v.program_id
		WHERE p.user_id = ? AND p.id = ? AND v.version = ?`, userID, id, version).Scan(&body)
	return body, err
}

// ProgramVersionInfo is one row of the editor's VERSIONS panel. The body is not
// in it: the panel lists what exists, and opening one is a separate ask.
type ProgramVersionInfo struct {
	Version   int
	CreatedAt time.Time
}

// ListProgramVersions returns a program's versions, newest first.
func (d *DB) ListProgramVersions(ctx context.Context, userID, id int64) ([]ProgramVersionInfo, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT v.version, v.created_at FROM program_versions v JOIN programs p ON p.id = v.program_id
		WHERE p.user_id = ? AND p.id = ? ORDER BY v.version DESC`, userID, id)
	if err != nil {
		return nil, fmt.Errorf("db: list program versions: %w", err)
	}
	defer rows.Close()

	var out []ProgramVersionInfo
	for rows.Next() {
		var (
			v       ProgramVersionInfo
			created int64
		)
		if err := rows.Scan(&v.Version, &created); err != nil {
			return nil, fmt.Errorf("db: list program versions: %w", err)
		}
		v.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list program versions: %w", err)
	}
	return out, nil
}

// DeleteProgram removes one of the user's own programs, or returns
// sql.ErrNoRows if there was nothing of theirs to delete.
func (d *DB) DeleteProgram(ctx context.Context, userID, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM programs WHERE user_id = ? AND id = ?`, userID, id)
	if err != nil {
		return fmt.Errorf("db: delete program: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: delete program: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// scannable is satisfied by both *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}

func scanUser(s scannable) (User, error) {
	var (
		u       User
		created int64
	)
	if err := s.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.DisplayName, &created); err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return u, nil
}

func scanProgram(s scannable) (Program, error) {
	var (
		prog             Program
		created, updated int64
	)
	if err := s.Scan(&prog.ID, &prog.UserID, &prog.Name, &prog.JSON,
		&prog.Version, &prog.ApprovedVersion, &created, &updated); err != nil {
		return Program{}, err
	}
	prog.CreatedAt = time.Unix(created, 0).UTC()
	prog.UpdatedAt = time.Unix(updated, 0).UTC()
	return prog, nil
}
