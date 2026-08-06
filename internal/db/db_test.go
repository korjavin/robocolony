package db

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/korjavin/robocolony/sql/migrations"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrationsApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("first Open() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// Re-opening must be a no-op, not a re-application.
	second, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("second Open() = %v", err)
	}
	defer second.Close()

	for _, table := range []string{"users", "sessions", "programs", "blueprints", "lobbies"} {
		var n int
		if err := second.QueryRowContext(t.Context(),
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil {
			t.Fatalf("lookup %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s: found %d, want 1", table, n)
		}
	}

	var mode string
	if err := second.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenUnwritableDirFailsLoudly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := Open(t.Context(), filepath.Join(dir, "sub", "test.db")); err == nil {
		t.Fatal("Open() on an unwritable directory succeeded, want an error")
	}
}

func TestUserRoundTrip(t *testing.T) {
	d := openTest(t)
	ctx := t.Context()

	created, err := d.UpsertUser(ctx, "sub-1", "a@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	if created.ID == 0 || created.CreatedAt.IsZero() {
		t.Fatalf("UpsertUser() = %+v, want an id and a timestamp", created)
	}

	got, err := d.UserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("UserByID() = %v", err)
	}
	if got != created {
		t.Errorf("UserByID() = %+v, want %+v", got, created)
	}

	// A second login with the same subject updates the profile in place.
	again, err := d.UpsertUser(ctx, "sub-1", "new@example.com", "Ada L")
	if err != nil {
		t.Fatalf("second UpsertUser() = %v", err)
	}
	if again.ID != created.ID {
		t.Errorf("second UpsertUser() id = %d, want %d", again.ID, created.ID)
	}
	if again.Email != "new@example.com" || again.DisplayName != "Ada L" {
		t.Errorf("second UpsertUser() = %+v, want the refreshed profile", again)
	}

	if _, err := d.UserByID(ctx, 404); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("UserByID(missing) = %v, want sql.ErrNoRows", err)
	}
}

func TestSessions(t *testing.T) {
	d := openTest(t)
	ctx := t.Context()

	user, err := d.UpsertUser(ctx, "sub-1", "a@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}

	if err := d.CreateSession(ctx, "hash-live", user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession() = %v", err)
	}
	got, err := d.SessionUser(ctx, "hash-live")
	if err != nil {
		t.Fatalf("SessionUser() = %v", err)
	}
	if got != user {
		t.Errorf("SessionUser() = %+v, want %+v", got, user)
	}

	if err := d.CreateSession(ctx, "hash-stale", user.ID, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("CreateSession(expired) = %v", err)
	}
	if _, err := d.SessionUser(ctx, "hash-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SessionUser(expired) = %v, want sql.ErrNoRows", err)
	}

	if err := d.DeleteSession(ctx, "hash-live"); err != nil {
		t.Fatalf("DeleteSession() = %v", err)
	}
	if _, err := d.SessionUser(ctx, "hash-live"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SessionUser(deleted) = %v, want sql.ErrNoRows", err)
	}

	// Sessions must not outlive their user.
	if _, err := d.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, user.ID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := d.SessionUser(ctx, "hash-stale"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("session survived its user: %v", err)
	}
}

func TestProgramRoundTrip(t *testing.T) {
	d := openTest(t)
	ctx := t.Context()

	owner, err := d.UpsertUser(ctx, "sub-1", "a@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	other, err := d.UpsertUser(ctx, "sub-2", "b@example.com", "Bob")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}

	const rules = `{"rules":[{"when":"carrying_nothing","then":"explore"}]}`
	p, err := d.CreateProgram(ctx, owner.ID, "scavenger", rules)
	if err != nil {
		t.Fatalf("CreateProgram() = %v", err)
	}
	if p.JSON != rules || p.Name != "scavenger" || p.UserID != owner.ID {
		t.Fatalf("CreateProgram() = %+v, want the values written", p)
	}

	got, err := d.ProgramByID(ctx, owner.ID, p.ID)
	if err != nil {
		t.Fatalf("ProgramByID() = %v", err)
	}
	if got != p {
		t.Errorf("ProgramByID() = %+v, want %+v", got, p)
	}

	list, err := d.ListPrograms(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(list) != 1 || list[0].ID != p.ID {
		t.Errorf("ListPrograms() = %+v, want just %d", list, p.ID)
	}

	updated, err := d.UpdateProgram(ctx, owner.ID, p.ID, "scout", `{"rules":[]}`)
	if err != nil {
		t.Fatalf("UpdateProgram() = %v", err)
	}
	if updated.Name != "scout" || updated.JSON != `{"rules":[]}` {
		t.Errorf("UpdateProgram() = %+v, want the new values", updated)
	}

	// Duplicate names within one library are rejected.
	if _, err := d.CreateProgram(ctx, owner.ID, "scout", rules); err == nil {
		t.Error("CreateProgram(duplicate name) succeeded, want an error")
	}
	// ...but two users may each have a "scout".
	if _, err := d.CreateProgram(ctx, other.ID, "scout", rules); err != nil {
		t.Errorf("CreateProgram(same name, other user) = %v", err)
	}

	// Ownership is enforced in the WHERE clause, not by the caller.
	if _, err := d.ProgramByID(ctx, other.ID, p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ProgramByID(wrong user) = %v, want sql.ErrNoRows", err)
	}
	if _, err := d.UpdateProgram(ctx, other.ID, p.ID, "stolen", rules); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("UpdateProgram(wrong user) = %v, want sql.ErrNoRows", err)
	}
	if err := d.DeleteProgram(ctx, other.ID, p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteProgram(wrong user) = %v, want sql.ErrNoRows", err)
	}

	if err := d.DeleteProgram(ctx, owner.ID, p.ID); err != nil {
		t.Fatalf("DeleteProgram() = %v", err)
	}
	if _, err := d.ProgramByID(ctx, owner.ID, p.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ProgramByID(deleted) = %v, want sql.ErrNoRows", err)
	}
}

// Library ids are re-sent by the lobby picker to keep an approval alive, so an
// id SQLite hands to a second row would swap a player's approved design for its
// replacement. 005 rebuilt both tables with AUTOINCREMENT to make that
// impossible.
func TestLibraryIDsAreNeverReused(t *testing.T) {
	d := openTest(t)
	ctx := t.Context()

	owner, err := d.UpsertUser(ctx, "sub-1", "a@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}

	p, err := d.CreateProgram(ctx, owner.ID, "gone", "{}")
	if err != nil {
		t.Fatalf("CreateProgram() = %v", err)
	}
	b, err := d.CreateBlueprint(ctx, owner.ID, "gone", "{}")
	if err != nil {
		t.Fatalf("CreateBlueprint() = %v", err)
	}

	// Both are the highest id in their table, which is exactly the rowid a
	// non-AUTOINCREMENT table would recycle.
	if err := d.DeleteProgram(ctx, owner.ID, p.ID); err != nil {
		t.Fatalf("DeleteProgram() = %v", err)
	}
	if err := d.DeleteBlueprint(ctx, owner.ID, b.ID); err != nil {
		t.Fatalf("DeleteBlueprint() = %v", err)
	}

	next, err := d.CreateProgram(ctx, owner.ID, "replacement", "{}")
	if err != nil {
		t.Fatalf("CreateProgram(replacement) = %v", err)
	}
	if next.ID <= p.ID {
		t.Errorf("replacement program id = %d, want above the deleted %d", next.ID, p.ID)
	}
	nextBP, err := d.CreateBlueprint(ctx, owner.ID, "replacement", "{}")
	if err != nil {
		t.Fatalf("CreateBlueprint(replacement) = %v", err)
	}
	if nextBP.ID <= b.ID {
		t.Errorf("replacement blueprint id = %d, want above the deleted %d", nextBP.ID, b.ID)
	}
}

// The 005 rebuild drops and recreates both library tables, so it has to carry
// every id and the blueprints -> programs reference across unchanged.
func TestMigration005PreservesLibraryRows(t *testing.T) {
	ctx := t.Context()
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() = %v", err)
	}
	defer sqlDB.Close()

	p, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}
	if _, err := p.UpTo(ctx, 4); err != nil {
		t.Fatalf("UpTo(4) = %v", err)
	}

	// Ids well above 1 so a rebuild that renumbered would show.
	for _, stmt := range []string{
		`INSERT INTO users (id, google_sub, email, display_name) VALUES (3, 'sub-1', 'a@example.com', 'Ada')`,
		`INSERT INTO programs (id, user_id, name, json) VALUES (7, 3, 'scavenger', '{}')`,
		`INSERT INTO blueprints (id, user_id, name, json, default_program_id) VALUES (9, 3, 'hauler', '{}', 7)`,
		`INSERT INTO lobbies (id, owner_id, name, settings_json, state) VALUES (1, 3, 'l', '{}', 'open')`,
		// An approval whose library rows were deleted before the migration:
		// nothing left in the tables carries ids 50 and 51.
		`INSERT INTO lobby_members (lobby_id, user_id, loadout_json) VALUES (1, 3,
		   '{"entries":[{"blueprint_id":50,"blueprint_name":"gone","program_id":51,"program_name":"gone"}]}')`,
	} {
		if _, err := sqlDB.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("Up() = %v", err)
	}

	var progID, bpID, defaultProgID int64
	if err := sqlDB.QueryRowContext(ctx, `SELECT id FROM programs`).Scan(&progID); err != nil {
		t.Fatalf("read program: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT id, default_program_id FROM blueprints`).Scan(&bpID, &defaultProgID); err != nil {
		t.Fatalf("read blueprint: %v", err)
	}
	if progID != 7 || bpID != 9 || defaultProgID != 7 {
		t.Errorf("after 005: program %d, blueprint %d, default_program_id %d; want 7, 9, 7", progID, bpID, defaultProgID)
	}

	// The unique index has to come across too, or duplicate names get in.
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO blueprints (user_id, name, json) VALUES (3, 'hauler', '{}')`); err == nil {
		t.Error("duplicate blueprint name accepted after 005, want the unique index to reject it")
	}

	// And the migrated table must not recycle the id it just carried over, nor
	// the higher ids a surviving approval still points at (50 and 51), whose
	// rows were deleted before the rebuild could see them.
	if _, err := sqlDB.ExecContext(ctx, `DELETE FROM blueprints WHERE id = 9`); err != nil {
		t.Fatalf("delete blueprint: %v", err)
	}
	var rebornBP, rebornProg int64
	if err := sqlDB.QueryRowContext(ctx,
		`INSERT INTO blueprints (user_id, name, json) VALUES (3, 'replacement', '{}') RETURNING id`).Scan(&rebornBP); err != nil {
		t.Fatalf("insert replacement blueprint: %v", err)
	}
	if err := sqlDB.QueryRowContext(ctx,
		`INSERT INTO programs (user_id, name, json) VALUES (3, 'replacement', '{}') RETURNING id`).Scan(&rebornProg); err != nil {
		t.Fatalf("insert replacement program: %v", err)
	}
	if rebornBP <= 50 {
		t.Errorf("replacement blueprint id = %d, want above the approved-but-deleted 50", rebornBP)
	}
	if rebornProg <= 51 {
		t.Errorf("replacement program id = %d, want above the approved-but-deleted 51", rebornProg)
	}

	// The rollback rebuilds the same two tables, so it has to run and keep the
	// rows as well.
	if _, err := p.Down(ctx); err != nil {
		t.Fatalf("Down() = %v", err)
	}
	var rolledBack int64
	if err := sqlDB.QueryRowContext(ctx, `SELECT id FROM blueprints WHERE name = 'replacement'`).Scan(&rolledBack); err != nil {
		t.Fatalf("read blueprint after Down: %v", err)
	}
	if rolledBack != rebornBP {
		t.Errorf("after Down blueprint id = %d, want %d", rolledBack, rebornBP)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	d := openTest(t)
	if _, err := d.CreateProgram(t.Context(), 999, "orphan", "{}"); err == nil {
		t.Error("CreateProgram() for a missing user succeeded, want a foreign key violation")
	}
}

// TestMigrationVersionsUniqueAndContiguous catches the collision two branches
// can each pass on their own: both add NNN_*.sql with the same NNN, the
// filenames differ so git merges them without a conflict, and goose refuses the
// provider only once both are on master — breaking every test that opens a
// database, on whatever unrelated PR runs next. Reading the embedded names
// directly names the offending pair instead.
func TestMigrationVersionsUniqueAndContiguous(t *testing.T) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("Glob() = %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations embedded, want at least 001_init.sql")
	}

	seen := make(map[int]string, len(names))
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Errorf("migration %q: want NNN_name.sql", name)
			continue
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			t.Errorf("migration %q: version prefix %q is not a number", name, prefix)
			continue
		}
		if prev, dup := seen[version]; dup {
			t.Errorf("migrations %q and %q share version %d; renumber against origin/master before merging", prev, name, version)
			continue
		}
		seen[version] = name
	}

	for version := 1; version <= len(seen); version++ {
		if seen[version] == "" {
			t.Errorf("no migration with version %d; numbers must be contiguous from 1", version)
		}
	}
}
