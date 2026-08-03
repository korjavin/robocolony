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
//     idles. Programs are untrusted input, so no path here panics.
//
// Work per tick is bounded: the rule list is walked at most once, and each
// condition walk is depth-capped. A program cannot make a tick do unbounded
// work no matter how it is shaped.
type Evaluator struct {
	program   Program
	programID string
	revision  int
	trace     Trace
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
}

const reasonNoMatch = "no rule matched"

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

	e.trace = tr
	return act
}

// matcher evaluates one rule's condition against one view. It also remembers
// the signal a received_* predicate matched on, so save_signal_position in the
// same rule stores that signal's origin rather than an unrelated one (design
// §10.9 rule 2).
type matcher struct {
	v      sim.RobotView
	signal *sim.Signal
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
	}
	return false
}

func (m *matcher) pred(id PredicateID, arg int) bool {
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
		// No reload model yet, so an installed weapon is always ready. E5.1
		// replaces this with the cooldown test.
		return v.Blueprint.Has(sim.KindWeapon)
	case VisibleTargetInWpnRange, DetectedTargetInWpnRange:
		// Weapon ranges arrive with E5.1. Until then these are honestly false
		// rather than guessed at.
		return false
	}
	return false // unknown predicate: never true, never a panic
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

	case AttackVisibleTarget, AttackRadarTarget:
		return idle("combat is not implemented yet")
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
