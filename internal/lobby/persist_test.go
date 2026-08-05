package lobby

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// seatedLobby creates a lobby with one member and flips it to running, which is
// the state Restore looks for.
func seatedLobby(t *testing.T, svc *Service, database *db.DB, set Settings) (db.Lobby, []db.Member) {
	t.Helper()
	owner := newUser(t, database, "ada")
	view, err := svc.Create(t.Context(), owner.ID, "persisted match", set)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if err := database.StartLobby(t.Context(), view.ID, owner.ID); err != nil {
		t.Fatalf("StartLobby() = %v", err)
	}
	lobby, err := database.LobbyByID(t.Context(), view.ID)
	if err != nil {
		t.Fatalf("LobbyByID() = %v", err)
	}
	members, err := database.LobbyMembers(t.Context(), view.ID)
	if err != nil {
		t.Fatalf("LobbyMembers() = %v", err)
	}
	return lobby, members
}

// recall and reprogram mirror what internal/server's robot commands do, through
// the same Apply that records them.
func recall(t *testing.T, m *Match, robotID int) {
	t.Helper()
	err := m.Apply(Command{Kind: CmdRecall, Robot: robotID}, func(w *sim.World, _ *prog.Runtime) error {
		w.RobotByID(robotID).Recalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Apply(recall) = %v", err)
	}
}

func reprogram(t *testing.T, m *Match, robotID int, id string, p prog.Program) {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal program = %v", err)
	}
	cmd := Command{Kind: CmdProgram, Robot: robotID, ProgramID: id, Program: raw}
	err = m.Apply(cmd, func(w *sim.World, rt *prog.Runtime) error {
		rt.Install(id, p)
		w.RobotByID(robotID).Reprogram(id)
		return nil
	})
	if err != nil {
		t.Fatalf("Apply(program) = %v", err)
	}
}

// liveMatch runs a match forward with a couple of player commands mixed in, and
// saves its replay record. It returns the match, so a test can compare what was
// restored against what was actually running.
func liveMatch(t *testing.T, svc *Service, lobby db.Lobby, set Settings, members []db.Member) *Match {
	t.Helper()
	m, err := newMatch(lobby, set, members)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	step := func(n int) {
		t.Helper()
		for range n {
			if !m.step() {
				t.Fatalf("match ended early at tick %d", m.world.Tick)
			}
		}
	}
	// Commands at different ticks, and two at the same tick, so the replay has
	// to get both the schedule and the order right.
	step(37)
	first := m.world.Robots[0].ID
	recall(t, m, first)
	step(64)
	second := m.world.Robots[1].ID
	reprogram(t, m, second, "test-install", DefaultProgram())
	recall(t, m, second)
	step(112)

	svc.save(m)
	return m
}

// TestStoredSettingsWithoutArenaStay64: a lobby row written before the arena
// setting existed has no "arena" key, and Restore rebuilds its match from that
// JSON. If the missing key stopped meaning 64x64 every stored match would
// replay into a different world, and its recorded commands into a world where
// they never made sense.
func TestStoredSettingsWithoutArenaStay64(t *testing.T) {
	set, err := decodeSettings(`{"duration_sec":600,"richness":0.02,"spawn_per_min":6,"max_players":4,"seed":23}`)
	if err != nil {
		t.Fatalf("decodeSettings() = %v", err)
	}
	if set.Arena != "" {
		t.Fatalf("Arena = %q, want empty: the fixture has no arena key", set.Arena)
	}
	if opts := set.GenOpts(2); opts.Width != 64 || opts.Height != 64 {
		t.Fatalf("GenOpts() = %dx%d, want 64x64", opts.Width, opts.Height)
	}
}

// TestReplayPreservesStateHash is the acceptance test for this bead: a match
// run some ticks in, persisted, and rebuilt from what reached the disk is the
// same world, down to the state hash the E1.1 determinism guard uses.
//
// The AI case is the same assertion with computer colonies seated (design §12
// P2). It is worth its own run because an AI colony makes decisions all match
// and records not one command: the whole reason it is safe is that those
// decisions come from the same deterministic evaluator a player's robots use,
// and the profile list travels in the settings the replay re-reads. If AI
// behaviour ever stopped being a pure function of the seed, this is what would
// notice.
func TestReplayPreservesStateHash(t *testing.T) {
	withAI := shortSettings(600)
	withAI.AI = Profiles()
	withAI.MaxPlayers = maxPlayers - len(withAI.AI)
	for name, set := range map[string]Settings{"humans only": shortSettings(600), "with ai": withAI} {
		t.Run(name, func(t *testing.T) { testReplayPreservesStateHash(t, set) })
	}
}

func testReplayPreservesStateHash(t *testing.T, set Settings) {
	svc, database := newService(t)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	live := liveMatch(t, svc, lobby, set, members)

	rec, err := database.MatchLogByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v", err)
	}
	if rec.Tick != int64(live.world.Tick) {
		t.Fatalf("saved tick %d, match is at %d", rec.Tick, live.world.Tick)
	}

	restored, err := replay(lobby, set, members, rec)
	if err != nil {
		t.Fatalf("replay() = %v", err)
	}
	if got, want := restored.world.StateHash(), live.world.StateHash(); got != want {
		t.Fatalf("restored world hashes %#x, the live one %#x: the replay is not faithful", got, want)
	}
	if got, want := restored.world.Tick, live.world.Tick; got != want {
		t.Errorf("restored at tick %d, want %d", got, want)
	}
	if !restored.Started.Equal(live.Started.Truncate(time.Second)) {
		t.Errorf("restored start time %v, want %v", restored.Started, live.Started)
	}
	// The next tick must diverge no further than the tick before it: this is
	// what proves the rng and the id allocator came back too, neither of which
	// StateHash reads directly.
	restored.step()
	live.step()
	if got, want := restored.world.StateHash(), live.world.StateHash(); got != want {
		t.Fatalf("one tick after restore the worlds hash %#x and %#x: the rng state did not survive", got, want)
	}
}

// TestRestoreResumesRunningMatch is the same thing end to end: a second service
// over the same database brings the match back and starts ticking it again.
func TestRestoreResumesRunningMatch(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(600)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	live := liveMatch(t, svc, lobby, set, members)

	next := New(database) // the process after the deploy
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := next.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})
	if err := next.Restore(t.Context()); err != nil {
		t.Fatalf("Restore() = %v", err)
	}

	m, ok := next.reg.Get(lobby.ID)
	if !ok {
		t.Fatal("Restore() did not put the running match back in the registry")
	}
	if got := m.Info().Tick; got < live.world.Tick {
		t.Errorf("restored match is at tick %d, want at least %d", got, live.world.Tick)
	}
	if got := len(m.Colonies); got != len(members) {
		t.Errorf("restored match has %d colonies, want %d", got, len(members))
	}
	// Still running, so the lobby row must not have been reaped.
	after, err := database.LobbyByID(t.Context(), lobby.ID)
	if err != nil || after.State != db.LobbyRunning {
		t.Errorf("lobby state is %q (err %v), want running", after.State, err)
	}
}

// TestRestoreAbandonsUnusableRecords: a record the server cannot trust must
// finish that one match and let the process come up, never panic and never
// resurrect a match into a world that is not the one the players left.
func TestRestoreAbandonsUnusableRecords(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, rec *db.MatchLog)
	}{
		{"truncated command log", func(_ *testing.T, rec *db.MatchLog) {
			rec.Commands = `[{"tick":37,"kind":"rec`
		}},
		{"commands that are not commands", func(_ *testing.T, rec *db.MatchLog) {
			rec.Commands = `{"nope":true}`
		}},
		{"unknown command kind", func(_ *testing.T, rec *db.MatchLog) {
			rec.Commands = `[{"tick":1,"kind":"launch_nukes","robot":1}]`
		}},
		{"command for a robot that never existed", func(_ *testing.T, rec *db.MatchLog) {
			rec.Commands = `[{"tick":1,"kind":"recall","robot":999999}]`
		}},
		{"program that does not decode", func(_ *testing.T, rec *db.MatchLog) {
			rec.Commands = `[{"tick":1,"kind":"program","robot":1,"program_id":"x","program":{"v":0}}]`
		}},
		{"another build's fingerprint", func(_ *testing.T, rec *db.MatchLog) {
			rec.Fingerprint = "from-a-build-that-balanced-weapons-differently"
		}},
		{"tick past the match duration", func(_ *testing.T, rec *db.MatchLog) {
			rec.Tick = 1 << 40
		}},
		{"negative tick", func(_ *testing.T, rec *db.MatchLog) {
			rec.Tick = -1
		}},
		{"no record at all", func(t *testing.T, rec *db.MatchLog) {
			rec.LobbyID = 0 // signals "delete instead of saving"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, database := newService(t)
			set := shortSettings(600)
			lobby, members := seatedLobby(t, svc, database, set)
			set.Seed = mustSettings(t, lobby).Seed
			liveMatch(t, svc, lobby, set, members)

			rec, err := database.MatchLogByID(t.Context(), lobby.ID)
			if err != nil {
				t.Fatalf("MatchLogByID() = %v", err)
			}
			tc.break_(t, &rec)
			if rec.LobbyID == 0 {
				if err := database.DeleteMatchLog(t.Context(), lobby.ID); err != nil {
					t.Fatalf("DeleteMatchLog() = %v", err)
				}
			} else if err := database.SaveMatchLog(t.Context(), rec); err != nil {
				t.Fatalf("SaveMatchLog() = %v", err)
			}

			next := New(database)
			if err := next.Restore(t.Context()); err != nil {
				t.Fatalf("Restore() = %v, want a bad record to be survivable", err)
			}
			if _, ok := next.reg.Get(lobby.ID); ok {
				t.Error("Restore() resurrected a match from a record it could not trust")
			}
			after, err := database.LobbyByID(t.Context(), lobby.ID)
			if err != nil {
				t.Fatalf("LobbyByID() = %v", err)
			}
			if after.State != db.LobbyFinished {
				t.Errorf("lobby state is %q, want finished", after.State)
			}
			if _, err := database.MatchLogByID(t.Context(), lobby.ID); err == nil {
				t.Error("the unusable record is still on disk")
			}
		})
	}
}

// TestFinishedMatchDropsItsRecord: a match that reaches its duration must leave
// nothing behind for a restart to replay.
func TestFinishedMatchDropsItsRecord(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	m, err := newMatch(lobby, set, members)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	for range 10 {
		m.step()
	}
	svc.save(m)
	if _, err := database.MatchLogByID(t.Context(), lobby.ID); err != nil {
		t.Fatalf("MatchLogByID() = %v, want a saved record", err)
	}

	for m.step() { //nolint:revive // run it to its end
	}
	svc.forget(m)
	if _, err := database.MatchLogByID(t.Context(), lobby.ID); err == nil {
		t.Error("a finished match left its replay record behind")
	}
	// And a save after the end must not put one back.
	svc.save(m)
	if _, err := database.MatchLogByID(t.Context(), lobby.ID); err == nil {
		t.Error("saving a finished match wrote a record a restart could replay")
	}
}

// TestStartPersistsImmediately: a match killed ungracefully in its first
// seconds must still be replayable, so the record cannot wait for the first
// save interval.
func TestStartPersistsImmediately(t *testing.T) {
	svc, database := newService(t)
	owner := newUser(t, database, "ada")
	view, err := svc.Create(t.Context(), owner.ID, "fresh match", shortSettings(600))
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Start(t.Context(), view.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	// The driver writes the record on its own goroutine, so poll rather than
	// assume it beat this line. Well under the ten seconds saveEvery would take.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := database.MatchLogByID(t.Context(), view.ID); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("no replay record for a match that has been running: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFingerprintIsStable: the guard must not fire on a build that has not
// changed, or every deploy would abandon every match for nothing.
func TestFingerprintIsStable(t *testing.T) {
	if got := fingerprint(); got == "" || got != fingerprint() {
		t.Fatalf("fingerprint() = %q, want a stable non-empty value", got)
	}
}

func mustSettings(t *testing.T, lobby db.Lobby) Settings {
	t.Helper()
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		t.Fatalf("decodeSettings() = %v", err)
	}
	return set
}
