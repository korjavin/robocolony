package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Blueprint queries. Like every other user-owned row (see queries.go) a
// blueprint is looked up by (user_id, id), so the ownership check lives in the
// WHERE clause and a wrong id reads as sql.ErrNoRows.

// Blueprint is a saved physical configuration from a player's library
// (design §5.1). JSON is the component list; the server validates it against
// design §6.3 before it gets here.
type Blueprint struct {
	ID               int64
	UserID           int64
	Name             string
	JSON             string
	DefaultProgramID sql.NullInt64
}

const blueprintColumns = `id, user_id, name, json, default_program_id`

// CreateBlueprint saves a new blueprint. A duplicate name for the same user
// violates a unique index; IsDuplicateName reports that case.
func (d *DB) CreateBlueprint(ctx context.Context, userID int64, name, blueprintJSON string) (Blueprint, error) {
	return scanBlueprint(d.QueryRowContext(ctx,
		`INSERT INTO blueprints (user_id, name, json) VALUES (?, ?, ?) RETURNING `+blueprintColumns,
		userID, name, blueprintJSON))
}

// BlueprintByID returns one of the user's own blueprints, or sql.ErrNoRows.
func (d *DB) BlueprintByID(ctx context.Context, userID, id int64) (Blueprint, error) {
	return scanBlueprint(d.QueryRowContext(ctx,
		`SELECT `+blueprintColumns+` FROM blueprints WHERE user_id = ? AND id = ?`, userID, id))
}

// ListBlueprints returns the user's blueprints, oldest first: the starter kit
// should stay at the top of the picker.
func (d *DB) ListBlueprints(ctx context.Context, userID int64) ([]Blueprint, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+blueprintColumns+` FROM blueprints WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list blueprints: %w", err)
	}
	defer rows.Close()

	var out []Blueprint
	for rows.Next() {
		b, err := scanBlueprint(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list blueprints: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list blueprints: %w", err)
	}
	return out, nil
}

// IsDuplicateName reports whether err is a unique-index violation, which for
// programs and blueprints alike can only be the (user_id, name) index.
//
// ponytail: string match, because modernc.org/sqlite does not export a typed
// constraint error. If it ever does, switch to errors.As here and nowhere else.
func IsDuplicateName(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func scanBlueprint(s scannable) (Blueprint, error) {
	var b Blueprint
	if err := s.Scan(&b.ID, &b.UserID, &b.Name, &b.JSON, &b.DefaultProgramID); err != nil {
		return Blueprint{}, err
	}
	return b, nil
}
