package prog

import "github.com/korjavin/robocolony/internal/sim"

// Evaluator runs one Program as a sim.Controller (design §10.5).
//
// Semantics, all locked before this file was written:
//   - rules evaluate in visible top-to-bottom order;
//   - a matching rule executes every action in Then, in slice order;
//   - a rule holding a primary action ends the tick — evaluation stops there;
//   - a rule holding only side effects (memory writes, broadcasts) executes
//     them and evaluation continues down the list (AGENTS.md, design §10.8);
//   - nothing matched, or the matched rule's target does not exist: the robot
//     idles, unless the deposit reflex below has something to do with the tick.
//     Programs are untrusted input, so no path here panics.
//
// Work per tick is bounded: the rule list is walked at most once, and each
// condition walk is depth-capped. A program cannot make a tick do unbounded
// work no matter how it is shaped.
type Evaluator struct {
	program   Program
	programID string
	revision  int
	trace     Trace

	// hist is the retained trace ring for a robot someone is watching, and nil
	// — the normal case — for every other robot. See history.go: recording is
	// opt-in precisely so that a fleet nobody has selected costs nothing.
	hist *history

	// explain is the last tick's condition table, recorded only while hist is
	// non-nil. Overwritten per tick like trace, and for the same reason: it is
	// the answer to "why now", not an archive. See explain.go.
	explain Explanation
}

// Trace is which rule controlled the tick and why. Deliberately fixed-size:
// E4.3 renders the live one and E7.4 builds history on top of it, so a growing
// per-robot log here would be a leak in a long match.
type Trace struct {
	Tick   uint64   `json:"tick"`
	Rule   int      `json:"rule"`         // 0-based rule that took the tick, -1 when none did
	Action ActionID `json:"action"`       // primary action chosen, empty when idle
	Reason string   `json:"reason"`       // short human phrase for the observer
	Side   int      `json:"side_effects"` // zero-tick actions executed this tick
	Idle   bool     `json:"idle"`         // the tick produced no primary action

	// matched is one bit per rule index that matched this tick, read through
	// Matched. Rule alone names only the rule that *ended* the tick, so a rule
	// of nothing but zero-tick side effects — design §10.8 rules 1 and 5 — ran
	// every tick and looked dead to every observer.
	//
	// A bitset keeps Trace what E3.2 made it: a fixed-size struct overwritten
	// per tick. 32 bytes covers all MaxRules however long the match runs, where
	// a slice would grow with it. Unexported, so it stays off the wire frame —
	// E7.4 owns retained history and can export a view of this when it needs
	// one.
	matched [(MaxRules + 63) / 64]uint64
}

// Matched reports whether the rule at this index matched during the traced
// tick. True for a side-effect-only rule that ran and let evaluation continue,
// as well as for the rule that took the tick.
func (t Trace) Matched(rule int) bool {
	if rule < 0 || rule >= MaxRules {
		return false
	}
	return t.matched[rule/64]&(1<<uint(rule%64)) != 0
}

const reasonNoMatch = "no rule matched"

// reasonReflexDeposit is what the match inspector shows for a tick the deposit
// reflex took. A reflex tick is emphatically not "no rule matched": the robot
// did something, and the player has to be able to read why from the trace
// without knowing this file exists.
const reasonReflexDeposit = "at own base carrying a component: depositing is automatic"

// New wraps a program in an evaluator. It truncates an over-long rule list
// rather than rejecting it: Decode and Validate are where a bad program is
// reported, and a controller must never be the thing that stalls a tick.
func New(p Program) *Evaluator {
	if len(p.Rules) > MaxRules {
		p.Rules = p.Rules[:MaxRules]
	}
	return &Evaluator{program: p, trace: Trace{Rule: -1, Reason: reasonNoMatch, Idle: true}}
}

// Program returns the program being evaluated.
func (e *Evaluator) Program() Program { return e.program }

// Trace returns the last decision's explanation.
func (e *Evaluator) Trace() Trace { return e.trace }

// ClearMemory wipes all three coordinate points. Design §10.6: memory is
// cleared whenever the robot is reprogrammed at its base. Runtime.Control calls
// it on a program swap; E6.3 may call it directly.
func ClearMemory(r *sim.Robot) { r.Memory = [sim.MemPoints]sim.MemPoint{} }

// Decide implements sim.Controller.
func (e *Evaluator) Decide(v sim.RobotView) sim.Action {
	var act sim.Action
	tr := Trace{Tick: v.Tick, Rule: -1, Reason: reasonNoMatch, Idle: true}

	for i := range e.program.Rules {
		rule := &e.program.Rules[i]
		m := matcher{v: v}
		if !m.cond(rule.When, 0) {
			continue
		}
		tr.matched[i/64] |= 1 << uint(i%64)

		// Every action of the matched rule runs, in slice order. The primary
		// one ends the tick wherever it sits, so side effects after it still
		// execute — they are zero-tick and sim applies them before the primary
		// anyway.
		var primary *Action
		for j := range rule.Then {
			a := rule.Then[j]
			spec, ok := LookupAction(a.Do)
			if !ok {
				continue // unknown action: Validate reports it, evaluation ignores it
			}
			if spec.Primary {
				if primary == nil {
					primary = &rule.Then[j]
				}
				continue
			}
			if sideEffect(&act, a, v, m.signal) {
				tr.Side++
			}
		}
		if primary == nil {
			continue // side effects only: keep scanning (AGENTS.md action model)
		}

		kind, coord, why := resolvePrimary(*primary, v)
		tr.Rule, tr.Action, tr.Idle = i, primary.Do, kind == sim.ActNone
		if why == "" {
			why = actionLabel(primary.Do)
		}
		tr.Reason = why
		act.Kind, act.Coord = kind, coord
		break
	}

	if idleAtOwnBaseWithCargo(v, act) {
		act.Kind, act.Coord = sim.ActDeposit, sim.Coord{}
		// Rule goes back to -1: no rule did this. Leaving it pointing at the
		// rule whose action came to nothing would print "rule 3 deposited" in
		// the inspector over a rule that says move_to_point. The matched bitset
		// is untouched, so that rule still shows as having matched — which it
		// did — and the observer sees the reflex took the tick from it.
		tr.Rule, tr.Action, tr.Idle, tr.Reason = -1, DepositComponentAtBase, false, reasonReflexDeposit
	}

	e.trace = tr
	e.explainTick(v)
	e.record(tr, v, act)
	return act
}

// idleAtOwnBaseWithCargo reports whether this tick is one the deposit reflex
// should take: the robot is standing at its own base holding a component, and
// what the program chose to do with the tick accomplishes nothing.
//
// The reflex exists because depositing there is not a decision. A carried
// component is worth zero — sim.World.FleetValue sums *installed* components,
// not cargo — capacity is exactly one, and the base is the only thing that can
// take it. There is no program that wants to stand on its own base holding a
// component it will not put down, so making the player write a rule for it is
// ceremony (rc-tad.13). drop_component is the action for the interesting case,
// and it is a real action: an explicit rule still wins.
//
// "Accomplishes nothing" is the whole safety argument, and it is deliberately
// narrow:
//
//   - ActNone — nothing matched, or the matched rule's action had no target.
//     The tick was going to be spent idling.
//   - ActMoveTo the cell the robot already stands on, which sim resolves to a
//     TargetReached and an idle tick.
//   - ActMoveTo own base while already at own base. at_own_base is a radius
//     (sim's interact range), so "go home" from one cell out is a step that
//     changes nothing a program can want while carrying: the robot could
//     already deposit from where it stands. This is the clause that makes the
//     §10.7 scavenger without its deposit rule tick-for-tick identical to the
//     one with it.
//
// Anything else — a turn, an attack, a pick-up, a drop, stop, a move that goes
// somewhere — is a decision the program made and the reflex keeps its hands
// off. stop in particular: a rule that says stand still said it on purpose.
//
// The manipulator check is not decoration. sim refuses a deposit without one,
// and an unarmed-handed robot would otherwise burn the longer interact tick
// cost every tick to accomplish exactly the idling it was already doing.
//
// Scope: this is part of running a program, so a recalled robot — whose program
// sim suspends entirely (design §4.2, World.Step) — does not deposit on arrival
// and holds its cargo until it is reprogrammed. Deliberate. Making recall
// deposit would be a change to sim's recall contract and to the score of every
// match that ever used it, to save a robot one tick after a reprogram it is
// standing still for anyway.
//
// One accepted edge, in the ActMoveTo branch: sim would have set
// target_reached on a move to the cell the robot already stands on, and a
// deposit does not. So a program that both wastes a tick this way at its own
// base while carrying *and* reads target_reached afterwards loses that flag
// transition. It is left as is deliberately — preserving it would mean widening
// sim.Action so a controller can raise a perception flag without acting, which
// is a much larger change to a package under the determinism guard, in service
// of a program whose distinguishing behaviour is standing on its own base
// holding a component it never puts down.
//
// Reads nothing but the view: deterministic, and it cannot touch w.rng.
func idleAtOwnBaseWithCargo(v sim.RobotView, act sim.Action) bool {
	if v.Cargo == sim.VariantNone || !v.AtBase || !v.Blueprint.Has(sim.KindManipulator) {
		return false
	}
	switch act.Kind {
	case sim.ActNone:
		return true
	case sim.ActMoveTo:
		return act.Coord == v.Coord || (v.HasBase && act.Coord == v.Base)
	}
	return false
}

// matcher evaluates one rule's condition against one view. It also remembers
// the signal a received_* predicate matched on, so save_signal_position in the
// same rule stores that signal's origin rather than an unrelated one (design
// §10.9 rule 2).
type matcher struct {
	v      sim.RobotView
	signal *sim.Signal
	// anyWorld reads every world-observable predicate as true, leaving the
	// self-state ones answering from v. Decide never sets it; warnInertStart
	// does, to ask "could anything outside the robot make this rule match?"
	// without enumerating world states.
	anyWorld bool
	// neg is the polarity of the node being walked: true inside an odd number
	// of NOTs. It exists only for anyWorld — see pred. Decide's answer is
	// exact and needs no polarity.
	neg bool
}

// cond walks the condition tree. Depth is capped exactly as Validate caps it,
// so a hand-built program that never went through Decode still cannot blow the
// stack; over-deep branches read as false, which is the safe-failure answer.
func (m *matcher) cond(c Condition, depth int) bool {
	if depth >= MaxCondDepth {
		return false
	}
	switch c.Op {
	case OpPred:
		return m.pred(c.Pred, c.Arg)
	case OpAnd:
		if len(c.Of) == 0 {
			return false // structurally invalid; never silently true
		}
		for _, k := range c.Of {
			if !m.cond(k, depth+1) {
				return false
			}
		}
		return true
	case OpOr:
		for _, k := range c.Of {
			if m.cond(k, depth+1) {
				return true
			}
		}
		return false
	case OpNot:
		if len(c.Of) != 1 {
			return false // structurally invalid; never silently true
		}
		// The polarity flip is saved and restored around the recursion rather
		// than passed down, so a sibling of this NOT is unaffected by it.
		m.neg = !m.neg
		got := m.cond(c.Of[0], depth+1)
		m.neg = !m.neg
		return !got
	}
	return false
}

func (m *matcher) pred(id PredicateID, arg int) bool {
	// A world predicate whose sensor the blueprint lacks is not something the
	// world can deliver either — dead_predicate says so separately.
	//
	// Polarity decides which way "optimistic" points. Positive, the hopeful
	// answer is that the world supplies the sighting, so this reads true.
	// Under a NOT the hopeful answer is the opposite one — NOT sees_enemy_robot
	// is satisfied precisely when nothing is visible, which is what a clean
	// start already *is* — so the override steps aside and the clean-start view
	// answers, which is false for every world predicate. Forcing true here
	// instead would report a working "sees_component AND NOT sees_enemy_robot"
	// program as inert.
	if m.anyWorld && !m.neg {
		if spec, ok := LookupPredicate(id); ok && spec.World && len(missing(spec.Needs, m.v.Blueprint)) == 0 {
			return true
		}
	}
	v := &m.v
	switch id {
	case CarryingComponent:
		return v.Cargo != sim.VariantNone
	case CarryingNothing:
		return v.Cargo == sim.VariantNone
	case HealthBelow:
		return healthCmp(v, arg) < 0
	case HealthAbove:
		return healthCmp(v, arg) > 0

	case AtOwnBase:
		return v.AtBase
	case AtPoint:
		c, ok := point(v, arg)
		return ok && c == v.Coord
	case PointIsSet:
		_, ok := point(v, arg)
		return ok
	case PointIsEmpty:
		_, ok := point(v, arg)
		return !ok // an out-of-range point holds no coordinate either

	case SeesComponent:
		return len(v.VisibleComponents) > 0
	case ComponentInReach:
		return v.ComponentInReach
	case SeesEnemyRobot, EnemyVisible:
		return len(v.VisibleEnemies) > 0
	case SeesObstacle:
		return v.ObstacleAhead

	case RadarDetectsTarget:
		return len(v.RadarTargets) > 0

	case ReceivedComeHere:
		return m.hear(sim.ComeHere)
	case ReceivedAvoidHere:
		return m.hear(sim.AvoidHere)

	case PathBlocked:
		return v.PathBlocked
	case TargetReached:
		return v.TargetReached
	case TargetUnreachable:
		return v.TargetUnreachable

	case HasWeapon:
		return v.Blueprint.Has(sim.KindWeapon)
	case WeaponReady:
		return v.WeaponReady
	case VisibleTargetInWpnRange:
		return inWeaponRange(v, v.VisibleEnemies)
	case DetectedTargetInWpnRange:
		s, ok := radarEnemy(*v)
		return ok && s.Distance <= v.WeaponRange
	}
	return false // unknown predicate: never true, never a panic
}

// inWeaponRange reports whether the nearest of a sighting list is reachable by
// a weapon that is loaded right now. The lists arrive nearest-first, and an
// unarmed or fully reloading blueprint has range 0 while every sighting is at
// least one cell away, so this is false without a special case.
func inWeaponRange(v *sim.RobotView, s []sim.Sighting) bool {
	return len(s) > 0 && s[0].Distance <= v.WeaponRange
}

// radarEnemy is the nearest radar contact that is a robot. It discriminates on
// Kind, not on Variant: a base carries VariantNone too, so a Variant test would
// let an enemy-base radar point attack_radar_target at an indestructible base
// and burn the tick forever. Design §7.2 is explicit that bases are navigation
// landmarks, not attack objectives.
func radarEnemy(v sim.RobotView) (sim.Sighting, bool) {
	for _, s := range v.RadarTargets {
		if s.Kind == sim.SightRobot {
			return s, true
		}
	}
	return sim.Sighting{}, false
}

// hear reports whether a signal of this kind arrived, latching the first one so
// save_signal_position in the same rule can use it. v.Signals arrives in sender
// order from sim, so "first" is deterministic.
func (m *matcher) hear(k sim.SignalKind) bool {
	for i := range m.v.Signals {
		if m.v.Signals[i].Kind != k {
			continue
		}
		if m.signal == nil {
			s := m.v.Signals[i]
			m.signal = &s
		}
		return true
	}
	return false
}

// healthCmp compares health against a percentage of the blueprint's starting
// health: negative when below, positive when above, zero when equal or unknown.
func healthCmp(v *sim.RobotView, pct int) int {
	full := sim.StartingHealth(v.Blueprint)
	if full <= 0 {
		return 0
	}
	switch got, want := v.Health*100, pct*full; {
	case got < want:
		return -1
	case got > want:
		return 1
	}
	return 0
}

// point resolves a 1-based memory point argument. An out-of-range index reads
// as empty rather than panicking.
func point(v *sim.RobotView, arg int) (sim.Coord, bool) {
	if arg < 1 || arg > sim.MemPoints {
		return sim.Coord{}, false
	}
	p := v.Memory[arg-1]
	return p.Coord, p.Set
}

// nearestVisible is the closest thing forward vision reports, components and
// enemies alike. Both slices arrive nearest-first with ties already broken on
// id; choosing between the two heads the same way keeps the pick independent of
// which list it came from, which is the determinism trap here.
func nearestVisible(v sim.RobotView) (sim.Sighting, bool) {
	c, e := v.VisibleComponents, v.VisibleEnemies
	switch {
	case len(c) == 0 && len(e) == 0:
		return sim.Sighting{}, false
	case len(e) == 0:
		return c[0], true
	case len(c) == 0:
		return e[0], true
	case e[0].Distance != c[0].Distance:
		if e[0].Distance < c[0].Distance {
			return e[0], true
		}
		return c[0], true
	case e[0].ID < c[0].ID:
		return e[0], true
	}
	return c[0], true
}

// sideEffect executes one zero-tick action, reporting whether it did anything.
// A side effect with no target — save_visible_target with nothing in sight —
// writes nothing and never ends the rule scan.
func sideEffect(act *sim.Action, a Action, v sim.RobotView, sig *sim.Signal) bool {
	switch a.Do {
	case SaveCurrentPosition:
		return write(act, a.Arg, v.Coord, true)
	case SaveVisibleTarget:
		s, ok := nearestVisible(v)
		return write(act, a.Arg, s.Coord, ok)
	case SaveRadarTarget:
		if len(v.RadarTargets) == 0 {
			return false
		}
		return write(act, a.Arg, v.RadarTargets[0].Coord, true)
	case SaveSignalPosition:
		s, ok := heardSignal(v, sig)
		return write(act, a.Arg, s.Coord, ok)
	case ClearPoint:
		if a.Arg < 1 || a.Arg > sim.MemPoints {
			return false
		}
		act.Memory = append(act.Memory, sim.MemWrite{Point: a.Arg - 1, Clear: true})
		return true
	case BroadcastComeHere:
		act.Broadcasts = append(act.Broadcasts, sim.ComeHere)
		return true
	case BroadcastAvoidHere:
		act.Broadcasts = append(act.Broadcasts, sim.AvoidHere)
		return true
	}
	return false
}

// write queues a memory write, dropping it when the point index is out of range
// or the source coordinate does not exist (design §10.6: a write replaces, and
// a write of nothing is not a write).
func write(act *sim.Action, arg int, c sim.Coord, ok bool) bool {
	if !ok || arg < 1 || arg > sim.MemPoints {
		return false
	}
	act.Memory = append(act.Memory, sim.MemWrite{Point: arg - 1, Coord: c})
	return true
}

// heardSignal prefers the signal the rule's own received_* predicate matched,
// falling back to the first one heard this tick.
func heardSignal(v sim.RobotView, sig *sim.Signal) (sim.Signal, bool) {
	if sig != nil {
		return *sig, true
	}
	if len(v.Signals) > 0 {
		return v.Signals[0], true
	}
	return sim.Signal{}, false
}

// resolvePrimary turns one primary action into the sim action that carries it
// out. The third result is the trace reason: empty means "worked", anything
// else explains why the tick came out idle. Design §10.5 safe failure lives
// here — an invalid or missing target idles the robot, it never panics.
func resolvePrimary(a Action, v sim.RobotView) (sim.ActionKind, sim.Coord, string) {
	switch a.Do {
	case MoveForward:
		return sim.ActMoveForward, sim.Coord{}, ""
	case TurnLeft:
		return sim.ActTurnLeft, sim.Coord{}, ""
	case TurnRight:
		return sim.ActTurnRight, sim.Coord{}, ""
	case TurnRandom:
		return sim.ActTurnRandom, sim.Coord{}, ""
	case Stop:
		return sim.ActStop, sim.Coord{}, ""

	case MoveToOwnBase:
		if !v.HasBase {
			return idle("own base position is unknown")
		}
		return sim.ActMoveTo, v.Base, ""
	case MoveToVisibleTarget:
		s, ok := nearestVisible(v)
		if !ok {
			return idle("nothing is visible to move to")
		}
		return sim.ActMoveTo, s.Coord, ""
	case MoveToRadarTarget:
		if len(v.RadarTargets) == 0 {
			return idle("radar reports no target")
		}
		return sim.ActMoveTo, v.RadarTargets[0].Coord, ""
	case MoveToPoint:
		c, ok := point(&v, a.Arg)
		if !ok {
			return idle("point is empty")
		}
		return sim.ActMoveTo, c, ""
	case MoveAwayFromTarget:
		s, ok := nearestVisible(v)
		if !ok {
			return idle("nothing is visible to move away from")
		}
		return retreat(v.Coord, s.Coord)
	case MoveAwayFromPoint:
		c, ok := point(&v, a.Arg)
		if !ok {
			return idle("point is empty")
		}
		return retreat(v.Coord, c)

	case PickUpComponent:
		return sim.ActPickUp, sim.Coord{}, ""
	case DepositComponentAtBase:
		return sim.ActDeposit, sim.Coord{}, ""
	case DropComponent:
		return sim.ActDrop, sim.Coord{}, ""

	// Attacks aim at a cell, and only ever at an enemy the robot perceived this
	// tick: sim shoots at whatever hostile stands there, or wastes the tick.
	// Note attack_visible_target reads VisibleEnemies, not nearestVisible — a
	// loose component is not a target.
	case AttackVisibleTarget:
		if len(v.VisibleEnemies) == 0 {
			return idle("no enemy is visible to attack")
		}
		return sim.ActAttack, v.VisibleEnemies[0].Coord, ""
	case AttackRadarTarget:
		s, ok := radarEnemy(v)
		if !ok {
			return idle("radar reports no enemy to attack")
		}
		return sim.ActAttack, s.Coord, ""
	}
	return idle("unknown action")
}

// retreat is one step directly away from a coordinate. Navigating to the
// adjacent cell rather than to a far-off point keeps this inside sim's
// pathfinder: a barrier there simply reports the target unreachable.
func retreat(from, away sim.Coord) (sim.ActionKind, sim.Coord, string) {
	to := sim.Coord{X: from.X + sign(from.X-away.X), Y: from.Y + sign(from.Y-away.Y)}
	if to == from {
		return idle("standing on the thing to move away from")
	}
	return sim.ActMoveTo, to, ""
}

func idle(reason string) (sim.ActionKind, sim.Coord, string) {
	return sim.ActNone, sim.Coord{}, reason
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func actionLabel(id ActionID) string {
	if s, ok := LookupAction(id); ok {
		return s.Label
	}
	return string(id)
}
