package lobby

import (
	"fmt"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The POC starting kit. Design §2.1 has every player prepare a colony from an
// equal budget out of their own library; E6.1 builds that library, so until
// then every human colony starts from the same blueprint set and the same robot
// count, which is equal by construction. An AI colony starts from a canned kit
// of the same shape (ai.go) — the same blueprints, the same rule language, the
// same production, never a privileged controller.
const (
	DefaultBlueprintID = "bp-default-scavenger"
	DefaultProgramID   = "prog-default-scavenger"

	// startingRobots is the starting budget, expressed the only way it can be
	// before there is a library to spend it in.
	startingRobots = 3

	// headings is how many facings sim.Heading has. sim exports no count, and
	// Turn wraps, so a stale value here can only bias the draw, never panic.
	headings = 8
)

// The canonical body: the (locomotion, armor) pair whose blueprint keeps the
// bare id in every fan-out below. The rest of the catalogue's rows are
// suffixed. internal/server/programs.go seeds the player library against the
// canonical scavenger, so its id is load-bearing.
const (
	defaultArmor = sim.MediumArmor
	defaultLoco  = sim.Tracks
)

// DefaultBlueprint is the canonical starting blueprint: the one the library
// seeds and the one a program is checked against before anyone edits a design.
//
// The parts radar is not decoration: without one, prog.Validate rejects
// move_to_radar_target, and DefaultProgram — design §10.7 — degenerates into a
// blind random walk without it.
func DefaultBlueprint() sim.Blueprint { return canonicalOf(DefaultBlueprintID, scavengerKit()) }

// DefaultBlueprints is the *set* a colony starts with approved for production
// (design §5.1 has the player approve one or more): one scavenger and one base
// guard per (locomotion, armor) pair in the catalogue. That is the point of the
// set — the §5.2 build scan needs a blueprint's exact components, so a colony
// approving only the medium-armor tracks variant stalls forever the moment
// either medium armor or tracks runs out, however much light armor and how many
// legs it has scavenged. A locomotion or armor row added to the catalogue gets
// starter blueprints here for free.
//
// Eighteen blueprints, and §5.2 picks uniformly among the *buildable* ones, so
// the count only widens what a colony can do with what it actually holds: a
// colony with one armor tier, one locomotion unit and no weapon in stock still
// has exactly one choice. What it buys is that no single component ever stalls
// production — measured (see guardKit): before the guard, a colony out of parts
// radars sat on tracks, armor and a manipulator and never built again.
func DefaultBlueprints() []sim.Blueprint { return append(scavengerKit(), guardKit()...) }

// scavengerKit is the §10.7 scavenger body fanned out over the catalogue.
func scavengerKit() []sim.Blueprint {
	return fanOut(DefaultBlueprintID, "scavenger", DefaultProgramID, sim.Manipulator, sim.PartsRadar)
}

// The base guard, and the answer to rc-w9s.15: measured over 16 seeds × 6000
// ticks, the unarmed starter kit was annihilated by the aggressive AI profile on
// 16 seeds of 16 — and, once at zero robots, never came back, because a colony
// with no robot can collect nothing and its base stalls on the first component
// row it runs out of.
//
// Three properties make this a starting point rather than an answer, and all
// three are deliberate:
//
//   - The cheapest weapon in the catalogue (design §8.1: range 4, 45% accuracy)
//     and no radar at all. A guard cannot find anything; it shoots what walks in
//     front of it, inside a six-cell vision cone (design §7.1).
//   - It never leaves the base. guardProgram has no search and no pursuit, so a
//     guard contests nothing on the map and cannot farm an unarmed colony.
//   - No manipulator: it earns nothing. Every guard the base builds is
//     production a scavenger did not get.
//
// A player who designs a laser + enemy-radar hunter still beats it decisively,
// which is the line this must not cross: the default has to lose to a thoughtful
// design. See the measured ladder in ai.go for what it does to each profile.
//
// The weapon axis is deliberately *not* fanned out, unlike locomotion and armor,
// which means a colony holding only lasers still cannot build a guard. That is a
// stall of exactly the kind fanOut exists to prevent, and it is kept on purpose:
// fanning the guard over all three weapons was measured (16 seeds × 6000 ticks)
// and it broke the game the other way — the default kit came out ahead of the
// *aggressive* profile (1136 collected to 850, 216 robots left to 60, wiped on 2
// seeds instead of 16), and the laser-hunter design above stopped beating the
// default at all, winning 7 of 16 rather than 12.
// A salvaged laser the starter kit cannot use is a reason to open the blueprint
// editor, which is the game.
func guardKit() []sim.Blueprint {
	return fanOut(guardBlueprintID, "guard", guardProgramID, sim.AutoGun)
}

const (
	guardBlueprintID = "bp-default-guard"
	guardProgramID   = "prog-default-guard"
)

// guardProgram is a sentry: shoot what is in range, hold the base, walk back to
// it otherwise. Three rules, every one of them in design §10's catalogue, and
// nothing a player could not have written on their first afternoon — which is
// the point of shipping it as a default rather than a stronger body.
//
// turn_random at the base is the scan: forward vision is a 90° wedge (design
// §7.1), so a sentry that stood still would cover one quarter of the approaches
// and let a hunter shoot it in the back. It is also what keeps the program out
// of prog.Validate's inert_start warning — a fresh guard standing at its base
// matches this rule on tick one.
func guardProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "base guard", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.SeesEnemyRobot), prog.Pred(prog.VisibleTargetInWpnRange)),
			Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
		{When: prog.Pred(prog.AtOwnBase),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveToOwnBase)}},
	}}
}

// canonicalOf returns the member of a fan-out that kept the bare prefix as its
// id — the (defaultLoco, defaultArmor) body. fanOut always emits it, so the
// zero value is unreachable.
func canonicalOf(prefix string, bps []sim.Blueprint) sim.Blueprint {
	for _, bp := range bps {
		if bp.ID == prefix {
			return bp
		}
	}
	return sim.Blueprint{}
}

// fanOut builds one blueprint per (locomotion, armor) pair in the catalogue
// around a fixed set of extra components. The id is keyed on the variant
// numbers, which the catalogue promises never to reuse, and the canonical pair
// keeps the bare prefix as its id.
func fanOut(idPrefix, name, programID string, extra ...sim.Variant) []sim.Blueprint {
	locos, armors := variantsOfKind(sim.KindLocomotion), variantsOfKind(sim.KindArmor)
	out := make([]sim.Blueprint, 0, len(locos)*len(armors))
	for _, loco := range locos {
		for _, armor := range armors {
			id, label := idPrefix, name
			if loco != defaultLoco || armor != defaultArmor {
				id = fmt.Sprintf("%s-%d-%d", idPrefix, loco, armor)
				label = fmt.Sprintf("%s %s %s", armor, loco, name)
			}
			out = append(out, sim.Blueprint{
				ID:         id,
				Name:       label,
				Components: append([]sim.Variant{loco, armor}, extra...),
				ProgramID:  programID,
			})
		}
	}
	return out
}

// variantsOfKind is every catalogue row of one kind, in catalogue order.
func variantsOfKind(k sim.ComponentKind) []sim.Variant {
	var out []sim.Variant
	for _, c := range sim.Catalogue() {
		if c.Kind == k {
			out = append(out, c.Variant)
		}
	}
	return out
}

// DefaultProgram is the design §10.7 component scavenger, minus its first rule.
//
// §10.7 opens with at_own_base AND carrying_component -> deposit_component_at_base,
// which the evaluator now does by reflex (rc-tad.13, prog.idleAtOwnBaseWithCargo):
// rule 1 below tells the robot to go home, and arriving home with cargo deposits
// it. The two programs are not merely equivalent in outcome — they produce the
// same sim.World.StateHash tick for tick, which is what TestScavengerNeedsNoDepositRule
// asserts, so nothing measured on the six-rule version was retuned by dropping
// the line. It is the first program a player reads, and it is one rule shorter.
func DefaultProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "component scavenger", Rules: []prog.Rule{
		{When: prog.Pred(prog.CarryingComponent),
			Then: []prog.Action{prog.Do(prog.MoveToOwnBase)}},
		{When: prog.And(prog.Pred(prog.ComponentInReach), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.Do(prog.PickUpComponent)}},
		{When: prog.And(prog.Pred(prog.RadarDetectsTarget), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.Do(prog.MoveToRadarTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}

// kit is a colony's starting equipment: what it has approved for production,
// the programs those blueprints name, and one entry per robot it starts with.
// A human colony and an AI colony differ only in the value of this struct.
type kit struct {
	blueprints []sim.Blueprint
	programs   []namedProgram
	start      []sim.Blueprint // one per starting robot

	// budget is the match's starting budget when this is a player's colony,
	// and zero for the canned AI kits. Design §2.1's equal budget is a rule
	// about the *players*: an AI profile is a fixed opponent whose opening was
	// measured as it stands (see the ladder in ai.go), so pricing it at
	// whatever a host typed into the lobby form would retune that ladder
	// behind the numbers written down there. equipColony leaves a zero alone.
	budget int
}

// namedProgram is a program plus the runtime id blueprints refer to it by.
type namedProgram struct {
	id string
	p  prog.Program
}

// humanKit is the design §2.1 equal starting budget for a player's colony.
//
// The opening is unchanged by the guard: three scavengers, 345 in component
// value, exactly what startingBudget is defined as. A guard is something the
// base builds later, out of a weapon the colony had to scavenge first.
func humanKit() kit {
	return kit{
		blueprints: DefaultBlueprints(),
		programs: []namedProgram{
			{DefaultProgramID, DefaultProgram()},
			{guardProgramID, guardProgram()},
		},
		start: repeat(DefaultBlueprint(), startingRobots),
	}
}

func repeat(bp sim.Blueprint, n int) []sim.Blueprint {
	out := make([]sim.Blueprint, n)
	for i := range out {
		out[i] = bp
	}
	return out
}

// equipColony approves a kit's blueprints at a base, puts its starting robots
// on the map, and turns whatever the roster did not spend into base stock.
// Everything random here draws from the world's own rng, so two matches with
// the same seed, member list and AI list start identical.
func equipColony(w *sim.World, base *sim.Base, k kit) {
	base.Blueprints = append(base.Blueprints, k.blueprints...)
	spent := 0
	for _, bp := range k.start {
		// The budget bounds the whole roster, not just the one startingRoster
		// priced: the built-in kit is a fixed list of three robots, so a host
		// who sets a budget below what it costs has to see it cut here or that
		// colony would out-equip one whose player picked a loadout. The tail
		// goes first, which keeps the cheapest body. What the cut frees is not
		// lost; it comes back below as stock.
		if k.budget > 0 && spent+bp.Value() > k.budget {
			break
		}
		spent += bp.Value()
		w.Robots = append(w.Robots, &sim.Robot{
			ID:        w.NextID(),
			Colony:    base.Colony,
			Coord:     base.Coord,
			Heading:   sim.North.Turn(w.Rand().Intn(headings)),
			Health:    sim.StartingHealth(bp),
			Blueprint: bp,
			ProgramID: bp.ProgramID,
		})
	}
	spendRemainder(w, base, k.budget-spent)
}

// spendRemainder converts unspent starting budget into base inventory — design
// §12 P0, answered: it becomes parts rather than being lost or reserved.
//
// Equal starting strength (design §2.1) survives it because the loop only stops
// when nothing in the catalogue still fits, so every *player* colony leaves the
// shop having spent all but less than the cheapest component (20) of the same
// budget. A lean roster buys spares, never an advantage: parts in the base
// count for a quarter of what the same value counts for on a live robot
// (sim's inventoryScorePercent), so under-spending is still a loss.
//
// It draws from sim.Catalogue(), the same table the map spawns from, so nothing
// lands in a base that a colony could not have scavenged. The draw is on the
// world's rng, never math/rand: it is world state at tick zero and a replay
// rebuilds it from the seed.
func spendRemainder(w *sim.World, base *sim.Base, left int) {
	if base.Inventory == nil {
		base.Inventory = map[sim.Variant]int{}
	}
	cat := sim.Catalogue()
	fits := make([]sim.Component, 0, len(cat))
	for {
		fits = fits[:0]
		for _, c := range cat {
			if c.Value > 0 && c.Value <= left {
				fits = append(fits, c)
			}
		}
		if len(fits) == 0 {
			return
		}
		c := fits[w.Rand().Intn(len(fits))]
		base.Inventory[c.Variant]++
		left -= c.Value
	}
}
