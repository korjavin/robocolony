package prog

import (
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// scavengerProgram is design §10.7, rule for rule.
func scavengerProgram() Program {
	return Program{V: SchemaVersion, Name: "scavenger", Rules: []Rule{
		{When: And(Pred(AtOwnBase), Pred(CarryingComponent)), Then: []Action{Do(DepositComponentAtBase)}},
		{When: Pred(CarryingComponent), Then: []Action{Do(MoveToOwnBase)}},
		{When: And(Pred(ComponentInReach), Pred(CarryingNothing)), Then: []Action{Do(PickUpComponent)}},
		{When: And(Pred(RadarDetectsTarget), Pred(CarryingNothing)), Then: []Action{Do(MoveToRadarTarget)}},
		{When: Pred(SeesObstacle), Then: []Action{Do(TurnRandom)}},
		{When: Pred(CarryingNothing), Then: []Action{Do(MoveForward)}},
	}}
}

// scoutProgram is design §10.8. Rule 1 is the whole point: a side effect fires
// and evaluation keeps going, so the robot saves a sighting *and* moves.
func scoutProgram() Program {
	return Program{V: SchemaVersion, Name: "scout", Rules: []Rule{
		{When: And(Pred(SeesComponent), Pred(CarryingComponent)), Then: []Action{DoArg(SaveVisibleTarget, 1)}},
		{When: And(Pred(AtOwnBase), Pred(CarryingComponent)), Then: []Action{Do(DepositComponentAtBase)}},
		{When: Pred(CarryingComponent), Then: []Action{Do(MoveToOwnBase)}},
		{When: And(PredArg(AtPoint, 1), Pred(ComponentInReach), Pred(CarryingNothing)), Then: []Action{Do(PickUpComponent)}},
		{When: And(PredArg(AtPoint, 1), Pred(CarryingNothing)), Then: []Action{DoArg(ClearPoint, 1)}},
		{When: And(PredArg(PointIsSet, 1), Pred(CarryingNothing)), Then: []Action{DoArg(MoveToPoint, 1)}},
	}}
}

// defensiveProgram is design §10.9.
func defensiveProgram() Program {
	return Program{V: SchemaVersion, Name: "defender", Rules: []Rule{
		{When: And(PredArg(HealthBelow, 25), Pred(SeesEnemyRobot)), Then: []Action{Do(MoveAwayFromTarget)}},
		{When: Pred(ReceivedComeHere), Then: []Action{DoArg(SaveSignalPosition, 2)}},
		{When: And(Pred(SeesEnemyRobot), Pred(VisibleTargetInWpnRange)), Then: []Action{Do(AttackVisibleTarget)}},
		{When: PredArg(AtPoint, 2), Then: []Action{DoArg(ClearPoint, 2)}},
		{When: PredArg(PointIsSet, 2), Then: []Action{DoArg(MoveToPoint, 2)}},
		{When: Pred(RadarDetectsTarget), Then: []Action{Do(MoveToRadarTarget)}},
		{When: Pred(SeesObstacle), Then: []Action{Do(TurnRandom)}},
	}}
}

func scavengerBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         "bp-scavenger",
		Name:       "scavenger",
		Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Manipulator, sim.PartsRadar},
		ProgramID:  "prog-scavenge",
	}
}

func defenderBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         "bp-defender",
		Name:       "defender",
		Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Laser, sim.PartsRadar},
		ProgramID:  "prog-defend",
	}
}

// flatWorld is a generated arena with the barriers and components turned off,
// so a test can place exactly what it wants. Generation still owns the rng, the
// cells and the base, which is what keeps these tests honest about sim.
func flatWorld(t *testing.T, seed int64, side int) *sim.World {
	t.Helper()
	return sim.Generate(seed, sim.GenOpts{Width: side, Height: side, Colonies: 1})
}

func addRobot(w *sim.World, at sim.Coord, h sim.Heading, bp sim.Blueprint) *sim.Robot {
	r := &sim.Robot{
		ID:        w.NextID(),
		Colony:    0,
		Coord:     at,
		Heading:   h,
		Health:    sim.StartingHealth(bp),
		Blueprint: bp,
		ProgramID: bp.ProgramID,
	}
	w.Robots = append(w.Robots, r)
	return r
}

// mustValidate fails the test unless the program is legal on the blueprint. It
// keeps every example below honest: a program the editor would refuse to save
// is not a proof of anything.
func mustValidate(t *testing.T, p Program, bp sim.Blueprint) {
	t.Helper()
	if res := Validate(p, bp); !res.OK() {
		t.Fatalf("%s is invalid on %s: %v", p.Name, bp.Name, res.Errors)
	}
}

// TestScavengerExample is the epic's done condition: the design §10.7 program,
// unmodified, on a real generated arena with barriers in it, gets a loose
// component into base inventory with nothing else driving the robot.
func TestScavengerExample(t *testing.T) {
	// Richness places components only on cells the base can actually reach, so
	// trimming to the first one leaves exactly one reachable component. The
	// arena is 16x16 so that it is inside radar range (design §7.2) from
	// anywhere — otherwise §10.7 degenerates into a random walk.
	w := sim.Generate(7, sim.GenOpts{Width: 16, Height: 16, Colonies: 1, BarrierDensity: 0.08, Richness: 0.05})
	if len(w.Loose) == 0 {
		t.Fatal("generation produced no loose components to scavenge")
	}
	w.Loose = w.Loose[:1]
	want := w.Loose[0].Variant
	base := w.Bases[0]

	bp := scavengerBlueprint()
	if err := bp.Validate(); err != nil {
		t.Fatalf("blueprint invalid: %v", err)
	}
	prog := scavengerProgram()
	mustValidate(t, prog, bp)

	r := addRobot(w, base.Coord, sim.North, bp)
	rt := NewRuntime()
	rt.Install(bp.ProgramID, prog)
	w.Control = rt.Control

	const limit = 2000
	ticks := 0
	for ; ticks < limit && base.Inventory[want] == 0; ticks++ {
		w.Step()
	}

	if base.Inventory[want] != 1 {
		tr, _ := rt.Trace(r.ID)
		t.Fatalf("after %d ticks the base holds %d %v, want 1 (robot at %v, cargo %v, trace %+v)",
			ticks, base.Inventory[want], want, r.Coord, r.Cargo, tr)
	}
	if len(w.Loose) != 0 {
		t.Fatalf("%d loose components left, want 0", len(w.Loose))
	}
	if r.Cargo != sim.VariantNone {
		t.Fatalf("robot still carries %v", r.Cargo)
	}
	t.Logf("§10.7 scavenger delivered %v in %d ticks", want, ticks)
}

// TestScoutExample proves the side-effect model: design §10.8 rule 1 saves a
// sighting and rule 3 still moves the robot, in the same tick.
func TestScoutExample(t *testing.T) {
	w := flatWorld(t, 3, 16)
	base := w.Bases[0]
	base.Coord = sim.Coord{X: 2, Y: 2}

	bp := scavengerBlueprint()
	prog := scoutProgram()
	mustValidate(t, prog, bp)

	// Facing north at {8,8}, with a component three cells straight ahead: in
	// the forward cone (design §7.1) and well inside vision range.
	seen := sim.Coord{X: 8, Y: 5}
	w.Loose = append(w.Loose, &sim.LooseComponent{ID: w.NextID(), Coord: seen, Variant: sim.Laser})
	r := addRobot(w, sim.Coord{X: 8, Y: 8}, sim.North, bp)
	r.Cargo = sim.Manipulator // already carrying, so rules 1 and 3 both apply

	rt := NewRuntime()
	rt.Install(bp.ProgramID, prog)
	w.Control = rt.Control
	w.Step()

	if got := r.Memory[0]; !got.Set || got.Coord != seen {
		t.Fatalf("Point 1 = %+v, want the sighting at %v", got, seen)
	}
	if r.Coord == (sim.Coord{X: 8, Y: 8}) {
		t.Fatal("robot saved the sighting but never moved: the side effect ended the tick")
	}
	tr, ok := rt.Trace(r.ID)
	if !ok || tr.Rule != 2 || tr.Action != MoveToOwnBase || tr.Side != 1 {
		t.Fatalf("trace = %+v, want rule 2 / %s with 1 side effect", tr, MoveToOwnBase)
	}
	if r.Coord.Chebyshev(base.Coord) >= 8 {
		t.Fatalf("robot moved to %v, which is no closer to base %v", r.Coord, base.Coord)
	}
}

// TestDefensiveExample is design §10.5 "no implicit reaction": a COME_HERE
// signal only does something because rule 2 reads it. Delete that rule and the
// identical world produces no reaction at all.
func TestDefensiveExample(t *testing.T) {
	caller := Program{V: SchemaVersion, Name: "caller", Rules: []Rule{
		{When: Pred(CarryingNothing), Then: []Action{Do(BroadcastComeHere)}},
	}}

	run := func(t *testing.T, defender Program) (*sim.Robot, sim.Coord) {
		t.Helper()
		w := flatWorld(t, 5, 16)
		w.Bases[0].Coord = sim.Coord{X: 2, Y: 2}

		bp := defenderBlueprint()
		if err := bp.Validate(); err != nil {
			t.Fatalf("blueprint invalid: %v", err)
		}
		mustValidate(t, defender, bp)

		callerBP := bp
		callerBP.ID, callerBP.ProgramID = "bp-caller", "prog-call"
		shout := addRobot(w, sim.Coord{X: 12, Y: 12}, sim.North, callerBP)
		d := addRobot(w, sim.Coord{X: 4, Y: 4}, sim.North, bp)

		rt := NewRuntime()
		rt.Install(bp.ProgramID, defender)
		rt.Install(callerBP.ProgramID, caller)
		w.Control = rt.Control

		// Tick 1 broadcasts, tick 2 delivers, tick 3 lets the reaction show.
		for i := 0; i < 3; i++ {
			w.Step()
		}
		return d, shout.Coord
	}

	t.Run("reacts when the rule is present", func(t *testing.T) {
		d, from := run(t, defensiveProgram())
		if !d.Memory[1].Set || d.Memory[1].Coord != from {
			t.Fatalf("Point 2 = %+v, want the caller's position %v", d.Memory[1], from)
		}
	})

	t.Run("ignores the signal when the rule is removed", func(t *testing.T) {
		p := defensiveProgram()
		p.Rules = append(p.Rules[:1:1], p.Rules[2:]...) // drop rule 2
		d, _ := run(t, p)
		if d.Memory[1].Set {
			t.Fatalf("Point 2 = %+v: the robot reacted to COME_HERE with no rule for it", d.Memory[1])
		}
	})
}

// TestNoRuleMatches is design §10.5 "safe failure": no match, an empty program
// and an unreachable target all idle the robot and complete the tick.
func TestNoRuleMatches(t *testing.T) {
	cases := []struct {
		name    string
		program Program
		want    int // expected trace rule index
	}{
		{"empty program", Program{V: SchemaVersion}, -1},
		{"nothing matches", Program{V: SchemaVersion, Rules: []Rule{
			{When: Pred(CarryingComponent), Then: []Action{Do(DepositComponentAtBase)}},
			{When: PredArg(HealthBelow, 1), Then: []Action{Do(Stop)}},
		}}, -1},
		{"matched rule has no target", Program{V: SchemaVersion, Rules: []Rule{
			{When: Pred(CarryingNothing), Then: []Action{DoArg(MoveToPoint, 3)}},
		}}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := flatWorld(t, 11, 16)
			bp := scavengerBlueprint()
			r := addRobot(w, sim.Coord{X: 7, Y: 7}, sim.East, bp)
			rt := NewRuntime()
			rt.Install(bp.ProgramID, tc.program)
			w.Control = rt.Control

			for i := 0; i < 20; i++ {
				w.Step()
			}
			if w.Tick != 20 {
				t.Fatalf("tick loop stalled at %d", w.Tick)
			}
			if r.Coord != (sim.Coord{X: 7, Y: 7}) {
				t.Fatalf("idle robot moved to %v", r.Coord)
			}
			tr, ok := rt.Trace(r.ID)
			if !ok {
				t.Fatal("no trace recorded")
			}
			if tr.Rule != tc.want || !tr.Idle {
				t.Fatalf("trace = %+v, want rule %d and idle", tr, tc.want)
			}
			if tr.Reason == "" {
				t.Fatal("trace carries no reason")
			}
		})
	}
}

// TestProgramDrivenWorldIsDeterministic is the E1.1 guard extended to programs:
// sim cannot import prog, so this is the only place the two can be checked
// together. Nearest-target ties and signal delivery are the traps.
func TestProgramDrivenWorldIsDeterministic(t *testing.T) {
	build := func() *sim.World {
		w := sim.Generate(19, sim.GenOpts{Width: 24, Height: 24, Colonies: 2, BarrierDensity: 0.08, Richness: 0.03})
		rt := NewRuntime()
		rt.Install("prog-scavenge", scavengerProgram())
		rt.Install("prog-defend", defensiveProgram())
		bp, db := scavengerBlueprint(), defenderBlueprint()
		for i, b := range w.Bases {
			// Two robots per colony, co-located and identically equipped, so
			// that "nearest" is a genuine tie and a sighting picked by slice
			// order would diverge.
			for j := 0; j < 2; j++ {
				use := bp
				if j == 1 {
					use = db
				}
				r := &sim.Robot{
					ID:        w.NextID(),
					Colony:    sim.ColonyID(i),
					Coord:     b.Coord,
					Heading:   sim.East,
					Health:    sim.StartingHealth(use),
					Blueprint: use,
					ProgramID: use.ProgramID,
				}
				w.Robots = append(w.Robots, r)
			}
			b.Blueprints = append(b.Blueprints, bp)
		}
		w.Control = rt.Control
		return w
	}

	a, b := build(), build()
	if a.StateHash() != b.StateHash() {
		t.Fatalf("worlds differ before stepping: %d != %d", a.StateHash(), b.StateHash())
	}
	for i := 0; i < 600; i++ {
		a.Step()
		b.Step()
		if a.StateHash() != b.StateHash() {
			t.Fatalf("program-driven worlds diverged at tick %d", i+1)
		}
	}
	for i := 0; i < 16; i++ {
		if x, y := a.Rand().Int63(), b.Rand().Int63(); x != y {
			t.Fatalf("rng streams diverged at draw %d: %d != %d", i, x, y)
		}
	}
}

// TestSignalPositionPrefersTheMatchedSignal: two signals of different kinds in
// one tick must not make save_signal_position store the wrong one.
func TestSignalPositionPrefersTheMatchedSignal(t *testing.T) {
	v := sim.RobotView{
		Signals: []sim.Signal{
			{Kind: sim.AvoidHere, From: 2, Coord: sim.Coord{X: 1, Y: 1}},
			{Kind: sim.ComeHere, From: 7, Coord: sim.Coord{X: 9, Y: 9}},
		},
	}
	e := New(Program{Rules: []Rule{
		{When: Pred(ReceivedComeHere), Then: []Action{DoArg(SaveSignalPosition, 2), Do(Stop)}},
	}})
	got := e.Decide(v)
	want := []sim.MemWrite{{Point: 1, Coord: sim.Coord{X: 9, Y: 9}}}
	if len(got.Memory) != 1 || got.Memory[0] != want[0] {
		t.Fatalf("memory writes = %+v, want %+v", got.Memory, want)
	}
}

// TestNearestVisibleBreaksTiesOnID is the determinism landmine in this package:
// a component and an enemy at the same distance must resolve the same way
// regardless of which perception list they arrived in.
func TestNearestVisibleBreaksTiesOnID(t *testing.T) {
	comp := sim.Sighting{ID: 9, Coord: sim.Coord{X: 1, Y: 0}, Distance: 3}
	enemy := sim.Sighting{ID: 4, Coord: sim.Coord{X: 0, Y: 1}, Distance: 3}
	v := sim.RobotView{VisibleComponents: []sim.Sighting{comp}, VisibleEnemies: []sim.Sighting{enemy}}
	if got, ok := nearestVisible(v); !ok || got.ID != 4 {
		t.Fatalf("nearestVisible = %+v, want the lower id at equal distance", got)
	}
	v.VisibleEnemies[0].Distance = 4
	if got, ok := nearestVisible(v); !ok || got.ID != 9 {
		t.Fatalf("nearestVisible = %+v, want the closer sighting", got)
	}
}

// TestReprogramClearsMemory is design §10.6's last bullet, which E6.3 relies on.
func TestReprogramClearsMemory(t *testing.T) {
	w := flatWorld(t, 13, 16)
	bp := scavengerBlueprint()
	r := addRobot(w, sim.Coord{X: 6, Y: 6}, sim.North, bp)
	rt := NewRuntime()
	rt.Install("prog-a", Program{Rules: []Rule{
		{When: Pred(CarryingNothing), Then: []Action{DoArg(SaveCurrentPosition, 1), Do(Stop)}},
	}})
	rt.Install("prog-b", Program{Rules: []Rule{{When: Pred(CarryingNothing), Then: []Action{Do(Stop)}}}})
	r.ProgramID = "prog-a"
	w.Control = rt.Control

	w.Step()
	if !r.Memory[0].Set {
		t.Fatal("Point 1 was never written")
	}
	r.ProgramID = "prog-b"
	w.Step()
	if r.Memory[0].Set {
		t.Fatalf("Point 1 survived a reprogram: %+v", r.Memory[0])
	}
}

// TestDecideNeverPanics feeds the evaluator programs no editor would produce.
// Programs are untrusted input; a panic here would take the tick loop with it.
func TestDecideNeverPanics(t *testing.T) {
	deep := Pred(CarryingNothing)
	for i := 0; i < MaxCondDepth*3; i++ {
		deep = And(deep)
	}
	programs := []Program{
		{Rules: []Rule{{When: Condition{Op: "nonsense"}, Then: []Action{Do(Stop)}}}},
		{Rules: []Rule{{When: Pred("no_such_predicate"), Then: []Action{Do("no_such_action")}}}},
		{Rules: []Rule{{When: PredArg(AtPoint, 99), Then: []Action{DoArg(MoveToPoint, -4)}}}},
		{Rules: []Rule{{When: PredArg(PointIsSet, 0), Then: []Action{DoArg(SaveVisibleTarget, 0), DoArg(ClearPoint, 77)}}}},
		{Rules: []Rule{{When: And(), Then: nil}, {When: Or(), Then: []Action{}}}},
		{Rules: []Rule{{When: deep, Then: []Action{Do(MoveAwayFromTarget)}}}},
		{Rules: []Rule{{When: Pred(CarryingNothing), Then: []Action{Do(MoveForward), Do(Stop), Do(AttackVisibleTarget)}}}},
		{Rules: make([]Rule, MaxRules+10)},
	}

	views := []sim.RobotView{
		{},
		{Blueprint: scavengerBlueprint(), Health: 5},
		{
			Blueprint:         defenderBlueprint(),
			Health:            100,
			Coord:             sim.Coord{X: 3, Y: 3},
			VisibleComponents: []sim.Sighting{{ID: 1, Coord: sim.Coord{X: 3, Y: 3}, Distance: 0}},
			VisibleEnemies:    []sim.Sighting{{ID: 2, Coord: sim.Coord{X: 4, Y: 3}, Distance: 1}},
			RadarTargets:      []sim.Sighting{{ID: 3, Coord: sim.Coord{X: 9, Y: 9}, Distance: 6}},
			Signals:           []sim.Signal{{Kind: sim.ComeHere, From: 1}},
			Memory:            [sim.MemPoints]sim.MemPoint{{Coord: sim.Coord{X: 1, Y: 1}, Set: true}},
		},
	}

	for i, p := range programs {
		for j, v := range views {
			e := New(p)
			a := e.Decide(v)
			if tr := e.Trace(); tr.Rule < -1 {
				t.Fatalf("program %d view %d: bogus trace %+v", i, j, tr)
			}
			for _, m := range a.Memory {
				if m.Point < 0 || m.Point >= sim.MemPoints {
					t.Fatalf("program %d view %d: out-of-range memory write %+v", i, j, m)
				}
			}
		}
	}
}
