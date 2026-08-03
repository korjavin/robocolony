package sim

import "testing"

// recallWorld is a robot far from its own base, driven by a controller that
// would happily walk it in the wrong direction forever.
func recallWorld(t *testing.T) (*World, *Robot, *Base, *int) {
	t.Helper()
	w := arena(20)
	b := w.addBase(0, Coord{2, 2})
	r := w.addRobot(0, Coord{17, 17}, East, scavengerBlueprint())
	decisions := 0
	w.driveAll(funcController(func(RobotView) Action {
		decisions++
		return Action{Kind: ActMoveForward}
	}))
	return w, r, b, &decisions
}

// TestRecallReturnsToBase is design §4.2 steps 1-2: the recalled robot suspends
// its program, navigates home on its own, and stops there.
func TestRecallReturnsToBase(t *testing.T) {
	w, r, b, decisions := recallWorld(t)

	// Ten ticks of ordinary program control first, to prove the difference.
	for i := 0; i < 10; i++ {
		w.Step()
	}
	if *decisions == 0 {
		t.Fatal("the controller was never consulted before the recall")
	}
	before := *decisions

	r.Recalled = true
	arrived := -1
	for i := 0; i < 2000 && arrived < 0; i++ {
		w.Step()
		if w.AtOwnBase(r) {
			arrived = i
		}
	}
	if arrived < 0 {
		t.Fatalf("recalled robot never reached its base: at %v, base at %v", r.Coord, b.Coord)
	}
	if arrived == 0 {
		t.Fatal("the robot reached its base on the first tick: recall must be delayed by travel, not a teleport")
	}
	if *decisions != before {
		t.Errorf("the installed program was consulted %d times while recalled, want 0", *decisions-before)
	}

	// It stays: no wandering off once home, and still flagged as recalled so the
	// observer can show "at base, awaiting program".
	at := r.Coord
	for i := 0; i < 50; i++ {
		w.Step()
	}
	if r.Coord != at {
		t.Errorf("robot left its base after arriving: %v -> %v", at, r.Coord)
	}
	if !r.Recalled || !w.AtOwnBase(r) {
		t.Errorf("robot no longer awaiting a program: recalled=%v at_base=%v", r.Recalled, w.AtOwnBase(r))
	}
	if *decisions != before {
		t.Error("the installed program ran again while the robot waited at base")
	}
}

// A recall the robot cannot complete is the design working (§4.2 constraint),
// not a bug: it must not teleport, and it must not crash.
func TestRecallWalledOffNeverArrives(t *testing.T) {
	w, r, _, _ := recallWorld(t)
	for y := 0; y < w.Height; y++ {
		w.SetTerrain(Coord{10, y}, Barrier)
	}
	r.Recalled = true
	for i := 0; i < 200; i++ {
		w.Step()
	}
	if w.AtOwnBase(r) {
		t.Fatal("robot crossed a solid wall to get home")
	}
	if !r.TargetUnreachable {
		t.Error("a robot that cannot path home should report target_unreachable")
	}
}

// TestRecallIsNotAnAction pins design §4.2's "system-level command": no primary
// action a program can pick sets the recall flag, so recall cannot be written
// as a rule. The sweep covers the whole ActionKind enum, so a kind added later
// is covered too.
func TestRecallIsNotAnAction(t *testing.T) {
	for k := ActNone; k < actionKindCount; k++ {
		w := arena(9)
		w.addBase(0, Coord{4, 4})
		r := w.addRobot(0, Coord{6, 6}, East, scavengerBlueprint())
		w.apply(r, Action{
			Kind:       k,
			Coord:      Coord{4, 4},
			Memory:     []MemWrite{{Point: 0, Coord: Coord{1, 1}}},
			Broadcasts: []SignalKind{ComeHere},
		})
		if r.Recalled {
			t.Errorf("action kind %d set Recalled: recall must not be expressible as a program rule", k)
		}
	}
}

// TestReprogramClearsState is design §4.2 steps 4-5 at the simulation level.
func TestReprogramClearsState(t *testing.T) {
	w := arena(9)
	w.addBase(0, Coord{4, 4})
	r := w.addRobot(0, Coord{4, 4}, East, scavengerBlueprint())
	for i := range r.Memory {
		r.Memory[i] = MemPoint{Coord: Coord{X: i, Y: i}, Set: true}
	}
	r.Recalled = true
	r.PathBlocked, r.TargetReached, r.TargetUnreachable = true, true, true

	r.Reprogram("prog-new")

	if r.ProgramID != "prog-new" {
		t.Errorf("ProgramID = %q, want prog-new", r.ProgramID)
	}
	for i, m := range r.Memory {
		if m.Set || m.Coord != (Coord{}) {
			t.Errorf("memory point %d survived the reprogram: %+v", i+1, m)
		}
	}
	if r.Recalled {
		t.Error("robot is still recalled after being reprogrammed; it must leave base")
	}
	if r.PathBlocked || r.TargetReached || r.TargetUnreachable {
		t.Error("navigation flags of the suspended program survived the reprogram")
	}
}
