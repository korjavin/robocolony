package lobby

import (
	"context"
	"encoding/json"
	"errors"
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

// TestReplayFinishedMatchToItsEnd is the E9 half of the assertion above: a
// finished match's stored log replays to its *final* tick — the tick the old
// guard rejected outright — and lands on the same world.
func TestReplayFinishedMatchToItsEnd(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	rec, m := finishedMatch(t, svc, database, lobby, set, members)

	restored, err := replay(lobby, set, members, rec)
	if err != nil {
		t.Fatalf("replay() of a finished match = %v", err)
	}
	if got, want := restored.world.Tick, m.world.Tick; got != want {
		t.Fatalf("replayed to tick %d, want the final tick %d", got, want)
	}
	if got, want := restored.world.StateHash(), m.world.StateHash(); got != want {
		t.Fatalf("the replayed final world hashes %#x, the original %#x", got, want)
	}
	if !restored.Finished() {
		t.Error("a match replayed to its end does not report itself finished")
	}

	// The guard stays strict for the Restore path: the same record without
	// finished_at is a *running* match claiming to stand at its own end, which
	// is corrupt.
	rec.FinishedAt = time.Time{}
	if _, err := replay(lobby, set, members, rec); err == nil {
		t.Error("replay() accepted a running match recorded at its end tick")
	}
}

// TestFinishedMatchRefusesCommands: the finishing save is the last write of the
// record, so a command that lands after it would be lost from the log for good
// and leave the stored match replaying into a world nobody saw. The pre-check
// in internal/server cannot hold on its own — the finished flag is set under the
// match lock — so the refusal has to be in Apply.
func TestFinishedMatchRefusesCommands(t *testing.T) {
	set := shortSettings(60)
	m := testMatch(t, set, 1)
	for m.step() { //nolint:revive // run it to its end
	}
	var robot int
	m.Read(func(w *sim.World, _ *prog.Runtime) { robot = w.Robots[0].ID })

	before := len(m.log)
	err := m.Apply(Command{Kind: CmdRecall, Robot: robot}, func(w *sim.World, _ *prog.Runtime) error {
		w.RobotByID(robot).Recalled = true
		return nil
	})
	if !errors.Is(err, ErrMatchOver) {
		t.Fatalf("Apply() on a finished match = %v, want ErrMatchOver", err)
	}
	if len(m.log) != before {
		t.Errorf("the refused command was recorded anyway: log has %d entries, want %d", len(m.log), before)
	}
	if m.world.RobotByID(robot).Recalled {
		t.Error("the refused command was applied to the final world")
	}
}

// TestRebuildCost measures what a replay connection costs, because the client
// design makes every control (pause, scrub, speed) a reconnect and therefore a
// rebuild. It logs rather than asserts: the number is hardware, and a threshold
// here would only flake on CI.
//
// Default settings, the default four colonies, replayed from tick 0 to the end
// of a 600-second match: 0.23-0.32s when a core is free, 0.59s median over
// seven runs on a four-core box carrying other builds, and several seconds when
// that box is oversubscribed — which is scheduling, not simulation. That is
// what settled the design at "rebuild per connection" and left the
// warm-session cache unbuilt (see Service.Replay).
//
// It scales with the target tick, and rc-8hu left match duration with no
// ceiling, so a much longer match costs proportionally more.
func TestRebuildCost(t *testing.T) {
	if testing.Short() {
		t.Skip("6000 ticks of simulation")
	}
	svc, database := newService(t)
	set := DefaultSettings()
	set.Seed = 42
	set.AI = Profiles()[:3] // one human seat plus three AI: the default colony count
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	end := set.durationTicks()
	// An empty command log: the rebuild is newMatch plus the ticks, and a
	// handful of recalls is not measurable next to 6000 steps.
	rec := db.MatchLog{
		LobbyID: lobby.ID, Fingerprint: fingerprint(), Tick: int64(end),
		StartedAt: time.Now().UTC(), Commands: "[]", FinishedAt: time.Now().UTC(),
	}
	started := time.Now()
	m, err := replay(lobby, set, members, rec)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("replay() = %v", err)
	}
	t.Logf("rebuild to the end tick: %v for %d ticks, %d colonies, default settings",
		elapsed.Round(time.Millisecond), end, len(m.Colonies))
}

// finishedMatch runs a match to its end with a command in the middle and saves
// the finishing record, the way the tick driver does.
func finishedMatch(t *testing.T, svc *Service, database *db.DB, lobby db.Lobby, set Settings, members []db.Member) (db.MatchLog, *Match) {
	t.Helper()
	m, err := newMatch(lobby, set, members)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	for range 37 {
		m.step()
	}
	recall(t, m, m.world.Robots[0].ID)
	for m.step() { //nolint:revive // run it to its end
	}
	svc.save(m)
	// What matchEnded does on the driver goroutine.
	if err := database.SetLobbyState(t.Context(), lobby.ID, db.LobbyFinished); err != nil {
		t.Fatalf("SetLobbyState() = %v", err)
	}
	rec, err := database.MatchLogByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v", err)
	}
	return rec, m
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

// TestFinishedMatchKeepsItsRecord: a match that reaches its duration keeps its
// record (E9), marked finished and carrying the standing, at the exact final
// tick — and a restart must not resurrect it from that record.
func TestFinishedMatchKeepsItsRecord(t *testing.T) {
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
	rec, err := database.MatchLogByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v, want a saved record", err)
	}
	if !rec.FinishedAt.IsZero() {
		t.Error("a running match's record is marked finished")
	}

	for m.step() { //nolint:revive // run it to its end
	}
	svc.save(m) // the finishing save the tick driver makes

	rec, err = database.MatchLogByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v, want the finished match's record kept", err)
	}
	if rec.FinishedAt.IsZero() {
		t.Error("the record of a finished match has no finished_at")
	}
	if want := int64(set.durationTicks()); rec.Tick != want {
		t.Errorf("the record stops at tick %d, want the final tick %d", rec.Tick, want)
	}
	var sum Summary
	if err := json.Unmarshal([]byte(rec.Summary), &sum); err != nil {
		t.Fatalf("stored summary does not decode: %v", err)
	}
	if len(sum.Info.Colonies) != len(m.Colonies) {
		t.Errorf("the summary holds %d colonies, want %d", len(sum.Info.Colonies), len(m.Colonies))
	}
	if len(sum.History.Ticks) == 0 {
		t.Error("the summary holds no score series: the history graph would be empty")
	}

	// A restart must not put a tick driver back behind it, and must not delete
	// the history to do so. The lobby row still says running here, which is the
	// crash window between the finishing save and matchEnded.
	next := New(database)
	if err := next.Restore(t.Context()); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	if _, ok := next.reg.Get(lobby.ID); ok {
		t.Error("Restore() resurrected a match that had already finished")
	}
	if _, err := database.MatchLogByID(t.Context(), lobby.ID); err != nil {
		t.Errorf("Restore() destroyed a finished match's history: %v", err)
	}
	after, err := database.LobbyByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("LobbyByID() = %v", err)
	}
	if after.State != db.LobbyFinished {
		t.Errorf("lobby state is %q, want finished", after.State)
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
