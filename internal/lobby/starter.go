package lobby

import (
	"fmt"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The POC starting kit. Design §2.1 has every player prepare a colony from an
// equal budget out of their own library; E6.1 builds that library, so until
// then every colony starts from the same blueprint and the same robot count,
// which is equal by construction.
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

// defaultArmor is the tier the canonical starter blueprint wears; the other
// tiers come from the catalogue in DefaultBlueprints.
const defaultArmor = sim.MediumArmor

// DefaultBlueprint is the canonical starting blueprint: the one the library
// seeds and the one a program is checked against before anyone edits a design.
//
// The parts radar is not decoration: without one, prog.Validate rejects
// move_to_radar_target, and DefaultProgram — design §10.7 — degenerates into a
// blind random walk without it.
func DefaultBlueprint() sim.Blueprint { return scavenger(defaultArmor) }

// DefaultBlueprints is the *set* a colony starts with approved for production
// (design §5.1 has the player approve one or more): one scavenger per armor
// tier in the catalogue. That is the point of the set — the §5.2 build scan
// needs a blueprint's exact components, so a colony approving only the medium
// variant stalls forever the moment medium armor runs out, however much light
// and heavy armor it has scavenged. An armor row added to the catalogue gets a
// starter blueprint here for free.
func DefaultBlueprints() []sim.Blueprint {
	var out []sim.Blueprint
	for _, c := range sim.Catalogue() {
		if c.Kind == sim.KindArmor {
			out = append(out, scavenger(c.Variant))
		}
	}
	return out
}

// scavenger is the §10.7 scavenger body around one armor variant. The id is
// keyed on the variant number, which the catalogue promises never to reuse.
func scavenger(armor sim.Variant) sim.Blueprint {
	id, name := DefaultBlueprintID, "scavenger"
	if armor != defaultArmor {
		id, name = fmt.Sprintf("%s-%d", DefaultBlueprintID, armor), armor.String()+" scavenger"
	}
	return sim.Blueprint{
		ID:         id,
		Name:       name,
		Components: []sim.Variant{sim.Tracks, armor, sim.Manipulator, sim.PartsRadar},
		ProgramID:  DefaultProgramID,
	}
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

// equipColony approves the starting blueprints at a base and puts its starting
// robots on the map. Everything random here draws from the world's own rng, so
// two matches with the same seed and member count start identical.
func equipColony(w *sim.World, base *sim.Base) {
	base.Blueprints = append(base.Blueprints, DefaultBlueprints()...)
	bp := DefaultBlueprint()
	for i := 0; i < startingRobots; i++ {
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
