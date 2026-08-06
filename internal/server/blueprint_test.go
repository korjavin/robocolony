package server

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/sim"
)

// scavenger is the unarmed starter shape: tracks, medium armor, a manipulator
// and a parts radar. Legal, and every consequence branch has something to say
// about it.
var scavenger = []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Manipulator), int(sim.PartsRadar)}

// TestConsequencesTrackTheTables is the whole reason this page's sentences are
// written in Go. Every one of them restates a number the balance tables own, so
// the test asserts against those tables rather than against literal prose: when
// E7.3 retunes locomotion or armor, a sentence that has gone stale fails here
// instead of quietly misinforming a player.
func TestConsequencesTrackTheTables(t *testing.T) {
	bp := sim.Blueprint{Components: toVariants(scavenger)}
	lines := consequences(bp)
	if len(lines) == 0 {
		t.Fatal("a legal design got no consequences")
	}
	all := strings.Join(lines, "\n")

	wants := []struct {
		what string
		sub  string
	}{
		// The pace, in the unit the configurator's meter draws.
		{"ticks per cell", strconv.Itoa(sim.TicksPerCell(bp, sim.Open))},
		// The §3.1 traversal matrix: tracks are shut out of rubble.
		{"blocked terrain", "rubble"},
		// The §6.1 armor tier, and what it is worth under one named weapon.
		{"health", strconv.Itoa(sim.StartingHealth(bp))},
		{"weapon by name", "laser"},
		// §7.1 and §7.2: the wedge, and the radar this design paid for.
		{"sight range", strconv.Itoa(sim.VisionRange)},
		{"radar range", strconv.Itoa(sim.BlueprintRadarRange(bp))},
		// The opening the starting budget actually buys.
		{"fleet size", strconv.Itoa(lobby.StartingFleet(bp, lobby.DefaultStartingBudget()))},
	}
	for _, w := range wants {
		if !strings.Contains(all, w.sub) {
			t.Errorf("no consequence mentions the %s (%q):\n%s", w.what, w.sub, all)
		}
	}

	// The unarmed branch is the one that matters most: it is not a missing
	// number, it is a whole half of the rule language that will never fire.
	if !strings.Contains(all, "no weapon") {
		t.Errorf("an unarmed design does not say so:\n%s", all)
	}
	armed := sim.Blueprint{Components: toVariants(append(slices.Clone(scavenger), int(sim.Laser)))}
	if a := strings.Join(consequences(armed), "\n"); strings.Contains(a, "no weapon") {
		t.Errorf("an armed design is reported as unarmed:\n%s", a)
	}
}

// TestConsequencesStaySilentOnAnIllegalDesign: §6.3 has not decided what a
// half-built parts list is, and a confident sentence about a robot that cannot
// exist is worse than no sentence.
func TestConsequencesStaySilentOnAnIllegalDesign(t *testing.T) {
	for _, tt := range []struct {
		name       string
		components []int
	}{
		{"no locomotion", []int{int(sim.MediumArmor)}},
		{"no armor", []int{int(sim.Tracks)}},
		{"two radars", []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.PartsRadar), int(sim.EnemyRadar)}},
		{"empty", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := consequences(sim.Blueprint{Components: toVariants(tt.components)}); got != nil {
				t.Errorf("an illegal design got consequences: %v", got)
			}
		})
	}
}

// TestProgramFitMatchesValidate: the configurator's ✓/✗ column is what tells a
// player whether the design they are building can run anything they have
// written. It must be the same verdict prog.Validate gives — the editor's save
// gate and the lobby's approval both go through that, and a page that says
// "compatible" about a program neither will accept is worse than no column.
func TestProgramFitMatchesValidate(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	// Seeds the starter templates, which is where the interesting spread is:
	// one wants a radar, one wants a weapon.
	programs, err := lib.ListPrograms(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(programs) == 0 {
		t.Fatal("the starter library seeded no programs")
	}

	// An unarmed body: at least one starter program must be refused on it, or
	// this test proves nothing about the ✗ half of the column.
	bp := sim.Blueprint{Components: toVariants(scavenger)}
	fits, err := lib.programFit(t.Context(), user.ID, bp)
	if err != nil {
		t.Fatalf("programFit() = %v", err)
	}
	if len(fits) != len(programs) {
		t.Fatalf("programFit() judged %d programs, the library has %d", len(fits), len(programs))
	}
	blocked := 0
	for _, f := range fits {
		if !f.OK {
			blocked++
			if f.Blocked == "" {
				t.Errorf("%q is unfit with no reason given", f.Name)
			}
		}
	}
	if blocked == 0 {
		t.Errorf("no starter program is blocked on an unarmed design; the ✗ column is untested")
	}

	// Bolting a weapon on must not make anything *less* compatible: hardware is
	// only ever added to a blueprint, and prog.Validate's errors are all
	// missing-component errors.
	armedFits, err := lib.programFit(t.Context(), user.ID,
		sim.Blueprint{Components: toVariants(append(slices.Clone(scavenger), int(sim.Laser)))})
	if err != nil {
		t.Fatalf("programFit() = %v", err)
	}
	for i, f := range armedFits {
		if !f.OK && fits[i].OK {
			t.Errorf("%q runs unarmed but not with a laser bolted on: %s", f.Name, f.Blocked)
		}
	}
}

// TestPreviewCarriesWhatTheMetersDraw: the two meters and the silhouette are
// drawn from these five fields and nothing else, so a preview that omits one
// renders a blank panel rather than an error anyone would notice.
func TestPreviewCarriesWhatTheMetersDraw(t *testing.T) {
	lib, _ := newLibrary(t)
	stats, err := lib.PreviewBlueprint(scavenger)
	if err != nil {
		t.Fatalf("PreviewBlueprint() = %v", err)
	}
	bp := sim.Blueprint{Components: toVariants(scavenger)}
	for _, tt := range []struct {
		name      string
		got, want int
	}{
		{"ticks_per_cell", stats.TicksPerCell, sim.TicksPerCell(bp, sim.Open)},
		{"sight", stats.Sight, sim.VisionRange},
		{"radar", stats.Radar, sim.BlueprintRadarRange(bp)},
		{"budget", stats.Budget, lobby.DefaultStartingBudget()},
		{"fleet", stats.Fleet, lobby.StartingFleet(bp, lobby.DefaultStartingBudget())},
	} {
		if tt.got != tt.want || tt.want == 0 {
			t.Errorf("%s = %d, want %d (and non-zero)", tt.name, tt.got, tt.want)
		}
	}
}

// blind is a legal chassis carrying nothing that any rule needs: no weapon, no
// radar, no manipulator. Every hardware-gated row of the language is shut.
var blind = []int{int(sim.Tracks), int(sim.MediumArmor)}

// seeing is that chassis with a radar on it — the design against which a weapon
// is worth more than it is on blind, which is the whole point of pricing a part
// against the robot rather than against the catalogue.
var seeing = []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.EnemyRadar)}

// TestMarginalAgreesWithTheTables is the pin TestBlueprintPreviewAgreesWithSave
// puts on the preview itself, one part further on: the palette's price for a
// catalogue row is the answer sim and lobby would give for the design with that
// row fitted, never a second copy of the §6.3 rules or the §6.4 speed model.
//
// It also holds the two properties the palette's wording leans on — a part only
// ever adds mass, so the pace can only get slower and the fleet only smaller.
func TestMarginalAgreesWithTheTables(t *testing.T) {
	budget := lobby.DefaultStartingBudget()
	for _, tt := range []struct {
		name string
		base []int
	}{
		{"empty parts list", nil},
		{"unarmed scavenger", scavenger},
		{"armed and seeing", append(slices.Clone(seeing), int(sim.Laser))},
		{"already illegal", []int{int(sim.MediumArmor), int(sim.LightArmor)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bp := sim.Blueprint{Components: toVariants(tt.base)}
			cat := sim.Catalogue()
			got := marginals(bp)
			if len(got) != len(cat) {
				t.Fatalf("priced %d rows, the catalogue has %d", len(got), len(cat))
			}
			for i, m := range got {
				c := cat[i]
				if m.Variant != int(c.Variant) {
					t.Fatalf("row %d prices variant %d, want %d", i, m.Variant, c.Variant)
				}
				with := sim.Blueprint{Components: append(slices.Clone(bp.Components), c.Variant)}
				if wantOK := with.Validate() == nil; m.OK != wantOK {
					t.Fatalf("%s: ok = %v, want %v", c.Name, m.OK, wantOK)
				}
				if !m.OK {
					// A refused part is a reason, not numbers: §6.3 has not
					// decided what that design is.
					if m.Error == "" || m.TicksPerCell != 0 || m.Fleet != 0 {
						t.Fatalf("%s: refused but priced anyway: %+v", c.Name, m)
					}
					continue
				}
				if want := sim.TicksPerCell(with, sim.Open); m.TicksPerCell != want {
					t.Errorf("%s: ticks_per_cell = %d, want %d", c.Name, m.TicksPerCell, want)
				}
				if want := lobby.StartingFleet(with, budget); m.Fleet != want {
					t.Errorf("%s: fleet = %d, want %d", c.Name, m.Fleet, want)
				}
				base := blueprintStats(bp)
				if !base.OK {
					continue // no base numbers to be marginal to
				}
				if m.TicksPerCell < base.TicksPerCell || m.Fleet > base.Fleet {
					t.Errorf("%s: adding mass made the design faster or the fleet bigger: %+v vs %+v",
						c.Name, m, base)
				}
			}
		})
	}
}

// TestMarginalUnlocksCountsTheLanguage: "4 rules unlock" is what the palette is
// for, so it is pinned to the rows prog's catalogue actually gates on hardware.
// The numbers are literal on purpose — they are what a player reads, so adding a
// predicate that needs a radar has to fail here and be looked at rather than
// quietly change what the page promises.
func TestMarginalUnlocksCountsTheLanguage(t *testing.T) {
	unlocks := func(t *testing.T, base []int, v sim.Variant) int {
		t.Helper()
		for _, m := range marginals(sim.Blueprint{Components: toVariants(base)}) {
			if m.Variant == int(v) {
				return m.Unlocks
			}
		}
		t.Fatalf("variant %d is not in the catalogue", v)
		return 0
	}
	for _, tt := range []struct {
		name string
		base []int
		add  sim.Variant
		want int
	}{
		// fire, weapon_ready, visible_target_in_weapon_range.
		{"a weapon on a blind chassis", blind, sim.Laser, 3},
		// Those three plus the two rows that need a radar *and* a weapon.
		{"a weapon on a chassis that already sees", seeing, sim.Laser, 5},
		// radar_contact, move_to_radar_contact, remember the radar contact.
		{"a radar on a blind chassis", blind, sim.EnemyRadar, 3},
		// pick up, deliver, drop.
		{"a manipulator", blind, sim.Manipulator, 3},
		// Nothing: the scavenger already carries one.
		{"a part the design already has", scavenger, sim.Manipulator, 0},
		// A refused part unlocks nothing, whatever its Needs would have been.
		{"a second armored body", blind, sim.HeavyArmor, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := unlocks(t, tt.base, tt.add); got != tt.want {
				t.Errorf("unlocks = %d, want %d", got, tt.want)
			}
		})
	}
}
