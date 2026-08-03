package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

func newService(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	svc := New(database)
	// Registered after the close above, so it runs before it: a tick driver
	// must never outlive the database it settles its lobby row in.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})
	return svc, database
}

func newUser(t *testing.T, database *db.DB, name string) db.User {
	t.Helper()
	u, err := database.UpsertUser(t.Context(), "sub-"+name, name+"@example.com", name)
	if err != nil {
		t.Fatalf("UpsertUser(%s) = %v", name, err)
	}
	return u
}

// wantStatus asserts an error carries a particular HTTP status.
func wantStatus(t *testing.T, err error, code int) {
	t.Helper()
	var se statusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a status error, want %d", err, code)
	}
	if se.code != code {
		t.Fatalf("error %q has status %d, want %d", se.msg, se.code, code)
	}
}

// TestLobbyLifecycle is the bead's headline case: create, join, start, and the
// two refusals that keep a match honest.
func TestLobbyLifecycle(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")
	guest := newUser(t, database, "bob")

	created, err := svc.Create(ctx, owner.ID, "first match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if len(created.Members) != 1 || created.Members[0].UserID != owner.ID {
		t.Fatalf("Create() members = %+v, want just the owner", created.Members)
	}
	if created.Settings.Seed == 0 {
		t.Error("Create() drew no seed")
	}

	open, err := svc.List(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("List() = %d lobbies, %v, want 1 and no error", len(open), err)
	}

	joined, err := svc.Join(ctx, created.ID, guest.ID)
	if err != nil {
		t.Fatalf("Join() = %v", err)
	}
	if len(joined.Members) != 2 {
		t.Fatalf("Join() members = %+v, want two", joined.Members)
	}
	// Joining twice is idempotent, not an error: a double-clicked button must
	// not look like a failure.
	if _, err := svc.Join(ctx, created.ID, guest.ID); err != nil {
		t.Fatalf("second Join() = %v", err)
	}

	// Only the owner starts.
	_, err = svc.Start(ctx, created.ID, guest.ID)
	wantStatus(t, err, http.StatusForbidden)

	info, err := svc.Start(ctx, created.ID, owner.ID)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if len(info.Colonies) != 2 {
		t.Fatalf("Start() colonies = %+v, want one per member", info.Colonies)
	}
	if info.Colonies[0].UserID != owner.ID || info.Colonies[1].UserID != guest.ID {
		t.Errorf("colonies %+v are not in join order", info.Colonies)
	}
	if info.Seed != created.Settings.Seed {
		t.Errorf("match seed = %d, want the lobby's %d", info.Seed, created.Settings.Seed)
	}

	// Design §2.1: no one joins after the start, and nobody starts twice.
	third := newUser(t, database, "cy")
	_, err = svc.Join(ctx, created.ID, third.ID)
	wantStatus(t, err, http.StatusConflict)
	_, err = svc.Start(ctx, created.ID, owner.ID)
	wantStatus(t, err, http.StatusConflict)

	// A started lobby is no longer open, and its match is reachable.
	if open, err = svc.List(ctx); err != nil || len(open) != 0 {
		t.Fatalf("List() after start = %d lobbies, %v, want none", len(open), err)
	}
	if _, err := svc.Match(ctx, created.ID); err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if _, err := svc.Match(ctx, created.ID+999); err == nil {
		t.Error("Match(unknown) succeeded, want an error")
	}
}

func TestJoinAndLeave(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")
	guest := newUser(t, database, "bob")
	crowd := newUser(t, database, "cy")

	set := DefaultSettings()
	set.MaxPlayers = 2
	created, err := svc.Create(ctx, owner.ID, "small", set)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Join(ctx, created.ID, guest.ID); err != nil {
		t.Fatalf("Join() = %v", err)
	}
	// Full: max_players is enforced by the server, not by the form.
	_, err = svc.Join(ctx, created.ID, crowd.ID)
	wantStatus(t, err, http.StatusConflict)

	// The owner may not walk out of their own lobby.
	wantStatus(t, svc.Leave(ctx, created.ID, owner.ID), http.StatusConflict)

	if err := svc.Leave(ctx, created.ID, guest.ID); err != nil {
		t.Fatalf("Leave() = %v", err)
	}
	// Leaving twice is a conflict, not a silent success.
	wantStatus(t, svc.Leave(ctx, created.ID, guest.ID), http.StatusConflict)

	if _, err := svc.Join(ctx, created.ID, crowd.ID); err != nil {
		t.Fatalf("Join() after a seat freed = %v", err)
	}
}

func TestSettingsValidation(t *testing.T) {
	bad := func(mutate func(*Settings)) Settings {
		s := DefaultSettings()
		mutate(&s)
		return s
	}
	tests := []struct {
		name string
		set  Settings
		ok   bool
	}{
		{"defaults", DefaultSettings(), true},
		{"duration too short", bad(func(s *Settings) { s.DurationSec = 30 }), false},
		{"duration too long", bad(func(s *Settings) { s.DurationSec = 86400 }), false},
		{"duration zero", bad(func(s *Settings) { s.DurationSec = 0 }), false},
		{"richness too low", bad(func(s *Settings) { s.Richness = 0 }), false},
		{"richness too high", bad(func(s *Settings) { s.Richness = 0.9 }), false},
		{"richness at the edge", bad(func(s *Settings) { s.Richness = maxRichness }), true},
		{"spawn rate negative", bad(func(s *Settings) { s.SpawnPerMin = -1 }), false},
		{"spawn rate off", bad(func(s *Settings) { s.SpawnPerMin = 0 }), true},
		{"too many players", bad(func(s *Settings) { s.MaxPlayers = 99 }), false},
		{"no players", bad(func(s *Settings) { s.MaxPlayers = 0 }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.set.Validate(); (err == nil) != tt.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tt.ok)
			}
		})
	}

	// And the same rules on the way in: a lobby cannot be created around
	// settings the client made up.
	svc, database := newService(t)
	owner := newUser(t, database, "ada")
	_, err := svc.Create(t.Context(), owner.ID, "bad", bad(func(s *Settings) { s.DurationSec = 1 }))
	wantStatus(t, err, http.StatusBadRequest)
	_, err = svc.Create(t.Context(), owner.ID, "", DefaultSettings())
	wantStatus(t, err, http.StatusBadRequest)
}

// TestSeedIsServerChosen: the seed is exposed for reproducibility, never
// accepted.
func TestSeedIsServerChosen(t *testing.T) {
	svc, database := newService(t)
	owner := newUser(t, database, "ada")

	set := DefaultSettings()
	set.Seed = 1234
	created, err := svc.Create(t.Context(), owner.ID, "seeded", set)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if created.Settings.Seed == 1234 {
		t.Fatal("Create() honoured the client's seed")
	}

	var stored Settings
	row, err := database.LobbyByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("LobbyByID() = %v", err)
	}
	if err := json.Unmarshal([]byte(row.SettingsJSON), &stored); err != nil {
		t.Fatalf("stored settings: %v", err)
	}
	if stored.Seed != created.Settings.Seed {
		t.Errorf("stored seed = %d, want the returned %d", stored.Seed, created.Settings.Seed)
	}
}

// TestDefaultKitIsPlayable: the built-in blueprint every POC player starts
// with must be legal, and the §10.7 scavenger must be clean on it — including
// the parts radar, without which move_to_radar_target is an error and the
// program is a blind random walk.
func TestDefaultKitIsPlayable(t *testing.T) {
	bp := DefaultBlueprint()
	if err := bp.Validate(); err != nil {
		t.Fatalf("DefaultBlueprint().Validate() = %v", err)
	}
	if !bp.Has(sim.KindRadar) {
		t.Error("the default blueprint has no radar")
	}
	res := prog.Validate(DefaultProgram(), bp)
	if !res.OK() {
		t.Fatalf("DefaultProgram() on the default blueprint: %+v", res.Errors)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("DefaultProgram() warnings = %+v, want none", res.Warnings)
	}
}

func testMatch(t *testing.T, s Settings, members int) *Match {
	t.Helper()
	seats := make([]db.Member, members)
	for i := range seats {
		seats[i] = db.Member{UserID: int64(i + 1), DisplayName: "player"}
	}
	m, err := newMatch(db.Lobby{ID: 1, Name: "test"}, s, seats)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	return m
}

func shortSettings(duration int) Settings {
	s := DefaultSettings()
	s.DurationSec = duration
	s.Seed = 42
	return s
}

// TestMatchRuns proves the wiring E3 needs: the program runtime is installed,
// so the starting robots actually leave their base instead of idling.
func TestMatchRuns(t *testing.T) {
	m := testMatch(t, shortSettings(60), 2)

	// Keyed by robot id, not by slice index: a base that finishes a build in
	// these 100 ticks appends a robot the starting positions know nothing
	// about, and an index-based comparison then reads off the end.
	basis := map[int]sim.Coord{}
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if len(w.Robots) != 2*startingRobots {
			t.Fatalf("match started with %d robots, want %d", len(w.Robots), 2*startingRobots)
		}
		for _, r := range w.Robots {
			basis[r.ID] = r.Coord
		}
	})

	for i := 0; i < 100; i++ {
		m.step()
	}

	moved := 0
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, r := range w.Robots {
			if start, ok := basis[r.ID]; ok && r.Coord != start {
				moved++
			}
		}
	})
	if moved == 0 {
		t.Fatal("no robot moved in 100 ticks: the program runtime is not driving the world")
	}
}

// TestMatchTicks: a started match advances over wall time on its own goroutine,
// and stops when the registry shuts down.
func TestMatchTicks(t *testing.T) {
	reg := NewRegistry()
	m := testMatch(t, shortSettings(60), 1)
	if err := reg.Start(m, nil); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := reg.Start(m, nil); err == nil {
		t.Error("starting the same match twice succeeded, want an error")
	}
	if got, ok := reg.Get(m.ID); !ok || got != m {
		t.Fatal("Get() did not return the registered match")
	}

	deadline := time.Now().Add(3 * time.Second)
	for m.Info().Tick < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := m.Info().Tick; got < 3 {
		t.Fatalf("match reached tick %d in %v, want at least 3 at %d ticks/s", got, 3*time.Second, TickRate)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reg.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	stopped := m.Info().Tick
	time.Sleep(3 * tickInterval)
	if now := m.Info().Tick; now != stopped {
		t.Errorf("match advanced from tick %d to %d after shutdown", stopped, now)
	}
	// Shutdown is idempotent, and a match may not be started into a registry
	// that is draining.
	if err := reg.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown() = %v", err)
	}
	if err := reg.Start(testMatch(t, shortSettings(60), 1), nil); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Start() after shutdown = %v, want ErrShuttingDown", err)
	}
}

// TestMatchEndsAtDuration drives the whole loop: the driver notices the
// duration, calls back, and the lobby row is settled — with no HTTP request
// anywhere near it (design §2.2).
func TestMatchEndsAtDuration(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")

	// Straight into the database: Create would reject a match this short, and
	// waiting the legal minimum of 60s is not a test.
	set := DefaultSettings()
	set.DurationSec = 0
	set.Seed = 7
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	lobby, err := database.CreateLobby(ctx, owner.ID, "brief", string(encoded))
	if err != nil {
		t.Fatalf("CreateLobby() = %v", err)
	}
	if _, err := svc.Start(ctx, lobby.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := database.LobbyByID(ctx, lobby.ID)
		if err != nil {
			t.Fatalf("LobbyByID() = %v", err)
		}
		if row.State == db.LobbyFinished {
			m, _ := svc.reg.Get(lobby.ID)
			if !m.Finished() {
				t.Error("the lobby row is finished but the match is not")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the match never settled its lobby row")
}

// TestConcurrentReadsWhileTicking is the guard on this bead's real hazard: the
// tick driver writes the world ten times a second while handlers read it.
// Remove the mutex from Match and this test fails under -race.
func TestConcurrentReadsWhileTicking(t *testing.T) {
	reg := NewRegistry()
	m := testMatch(t, shortSettings(60), 2)
	if err := reg.Start(m, nil); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := reg.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})

	stop := time.Now().Add(300 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				info := m.Info()
				if info.State == "" || len(info.Colonies) != 2 {
					t.Errorf("Info() = %+v, want two colonies", info)
					return
				}
				m.Read(func(w *sim.World, rt *prog.Runtime) {
					// Exactly what E4.1's snapshot will do: touch live state
					// under the lock, and write nothing outside it.
					_ = w.StateHash()
					for _, r := range w.Robots {
						_, _ = rt.Trace(r.ID)
					}
				})
			}
		}()
	}
	wg.Wait()

	if m.Info().Tick == 0 {
		t.Fatal("the match never ticked while it was being read")
	}
}
