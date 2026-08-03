package sim

import (
	"math/rand"
	"testing"
)

// funcController adapts a plain function to Controller. E3 will supply the real
// one; sim only needs something to drive.
type funcController func(RobotView) Action

func (f funcController) Decide(v RobotView) Action { return f(v) }

func (w *World) driveAll(c Controller) {
	w.Control = func(*Robot) Controller { return c }
}

// arena is a hand-made empty world: no generation, no barriers, no surprises.
func arena(side int) *World {
	return &World{
		Width:  side,
		Height: side,
		Cells:  make([]Cell, side*side),
		rng:    rand.New(rand.NewSource(1)),
	}
}

func scavengerBlueprint() Blueprint {
	return Blueprint{
		ID:         "bp-scavenger",
		Name:       "scavenger",
		Components: []Variant{Tracks, MediumArmor, Manipulator, PartsRadar},
		ProgramID:  "prog-scavenge",
	}
}

func (w *World) addBase(colony ColonyID, at Coord) *Base {
	b := &Base{Colony: colony, Coord: at, Inventory: map[Variant]int{}}
	w.Bases = append(w.Bases, b)
	return b
}

func (w *World) addRobot(colony ColonyID, at Coord, h Heading, bp Blueprint) *Robot {
	r := &Robot{
		ID:        w.NextID(),
		Colony:    colony,
		Coord:     at,
		Heading:   h,
		Health:    StartingHealth(bp),
		Blueprint: bp,
		ProgramID: bp.ProgramID,
	}
	w.Robots = append(w.Robots, r)
	return r
}

// scavenger is design §10.7 as a Go controller: deposit at base, carry home,
// pick up what is in reach, otherwise head for the nearest radar contact.
var scavenger = funcController(func(v RobotView) Action {
	switch {
	case v.AtBase && v.Cargo != VariantNone:
		return Action{Kind: ActDeposit}
	case v.Cargo != VariantNone:
		return Action{Kind: ActMoveTo, Coord: v.Base}
	case v.ComponentInReach:
		return Action{Kind: ActPickUp}
	case len(v.RadarTargets) > 0:
		return Action{Kind: ActMoveTo, Coord: v.RadarTargets[0].Coord}
	case len(v.VisibleComponents) > 0:
		return Action{Kind: ActMoveTo, Coord: v.VisibleComponents[0].Coord}
	case v.ObstacleAhead:
		return Action{Kind: ActTurnRight}
	default:
		return Action{Kind: ActMoveForward}
	}
})

func TestScavengeRoundTrip(t *testing.T) {
	w := arena(16)
	base := w.addBase(0, Coord{2, 2})
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{9, 8}, Variant: Laser})
	r := w.addRobot(0, base.Coord, North, scavengerBlueprint())
	w.driveAll(scavenger)

	for i := 0; i < 500 && base.Inventory[Laser] == 0; i++ {
		w.Step()
	}

	if got := base.Inventory[Laser]; got != 1 {
		t.Fatalf("base holds %d lasers after the round trip, want exactly 1", got)
	}
	if len(base.SortedInventory()) != 1 {
		t.Fatalf("base inventory picked up extra entries: %v", base.SortedInventory())
	}
	if len(w.Loose) != 0 {
		t.Fatalf("%d loose components left, want 0", len(w.Loose))
	}
	if r.Cargo != VariantNone {
		t.Fatalf("robot still carries %v", r.Cargo)
	}
}

func TestNoManipulatorCannotScavenge(t *testing.T) {
	w := arena(8)
	base := w.addBase(0, Coord{2, 2})
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{2, 3}, Variant: Laser})
	r := w.addRobot(0, Coord{2, 2}, North, Blueprint{ID: "bp-blind", Components: []Variant{Tracks, MediumArmor}})
	w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActPickUp} }))

	for i := 0; i < 10; i++ {
		w.Step()
	}
	if r.Cargo != VariantNone || len(w.Loose) != 1 || len(base.Inventory) != 0 {
		t.Fatal("a robot without a manipulator collected a component (design §6.3)")
	}
}

// TestVisionIsDirectional is the point of design §7.1: vision is a forward
// wedge, not a radius. Note that a turn is 45° on an eight-heading grid, so
// facing something that started behind takes four of them.
func TestVisionIsDirectional(t *testing.T) {
	behind := Coord{5, 8} // due south of the robot
	west := Coord{2, 5}   // due west of the robot

	for _, tc := range []struct {
		name  string
		at    Coord
		turns int
		want  bool
	}{
		{"directly behind, facing north", behind, 0, false},
		{"directly behind, one turn", behind, 1, false},
		{"directly behind, two turns", behind, 2, false},
		{"directly behind, three turns (cone edge)", behind, 3, true},
		{"directly behind, four turns (dead ahead)", behind, 4, true},
		{"due west, facing north", west, 0, false},
		{"due west, two turns", west, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := arena(12)
			w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: tc.at, Variant: Laser})
			r := w.addRobot(0, Coord{5, 5}, North, scavengerBlueprint())
			w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActTurnLeft} }))
			for i := 0; i < tc.turns; i++ {
				w.Step()
			}
			seen, _ := w.look(r)
			if got := len(seen) > 0; got != tc.want {
				t.Fatalf("visible = %v after %d turns (heading %d), want %v", got, tc.turns, r.Heading, tc.want)
			}
		})
	}
}

// A robot must not see further than its cone reaches, in any direction.
func TestVisionRange(t *testing.T) {
	w := arena(40)
	r := w.addRobot(0, Coord{20, 20}, North, scavengerBlueprint())
	for _, d := range []int{visionRange, visionRange + 1} {
		w.Loose = []*LooseComponent{{ID: 1, Coord: Coord{20, 20 - d}, Variant: Laser}}
		seen, _ := w.look(r)
		if want := d <= visionRange; (len(seen) > 0) != want {
			t.Fatalf("component %d cells ahead: visible = %v, want %v", d, len(seen) > 0, want)
		}
	}
}

// The radar is omnidirectional and longer-ranged: it must report what vision
// cannot see (design §7.2), and only for a blueprint that carries one.
func TestRadarSeesBehindAndNeedsAComponent(t *testing.T) {
	w := arena(40)
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{20, 20 + radarRange}, Variant: Laser})
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{20, 20 + radarRange + 1}, Variant: Laser})

	withRadar := w.addRobot(0, Coord{20, 20}, North, scavengerBlueprint())
	if got := w.radar(withRadar); len(got) != 1 || got[0].Distance != radarRange {
		t.Fatalf("parts radar reported %v, want exactly the in-range component", got)
	}
	blind := w.addRobot(0, Coord{20, 20}, North, Blueprint{Components: []Variant{Tracks, MediumArmor}})
	if got := w.radar(blind); len(got) != 0 {
		t.Fatalf("a blueprint without a radar detected %v", got)
	}
}

func TestBlockedMovement(t *testing.T) {
	w := arena(8)
	w.SetTerrain(Coord{4, 3}, Barrier)
	r := w.addRobot(0, Coord{4, 4}, North, scavengerBlueprint())
	edge := w.addRobot(0, Coord{0, 0}, West, scavengerBlueprint())
	w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActMoveForward} }))

	w.Step()

	if r.Coord != (Coord{4, 4}) {
		t.Fatalf("robot walked into a barrier, now at %v", r.Coord)
	}
	if !r.PathBlocked {
		t.Fatal("blocked movement did not raise PathBlocked")
	}
	if edge.Coord != (Coord{0, 0}) || !edge.PathBlocked {
		t.Fatalf("robot walked off the world edge to %v (blocked=%v)", edge.Coord, edge.PathBlocked)
	}
	// The flag must clear again once the robot can actually move.
	r.Heading = South
	for i := 0; i < moveTicks(r.Blueprint)+1; i++ {
		w.Step()
	}
	if r.PathBlocked || r.Coord == (Coord{4, 4}) {
		t.Fatalf("PathBlocked did not clear after a successful move (at %v, blocked=%v)", r.Coord, r.PathBlocked)
	}
}

func TestMoveToRoutesAroundAWall(t *testing.T) {
	w := arena(12)
	for y := 0; y < 10; y++ {
		w.SetTerrain(Coord{5, y}, Barrier)
	}
	r := w.addRobot(0, Coord{2, 2}, North, scavengerBlueprint())
	dest := Coord{8, 2}
	w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActMoveTo, Coord: dest} }))

	for i := 0; i < 300 && r.Coord != dest; i++ {
		w.Step()
	}
	if r.Coord != dest {
		t.Fatalf("robot stuck at %v, want %v", r.Coord, dest)
	}
	if !r.TargetReached || r.TargetUnreachable {
		t.Fatalf("arrival flags wrong: reached=%v unreachable=%v", r.TargetReached, r.TargetUnreachable)
	}
}

func TestMoveToUnreachable(t *testing.T) {
	w := arena(12)
	for y := 0; y < 12; y++ {
		w.SetTerrain(Coord{5, y}, Barrier)
	}
	r := w.addRobot(0, Coord{2, 2}, North, scavengerBlueprint())
	w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActMoveTo, Coord: Coord{8, 2}} }))

	w.Step()
	if !r.TargetUnreachable || r.Coord != (Coord{2, 2}) {
		t.Fatalf("walled-off target: unreachable=%v at %v", r.TargetUnreachable, r.Coord)
	}
	// Opening the wall must be enough; nothing may have cached the old answer.
	w.SetTerrain(Coord{5, 2}, Open)
	w.Step()
	for i := 0; i < 300 && r.Coord != (Coord{8, 2}); i++ {
		w.Step()
	}
	if r.Coord != (Coord{8, 2}) {
		t.Fatalf("robot did not use the newly opened gap, stuck at %v", r.Coord)
	}
}

func TestAutoBuild(t *testing.T) {
	bp := scavengerBlueprint()
	if err := bp.Validate(); err != nil {
		t.Fatalf("test blueprint invalid: %v", err)
	}
	w := arena(8)
	b := w.addBase(0, Coord{4, 4})
	b.Blueprints = append(b.Blueprints, bp)
	// Exactly one robot's worth of parts, plus one component the blueprint
	// does not use: production must leave it alone.
	for _, v := range bp.Components {
		b.Inventory[v]++
	}
	b.Inventory[Laser] = 1

	w.Step() // starts the build
	if b.Build.Ticks != buildTicks(bp) {
		t.Fatalf("build timer = %d, want %d", b.Build.Ticks, buildTicks(bp))
	}
	if got := b.SortedInventory(); len(got) != 1 || got[0] != (InvEntry{Variant: Laser, Count: 1}) {
		t.Fatalf("inventory after reservation = %v, want just the spare laser", got)
	}

	for i := 0; i < buildTicks(bp)-1; i++ {
		w.Step()
		if len(w.Robots) != 0 {
			t.Fatalf("robot appeared after %d of %d build ticks", i+1, buildTicks(bp))
		}
	}
	w.Step()
	if len(w.Robots) != 1 {
		t.Fatalf("%d robots after the build time, want 1", len(w.Robots))
	}
	r := w.Robots[0]
	if r.Colony != b.Colony || r.Coord != b.Coord {
		t.Fatalf("robot spawned as colony %d at %v, want %d at %v", r.Colony, r.Coord, b.Colony, b.Coord)
	}
	if r.ProgramID != bp.ProgramID {
		t.Fatalf("robot program = %q, want the blueprint default %q", r.ProgramID, bp.ProgramID)
	}
	if r.Health != StartingHealth(bp) {
		t.Fatalf("robot health = %d, want %d", r.Health, StartingHealth(bp))
	}

	// Nothing is buildable now, so the base must simply wait (design §5.2.3).
	for i := 0; i < 4*buildTicks(bp); i++ {
		w.Step()
	}
	if len(w.Robots) != 1 {
		t.Fatalf("base kept building without parts: %d robots", len(w.Robots))
	}
	if got := b.SortedInventory(); len(got) != 1 || got[0].Variant != Laser {
		t.Fatalf("idle base disturbed the inventory: %v", got)
	}
}

// Design §5.3: a colony wiped down to zero robots rebuilds from inventory.
func TestBaseRebuildsFromInventory(t *testing.T) {
	bp := scavengerBlueprint()
	w := arena(8)
	b := w.addBase(0, Coord{4, 4})
	b.Blueprints = append(b.Blueprints, bp)
	for _, v := range bp.Components {
		b.Inventory[v] += 2
	}
	for i := 0; i < 2*(buildTicks(bp)+1); i++ {
		w.Step()
	}
	if len(w.Robots) != 2 {
		t.Fatalf("%d robots from two blueprints' worth of parts, want 2", len(w.Robots))
	}
	if len(b.Inventory) != 0 {
		t.Fatalf("inventory not fully consumed: %v", b.SortedInventory())
	}
}

// Blueprint choice must come from the world's rng, and consume it, so two
// worlds with the same seed choose the same way and a third with another seed
// eventually differs.
func TestAutoBuildPicksRandomlyFromTheWorldRand(t *testing.T) {
	build := func(seed int64) string {
		w := arena(8)
		w.rng = rand.New(rand.NewSource(seed))
		b := w.addBase(0, Coord{4, 4})
		for _, id := range []string{"a", "b", "c", "d"} {
			b.Blueprints = append(b.Blueprints, Blueprint{
				ID:         id,
				Components: []Variant{Tracks, MediumArmor},
				ProgramID:  "prog-" + id,
			})
		}
		b.Inventory[Tracks] = 1
		b.Inventory[MediumArmor] = 1
		w.Step()
		return b.Build.Blueprint.ID
	}
	first := build(1)
	if build(1) != first {
		t.Fatal("the same seed chose two different blueprints")
	}
	differs := false
	for seed := int64(2); seed < 40 && !differs; seed++ {
		differs = build(seed) != first
	}
	if !differs {
		t.Fatal("every seed chose the same blueprint: selection is not random")
	}
}

func TestSignalsReachTheColonyNextTick(t *testing.T) {
	w := arena(8)
	sender := w.addRobot(0, Coord{1, 1}, North, scavengerBlueprint())
	friend := w.addRobot(0, Coord{6, 6}, North, scavengerBlueprint())
	enemy := w.addRobot(1, Coord{6, 1}, North, scavengerBlueprint())

	heard := map[int][]Signal{}
	w.Control = func(r *Robot) Controller {
		return funcController(func(v RobotView) Action {
			heard[v.ID] = append(heard[v.ID], v.Signals...)
			if v.ID == sender.ID && v.Tick == 0 {
				return Action{Broadcasts: []SignalKind{ComeHere}}
			}
			return Action{}
		})
	}

	w.Step() // tick 0: broadcast
	w.Step() // tick 1: delivery

	if len(heard[friend.ID]) != 1 || heard[friend.ID][0].Kind != ComeHere {
		t.Fatalf("friendly robot heard %v, want one COME_HERE", heard[friend.ID])
	}
	if got := heard[friend.ID][0].Coord; got != (Coord{1, 1}) {
		t.Fatalf("signal carried %v, want the sender position", got)
	}
	if len(heard[enemy.ID]) != 0 {
		t.Fatalf("an enemy colony heard %v", heard[enemy.ID])
	}
	if len(heard[sender.ID]) != 0 {
		t.Fatal("the sender heard its own broadcast")
	}

	w.Step() // tick 2: the signal is gone
	if len(heard[friend.ID]) != 1 {
		t.Fatal("a signal outlived the tick after it was sent")
	}
}

func TestMemoryWritesAreZeroTick(t *testing.T) {
	w := arena(8)
	r := w.addRobot(0, Coord{3, 3}, South, scavengerBlueprint())
	w.driveAll(funcController(func(v RobotView) Action {
		return Action{
			Kind:   ActTurnRight,
			Memory: []MemWrite{{Point: 0, Coord: v.Coord}, {Point: 9, Coord: Coord{7, 7}}},
		}
	}))
	w.Step()

	if !r.Memory[0].Set || r.Memory[0].Coord != (Coord{3, 3}) {
		t.Fatalf("memory point 0 = %+v, want the robot position", r.Memory[0])
	}
	if r.Heading != South.Turn(1) {
		t.Fatal("the primary action did not run alongside the memory write")
	}
	// Out-of-range point indexes are ignored, not a crash.
	w.driveAll(funcController(func(RobotView) Action {
		return Action{Memory: []MemWrite{{Point: 0, Clear: true}}}
	}))
	w.Step()
	if r.Memory[0].Set {
		t.Fatal("clear_point left the register set")
	}
}

// A controller must not be able to write through its view into world state.
func TestViewDoesNotAliasWorldState(t *testing.T) {
	w := arena(8)
	r := w.addRobot(0, Coord{3, 3}, North, scavengerBlueprint())
	before := w.StateHash()
	w.driveAll(funcController(func(v RobotView) Action {
		v.Blueprint.Components[0] = Laser
		v.Memory[0] = MemPoint{Coord: Coord{7, 7}, Set: true}
		return Action{}
	}))
	w.Step()

	if r.Blueprint.Components[0] != Tracks || r.Memory[0].Set {
		t.Fatal("a controller mutated the robot through its view")
	}
	if w.Tick--; w.StateHash() != before {
		t.Fatal("world state changed under an idling controller")
	}
}

// Speed is mass-sensitive (design §6.4) and turns into ticks per cell.
func TestEffectiveSpeedFallsWithMass(t *testing.T) {
	light := Blueprint{Components: []Variant{Tracks, MediumArmor}}
	heavy := Blueprint{Components: []Variant{Tracks, MediumArmor, Manipulator, PartsRadar, Laser, Laser}}
	if EffectiveSpeed(heavy) >= EffectiveSpeed(light) {
		t.Fatalf("heavy robot is not slower: %d vs %d", EffectiveSpeed(heavy), EffectiveSpeed(light))
	}
	if moveTicks(heavy) < moveTicks(light) {
		t.Fatal("a slower robot must not cross a cell in fewer ticks")
	}
	absurd := Blueprint{Components: []Variant{Tracks, MediumArmor}}
	for i := 0; i < 50; i++ {
		absurd.Components = append(absurd.Components, Laser)
	}
	if got := EffectiveSpeed(absurd); got != minSpeed {
		t.Fatalf("speed = %d under absurd mass, want the %d floor", got, minSpeed)
	}
}

// A robot must be occupied for the whole cost of its action.
func TestMovementCostsTicks(t *testing.T) {
	w := arena(16)
	bp := scavengerBlueprint()
	r := w.addRobot(0, Coord{8, 8}, North, bp)
	w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActMoveForward} }))

	cost := moveTicks(bp)
	if cost < 2 {
		t.Skip("scavenger crosses a cell per tick; nothing to observe")
	}
	for i := 0; i < cost; i++ {
		w.Step()
	}
	if want := (Coord{8, 8 - 1}); r.Coord != want {
		t.Fatalf("after %d ticks robot is at %v, want %v", cost, r.Coord, want)
	}
	w.Step()
	if want := (Coord{8, 8 - 2}); r.Coord != want {
		t.Fatalf("robot at %v, want %v", r.Coord, want)
	}
}

func TestDropAndPickUpAgain(t *testing.T) {
	w := arena(8)
	w.addBase(0, Coord{1, 1})
	r := w.addRobot(0, Coord{4, 4}, North, scavengerBlueprint())
	r.Cargo = Laser
	w.driveAll(funcController(func(v RobotView) Action {
		if v.Cargo != VariantNone {
			return Action{Kind: ActDrop}
		}
		return Action{Kind: ActPickUp}
	}))

	w.Step()
	if len(w.Loose) != 1 || w.Loose[0].Coord != (Coord{4, 4}) || r.Cargo != VariantNone {
		t.Fatalf("drop left %d loose and cargo %v", len(w.Loose), r.Cargo)
	}
	for i := 0; i < interactTicks; i++ {
		w.Step()
	}
	if r.Cargo != Laser || len(w.Loose) != 0 {
		t.Fatalf("robot did not pick its own drop back up, cargo = %v", r.Cargo)
	}
}

// The tick loop must not smuggle in nondeterminism: two worlds with the same
// seed, driven by the same controller, stay bit-identical for a long run that
// exercises movement, vision, scavenging, deposits and production.
func TestTickLoopIsDeterministic(t *testing.T) {
	build := func() *World {
		w := Generate(9, DefaultGenOpts())
		bp := scavengerBlueprint()
		// Two approved blueprints, both buildable: with only one, production
		// picks index 0 every time and the random selection — the most likely
		// place for a stray global math/rand to hide — is never exercised.
		alt := Blueprint{
			ID:         "bp-runner",
			Name:       "runner",
			Components: []Variant{Tracks, MediumArmor, Manipulator},
			ProgramID:  "prog-run",
		}
		for _, b := range w.Bases {
			b.Blueprints = append(b.Blueprints, bp, alt)
			for _, v := range bp.Components {
				b.Inventory[v] += 3
			}
			for _, v := range alt.Components {
				b.Inventory[v] += 3
			}
		}
		for i, b := range w.Bases {
			w.addRobot(ColonyID(i), b.Coord, Heading(i), bp)
		}
		w.driveAll(scavenger)
		return w
	}
	a, b := build(), build()
	for i := 0; i < 600; i++ {
		a.Step()
		b.Step()
		if a.StateHash() != b.StateHash() {
			t.Fatalf("worlds diverged at tick %d", i)
		}
	}
	if len(a.Robots) < 2 {
		t.Fatal("the run never produced a robot; it is not exercising much")
	}
	for i := 0; i < 16; i++ {
		if x, y := a.Rand().Int63(), b.Rand().Int63(); x != y {
			t.Fatalf("rng streams diverged at draw %d: %d != %d", i, x, y)
		}
	}
}

// Generation must not strand a colony behind barriers: a base that cannot reach
// anything can never scavenge, and design §5.3 leaves it inactive forever.
// The default density strands nothing, but the lobby may set it much higher:
// at 0.30 more than half of all seeds grow pockets of open ground no base can
// reach, so sweep the densities a match can actually be configured with.
func TestGeneratedArenasAreConnected(t *testing.T) {
	for _, density := range []float64{0.0, 0.08, 0.30, 0.45} {
		for seed := int64(1); seed <= 30; seed++ {
			opts := DefaultGenOpts()
			opts.BarrierDensity = density
			w := Generate(seed, opts)
			reach := w.reachable(w.Bases[0].Coord, Tracks)
			for _, b := range w.Bases[1:] {
				if !reach[w.index(b.Coord)] {
					t.Fatalf("density %.2f seed %d: base %d at %v is walled off", density, seed, b.Colony, b.Coord)
				}
			}
			for _, l := range w.Loose {
				if !reach[w.index(l.Coord)] {
					t.Fatalf("density %.2f seed %d: loose component %d at %v is unreachable", density, seed, l.ID, l.Coord)
				}
			}
		}
	}
}

func TestPathIsAShortestRoute(t *testing.T) {
	w := arena(10)
	if p := w.path(Coord{1, 1}, Coord{4, 5}, Tracks); len(p) != 4 {
		t.Fatalf("path length %d over open ground, want the Chebyshev distance 4: %v", len(p), p)
	}
	if p := w.path(Coord{1, 1}, Coord{1, 1}, Tracks); p != nil {
		t.Fatalf("path to self = %v, want nil", p)
	}
	w.SetTerrain(Coord{5, 5}, Barrier)
	if p := w.path(Coord{1, 1}, Coord{5, 5}, Tracks); p != nil {
		t.Fatalf("path into a barrier = %v, want nil", p)
	}
	if p := w.path(Coord{1, 1}, Coord{20, 20}, Tracks); p != nil {
		t.Fatalf("path off the map = %v, want nil", p)
	}
}
