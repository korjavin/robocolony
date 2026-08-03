package lobby

import (
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

// DefaultBlueprint is the blueprint every colony starts with and keeps
// approved for production (design §5.1).
//
// The parts radar is not decoration: without one, prog.Validate rejects
// move_to_radar_target, and DefaultProgram — design §10.7 — degenerates into a
// blind random walk without it.
func DefaultBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         DefaultBlueprintID,
		Name:       "scavenger",
		Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Manipulator, sim.PartsRadar},
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

// equipColony approves the starting blueprint at a base and puts its starting
// robots on the map. Everything random here draws from the world's own rng, so
// two matches with the same seed and member count start identical.
func equipColony(w *sim.World, base *sim.Base) {
	bp := DefaultBlueprint()
	base.Blueprints = append(base.Blueprints, bp)
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
