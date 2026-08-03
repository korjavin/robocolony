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
	Arg   ArgKind     `json:"arg"`
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

// predicates is data, not a switch. Adding a predicate is adding a row.
var predicates = []PredicateSpec{
	{CarryingComponent, GroupSelf, "Carrying a component", ArgNone, nil},
	{CarryingNothing, GroupSelf, "Carrying nothing", ArgNone, nil},
	{HealthBelow, GroupSelf, "Health below %", ArgPercent, nil},
	{HealthAbove, GroupSelf, "Health above %", ArgPercent, nil},

	{AtOwnBase, GroupLocation, "At own base", ArgNone, nil},
	{AtPoint, GroupLocation, "At point", ArgPoint, nil},
	{PointIsSet, GroupLocation, "Point is set", ArgPoint, nil},
	{PointIsEmpty, GroupLocation, "Point is empty", ArgPoint, nil},

	{SeesComponent, GroupVision, "Sees a component", ArgNone, nil},
	{ComponentInReach, GroupVision, "Component in reach", ArgNone, nil},
	{SeesEnemyRobot, GroupVision, "Sees an enemy robot", ArgNone, nil},
	{SeesObstacle, GroupVision, "Sees an obstacle", ArgNone, nil},
	{VisibleTargetInWpnRange, GroupVision, "Visible target in weapon range", ArgNone, kindList{sim.KindWeapon}},

	{RadarDetectsTarget, GroupRadar, "Radar detects a target", ArgNone, kindList{sim.KindRadar}},
	{DetectedTargetInWpnRange, GroupRadar, "Detected target in weapon range", ArgNone, kindList{sim.KindRadar, sim.KindWeapon}},

	{ReceivedComeHere, GroupCommunication, "Received COME_HERE", ArgNone, nil},
	{ReceivedAvoidHere, GroupCommunication, "Received AVOID_HERE", ArgNone, nil},

	{PathBlocked, GroupReachability, "Path blocked", ArgNone, nil},
	{TargetReached, GroupReachability, "Target reached", ArgNone, nil},
	{TargetUnreachable, GroupReachability, "Target unreachable", ArgNone, nil},

	{WeaponReady, GroupCombat, "Weapon ready", ArgNone, kindList{sim.KindWeapon}},
	// has_weapon is the introspection predicate: false on an unarmed
	// blueprint is the answer, not a mistake, so it declares no Needs.
	{HasWeapon, GroupCombat, "Has a weapon", ArgNone, nil},
	{EnemyVisible, GroupCombat, "Enemy visible", ArgNone, nil},
}

// actions is data, not a switch. The Primary column is the locked rule action
// model: exactly the memory and communication rows are side effects.
var actions = []ActionSpec{
	{MoveForward, GroupMovement, "Move forward", ArgNone, true, nil},
	{TurnLeft, GroupMovement, "Turn left", ArgNone, true, nil},
	{TurnRight, GroupMovement, "Turn right", ArgNone, true, nil},
	{TurnRandom, GroupMovement, "Turn randomly", ArgNone, true, nil},
	{Stop, GroupMovement, "Stop", ArgNone, true, nil},

	{MoveToOwnBase, GroupNavigation, "Move to own base", ArgNone, true, nil},
	{MoveToVisibleTarget, GroupNavigation, "Move to visible target", ArgNone, true, nil},
	{MoveToRadarTarget, GroupNavigation, "Move to radar target", ArgNone, true, kindList{sim.KindRadar}},
	{MoveToPoint, GroupNavigation, "Move to point", ArgPoint, true, nil},
	{MoveAwayFromTarget, GroupNavigation, "Move away from target", ArgNone, true, nil},
	{MoveAwayFromPoint, GroupNavigation, "Move away from point", ArgPoint, true, nil},

	{PickUpComponent, GroupInteraction, "Pick up component", ArgNone, true, kindList{sim.KindManipulator}},
	{DepositComponentAtBase, GroupInteraction, "Deposit component at base", ArgNone, true, kindList{sim.KindManipulator}},
	{DropComponent, GroupInteraction, "Drop component", ArgNone, true, kindList{sim.KindManipulator}},

	{AttackVisibleTarget, GroupCombat, "Attack visible target", ArgNone, true, kindList{sim.KindWeapon}},
	{AttackRadarTarget, GroupCombat, "Attack radar target", ArgNone, true, kindList{sim.KindRadar, sim.KindWeapon}},

	{SaveCurrentPosition, GroupMemory, "Save current position", ArgPoint, false, nil},
	{SaveVisibleTarget, GroupMemory, "Save visible target", ArgPoint, false, nil},
	{SaveRadarTarget, GroupMemory, "Save radar target", ArgPoint, false, kindList{sim.KindRadar}},
	{SaveSignalPosition, GroupMemory, "Save signal position", ArgPoint, false, nil},
	{ClearPoint, GroupMemory, "Clear point", ArgPoint, false, nil},

	{BroadcastComeHere, GroupCommunication, "Broadcast COME_HERE", ArgNone, false, nil},
	{BroadcastAvoidHere, GroupCommunication, "Broadcast AVOID_HERE", ArgNone, false, nil},
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
