package lobby

import (
	"testing"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Every AI colony is a blueprint set plus a program library, so "is this
// profile playable" is answerable without running anything: the blueprints must
// satisfy design §6.3, every blueprint must name a program the kit installs,
// and every (program, blueprint) pair must pass prog.Validate with no errors
// and no warnings.
//
// The warning that matters most here is inert_start: a program no freshly built
// robot can match is a colony that stands at its base for the whole match, and
// that failure is invisible from the outside. A reactive_start *note* is fine —
// it is a note precisely because idling until something happens is a design.
func TestAIKitsAreValid(t *testing.T) {
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			k, ok := profile.kit()
			if !ok {
				t.Fatalf("Profiles() lists %q but kit() does not know it", profile)
			}
			programs := map[string]prog.Program{}
			for _, np := range k.programs {
				programs[np.id] = np.p
			}
			if len(k.start) == 0 {
				t.Fatal("profile starts with no robots")
			}

			seen := map[string]bool{}
			for _, bp := range k.blueprints {
				if err := bp.Validate(); err != nil {
					t.Fatalf("blueprint %q Validate() = %v", bp.ID, err)
				}
				if seen[bp.ID] {
					t.Errorf("duplicate blueprint id %q", bp.ID)
				}
				seen[bp.ID] = true

				p, installed := programs[bp.ProgramID]
				if !installed {
					t.Fatalf("blueprint %q names program %q, which the kit never installs", bp.ID, bp.ProgramID)
				}
				res := prog.Validate(p, bp)
				if !res.OK() {
					t.Fatalf("program %q on %q: %+v", bp.ProgramID, bp.ID, res.Errors)
				}
				if len(res.Warnings) != 0 {
					t.Errorf("program %q on %q warnings = %+v, want none", bp.ProgramID, bp.ID, res.Warnings)
				}
			}
			for _, bp := range k.start {
				if !seen[bp.ID] {
					t.Errorf("starting robot uses %q, which is not approved for production", bp.ID)
				}
			}
		})
	}
}

// A profile that does nothing is this bead's failure mode, and nothing about it
// shows up in a unit test of its rules: the robots simply never leave the base.
// So run each profile against a human colony for a few thousand ticks and
// assert it played — moved, scavenged and rebuilt.
func TestAIProfilesPlay(t *testing.T) {
	const ticks = 3000
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			m := aiMatch(t, profile)
			start := map[int]sim.Coord{}
			m.Read(func(w *sim.World, _ *prog.Runtime) {
				for _, r := range w.Robots {
					start[r.ID] = r.Coord
				}
			})
			for i := 0; i < ticks; i++ {
				m.step()
			}

			ai := m.Colonies[1]
			if ai.AI != profile {
				t.Fatalf("colony 1 is %q, want the AI seat", ai.AI)
			}
			var (
				stats sim.Stats
				alive int
				moved int
			)
			m.Read(func(w *sim.World, _ *prog.Runtime) {
				for _, b := range w.Bases {
					if b.Colony == ai.ID {
						stats = b.Stats
					}
				}
				for _, r := range w.Robots {
					if r.Colony != ai.ID {
						continue
					}
					alive++
					if c, ok := start[r.ID]; !ok || c != r.Coord {
						moved++
					}
				}
			})
			t.Logf("%s after %d ticks: robots=%d moved=%d collected=%d kills=%d losses=%d active=%d",
				profile, ticks, alive, moved, stats.Collected, stats.Kills, stats.Losses, stats.TicksActive)

			if moved == 0 {
				t.Error("no AI robot ever left the cell it was built on: the profile is idling")
			}
			if stats.Collected == 0 {
				t.Error("the AI colony deposited nothing in 3000 ticks: it cannot rebuild")
			}
			if stats.TicksActive != ticks {
				t.Errorf("colony was active for %d of %d ticks", stats.TicksActive, ticks)
			}
		})
	}
}

// The aggressive profile is the one with a job no other profile has, and a
// gunner that never shoots is a gunner that may as well not be in the kit. It
// needs a laser and an enemy radar it does not start with, so give the colony
// the parts and check it converts them into a fight.
func TestAggressiveProfileFights(t *testing.T) {
	m := aiMatch(t, ProfileAggressive)
	ai := m.Colonies[1]
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, b := range w.Bases {
			if b.Colony != ai.ID {
				continue
			}
			// Enough for several gunners on any body the fan-out offers.
			for _, v := range []sim.Variant{sim.Tracks, sim.Legs, sim.AntiGrav,
				sim.LightArmor, sim.MediumArmor, sim.HeavyArmor, sim.Laser, sim.EnemyRadar} {
				b.Inventory[v] = 8
			}
		}
	})
	for i := 0; i < 3000; i++ {
		m.step()
	}
	var kills, losses int
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, b := range w.Bases {
			if b.Colony == ai.ID {
				kills = b.Stats.Kills
			} else {
				losses += b.Stats.Losses
			}
		}
	})
	t.Logf("aggressive kills=%d, human losses=%d", kills, losses)
	if kills == 0 {
		t.Error("the aggressive profile destroyed nothing in 3000 ticks")
	}
}

// AI colonies must not cost the replay anything: they are programs over a
// deterministic evaluator, so two matches built from the same lobby row have to
// be identical tick for tick. If this ever fails, a restart rebuilds a world
// the players never left.
func TestAIMatchesAreDeterministic(t *testing.T) {
	set := shortSettings(600)
	set.AI = Profiles()
	set.MaxPlayers = maxPlayers - len(set.AI)
	if err := set.Validate(); err != nil {
		t.Fatalf("a full house of AI colonies is not even legal: %v", err)
	}

	hash := func() uint64 {
		m := testMatch(t, set, 1)
		for i := 0; i < 500; i++ {
			m.step()
		}
		var h uint64
		m.Read(func(w *sim.World, _ *prog.Runtime) { h = w.StateHash() })
		return h
	}
	if a, b := hash(), hash(); a != b {
		t.Fatalf("two runs of the same AI match hashed %d and %d", a, b)
	}
}

// An unknown profile in a stored settings row must fail the start loudly, not
// seat a colony with nothing approved for production.
func TestUnknownAIProfileIsRefused(t *testing.T) {
	set := shortSettings(600)
	set.AI = []Profile{"telepath"}
	if err := set.Validate(); err == nil {
		t.Error("Settings.Validate() accepted an unknown AI profile")
	}
	if _, err := newMatch(db.Lobby{ID: 1, Name: "test"}, set,
		[]db.Member{{UserID: 1, DisplayName: "player"}}); err == nil {
		t.Error("newMatch() accepted an unknown AI profile")
	}
}

// aiMatch is one human seat and one AI colony of the given profile.
func aiMatch(t *testing.T, profile Profile) *Match {
	t.Helper()
	set := shortSettings(3600)
	set.AI = []Profile{profile}
	m := testMatch(t, set, 1)
	if len(m.Colonies) != 2 {
		t.Fatalf("match has %d colonies, want 2", len(m.Colonies))
	}
	return m
}
