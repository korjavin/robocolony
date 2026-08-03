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
// (design §5.1 has the player approve one or more): one scavenger per
// (locomotion, armor) pair in the catalogue. That is the point of the set — the
// §5.2 build scan needs a blueprint's exact components, so a colony approving
// only the medium-armor tracks variant stalls forever the moment either medium
// armor or tracks runs out, however much light armor and how many legs it has
// scavenged. A locomotion or armor row added to the catalogue gets starter
// blueprints here for free.
//
// Nine blueprints, and §5.2 picks uniformly among the *buildable* ones, so the
// count only widens what a colony can do with what it actually holds: a colony
// with one armor tier and one locomotion unit in stock still has exactly one
// choice. What it buys is that no single component ever stalls production.
func DefaultBlueprints() []sim.Blueprint { return scavengerKit() }

// scavengerKit is the §10.7 scavenger body fanned out over the catalogue.
func scavengerKit() []sim.Blueprint {
	return fanOut(DefaultBlueprintID, "scavenger", DefaultProgramID, sim.Manipulator, sim.PartsRadar)
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

// DefaultProgram is the design §10.7 component scavenger, rule for rule.
func DefaultProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "component scavenger", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.AtOwnBase), prog.Pred(prog.CarryingComponent)),
			Then: []prog.Action{prog.Do(prog.DepositComponentAtBase)}},
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
}

// namedProgram is a program plus the runtime id blueprints refer to it by.
type namedProgram struct {
	id string
	p  prog.Program
}

// humanKit is the design §2.1 equal starting budget for a player's colony.
func humanKit() kit {
	return kit{
		blueprints: DefaultBlueprints(),
		programs:   []namedProgram{{DefaultProgramID, DefaultProgram()}},
		start:      repeat(DefaultBlueprint(), startingRobots),
	}
}

func repeat(bp sim.Blueprint, n int) []sim.Blueprint {
	out := make([]sim.Blueprint, n)
	for i := range out {
		out[i] = bp
	}
	return out
}

// equipColony approves a kit's blueprints at a base and puts its starting
// robots on the map. Everything random here draws from the world's own rng, so
// two matches with the same seed, member list and AI list start identical.
func equipColony(w *sim.World, base *sim.Base, k kit) {
	base.Blueprints = append(base.Blueprints, k.blueprints...)
	for _, bp := range k.start {
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
}
