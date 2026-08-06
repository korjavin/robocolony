package prog

import (
	"slices"

	"github.com/korjavin/robocolony/internal/sim"
)

// Condition-level trace: what every condition in the language was worth on one
// tick, and what each rule's conditions therefore came to.
//
// Trace answers "which rule took the tick". That is one number, and it is the
// wrong grain for three of the design's surfaces: the sensor truth table wants
// every condition in the catalogue with a truth value, the active-rule card
// wants the matched rule's WHEN list ticked off condition by condition, and the
// editor's shadow test wants "this rule would match, but rule N already took
// the tick" for a program that is not installed on anything.
//
// All three are the same answer at different zoom levels, so there is one
// function: Explain. It evaluates a program against a view and executes
// nothing.
//
//   - Pure. It runs the same matcher Decide runs, but no side effect is applied,
//     no primary action is returned to sim, and the view is a value copy — so
//     it cannot touch world state whether it is called on the installed program
//     during a tick or on an editor's draft between ticks. Nothing here reaches
//     StateHash, the replay log, or the rng.
//   - Opt-in on the live path. Decide records an Explanation only for a robot
//     someone is watching (history.go), so the fleet nobody has selected pays
//     nothing.
//   - Complete. Decide stops at the rule that takes the tick; Explain keeps
//     going, which is the only way to know that rule 7 would also have matched.
//     The rules below the winner are evaluated, not executed.
type Explanation struct {
	// Tick, Rule, Action, Reason and Idle mirror the Trace of the same
	// evaluation exactly — TestExplainAgreesWithDecide is what keeps them from
	// drifting, since the winner scan below is a second copy of Decide's.
	Tick   uint64
	Rule   int // rule that took the tick, -1 when none did
	Action ActionID
	Reason string
	Idle   bool

	Rules      []RuleVerdict    // one per rule, program order
	Conditions []ConditionState // catalogue order, see conditionTable
}

// Verdict is what one rule's conditions came to. It is a string because it goes
// straight onto the wire and an integer enum would have to be translated twice.
type Verdict string

const (
	// VerdictNotMet: the conditions did not match.
	VerdictNotMet Verdict = "not_met"
	// VerdictWon: the conditions matched and the rule's primary action ended
	// the tick. At most one rule per tick.
	VerdictWon Verdict = "won"
	// VerdictRan: the conditions matched, the rule holds nothing but zero-tick
	// side effects, so they executed and evaluation continued down the list
	// (AGENTS.md action model). Not shadowed — it got everything it asked for.
	VerdictRan Verdict = "ran"
	// VerdictShadowed: the conditions matched, but an earlier rule had already
	// taken the tick, so nothing in this rule ran. ShadowedBy names it. This is
	// the verdict the editor's shadow test exists for.
	VerdictShadowed Verdict = "shadowed"
)

// RuleVerdict is one rule's outcome.
type RuleVerdict struct {
	Rule       int
	Verdict    Verdict
	ShadowedBy int // rule that took the tick first; -1 for every other verdict
}

// ConditionState is one row of the truth table: a catalogue predicate, the
// argument it was asked with, and what it was worth this tick.
type ConditionState struct {
	Pred  PredicateID
	Group string // catalogue group, for the SELF/VISION/RADAR/... sections
	Arg   int

	True bool
	// Unknown marks a row nothing was asked: a predicate that takes an argument
	// which no rule in the program parameterises, so there is no threshold or
	// memory point to test it against. The truth table renders these as "·"
	// rather than as a confident false.
	Unknown bool
	// Impossible marks a predicate the blueprint can never satisfy because it
	// lacks the hardware — radar rows on a robot with no radar. Not an error:
	// testing a sensor you do not have is legal, just pointless (catalogue.go),
	// and the UI greys the row rather than hiding it.
	Impossible bool

	// Value is the number behind the truth value where one exists: the health
	// percentage behind health_below, the sighting count behind sees_component,
	// the distance behind an in-weapon-range test. HasValue because 0 is a
	// perfectly good count.
	Value    int
	HasValue bool
}

// Explain evaluates every rule of p against v and reports what each one came
// to, without executing anything.
//
// The winner scan mirrors Decide's: a rule matches, the first of its actions
// that the catalogue calls primary ends the tick wherever it sits in the slice,
// and a rule holding only side effects lets evaluation continue. It is a second
// copy of that logic rather than a refactor of Decide, because Decide walks
// conditions and executes side effects in one pass and splitting it would put a
// closure on the hot path of every robot in the match. TestExplainAgreesWithDecide
// is the guard on the copy.
func Explain(p Program, v sim.RobotView) Explanation {
	if len(p.Rules) > MaxRules {
		p.Rules = p.Rules[:MaxRules]
	}
	out := Explanation{
		Tick: v.Tick, Rule: -1, Reason: reasonNoMatch, Idle: true,
		Rules: make([]RuleVerdict, len(p.Rules)),
	}

	var act sim.Action
	won := -1
	for i := range p.Rules {
		rule := &p.Rules[i]
		out.Rules[i] = RuleVerdict{Rule: i, Verdict: VerdictNotMet, ShadowedBy: -1}
		m := matcher{v: v}
		if !m.cond(rule.When, 0) {
			continue
		}
		primary := primaryOf(rule)
		switch {
		case won >= 0:
			// Evaluation had already stopped here, so neither this rule's side
			// effects nor its primary action ran, whichever it holds.
			out.Rules[i] = RuleVerdict{Rule: i, Verdict: VerdictShadowed, ShadowedBy: won}
		case primary == nil:
			out.Rules[i] = RuleVerdict{Rule: i, Verdict: VerdictRan, ShadowedBy: -1}
		default:
			out.Rules[i] = RuleVerdict{Rule: i, Verdict: VerdictWon, ShadowedBy: -1}
			won = i
			kind, coord, why := resolvePrimary(*primary, v)
			if why == "" {
				why = actionLabel(primary.Do)
			}
			out.Rule, out.Action, out.Idle, out.Reason = i, primary.Do, kind == sim.ActNone, why
			act.Kind, act.Coord = kind, coord
		}
	}

	// The deposit reflex takes the tick from a program that came to nothing at
	// its own base while carrying (eval.go). Rule goes to -1 exactly as Trace
	// does, and the rule that won the *evaluation* keeps VerdictWon: it did win
	// the rule scan, and the reason line says what happened to its tick.
	if idleAtOwnBaseWithCargo(v, act) {
		out.Rule, out.Action, out.Idle, out.Reason = -1, DepositComponentAtBase, false, reasonReflexDeposit
	}

	out.Conditions = conditionTable(p, v)
	return out
}

// primaryOf is the action that would end the tick: the first one the catalogue
// knows and calls primary. Unknown actions are skipped exactly as Decide skips
// them — Validate is where a bad program is reported.
func primaryOf(r *Rule) *Action {
	for j := range r.Then {
		if r.Then[j].Primary() {
			return &r.Then[j]
		}
	}
	return nil
}

// explainTick records the condition table for a robot someone is watching, and
// does nothing at all for every other robot. Called unconditionally from Decide
// so that the decision path itself does not branch on being observed.
func (e *Evaluator) explainTick(v sim.RobotView) {
	if e.hist == nil {
		return
	}
	e.explain = Explain(e.program, v)
}

// Explanation returns the condition table and per-rule verdicts recorded on a
// watched robot's most recent decision, and whether there is one. False for a
// robot nobody is watching, and for a watched robot that has not decided since
// the watch started — a robot mid-move is not consulted every tick, so the
// table is stamped with the tick it was taken on, not with the tick it is read
// on.
//
// The returned slices are the recorded ones, not copies: callers read them.
func (rt *Runtime) Explanation(robotID int) (Explanation, bool) {
	e, ok := rt.robots[robotID]
	if !ok || e.hist == nil || e.explain.Conditions == nil {
		return Explanation{}, false
	}
	return e.explain, true
}

// conditionTable evaluates the whole catalogue against the view.
//
// The whole catalogue, not just the predicates the program uses: the design's
// sensor truth table is a fixed list of every condition in the language, so a
// player can see what is available and what is currently true before writing a
// rule for it. Rows come out in catalogue order, which is already grouped.
//
// Predicates that take an argument — a health percentage, a memory point — have
// no truth value in the abstract, so they get one row per distinct argument the
// program actually asks about, in ascending order, and a single Unknown row
// when the program never asks. That also makes the active rule's WHEN list a
// plain lookup: every (predicate, argument) pair a rule tests has a row here.
func conditionTable(p Program, v sim.RobotView) []ConditionState {
	args := usedArgs(p)
	out := make([]ConditionState, 0, len(predicates))
	for _, spec := range predicates {
		row := ConditionState{
			Pred:       spec.ID,
			Group:      spec.Group,
			Impossible: len(missing(spec.Needs, v.Blueprint)) > 0,
		}
		if spec.Arg == ArgNone {
			out = append(out, evalRow(row, 0, v))
			continue
		}
		used := args[spec.ID]
		if len(used) == 0 {
			row.Unknown = true
			out = append(out, row)
			continue
		}
		for _, a := range used {
			out = append(out, evalRow(row, a, v))
		}
	}
	return out
}

// evalRow answers one predicate at one argument. A fresh matcher per row: the
// signal latch is per-rule state and must not leak from one row to the next.
func evalRow(row ConditionState, arg int, v sim.RobotView) ConditionState {
	m := matcher{v: v}
	row.Arg = arg
	row.True = m.pred(row.Pred, arg)
	row.Value, row.HasValue = predValue(row.Pred, v)
	return row
}

// usedArgs collects the arguments the program asks each parameterised predicate
// about, deduplicated and sorted. The walk is depth-capped exactly as the
// evaluator's is, so a hand-built program that never went through Decode cannot
// blow the stack here either.
func usedArgs(p Program) map[PredicateID][]int {
	out := map[PredicateID][]int{}
	var walk func(c Condition, depth int)
	walk = func(c Condition, depth int) {
		if depth >= MaxCondDepth {
			return
		}
		if c.Op == OpPred {
			spec, ok := LookupPredicate(c.Pred)
			if ok && spec.Arg != ArgNone && !slices.Contains(out[c.Pred], c.Arg) {
				out[c.Pred] = append(out[c.Pred], c.Arg)
			}
			return
		}
		for _, k := range c.Of {
			walk(k, depth+1)
		}
	}
	for i := range p.Rules {
		walk(p.Rules[i].When, 0)
	}
	for id := range out {
		slices.Sort(out[id])
	}
	return out
}

// predValue is the number behind a truth value, for the predicates that have
// one. "health_below 25 is false" is much less useful than "health_below 25 is
// false, health is 62%", and the same goes for a range test the player expects
// to be true: the distance says how far off it was.
func predValue(id PredicateID, v sim.RobotView) (int, bool) {
	switch id {
	case HealthBelow, HealthAbove:
		if full := sim.StartingHealth(v.Blueprint); full > 0 {
			return v.Health * 100 / full, true
		}
	case SeesComponent:
		return len(v.VisibleComponents), true
	case SeesEnemyRobot, EnemyVisible:
		return len(v.VisibleEnemies), true
	case RadarDetectsTarget:
		return len(v.RadarTargets), true
	case VisibleTargetInWpnRange:
		if len(v.VisibleEnemies) > 0 {
			return v.VisibleEnemies[0].Distance, true
		}
	case DetectedTargetInWpnRange:
		if s, ok := radarEnemy(v); ok {
			return s.Distance, true
		}
	}
	return 0, false
}
