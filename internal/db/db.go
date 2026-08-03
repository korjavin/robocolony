// Package db owns the SQLite connection and the embedded schema migrations.
// It exposes hand-written queries as methods on *DB; there is no code
// generation step.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go driver: the build must stay CGO_ENABLED=0

	"github.com/korjavin/robocolony/sql/migrations"
)

// DB is the application's handle on SQLite. It embeds *sql.DB so the stdlib
// API stays available next to the query methods.
type DB struct{ *sql.DB }

// Open opens — creating it if absent — the SQLite database at path and applies
// every pending migration. Calling it against an already-migrated database is a
// no-op, so startup is idempotent across redeploys.
//
// It fails rather than degrades: a database that cannot be created or written
// is returned as an error here, not as a nil handle that explodes at the first
// query.
func Open(ctx context.Context, path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("db: create directory %s: %w (DB_PATH must be inside a directory writable by uid %d)", dir, err, os.Getuid())
	}

	// WAL lets readers run while a write is in flight; busy_timeout absorbs the
	// contention that remains, since SQLite still allows only one writer.
	// foreign_keys defaults to off in SQLite and is per-connection.
	// ponytail: default pool size, tune only if SQLITE_BUSY shows up in logs.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	// sql.Open is lazy, so ping to surface an unwritable path at startup.
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: open %s: %w (the file and its directory must be writable by uid %d)", path, err, os.Getuid())
	}

	version, err := migrate(ctx, sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	slog.Info("database ready", "path", path, "schema_version", version)
	return &DB{sqlDB}, nil
}

// migrate applies the embedded migrations and reports the resulting version.
func migrate(ctx context.Context, sqlDB *sql.DB) (int64, error) {
	// The Provider API keeps migration state on this instance instead of in
	// goose's package-level globals, so tests can migrate in parallel.
	p, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, migrations.FS)
	if err != nil {
		return 0, fmt.Errorf("db: load migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return 0, fmt.Errorf("db: apply migrations: %w", err)
	}
	version, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	return version, nil
}
