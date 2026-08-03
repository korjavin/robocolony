package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestForeignKeysEnforced(t *testing.T) {
	d := openTest(t)
	if _, err := d.CreateProgram(t.Context(), 999, "orphan", "{}"); err == nil {
		t.Error("CreateProgram() for a missing user succeeded, want a foreign key violation")
	}
}
