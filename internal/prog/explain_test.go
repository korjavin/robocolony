package prog

import (
	"reflect"
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// baseView is a robot that perceives nothing: no sightings, no radar, no
// signals, empty memory, full health. Every case below starts here and changes
// exactly the fields it is about, so a scenario's name is the whole diff.
func baseView(bp sim.Blueprint) sim.RobotView {
	return sim.RobotView{
		Tick:      42,
		ID:        7,
		Coord:     sim.Coord{X: 5, Y: 5},
		Heading:   sim.North,
		Health:    sim.StartingHealth(bp),
		Blueprint: bp,
		Base:      sim.Coord{X: 1, Y: 1},
		HasBase:   true,
	}
}

// explainCases are the situations the truth table has to get right, each one
// paired with the program whose rules read it.
func explainCases(t *testing.T) []struct {
	name string
	p    Program
	v    sim.RobotView
} {
	t.Helper()
	scav, scout, def := scavengerProgram(), scoutProgram(), defensiveProgram()
	sbp, dbp := scavengerBlueprint(), defenderBlueprint()
	mustValidate(t, scav, sbp)
	mustValidate(t, scout, sbp)
	mustValidate(t, def, dbp)

	patrolling := baseView(sbp)

	carryingHome := baseView(sbp)
	carryingHome.Cargo = sim.Manipulator

	// At its own base holding something, with a program whose rule for that
	// case is the first one: the ordinary deposit.
	depositing := baseView(sbp)
	depositing.Cargo, depositing.AtBase, depositing.Coord = sim.Manipulator, true, sim.Coord{X: 1, Y: 1}

	// Radar contact and empty hands: a later rule wins while earlier ones are
	// not met.
	onRadar := baseView(sbp)
	onRadar.RadarTargets = []sim.Sighting{{ID: 9, Kind: sim.SightRobot, Coord: sim.Coord{X: 9, Y: 9}, Distance: 6}}

	// A sighting in the cone plus cargo: scout's rule 0 is side effects only,
	// so it runs and evaluation continues to the rule that takes the tick.
	scouting := baseView(sbp)
	scouting.Cargo = sim.Manipulator
	scouting.VisibleComponents = []sim.Sighting{{ID: 3, Coord: sim.Coord{X: 5, Y: 2}, Variant: sim.AutoGun, Distance: 3}}

	// Hurt, with an enemy in range: the defensive program's flee rule and its
	// attack rule both match, and the flee rule is first.
	underFire := baseView(dbp)
	underFire.Health = sim.StartingHealth(dbp) / 10
	underFire.WeaponReady, underFire.WeaponRange = true, 6
	underFire.VisibleEnemies = []sim.Sighting{{ID: 11, Kind: sim.SightRobot, Coord: sim.Coord{X: 5, Y: 2}, Colony: 1, Distance: 3}}

	// A signal this tick, so the communication rows and the signal latch are
	// exercised rather than assumed.
	hailed := baseView(dbp)
	hailed.Signals = []sim.Signal{{Kind: sim.ComeHere, From: 12, Colony: 0, Coord: sim.Coord{X: 8, Y: 8}}}

	return []struct {
		name string
		p    Program
		v    sim.RobotView
	}{
		{"scavenger patrolling", scav, patrolling},
		{"scavenger carrying home", scav, carryingHome},
		{"scavenger depositing", scav, depositing},
		{"scavenger on radar contact", scav, onRadar},
		{"scout with a sighting and cargo", scout, scouting},
		{"defender under fire", def, underFire},
		{"defender hailed", def, hailed},
		{"empty program", Program{V: SchemaVersion}, patrolling},
		// The deposit reflex takes the tick from a rule whose action comes to
		// nothing at its own base while carrying (eval.go). Explain has to
		// mirror that or the editor reports a winner the robot never obeyed.
		{"deposit reflex", Program{V: SchemaVersion, Rules: []Rule{
			{When: Pred(CarryingComponent), Then: []Action{Do(MoveToOwnBase)}},
		}}, depositing},
	}
}

// TestExplainAgreesWithDecide is the acceptance test: one tick's recorded
// condition table matches what the evaluator actually decided.
//
// It is also the guard on the one duplication in explain.go — Explain's winner
// scan is a second copy of Decide's, so this compares the two on every case.
func TestExplainAgreesWithDecide(t *testing.T) {
	for _, c := range explainCases(t) {
		t.Run(c.name, func(t *testing.T) {
			e := New(c.p)
			e.Decide(c.v)
			tr := e.Trace()
			ex := Explain(c.p, c.v)

			if ex.Tick != tr.Tick || ex.Rule != tr.Rule || ex.Action != tr.Action ||
				ex.Idle != tr.Idle || ex.Reason != tr.Reason {
				t.Fatalf("Explain = {tick %d rule %d action %q idle %v reason %q}, Decide = {tick %d rule %d action %q idle %v reason %q}",
					ex.Tick, ex.Rule, ex.Action, ex.Idle, ex.Reason,
					tr.Tick, tr.Rule, tr.Action, tr.Idle, tr.Reason)
			}
			if len(ex.Rules) != len(c.p.Rules) {
				t.Fatalf("got %d verdicts for %d rules", len(ex.Rules), len(c.p.Rules))
			}

			// Decide stops at the rule that takes the tick, so its matched
			// bitset covers exactly the rules up to and including the winner.
			// Explain keeps going, which is the whole point of it.
			won := -1
			for i, rv := range ex.Rules {
				if rv.Verdict == VerdictWon {
					won = i
				}
			}
			for i, rv := range ex.Rules {
				matched := rv.Verdict != VerdictNotMet
				switch {
				case won >= 0 && i > won:
					if tr.Matched(i) {
						t.Errorf("rule %d is past the winner %d but Decide marked it matched", i, won)
					}
					if matched && rv.ShadowedBy != won {
						t.Errorf("rule %d verdict %q shadowed_by %d, want %d", i, rv.Verdict, rv.ShadowedBy, won)
					}
				case tr.Matched(i) != matched:
					t.Errorf("rule %d: Decide matched=%v, Explain verdict=%q", i, tr.Matched(i), rv.Verdict)
				}
				if rv.Verdict != VerdictShadowed && rv.ShadowedBy != -1 {
					t.Errorf("rule %d verdict %q carries shadowed_by %d", i, rv.Verdict, rv.ShadowedBy)
				}
			}

			// And the table is the same answer at condition grain: replaying
			// each rule's WHEN tree over nothing but the recorded truth values
			// must reproduce its verdict.
			for i, rule := range c.p.Rules {
				want := ex.Rules[i].Verdict != VerdictNotMet
				if got := fromTable(t, ex, rule.When, 0); got != want {
					t.Errorf("rule %d: truth table says %v, verdict says %v", i, got, want)
				}
			}
		})
	}
}

// fromTable re-evaluates a condition using only the recorded table, which is
// all the browser will have. A predicate with no row is a test failure, not a
// false: a missing row is exactly the bug this is looking for.
func fromTable(t *testing.T, ex Explanation, c Condition, depth int) bool {
	t.Helper()
	if depth >= MaxCondDepth {
		return false
	}
	switch c.Op {
	case OpPred:
		for _, row := range ex.Conditions {
			if row.Pred == c.Pred && row.Arg == c.Arg {
				if row.Unknown {
					t.Errorf("predicate %s(%d) is used by a rule but its row is unknown", c.Pred, c.Arg)
				}
				return row.True
			}
		}
		t.Errorf("predicate %s(%d) is used by a rule but has no row in the table", c.Pred, c.Arg)
		return false
	case OpAnd:
		if len(c.Of) == 0 {
			return false
		}
		for _, k := range c.Of {
			if !fromTable(t, ex, k, depth+1) {
				return false
			}
		}
		return true
	case OpOr:
		for _, k := range c.Of {
			if fromTable(t, ex, k, depth+1) {
				return true
			}
		}
		return false
	case OpNot:
		if len(c.Of) != 1 {
			return false
		}
		return !fromTable(t, ex, c.Of[0], depth+1)
	}
	return false
}

// TestExplainReportsShadowedRules is the editor's shadow test: given a draft
// rule list and a robot's state, which rule wins and which rules would have
// matched but never got the tick.
func TestExplainReportsShadowedRules(t *testing.T) {
	bp := scavengerBlueprint()
	draft := Program{V: SchemaVersion, Name: "draft", Rules: []Rule{
		{When: Pred(CarryingComponent), Then: []Action{Do(MoveToOwnBase)}},             // wins
		{When: Pred(CarryingComponent), Then: []Action{DoArg(SaveCurrentPosition, 1)}}, // shadowed, never ran
		{When: Pred(CarryingNothing), Then: []Action{Do(MoveForward)}},                 // not met
		{When: PredArg(HealthAbove, 10), Then: []Action{Do(Stop)}},                     // matches, shadowed
	}}
	mustValidate(t, draft, bp)

	// Carrying, but out in the field: at its own base this would be the deposit
	// reflex's tick rather than rule 0's, which TestExplainAgreesWithDecide
	// covers separately.
	v := baseView(bp)
	v.Cargo = sim.Manipulator
	before := v

	ex := Explain(draft, v)

	if ex.Rule != 0 || ex.Action != MoveToOwnBase || ex.Idle {
		t.Fatalf("winner = rule %d action %q idle %v, want rule 0 move_to_own_base", ex.Rule, ex.Action, ex.Idle)
	}
	want := []RuleVerdict{
		{Rule: 0, Verdict: VerdictWon, ShadowedBy: -1},
		{Rule: 1, Verdict: VerdictShadowed, ShadowedBy: 0},
		{Rule: 2, Verdict: VerdictNotMet, ShadowedBy: -1},
		{Rule: 3, Verdict: VerdictShadowed, ShadowedBy: 0},
	}
	if !reflect.DeepEqual(ex.Rules, want) {
		t.Errorf("verdicts = %+v, want %+v", ex.Rules, want)
	}

	// Nothing was executed and nothing was written: the shadow test evaluates a
	// draft against a robot it must not touch.
	if !reflect.DeepEqual(v, before) {
		t.Error("Explain modified the view it was given")
	}
	if again := Explain(draft, v); !reflect.DeepEqual(again.Rules, ex.Rules) {
		t.Error("two identical Explain calls disagreed")
	}
}

// TestExplainRunsSideEffectOnlyRules separates "matched and got what it asked
// for" from "matched and was shadowed". A rule of nothing but zero-tick side
// effects is not shadowed by the rule below it — it ran, and evaluation
// continued past it on purpose (AGENTS.md action model).
func TestExplainRunsSideEffectOnlyRules(t *testing.T) {
	bp := scavengerBlueprint()
	p := Program{V: SchemaVersion, Name: "side effects", Rules: []Rule{
		{When: Pred(CarryingNothing), Then: []Action{DoArg(SaveCurrentPosition, 1)}},
		{When: Pred(CarryingNothing), Then: []Action{Do(MoveForward)}},
	}}
	mustValidate(t, p, bp)

	ex := Explain(p, baseView(bp))
	if ex.Rules[0].Verdict != VerdictRan {
		t.Errorf("rule 0 verdict = %q, want %q", ex.Rules[0].Verdict, VerdictRan)
	}
	if ex.Rules[1].Verdict != VerdictWon {
		t.Errorf("rule 1 verdict = %q, want %q", ex.Rules[1].Verdict, VerdictWon)
	}
}

// TestExplainMarksImpossibleOnBlueprint: a condition whose sensor the robot
// does not carry can never be true, and the truth table has to say so rather
// than showing a bare ✗ the player will read as "not right now".
func TestExplainMarksImpossibleOnBlueprint(t *testing.T) {
	bare := sim.Blueprint{
		ID: "bp-bare", Name: "bare", ProgramID: "p",
		Components: []sim.Variant{sim.Tracks, sim.LightArmor},
	}
	cases := []struct {
		name string
		bp   sim.Blueprint
		pred PredicateID
		want bool
	}{
		{"no radar", bare, RadarDetectsTarget, true},
		{"no weapon", bare, WeaponReady, true},
		{"no weapon, no radar", bare, DetectedTargetInWpnRange, true},
		{"radar fitted", scavengerBlueprint(), RadarDetectsTarget, false},
		{"weapon fitted", defenderBlueprint(), WeaponReady, false},
		{"weapon missing on scavenger", scavengerBlueprint(), VisibleTargetInWpnRange, true},
		// has_weapon is introspection: false on an unarmed blueprint is the
		// answer, not an impossibility (catalogue.go).
		{"has_weapon is never impossible", bare, HasWeapon, false},
		{"self state is never impossible", bare, CarryingNothing, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ex := Explain(Program{V: SchemaVersion}, baseView(c.bp))
			row, ok := rowFor(ex, c.pred, 0)
			if !ok {
				t.Fatalf("no row for %s", c.pred)
			}
			if row.Impossible != c.want {
				t.Errorf("%s impossible = %v, want %v", c.pred, row.Impossible, c.want)
			}
			if row.Impossible && row.True {
				t.Errorf("%s is impossible on this blueprint yet reads true", c.pred)
			}
		})
	}
}

// TestExplainCoversTheCatalogue: the truth table is the whole language, not
// just the predicates this program happens to use — the player reads it to
// decide what to write next. Parameterised predicates get a row per argument
// the program asks about, and an unknown row when it asks about none.
func TestExplainCoversTheCatalogue(t *testing.T) {
	bp := scavengerBlueprint()
	p := Program{V: SchemaVersion, Rules: []Rule{
		{When: And(PredArg(HealthBelow, 25), Not(PredArg(HealthBelow, 10))), Then: []Action{Do(Stop)}},
		{When: Or(PredArg(AtPoint, 3), PredArg(AtPoint, 1)), Then: []Action{Do(Stop)}},
	}}
	mustValidate(t, p, bp)
	ex := Explain(p, baseView(bp))

	for _, spec := range predicates {
		if _, ok := rowForPred(ex, spec.ID); !ok {
			t.Errorf("catalogue predicate %s has no row", spec.ID)
		}
	}

	// One row per distinct argument, in ascending order.
	var health []int
	for _, row := range ex.Conditions {
		if row.Pred == HealthBelow {
			health = append(health, row.Arg)
		}
	}
	if !reflect.DeepEqual(health, []int{10, 25}) {
		t.Errorf("health_below rows = %v, want [10 25]", health)
	}
	if row, _ := rowFor(ex, AtPoint, 3); row.Unknown {
		t.Error("at_point(3) is used by a rule but reads unknown")
	}
	// point_is_set takes an argument no rule parameterises: there is nothing to
	// test it against, so the row is "·" rather than a confident false.
	row, ok := rowForPred(ex, PointIsSet)
	if !ok || !row.Unknown {
		t.Errorf("point_is_set row = %+v, want unknown", row)
	}
	if row.True {
		t.Error("an unknown row must not also claim to be true")
	}

	// Rows are in catalogue order, which is what groups them for the UI.
	seen := 0
	for _, row := range ex.Conditions {
		for seen < len(predicates) && predicates[seen].ID != row.Pred {
			seen++
		}
		if seen == len(predicates) {
			t.Fatalf("row %s is out of catalogue order", row.Pred)
		}
	}
}

// TestExplainReportsUnderlyingValues: "health_below 25 is false" is much less
// useful than the same line with the robot's actual percentage next to it.
func TestExplainReportsUnderlyingValues(t *testing.T) {
	bp := defenderBlueprint()
	v := baseView(bp)
	v.Health = sim.StartingHealth(bp) / 4
	v.VisibleEnemies = []sim.Sighting{{ID: 2, Kind: sim.SightRobot, Coord: sim.Coord{X: 5, Y: 3}, Distance: 2}}
	p := Program{V: SchemaVersion, Rules: []Rule{
		{When: PredArg(HealthBelow, 50), Then: []Action{Do(Stop)}},
	}}
	ex := Explain(p, v)

	row, ok := rowFor(ex, HealthBelow, 50)
	if !ok || !row.HasValue || row.Value != 25 {
		t.Errorf("health_below(50) row = %+v, want value 25", row)
	}
	if !row.True {
		t.Error("health_below(50) at 25% should be true")
	}
	if row, _ := rowForPred(ex, SeesEnemyRobot); !row.HasValue || row.Value != 1 {
		t.Errorf("sees_enemy_robot row = %+v, want value 1", row)
	}
	if row, _ := rowForPred(ex, CarryingNothing); row.HasValue {
		t.Errorf("carrying_nothing has no underlying number, got %+v", row)
	}
}

func rowFor(ex Explanation, id PredicateID, arg int) (ConditionState, bool) {
	for _, row := range ex.Conditions {
		if row.Pred == id && row.Arg == arg {
			return row, true
		}
	}
	return ConditionState{}, false
}

func rowForPred(ex Explanation, id PredicateID) (ConditionState, bool) {
	for _, row := range ex.Conditions {
		if row.Pred == id {
			return row, true
		}
	}
	return ConditionState{}, false
}

// TestExplanationIsRecordedOnlyForWatchedRobots: the table costs a second walk
// of the rule list, so it is opt-in exactly as the trace ring is. A robot
// nobody selected must record nothing.
func TestExplanationIsRecordedOnlyForWatchedRobots(t *testing.T) {
	w := flatWorld(t, 11, 12)
	bp := scavengerBlueprint()
	watched := addRobot(w, w.Bases[0].Coord, sim.North, bp)
	ignored := addRobot(w, sim.Coord{X: 8, Y: 8}, sim.South, bp)

	rt := NewRuntime()
	rt.Install(bp.ProgramID, scavengerProgram())
	w.Control, w.OnDestroy = rt.Control, rt.Forget

	w.Step()
	if _, ok := rt.Explanation(watched.ID); ok {
		t.Fatal("a robot nobody is watching recorded a condition table")
	}

	rt.Watch(watched.ID, w.Tick)
	for i := 0; i < 20; i++ {
		w.Step()
	}

	ex, ok := rt.Explanation(watched.ID)
	if !ok {
		t.Fatal("a watched robot recorded no condition table")
	}
	if len(ex.Conditions) == 0 || len(ex.Rules) != len(scavengerProgram().Rules) {
		t.Fatalf("recorded %d rows and %d verdicts", len(ex.Conditions), len(ex.Rules))
	}
	// Stamped with the tick it was decided on, not the tick it is read on: a
	// robot mid-move is not consulted every tick.
	if ex.Tick >= w.Tick {
		t.Errorf("explanation tick %d, world is at %d", ex.Tick, w.Tick)
	}
	if tr, _ := rt.Trace(watched.ID); tr.Tick != ex.Tick || tr.Rule != ex.Rule {
		t.Errorf("explanation is tick %d rule %d, trace is tick %d rule %d",
			ex.Tick, ex.Rule, tr.Tick, tr.Rule)
	}
	if _, ok := rt.Explanation(ignored.ID); ok {
		t.Error("an unwatched robot recorded a condition table")
	}

	// A dropped watch takes the table with it. Otherwise a watch that was
	// evicted and re-established would pair an empty history ring with a table
	// dated before the gap, and the inspector would show the robot's mind as it
	// was several seconds ago.
	rt.drop(watched.ID)
	rt.Watch(watched.ID, w.Tick)
	if stale, ok := rt.Explanation(watched.ID); ok {
		t.Errorf("a re-established watch served a table from tick %d before the robot decided again", stale.Tick)
	}
	for i := 0; i < 20; i++ {
		w.Step()
	}
	if _, ok := rt.Explanation(watched.ID); !ok {
		t.Error("a re-established watch never started recording again")
	}
}
