package lobby

import (
	"strings"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// AI colonies, design §12 P2. A lobby may be filled out with computer colonies
// so that one player is still playing a game: competition for scarce parts,
// battlefields worth salvaging, and a reason to write a defensive program.
//
// The whole of an AI colony is the kit below — a canned blueprint set plus a
// program library. There is no AI controller, no AI tick hook and no AI branch
// anywhere in the simulation: an AI colony is driven by prog.Evaluator over the
// same rule language a player writes in, sees exactly what sim.RobotView gives
// any robot, and builds through the same design §5.2 production scan. Anything
// that gave one of these colonies knowledge a human colony's robots could not
// perceive would be a bug in this file, not a feature.
//
// Two consequences fall out for free:
//
//   - Replay (persist.go) needs nothing new. The profile list lives in the
//     lobby settings, which a restart re-reads, and everything downstream of it
//     is the deterministic evaluator. An AI colony makes no decision the seed
//     does not already determine, so it records no commands.
//   - Design §5.3 still holds. An AI base is as indestructible as a human one
//     and rebuilds from inventory; nothing here eliminates a colony.

// Profile is an AI colony's canned strategy. The strings are stored in the
// lobby settings JSON; never repurpose one.
//
// The four are a difficulty ladder, not a list, and the rungs are measured
// rather than asserted: each line below is 16 seeds × 6000 ticks of the profile
// against one colony running the default starter kit and the §10.7 program —
// the loadout a player actually begins with. "Wiped" is the human colony at
// zero live robots at any point; its base is indestructible either way (§5.3),
// so a wipe is a setback, not an elimination.
//
//	tutorial     0/16 wiped, 0 human losses, human out-collects it 1376 to 471
//	peaceful     0/16 wiped, 0 human losses, human out-collects it 1096 to 790
//	defensive    5/16 wiped, never before tick 4629, human still ahead 1174 to 766
//	aggressive  16/16 wiped, avg tick 1079, earliest 636, human behind 510 to 1111
//
// Retuning a profile means moving its rung, so re-measure when you do.
type Profile string

const (
	// ProfileTutorial is the sparring partner: two robots instead of three, a
	// radar-less body and a program that only chases what it can see. It
	// competes for parts without ever threatening anything — it carries no
	// weapon in any blueprint, so it cannot take a robot off the board at all.
	ProfileTutorial Profile = "tutorial"
	// ProfilePeaceful is the economic rival: the reference §10.7 scavenger
	// colony, unarmed, playing the same opening a human does. It is the first
	// profile that actually competes, and still the second that cannot fight.
	ProfilePeaceful Profile = "peaceful"
	// ProfileDefensive scavenges and keeps armed responders (§10.9) that its
	// scavengers can call in over the colony signal channel. It hurts a player
	// who walks into it and does not go looking — the wipes it manages land in
	// the last quarter of a match, not the first.
	ProfileDefensive Profile = "defensive"
	// ProfileAggressive scavenges to fund gunners that hunt on enemy radar. It
	// is brutal on purpose and it is the profile a player opts into: against
	// the unarmed starter kit it clears the board inside the first fifth of a
	// match, every seed. See the ladder above before softening anything here.
	ProfileAggressive Profile = "aggressive"
)

// Profiles is every profile a lobby may seat, in menu order.
func Profiles() []Profile {
	return []Profile{ProfileTutorial, ProfilePeaceful, ProfileDefensive, ProfileAggressive}
}

// Valid reports whether p names a profile this build knows.
func (p Profile) Valid() bool {
	_, ok := p.kit()
	return ok
}

// DisplayName is what the lobby and the match scoreboard call the colony.
func (p Profile) DisplayName() string {
	if p == "" {
		return "AI"
	}
	return strings.ToUpper(string(p[:1])) + string(p[1:]) + " AI"
}

// AI program ids. They live in the same runtime namespace as a player's
// installs, so they are prefixed rather than named after the design section.
const (
	foragerProgramID   = "prog-ai-forager"
	spotterProgramID   = "prog-ai-spotter"
	responderProgramID = "prog-ai-responder"
	hunterProgramID    = "prog-ai-hunter"
)

// AI blueprint id prefixes, fanned out over locomotion × armor like the starter
// kit and for the same reason: a colony that can only build one body stalls the
// moment one component row runs out.
const (
	foragerBlueprintID  = "bp-ai-forager"
	spotterBlueprintID  = "bp-ai-spotter"
	defenderBlueprintID = "bp-ai-defender"
	gunnerBlueprintID   = "bp-ai-gunner"
)

// kit returns the profile's starting equipment, or false for a profile this
// build does not know — a settings row written by a newer server, say.
func (p Profile) kit() (kit, bool) {
	switch p {
	case ProfileTutorial:
		// No radar and no parts radar to lose: a cheap body the colony can
		// always rebuild, driven by a program that only reacts to what is in
		// its own vision cone. It is beatable on purpose.
		bps := fanOut(foragerBlueprintID, "forager", foragerProgramID, sim.Manipulator)
		return kit{
			blueprints: bps,
			programs:   []namedProgram{{foragerProgramID, foragerProgram()}},
			start:      repeat(canonicalOf(foragerBlueprintID, bps), 2),
		}, true

	case ProfilePeaceful:
		// Exactly the human opening: same blueprints, same §10.7 program, same
		// three robots. The colony a solo player is actually racing.
		return humanKit(), true

	case ProfileDefensive:
		// Scavengers that shout when they see trouble, plus heavy responders
		// that come when called. The scavengers earn; the responders make the
		// colony's own half of the map expensive to walk into.
		scavs := fanOut(spotterBlueprintID, "spotter", spotterProgramID, sim.Manipulator, sim.PartsRadar)
		defs := fanOut(defenderBlueprintID, "defender", responderProgramID, sim.Laser, sim.PartsRadar)
		return kit{
			blueprints: append(scavs, defs...),
			programs: []namedProgram{
				{spotterProgramID, spotterProgram()},
				{responderProgramID, responderProgram()},
			},
			start: append(repeat(canonicalOf(spotterBlueprintID, scavs), 2),
				canonicalOf(defenderBlueprintID, defs)),
		}, true

	case ProfileAggressive:
		// Scavengers fund the colony, gunners spend it. The gunner needs a
		// laser and an enemy radar it does not start with, so an aggressive
		// colony has to scavenge before it can hunt — and §5.2's random pick
		// among buildable blueprints then mixes the two on its own.
		scavs := scavengerKit()
		guns := fanOut(gunnerBlueprintID, "gunner", hunterProgramID, sim.Laser, sim.EnemyRadar)
		return kit{
			blueprints: append(scavs, guns...),
			programs: []namedProgram{
				{DefaultProgramID, DefaultProgram()},
				{hunterProgramID, hunterProgram()},
			},
			start: append(repeat(DefaultBlueprint(), 2), canonicalOf(gunnerBlueprintID, guns)),
		}, true
	}
	return kit{}, false
}

// foragerProgram is design §10.7 with the radar rule replaced by a vision rule:
// the same shape a player writes before they can afford a parts radar, and the
// handicap that makes the tutorial profile a tutorial.
func foragerProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "forager", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.AtOwnBase), prog.Pred(prog.CarryingComponent)),
			Then: []prog.Action{prog.Do(prog.DepositComponentAtBase)}},
		{When: prog.Pred(prog.CarryingComponent),
			Then: []prog.Action{prog.Do(prog.MoveToOwnBase)}},
		{When: prog.And(prog.Pred(prog.ComponentInReach), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.Do(prog.PickUpComponent)}},
		{When: prog.And(prog.Pred(prog.SeesComponent), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.Do(prog.MoveToVisibleTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}

// spotterProgram is design §10.7 with one rule in front of it: an unarmed
// scavenger that sees an enemy calls the colony to where it stands and carries
// on working. broadcast_come_here is a side effect under the locked action
// model, so the rule below it still runs in the same tick — this costs the
// scavenger nothing, and it is the signal responderProgram waits for.
func spotterProgram() prog.Program {
	p := DefaultProgram()
	p.Name = "spotter"
	p.Rules = append([]prog.Rule{{
		When: prog.Pred(prog.SeesEnemyRobot),
		Then: []prog.Action{prog.Do(prog.BroadcastComeHere)},
	}}, p.Rules...)
	return p
}

// responderProgram is design §10.9, the defensive responder, rule for rule,
// plus one rule the design section does not print: a final patrol step. §10.9
// as written has no rule a robot standing at a quiet base can match, so a
// responder with nothing to answer would stand still for the whole match. The
// patrol rule is the same carrying_nothing idiom §10.7 ends with — the
// responder carries no manipulator, so it is simply "otherwise, walk".
func responderProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "defensive responder", Rules: []prog.Rule{
		{When: prog.And(prog.PredArg(prog.HealthBelow, 25), prog.Pred(prog.SeesEnemyRobot)),
			Then: []prog.Action{prog.Do(prog.MoveAwayFromTarget)}},
		{When: prog.Pred(prog.ReceivedComeHere),
			Then: []prog.Action{prog.DoArg(prog.SaveSignalPosition, 2)}},
		{When: prog.And(prog.Pred(prog.SeesEnemyRobot), prog.Pred(prog.VisibleTargetInWpnRange)),
			Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
		{When: prog.PredArg(prog.AtPoint, 2),
			Then: []prog.Action{prog.DoArg(prog.ClearPoint, 2)}},
		{When: prog.PredArg(prog.PointIsSet, 2),
			Then: []prog.Action{prog.DoArg(prog.MoveToPoint, 2)}},
		{When: prog.Pred(prog.RadarDetectsTarget),
			Then: []prog.Action{prog.Do(prog.MoveToRadarTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}

// hunterProgram is the aggressive profile's half of the language the worked
// examples never reach: weapons and attack rules. It runs on a body with an
// enemy radar, so every radar contact is an enemy robot — there is no rule here
// that needs to tell a contact's kind apart, and none that could see anything a
// player's own gunner could not.
//
// Shoot what is in range first (visible, then radar), close on radar contacts
// otherwise, and patrol when the map is quiet. move_to_visible_target is
// deliberately absent: radar range is longer than the vision cone and reports
// only robots, so a visual approach rule would only ever pull the gunner
// towards loose components it has no manipulator to pick up.
func hunterProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "hunter", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.SeesEnemyRobot), prog.Pred(prog.VisibleTargetInWpnRange)),
			Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
		{When: prog.Pred(prog.DetectedTargetInWpnRange),
			Then: []prog.Action{prog.Do(prog.AttackRadarTarget)}},
		{When: prog.Pred(prog.RadarDetectsTarget),
			Then: []prog.Action{prog.Do(prog.MoveToRadarTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}
