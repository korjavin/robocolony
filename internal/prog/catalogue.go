package prog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/korjavin/robocolony/internal/sim"
)

// ArgKind is the parameter a predicate or action takes.
type ArgKind string

const (
	ArgNone    ArgKind = "none"
	ArgPoint   ArgKind = "point"   // 1..sim.MemPoints (design §7.4)
	ArgPercent ArgKind = "percent" // 0..100
)

// check validates an argument value, returning "" when it is in range.
func (k ArgKind) check(v int) string {
	switch k {
	case ArgPoint:
		if v < 1 || v > sim.MemPoints {
			return fmt.Sprintf("point argument must be 1..%d, got %d", sim.MemPoints, v)
		}
	case ArgPercent:
		if v < 0 || v > 100 {
			return fmt.Sprintf("percent argument must be 0..100, got %d", v)
		}
	default:
		if v != 0 {
			return fmt.Sprintf("takes no argument, got %d", v)
		}
	}
	return ""
}

// kindList serializes component kinds by name so the editor does not have to
// know sim's enum values.
type kindList []sim.ComponentKind

func (l kindList) MarshalJSON() ([]byte, error) {
	names := make([]string, len(l))
	for i, k := range l {
		names[i] = k.String()
	}
	return json.Marshal(names)
}

// PredicateSpec is one row of the design §10.3 condition catalogue.
type PredicateSpec struct {
	ID    PredicateID `json:"id"`
	Group string      `json:"group"`
	Label string      `json:"label"`
	// Desc is the player-facing meaning, one or two sentences. It lives here
	// rather than in the editor's JavaScript so it cannot drift from the
	// evaluator that implements it; TestCatalogueIsDocumented keeps it filled.
	Desc string  `json:"desc"`
	Arg  ArgKind `json:"arg"`
	// World marks a predicate something outside the robot can make true while
	// the robot does nothing at all: what walks into view, what radar picks up,
	// what a colony mate broadcasts. The rest answer from the robot's own
	// state — cargo, health, hardware, memory points, standing at its own base
	// — or only from what the robot itself did, which for a robot that is not
	// acting is the same thing.
	//
	// warnInertStart uses the split to tell a program merely waiting for a
	// stimulus from one nothing outside it can ever unblock. It is a
	// per-predicate label, not reachability analysis.
	World bool `json:"world"`
	// Needs lists components without which the predicate can never be true.
	// A missing component here is a warning, not an error: testing a sensor
	// you do not have is legal, just pointless.
	Needs kindList `json:"needs,omitempty"`
}

// ActionSpec is one row of the design §10.4 action catalogue.
type ActionSpec struct {
	ID    ActionID `json:"id"`
	Group string   `json:"group"`
	Label string   `json:"label"`
	Desc  string   `json:"desc"` // see PredicateSpec.Desc
	Arg   ArgKind  `json:"arg"`
	// Primary implements the locked rule action model (AGENTS.md): a primary
	// action ends the tick, a side effect executes and evaluation continues
	// down the rule list.
	Primary bool `json:"primary"`
	// Needs lists components the action physically requires. Missing = error.
	Needs kindList `json:"needs,omitempty"`
}

// Catalogue is the whole language, serializable straight to the editor UI so
// the two cannot drift.
type Catalogue struct {
	V          int             `json:"v"`
	Predicates []PredicateSpec `json:"predicates"`
	Actions    []ActionSpec    `json:"actions"`
}

// Predicate identifiers. Every row of design §10.3 is here, including ones
// whose sensor the POC does not implement yet: a saved program must keep
// parsing when E5/E7 wire the behaviour up.
const (
	CarryingComponent PredicateID = "carrying_component"
	CarryingNothing   PredicateID = "carrying_nothing"
	HealthBelow       PredicateID = "health_below"
	HealthAbove       PredicateID = "health_above"

	AtOwnBase    PredicateID = "at_own_base"
	AtPoint      PredicateID = "at_point"
	PointIsSet   PredicateID = "point_is_set"
	PointIsEmpty PredicateID = "point_is_empty"

	SeesComponent            PredicateID = "sees_component"
	ComponentInReach         PredicateID = "component_in_reach"
	SeesEnemyRobot           PredicateID = "sees_enemy_robot"
	SeesObstacle             PredicateID = "sees_obstacle"
	VisibleTargetInWpnRange  PredicateID = "visible_target_in_weapon_range"
	RadarDetectsTarget       PredicateID = "radar_detects_target"
	DetectedTargetInWpnRange PredicateID = "detected_target_in_weapon_range"

	ReceivedComeHere  PredicateID = "received_come_here"
	ReceivedAvoidHere PredicateID = "received_avoid_here"

	PathBlocked       PredicateID = "path_blocked"
	TargetReached     PredicateID = "target_reached"
	TargetUnreachable PredicateID = "target_unreachable"

	WeaponReady  PredicateID = "weapon_ready"
	HasWeapon    PredicateID = "has_weapon"
	EnemyVisible PredicateID = "enemy_visible"
)

// Action identifiers, design §10.4.
const (
	MoveForward ActionID = "move_forward"
	TurnLeft    ActionID = "turn_left"
	TurnRight   ActionID = "turn_right"
	TurnRandom  ActionID = "turn_random"
	Stop        ActionID = "stop"

	MoveToOwnBase       ActionID = "move_to_own_base"
	MoveToVisibleTarget ActionID = "move_to_visible_target"
	MoveToRadarTarget   ActionID = "move_to_radar_target"
	MoveToPoint         ActionID = "move_to_point"
	MoveAwayFromTarget  ActionID = "move_away_from_target"
	MoveAwayFromPoint   ActionID = "move_away_from_point"

	PickUpComponent        ActionID = "pick_up_component"
	DepositComponentAtBase ActionID = "deposit_component_at_base"
	DropComponent          ActionID = "drop_component"

	AttackVisibleTarget ActionID = "attack_visible_target"
	AttackRadarTarget   ActionID = "attack_radar_target"

	SaveCurrentPosition ActionID = "save_current_position"
	SaveVisibleTarget   ActionID = "save_visible_target"
	SaveRadarTarget     ActionID = "save_radar_target"
	SaveSignalPosition  ActionID = "save_signal_position"
	ClearPoint          ActionID = "clear_point"

	BroadcastComeHere  ActionID = "broadcast_come_here"
	BroadcastAvoidHere ActionID = "broadcast_avoid_here"
)

// Catalogue groups.
const (
	GroupSelf          = "self"
	GroupLocation      = "location"
	GroupVision        = "vision"
	GroupRadar         = "radar"
	GroupCommunication = "communication"
	GroupReachability  = "reachability"
	GroupCombat        = "combat"
	GroupMovement      = "movement"
	GroupNavigation    = "navigation"
	GroupInteraction   = "interaction"
	GroupMemory        = "memory"
)

// predicates is data, not a switch. Adding a predicate is adding a row. The
// World column is the world-observable / self-state split warnInertStart reads.
var predicates = []PredicateSpec{
	{CarryingComponent, GroupSelf, "Carrying a component",
		"True while the robot is holding a component. It can hold only one at a time.", ArgNone, false, nil},
	{CarryingNothing, GroupSelf, "Carrying nothing",
		"True while the robot's hands are empty. This is how a fresh robot starts.", ArgNone, false, nil},
	{HealthBelow, GroupSelf, "Health below %",
		"True once damage has taken the robot below this share of the health it was built with.", ArgPercent, false, nil},
	{HealthAbove, GroupSelf, "Health above %",
		"True while the robot still has more than this share of the health it was built with.", ArgPercent, false, nil},

	{AtOwnBase, GroupLocation, "At own base",
		"True while the robot stands within reach of its own base — where it can deposit cargo.", ArgNone, false, nil},
	{AtPoint, GroupLocation, "At point",
		"True while the robot stands on the coordinate remembered in this point. False whenever the point is empty.", ArgPoint, false, nil},
	{PointIsSet, GroupLocation, "Point is set",
		"True when this memory point holds a coordinate. All three points start empty and are wiped on reprogramming.", ArgPoint, false, nil},
	{PointIsEmpty, GroupLocation, "Point is empty",
		"True when this memory point holds nothing — the opposite of Point is set.", ArgPoint, false, nil},

	{SeesComponent, GroupVision, "Sees a component",
		"True when a loose component is anywhere in the forward vision cone, however far away.", ArgNone, true, nil},
	{ComponentInReach, GroupVision, "Component in reach",
		"True only when a loose component is on this cell or right next to it — close enough to pick up.", ArgNone, true, nil},
	{SeesEnemyRobot, GroupVision, "Sees an enemy robot",
		"True when a robot of another colony is in the forward vision cone.", ArgNone, true, nil},
	{SeesObstacle, GroupVision, "Sees an obstacle",
		"True when the cell straight ahead cannot be entered. Pair it with a turn or the robot jams.", ArgNone, true, nil},
	{VisibleTargetInWpnRange, GroupVision, "Visible target in weapon range",
		"True when the nearest robot seen ahead is close enough for a loaded weapon to hit.", ArgNone, true, kindList{sim.KindWeapon}},

	{RadarDetectsTarget, GroupRadar, "Radar detects a target",
		"True when the radar reports anything at all. Radar sees in every direction, not just ahead.", ArgNone, true, kindList{sim.KindRadar}},
	{DetectedTargetInWpnRange, GroupRadar, "Detected target in weapon range",
		"True when the nearest enemy robot on radar is close enough for a loaded weapon to hit.", ArgNone, true, kindList{sim.KindRadar, sim.KindWeapon}},

	{ReceivedComeHere, GroupCommunication, "Received COME_HERE",
		"True for the one tick after a colony mate broadcast COME_HERE. Signals are not remembered — save the position in the same rule or it is gone.", ArgNone, true, nil},
	{ReceivedAvoidHere, GroupCommunication, "Received AVOID_HERE",
		"True for the one tick after a colony mate broadcast AVOID_HERE. Not remembered either.", ArgNone, true, nil},

	// The reachability group looks like world observation but is not: sim raises
	// all three only as a consequence of a move the robot itself attempted
	// (internal/sim/tick.go step and moveTo). A robot that matches no rule never
	// moves, so no amount of world change sets them — self-state, for the one
	// question World is asked.
	{PathBlocked, GroupReachability, "Path blocked",
		"True when the robot's last attempt to move was refused, usually by a wall or another robot.", ArgNone, false, nil},
	{TargetReached, GroupReachability, "Target reached",
		"True on the tick the robot arrives where its last move-to action was heading.", ArgNone, false, nil},
	{TargetUnreachable, GroupReachability, "Target unreachable",
		"True when the last move-to action found no route to its destination at all.", ArgNone, false, nil},

	// weapon_ready and has_weapon read the robot's own hardware, not the world.
	{WeaponReady, GroupCombat, "Weapon ready",
		"True while at least one installed weapon has finished reloading.", ArgNone, false, kindList{sim.KindWeapon}},
	// has_weapon is the introspection predicate: false on an unarmed
	// blueprint is the answer, not a mistake, so it declares no Needs.
	{HasWeapon, GroupCombat, "Has a weapon",
		"True when this blueprint carries a weapon at all. False is a legitimate answer, not a mistake.", ArgNone, false, nil},
	{EnemyVisible, GroupCombat, "Enemy visible",
		"The same test as Sees an enemy robot, spelled the way the combat rules read.", ArgNone, true, nil},
}

// actions is data, not a switch. The Primary column is the locked rule action
// model: exactly the memory and communication rows are side effects.
var actions = []ActionSpec{
	{MoveForward, GroupMovement, "Move forward",
		"Step one cell in the direction the robot is facing.", ArgNone, true, nil},
	{TurnLeft, GroupMovement, "Turn left",
		"Turn one step anticlockwise without moving.", ArgNone, true, nil},
	{TurnRight, GroupMovement, "Turn right",
		"Turn one step clockwise without moving.", ArgNone, true, nil},
	{TurnRandom, GroupMovement, "Turn randomly",
		"Face a random direction. The usual way out of a corner or off a wall.", ArgNone, true, nil},
	{Stop, GroupMovement, "Stop",
		"Stand still this tick, abandoning any navigation already under way.", ArgNone, true, nil},

	{MoveToOwnBase, GroupNavigation, "Move to own base",
		"Take one step along the route home. Idles if the robot has no base.", ArgNone, true, nil},
	{MoveToVisibleTarget, GroupNavigation, "Move to visible target",
		"Take one step towards the nearest thing seen ahead, component or enemy. Idles if nothing is in sight.", ArgNone, true, nil},
	{MoveToRadarTarget, GroupNavigation, "Move to radar target",
		"Take one step towards the nearest radar contact. Idles if radar reports nothing.", ArgNone, true, kindList{sim.KindRadar}},
	{MoveToPoint, GroupNavigation, "Move to point",
		"Take one step along the route to the coordinate remembered in this point. Idles while the point is empty.", ArgPoint, true, nil},
	{MoveAwayFromTarget, GroupNavigation, "Move away from target",
		"Take one step directly away from the nearest thing seen ahead. The retreat action.", ArgNone, true, nil},
	{MoveAwayFromPoint, GroupNavigation, "Move away from point",
		"Take one step directly away from the coordinate remembered in this point.", ArgPoint, true, nil},

	{PickUpComponent, GroupInteraction, "Pick up component",
		"Pick up a loose component on this cell or next to it. Needs empty hands and something in reach.", ArgNone, true, kindList{sim.KindManipulator}},
	{DepositComponentAtBase, GroupInteraction, "Deposit component at base",
		"Hand the carried component to own base and score its value. Only works while at the base.", ArgNone, true, kindList{sim.KindManipulator}},
	{DropComponent, GroupInteraction, "Drop component",
		"Put the carried component down here, leaving it loose for anyone to take.", ArgNone, true, kindList{sim.KindManipulator}},

	{AttackVisibleTarget, GroupCombat, "Attack visible target",
		"Fire on the nearest enemy robot seen ahead. Loose components are never targets.", ArgNone, true, kindList{sim.KindWeapon}},
	{AttackRadarTarget, GroupCombat, "Attack radar target",
		"Fire on the nearest enemy robot the radar reports, even out of sight.", ArgNone, true, kindList{sim.KindRadar, sim.KindWeapon}},

	{SaveCurrentPosition, GroupMemory, "Save current position",
		"Remember where the robot is standing in this point, replacing whatever it held.", ArgPoint, false, nil},
	{SaveVisibleTarget, GroupMemory, "Save visible target",
		"Remember where the nearest thing seen ahead is. Writes nothing when nothing is in sight.", ArgPoint, false, nil},
	{SaveRadarTarget, GroupMemory, "Save radar target",
		"Remember where the nearest radar contact is. Writes nothing when radar reports nothing.", ArgPoint, false, kindList{sim.KindRadar}},
	{SaveSignalPosition, GroupMemory, "Save signal position",
		"Remember where the signal this rule matched came from — the only way to keep it past this tick.", ArgPoint, false, nil},
	{ClearPoint, GroupMemory, "Clear point",
		"Forget this point, leaving it empty again.", ArgPoint, false, nil},

	{BroadcastComeHere, GroupCommunication, "Broadcast COME_HERE",
		"Call the whole colony to this position. Every colony mate hears it on the next tick.", ArgNone, false, nil},
	{BroadcastAvoidHere, GroupCommunication, "Broadcast AVOID_HERE",
		"Warn the whole colony away from this position. Heard on the next tick.", ArgNone, false, nil},
}

// Language returns the full catalogue, ready to serialize to the editor.
func Language() Catalogue {
	return Catalogue{V: SchemaVersion, Predicates: slices.Clone(predicates), Actions: slices.Clone(actions)}
}

// LookupPredicate returns the spec for an identifier.
func LookupPredicate(id PredicateID) (PredicateSpec, bool) {
	for _, s := range predicates {
		if s.ID == id {
			return s, true
		}
	}
	return PredicateSpec{}, false
}

// LookupAction returns the spec for an identifier.
func LookupAction(id ActionID) (ActionSpec, bool) {
	for _, s := range actions {
		if s.ID == id {
			return s, true
		}
	}
	return ActionSpec{}, false
}

// missing returns the components in needs that the blueprint does not carry.
func missing(needs kindList, b sim.Blueprint) []string {
	var out []string
	for _, k := range needs {
		if !b.Has(k) {
			out = append(out, k.String())
		}
	}
	return out
}

func ordinal(i int) string { return strconv.Itoa(i + 1) }
