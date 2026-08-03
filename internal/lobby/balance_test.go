package lobby

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The balance harness. The measured ladder in ai.go and the guard in starter.go
// are both balance judgements, and a balance judgement nobody can re-run is an
// opinion: PR #30 and PR #31 each measured this with a throwaway harness, and
// the numbers they left behind could not be checked against a later build. This
// is that harness, kept.
//
// It asserts nothing. Sixteen full 6000-tick matches per profile is far too slow
// for every `go test ./...`, and the numbers are a judgement rather than a
// contract — the structural properties the judgement rests on are pinned by
// ordinary tests (TestStarterGuardCannotHunt, TestProfileLadderIsArmedInOrder).
// So it is opt-in, and it prints:
//
//	ROBOCOLONY_BALANCE=1 go test ./internal/lobby/ -run TestBalance -v
//
// "Wiped" is the human colony at zero live robots at any tick; "dead at end" is
// zero live robots when the match runs out. They are usually the same seeds, and
// that is the point: a colony with no robot collects nothing, so its base stalls
// on the first component row it lacks and it never recovers (design §5.3's
// rebuild path has no other input). A wipe is only a setback on paper.
func balanceEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("ROBOCOLONY_BALANCE") == "" {
		t.Skip("set ROBOCOLONY_BALANCE=1 to run the balance harness")
	}
}

const (
	balanceSeeds = 16
	balanceTicks = 6000
)

// balanceRun is one match: seat 0 is the colony under test, every later seat is
// an opponent. loadouts[i] nil means seat i takes the built-in kit.
type balanceRun struct {
	wiped     bool
	wipeTick  uint64
	stats     []sim.Stats
	alive     []int
	score     []int
	colonyIDs []sim.ColonyID
}

func runBalance(t *testing.T, seed int64, ai []Profile, loadouts [][]byte) balanceRun {
	t.Helper()
	set := DefaultSettings()
	set.DurationSec = balanceTicks / TickRate
	set.Seed = seed
	set.AI = ai
	set.MaxPlayers = max(len(loadouts), 1)

	members := make([]db.Member, len(loadouts))
	for i, lo := range loadouts {
		members[i] = db.Member{UserID: int64(i + 1), DisplayName: "seat", Loadout: lo}
	}
	m, err := newMatch(db.Lobby{ID: 1, Name: "balance"}, set, members)
	if err != nil {
		t.Fatalf("newMatch(seed %d) = %v", seed, err)
	}

	run := balanceRun{colonyIDs: make([]sim.ColonyID, len(m.Colonies))}
	for i, c := range m.Colonies {
		run.colonyIDs[i] = c.ID
	}
	subject := run.colonyIDs[0]
	for range balanceTicks {
		m.step()
		if run.wiped {
			continue
		}
		m.Read(func(w *sim.World, _ *prog.Runtime) {
			for _, r := range w.Robots {
				if r.Colony == subject {
					return
				}
			}
			run.wiped, run.wipeTick = true, w.Tick
		})
	}

	run.stats = make([]sim.Stats, len(run.colonyIDs))
	run.alive = make([]int, len(run.colonyIDs))
	run.score = make([]int, len(run.colonyIDs))
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		index := map[sim.ColonyID]int{}
		for i, id := range run.colonyIDs {
			index[id] = i
		}
		for _, b := range w.Bases {
			if i, ok := index[b.Colony]; ok {
				run.stats[i] = b.Stats
			}
		}
		for _, r := range w.Robots {
			if i, ok := index[r.Colony]; ok {
				run.alive[i]++
			}
		}
		for _, res := range w.Leaderboard() {
			if i, ok := index[res.Colony]; ok {
				run.score[i] = res.Score
			}
		}
	})
	return run
}

// TestBalanceLadder is the measurement behind the ladder in ai.go: the built-in
// starter kit against each AI profile, 16 seeds × 6000 ticks.
func TestBalanceLadder(t *testing.T) {
	balanceEnabled(t)
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			t.Parallel()
			var (
				wipes, dead    int
				earliest       uint64
				sum            uint64
				hCol, aCol     int
				hLoss, aLoss   int
				hAlive, aAlive int
			)
			for seed := int64(1); seed <= balanceSeeds; seed++ {
				r := runBalance(t, seed, []Profile{profile}, [][]byte{nil})
				if r.wiped {
					wipes++
					sum += r.wipeTick
					if earliest == 0 || r.wipeTick < earliest {
						earliest = r.wipeTick
					}
				}
				if r.alive[0] == 0 {
					dead++
				}
				hCol, aCol = hCol+r.stats[0].Collected, aCol+r.stats[1].Collected
				hLoss, aLoss = hLoss+r.stats[0].Losses, aLoss+r.stats[1].Losses
				hAlive, aAlive = hAlive+r.alive[0], aAlive+r.alive[1]
			}
			var avg uint64
			if wipes > 0 {
				avg = sum / uint64(wipes)
			}
			t.Logf("%-11s %2d/%d wiped (%d dead at end), earliest %d, avg %d | collected %d to %d | losses %d to %d | alive %d to %d",
				profile, wipes, balanceSeeds, dead, earliest, avg, hCol, aCol, hLoss, aLoss, hAlive, aAlive)
		})
	}
}

// TestBalanceCustomVsDefault is the other half of the judgement, and the line
// the starter kit must not cross: a thoughtful design has to beat the default
// clearly, or engineering robots has stopped being the game.
//
// The design is the obvious one a player writes once they have read §10: keep
// the scavenger opening, approve a laser + enemy-radar hunter behind it, and let
// §5.2 mix the two as parts arrive.
func TestBalanceCustomVsDefault(t *testing.T) {
	balanceEnabled(t)
	lo := customLoadout(t)
	var wins, cCol, dCol, cLoss, dLoss, cAlive, dAlive int
	for seed := int64(1); seed <= balanceSeeds; seed++ {
		r := runBalance(t, seed, nil, [][]byte{lo, nil})
		if r.score[0] > r.score[1] {
			wins++
		}
		t.Logf("seed %2d: custom %5d, default %5d", seed, r.score[0], r.score[1])
		cCol, dCol = cCol+r.stats[0].Collected, dCol+r.stats[1].Collected
		cLoss, dLoss = cLoss+r.stats[0].Losses, dLoss+r.stats[1].Losses
		cAlive, dAlive = cAlive+r.alive[0], dAlive+r.alive[1]
	}
	t.Logf("custom wins %d/%d | collected %d to %d | losses %d to %d | alive %d to %d",
		wins, balanceSeeds, cCol, dCol, cLoss, dLoss, cAlive, dAlive)
}

// customLoadout is that design, in the shape PUT /api/lobbies/{id}/loadout
// stores. The first entry is the opening body (see startingRoster).
func customLoadout(t *testing.T) []byte {
	t.Helper()
	encode := func(p prog.Program) json.RawMessage {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("encode %q: %v", p.Name, err)
		}
		return b
	}
	l := Loadout{Entries: []LoadoutEntry{
		{BlueprintID: 1, BlueprintName: "scavenger", ProgramID: 1, ProgramName: "scavenger",
			Components: []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Manipulator), int(sim.PartsRadar)},
			Program:    encode(DefaultProgram())},
		{BlueprintID: 2, BlueprintName: "hunter", ProgramID: 2, ProgramName: "hunter",
			Components: []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Laser), int(sim.EnemyRadar)},
			Program:    encode(hunterProgram())},
	}}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("encode loadout: %v", err)
	}
	return b
}

// TestBalanceIdleReason is the diagnostic that found the fix: it prints what the
// colony under test is holding and why its base is building nothing. "No
// approved blueprint is fully covered by the inventory" next to a stock of
// tracks, armor and a manipulator is a production stall on one missing row —
// which is how a wiped colony stays wiped.
func TestBalanceIdleReason(t *testing.T) {
	balanceEnabled(t)
	set := DefaultSettings()
	set.DurationSec = balanceTicks / TickRate
	set.Seed = 1
	set.AI = []Profile{ProfileAggressive}
	set.MaxPlayers = 1
	m, err := newMatch(db.Lobby{ID: 1, Name: "balance"}, set,
		[]db.Member{{UserID: 1, DisplayName: "seat"}})
	if err != nil {
		t.Fatal(err)
	}
	subject := m.Colonies[0].ID
	for i := range balanceTicks {
		m.step()
		if (i+1)%500 != 0 {
			continue
		}
		m.Read(func(w *sim.World, _ *prog.Runtime) {
			alive := 0
			for _, r := range w.Robots {
				if r.Colony == subject {
					alive++
				}
			}
			for _, b := range w.Bases {
				if b.Colony != subject {
					continue
				}
				t.Logf("t=%d robots=%d collected=%d losses=%d idle=%q inventory=%v",
					w.Tick, alive, b.Stats.Collected, b.Stats.Losses, b.IdleReason(), b.SortedInventory())
			}
		})
	}
}
