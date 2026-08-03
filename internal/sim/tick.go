package sim

import "slices"

// Balance constants. E7.3 rebalances the simulation by editing the numbers in
// this block; no logic below should need restructuring to retune.
const (
	// Speed model, design §6.4:
	//   effective_speed = locomotion_base_speed - mass_penalty(total_mass)
	// A terrain modifier is the third term in the design formula; the POC
	// terrain set is Open plus a hard Barrier, so it is uniformly zero and is
	// omitted until E7.3 adds the rubble/track-favoured rows.
	baseSpeedTracks   = 12
	baseSpeedUnknown  = 8  // locomotion variants E7.2 has not tuned yet
	massPerSpeedPoint = 20 // every N units of mass costs one speed point
	minSpeed          = 2
	speedScale        = 12 // ticks to cross one cell = ceil(speedScale / speed)

	// Action durations, in ticks. Every action costs at least one tick.
	turnTicks     = 1
	interactTicks = 2
	idleTicks     = 1
	// Firing occupies the robot for one tick; the reload is per weapon module
	// and lives in the weapon table (component.go), which is why a two-weapon
	// robot can fire on consecutive ticks and a one-weapon robot cannot.
	attackTicks = 1

	// Forward vision, design §7.1.
	visionRange = 6
	// cos² of the cone's half-angle, as an exact rational so the test stays
	// integer-only. 1/2 is ±45°: a 90° wedge, and nothing behind the robot.
	visionCosSqNum = 1
	visionCosSqDen = 2

	// Specialist radar, design §7.2. Omnidirectional, one target class.
	radarRange = 16

	// Reach for pick up and deposit: the robot's own cell or one cell away.
	interactRange = 1

	// Base production, design §5.2.
	buildTicksBase         = 20
	buildTicksPerComponent = 5
)

// SignalKind is one of the two shared-channel signals (design §7.5).
type SignalKind uint8

const (
	ComeHere SignalKind = iota
	AvoidHere
)

// Signal is one broadcast on the friendly channel. Per the locked decision in
// AGENTS.md the channel is global: every robot of the sender's colony hears it,
// with no radius. A signal exists for exactly one tick, the tick after it was
// sent, and never interrupts the receiver by itself (design §7.5).
type Signal struct {
	Kind   SignalKind
	From   int // sender robot id
	Colony ColonyID
	Coord  Coord // sender position at send time
}

// ActionKind is the primary action of one evaluation cycle (design §10.4).
type ActionKind uint8

const (
	ActNone ActionKind = iota // idle
	ActMoveForward
	ActTurnLeft
	ActTurnRight
	ActStop
	ActMoveTo // navigate one step towards Action.Coord over a BFS path
	ActPickUp
	ActDeposit
	ActDrop
	// ActTurnRandom is design §10.4's turn_random. It lives here rather than in
	// the controller because randomness must come from the world's seeded rng —
	// a controller that rolled its own would break the determinism guard.
	ActTurnRandom
	// ActAttack fires at Action.Coord. Combat is never automatic (design §8):
	// this happens only because a rule selected a target and issued an attack.
	ActAttack
)

// MemWrite is a zero-tick write to one coordinate register (design §7.4).
// Point is a 0-based index into Robot.Memory; the editor numbers them 1..3.
type MemWrite struct {
	Point int
	Coord Coord
	Clear bool
}

// Action is what a Controller returns for one tick: any number of zero-tick
// bookkeeping side effects, then at most one primary action. This is the
// action model locked in AGENTS.md — memory writes and broadcasts do not end
// the rule scan, the primary action does.
type Action struct {
	Kind  ActionKind
	Coord Coord // destination for ActMoveTo

	Memory     []MemWrite   // applied before Kind
	Broadcasts []SignalKind // applied before Kind, heard next tick
}

// Sighting is one perceived entity. Variant is the component type for a loose
// component and VariantNone for a robot.
type Sighting struct {
	ID       int
	Coord    Coord
	Variant  Variant
	Colony   ColonyID
	Distance int
}

// RobotView is everything a program may know (design §7.3): own position, base
// position, memory, cargo, health, what forward vision and the installed radar
// report, and the signals heard this tick. It is a value copy, so a controller
// cannot reach into world state — and it deliberately carries no map, no
// terrain beyond the cell ahead, and nothing about entities it cannot perceive.
type RobotView struct {
	Tick      uint64
	ID        int
	Colony    ColonyID
	Coord     Coord
	Heading   Heading
	Health    int
	Cargo     Variant
	Blueprint Blueprint
	Base      Coord
	HasBase   bool
	Memory    [MemPoints]MemPoint

	VisibleComponents []Sighting // forward cone only, nearest first
	VisibleEnemies    []Sighting // forward cone only, nearest first
	RadarTargets      []Sighting // radar only, omnidirectional, nearest first
	Signals           []Signal   // heard this tick, own colony, never own

	// WeaponReady is true when at least one installed weapon is off cooldown,
	// and WeaponRange is how far those reloaded weapons reach — zero while
	// everything is reloading. Both are about this tick, so a rule that tests
	// them and attacks always finds a weapon that can take the shot.
	WeaponReady bool
	WeaponRange int

	ObstacleAhead     bool // the cell straight ahead is impassable
	ComponentInReach  bool // a loose component is co-located or adjacent
	AtBase            bool // within reach of own base
	PathBlocked       bool // last movement was refused
	TargetReached     bool // last ActMoveTo arrived
	TargetUnreachable bool // last ActMoveTo found no path
}

// Controller decides one robot's action for one tick. internal/prog implements
// this over the rule program in E3; sim must never import it.
type Controller interface {
	Decide(RobotView) Action
}

// baseSpeed is the locomotion unit's base speed (design §6.1).
func baseSpeed(locomotion Variant) int {
	switch locomotion {
	case Tracks:
		return baseSpeedTracks
	default:
		return baseSpeedUnknown
	}
}

// locomotionOf returns the blueprint's locomotion variant. Validate guarantees
// exactly one on an approved blueprint; an unvalidated one falls back to the
// untuned default.
func locomotionOf(bp Blueprint) Variant {
	for _, v := range bp.Components {
		if k, ok := v.Kind(); ok && k == KindLocomotion {
			return v
		}
	}
	return VariantNone
}

// EffectiveSpeed implements design §6.4: heavier robots are slower, and the
// locomotion unit sets both the ceiling and the floor.
func EffectiveSpeed(bp Blueprint) int {
	return max(baseSpeed(locomotionOf(bp))-bp.Mass()/massPerSpeedPoint, minSpeed)
}

// moveTicks is how many ticks one cell of movement costs.
func moveTicks(bp Blueprint) int {
	s := EffectiveSpeed(bp)
	return max(1, (speedScale+s-1)/s)
}

// StartingHealth derives durability from the armoured body (design §6.1),
// reading the armor tier table rather than a formula so E7.3 can make heavy
// armor tough without also making it heavy. It is exported because it is also
// the denominator of the health_below/health_above predicates (design §10.3),
// which are evaluated outside this package.
func StartingHealth(bp Blueprint) int {
	health := healthBase
	for _, v := range bp.Components {
		if k, ok := v.Kind(); ok && k == KindArmor {
			health += armorHealth(v)
		}
	}
	return health
}

// baseOf returns the colony's base.
func (w *World) baseOf(colony ColonyID) *Base {
	for _, b := range w.Bases {
		if b.Colony == colony {
			return b
		}
	}
	return nil
}

// Step advances the world by one tick: every robot that is not mid-action
// decides and acts, wrecks are swept and salvaged, then every base advances
// production. Order is fixed — robots in slice order, the sweep, then bases in
// slice order — so two worlds with equal state take equal steps. Nothing here
// iterates a map.
//
// A finished match (design §9) does not step at all: the world freezes exactly
// as it was and stays readable.
func (w *World) Step() {
	if w.Ended() {
		return
	}
	inbox := w.signals
	var next []Signal

	for _, r := range w.Robots {
		// Weapons reload while the robot is busy with something else, so this
		// runs before the action cooldown check, not after it.
		for i := range r.WeaponCooldown {
			if r.WeaponCooldown[i] > 0 {
				r.WeaponCooldown[i]--
			}
		}
		if r.Cooldown > 0 {
			r.Cooldown--
			continue
		}
		if w.Control == nil {
			continue
		}
		c := w.Control(r)
		if c == nil {
			continue
		}
		a := c.Decide(w.View(r, inbox))
		next = append(next, w.apply(r, a)...)
	}

	// Combat only ever clamps health at zero; removal happens here, once, after
	// the loop above has finished walking the robot slice.
	w.sweepDestroyed()

	// Bases last, so a robot produced this tick first acts on the next one —
	// and so a colony wiped out this tick can already start rebuilding from
	// inventory (design §5.3).
	for _, b := range w.Bases {
		if w.hasRobots(b.Colony) {
			b.Stats.TicksActive++
		}
		w.produce(b)
	}

	w.signals = next
	w.Tick++
}

// View assembles what the robot may perceive this tick.
func (w *World) View(r *Robot, inbox []Signal) RobotView {
	v := RobotView{
		Tick:              w.Tick,
		ID:                r.ID,
		Colony:            r.Colony,
		Coord:             r.Coord,
		Heading:           r.Heading,
		Health:            r.Health,
		Cargo:             r.Cargo,
		Blueprint:         r.Blueprint,
		Memory:            r.Memory,
		PathBlocked:       r.PathBlocked,
		TargetReached:     r.TargetReached,
		TargetUnreachable: r.TargetUnreachable,
	}
	// A Blueprint is a value, but its Components slice is not: without the
	// clone a controller could write through the view into live world state,
	// changing the robot's mass and every hash after it.
	v.Blueprint.Components = slices.Clone(r.Blueprint.Components)
	if b := w.baseOf(r.Colony); b != nil {
		v.Base, v.HasBase = b.Coord, true
		v.AtBase = r.Coord.Chebyshev(b.Coord) <= interactRange
	}

	v.VisibleComponents, v.VisibleEnemies = w.look(r)
	v.RadarTargets = w.radar(r)
	v.WeaponReady, v.WeaponRange = weaponry(r)
	v.ObstacleAhead = !w.Passable(add(r.Coord, r.Heading.Delta()), locomotionOf(r.Blueprint))
	v.ComponentInReach = w.componentInReach(r) != nil

	for _, s := range inbox {
		if s.Colony == r.Colony && s.From != r.ID {
			v.Signals = append(v.Signals, s)
		}
	}
	return v
}

// apply runs the zero-tick side effects, then the primary action, and charges
// the robot's cooldown. It returns the signals broadcast this tick.
func (w *World) apply(r *Robot, a Action) []Signal {
	for _, m := range a.Memory {
		if m.Point < 0 || m.Point >= MemPoints {
			continue
		}
		if m.Clear {
			r.Memory[m.Point] = MemPoint{}
		} else {
			r.Memory[m.Point] = MemPoint{Coord: m.Coord, Set: true}
		}
	}
	var sent []Signal
	for _, k := range a.Broadcasts {
		sent = append(sent, Signal{Kind: k, From: r.ID, Colony: r.Colony, Coord: r.Coord})
	}
	r.Cooldown = max(1, w.primary(r, a)) - 1
	return sent
}

// primary executes one primary action and returns its cost in ticks.
func (w *World) primary(r *Robot, a Action) int {
	switch a.Kind {
	case ActMoveForward:
		return w.step(r, add(r.Coord, r.Heading.Delta()))

	case ActTurnLeft:
		r.Heading = r.Heading.Turn(-1)
		return turnTicks

	case ActTurnRight:
		r.Heading = r.Heading.Turn(1)
		return turnTicks

	case ActTurnRandom:
		r.Heading = Heading(w.rng.Intn(int(headingCount)))
		return turnTicks

	case ActMoveTo:
		return w.moveTo(r, a.Coord)

	case ActAttack:
		return w.attack(r, a.Coord)

	case ActPickUp:
		w.pickUp(r)
		return interactTicks

	case ActDeposit:
		w.deposit(r)
		return interactTicks

	case ActDrop:
		w.drop(r)
		return interactTicks

	default: // ActNone, ActStop
		return idleTicks
	}
}

// step moves the robot one cell. A world edge or a barrier refuses the move and
// raises PathBlocked for the next perception cycle rather than erroring
// (design §10.3 path_blocked). Robots do not block each other: the POC has no
// unit collision, only terrain.
func (w *World) step(r *Robot, to Coord) int {
	if !w.Passable(to, locomotionOf(r.Blueprint)) {
		r.PathBlocked = true
		return idleTicks
	}
	r.Coord = to
	r.PathBlocked = false
	return moveTicks(r.Blueprint)
}

// moveTo takes one step along a freshly computed BFS path. The path is
// recomputed every time and never cached, so a barrier that moved between ticks
// cannot strand a robot on a stale route.
func (w *World) moveTo(r *Robot, dest Coord) int {
	r.TargetUnreachable = false
	if r.Coord == dest {
		r.TargetReached = true
		return idleTicks
	}
	r.TargetReached = false

	// ponytail: BFS per move action, O(cells) and one visited slice each time.
	// Cache a path only if profiling says so — and then invalidate on terrain
	// change, which is exactly the bug the recompute avoids.
	path := w.path(r.Coord, dest, locomotionOf(r.Blueprint))
	if len(path) == 0 {
		r.TargetUnreachable = true
		r.PathBlocked = true
		return idleTicks
	}
	r.Heading = headingTo(r.Coord, path[0])
	cost := w.step(r, path[0])
	r.TargetReached = r.Coord == dest
	return cost
}

// weaponry reports whether any installed weapon is off cooldown, and how far
// the ready ones reach. Both describe *this* tick: reach ignores a reloading
// long-range module, so "in weapon range" means "readyWeapon would find
// something" and a rule can never pick an attack the tick cannot carry out.
//
// Slots past MaxWeapons are ignored: Validate rejects them, and a hand-built
// blueprint must not index past the cooldown array.
func weaponry(r *Robot) (ready bool, reach int) {
	for i, v := range r.Blueprint.Weapons() {
		if i >= MaxWeapons {
			break
		}
		spec, ok := WeaponStats(v)
		if !ok || r.WeaponCooldown[i] > 0 {
			continue
		}
		ready = true
		reach = max(reach, spec.Range)
	}
	return ready, reach
}

// readyWeapon picks the weapon that fires at a target dist cells away: the
// first one in slot order that is both reloaded and long enough to reach.
//
// ponytail: slot order, not "best weapon for the shot". Design §12 P1 leaves
// selection open and E7.9 owns it; the smallest deterministic rule is first
// ready in slot order, and a player who wants the cannon to fire first installs
// it first.
func readyWeapon(r *Robot, dist int) (slot int, spec WeaponSpec, ok bool) {
	for i, v := range r.Blueprint.Weapons() {
		if i >= MaxWeapons {
			break
		}
		s, known := WeaponStats(v)
		if !known || r.WeaponCooldown[i] > 0 || dist > s.Range {
			continue
		}
		return i, s, true
	}
	return 0, WeaponSpec{}, false
}

// enemyAt returns the hostile robot standing on a cell, lowest id first so a
// stack of them resolves the same way every run. Friendly robots are invisible
// to this: design §12 P1 leaves friendly fire open and the POC answer is no
// friendly fire. Bases are indestructible (design §5.3) and are not targets.
//
// A robot already at zero health is not a target either. It is still in the
// slice until the end-of-tick sweep, and without this a second shooter later in
// the same tick would shoot the wreck and be credited a second kill for it.
func (w *World) enemyAt(colony ColonyID, at Coord) *Robot {
	var best *Robot
	for _, o := range w.Robots {
		if o.Colony == colony || o.Coord != at || isDestroyed(o) {
			continue
		}
		if best == nil || o.ID < best.ID {
			best = o
		}
	}
	return best
}

// attack resolves one shot at a cell (design §8). A missing target, no weapon,
// a reloading weapon or a target out of every weapon's range all come out as a
// wasted tick — never an error, never a panic, and never an rng draw, so an
// idle attack cannot shift the random stream.
//
// Health may reach zero here; the robot itself is removed by the end-of-tick
// sweep, never mid-loop.
func (w *World) attack(r *Robot, at Coord) int {
	target := w.enemyAt(r.Colony, at)
	if target == nil {
		return idleTicks
	}
	slot, spec, ok := readyWeapon(r, r.Coord.Chebyshev(at))
	if !ok {
		return idleTicks
	}
	r.WeaponCooldown[slot] = spec.Cooldown
	// The world's rng, never math/rand: this roll is the easiest place in the
	// whole package to break determinism.
	if w.rng.Intn(100) < spec.Accuracy {
		target.Health = max(target.Health-spec.Damage, 0)
		// Credited on the killing blow only: enemyAt refuses a target that is
		// already at zero, so no second shooter can claim the same wreck.
		if isDestroyed(target) {
			if s := w.statsOf(r.Colony); s != nil {
				s.Kills++
			}
		}
	}
	return attackTicks
}

// componentInReach returns the nearest loose component the robot could pick up,
// or nil. Ties break on id so the choice never depends on slice order.
func (w *World) componentInReach(r *Robot) *LooseComponent {
	var best *LooseComponent
	bestDist := 0
	for _, l := range w.Loose {
		d := r.Coord.Chebyshev(l.Coord)
		if d > interactRange {
			continue
		}
		if best == nil || d < bestDist || (d == bestDist && l.ID < best.ID) {
			best, bestDist = l, d
		}
	}
	return best
}

// pickUp implements pick_up_component. A manipulator is required (design §6.3)
// and the robot carries at most one component (design §12 P0, smallest answer).
func (w *World) pickUp(r *Robot) {
	if !r.Blueprint.Has(KindManipulator) || r.Cargo != VariantNone {
		return
	}
	l := w.componentInReach(r)
	if l == nil {
		return
	}
	r.Cargo = l.Variant
	w.Loose = slices.DeleteFunc(w.Loose, func(x *LooseComponent) bool { return x == l })
}

// deposit implements deposit_component_at_base: own base only, manipulator
// required, component moves into base inventory.
func (w *World) deposit(r *Robot) {
	if !r.Blueprint.Has(KindManipulator) || r.Cargo == VariantNone {
		return
	}
	b := w.baseOf(r.Colony)
	if b == nil || r.Coord.Chebyshev(b.Coord) > interactRange {
		return
	}
	if b.Inventory == nil {
		b.Inventory = map[Variant]int{}
	}
	b.Inventory[r.Cargo]++
	b.Stats.Collected++
	r.Cargo = VariantNone
}

// drop implements drop_component: the cargo becomes an ordinary loose
// component on the robot's cell.
func (w *World) drop(r *Robot) {
	if r.Cargo == VariantNone {
		return
	}
	w.dropAt(r.Coord, r.Cargo)
	r.Cargo = VariantNone
}

// BuildOrder is a base's current assembly job. Ticks == 0 means idle.
type BuildOrder struct {
	Blueprint Blueprint
	Ticks     int
}

// buildTicks is how long a blueprint takes to assemble (design §12 P1 is still
// open between fixed and mass-dependent; this is the component-count answer).
func buildTicks(bp Blueprint) int {
	return buildTicksBase + len(bp.Components)*buildTicksPerComponent
}

// produce runs design §5.2 for one base: finish the current job, then start a
// new one if the inventory covers an approved blueprint. The base is
// indestructible and rebuilds purely from inventory (design §5.3), so this is
// the colony's only recovery path and has no failure state — it just waits.
func (w *World) produce(b *Base) {
	if b.Build.Ticks > 0 {
		b.Build.Ticks--
		if b.Build.Ticks == 0 {
			w.spawn(b, b.Build.Blueprint)
			b.Build = BuildOrder{}
		}
		return
	}

	// 1-3. Every approved blueprint the inventory fully covers.
	var buildable []Blueprint
	for _, bp := range b.Blueprints {
		if covers(b.Inventory, bp) {
			buildable = append(buildable, bp)
		}
	}
	if len(buildable) == 0 {
		return
	}
	// 4. Random selection, from the world's seeded rng — never math/rand.
	bp := buildable[w.rng.Intn(len(buildable))]
	// 5. Reserve and consume.
	for _, v := range bp.Components {
		b.Inventory[v]--
		if b.Inventory[v] <= 0 {
			// Keep the inventory canonical: an absent variant and a
			// zero-count variant must not be two hashable states.
			delete(b.Inventory, v)
		}
	}
	// 6. Assemble over a build time; 7. release happens when the timer expires.
	b.Build = BuildOrder{Blueprint: bp, Ticks: buildTicks(bp)}
}

// covers reports whether the inventory holds every component the blueprint
// needs, counting duplicates. Only map lookups, never map iteration.
func covers(inv map[Variant]int, bp Blueprint) bool {
	if len(bp.Components) == 0 {
		return false
	}
	used := map[Variant]int{}
	for _, v := range bp.Components {
		used[v]++
		if inv[v] < used[v] {
			return false
		}
	}
	return true
}

// spawn releases a finished robot at the base with the blueprint's default
// program (design §5.2 step 7).
func (w *World) spawn(b *Base, bp Blueprint) {
	w.Robots = append(w.Robots, &Robot{
		ID:        w.NextID(),
		Colony:    b.Colony,
		Coord:     b.Coord,
		Heading:   Heading(w.rng.Intn(int(headingCount))),
		Health:    StartingHealth(bp),
		Blueprint: bp,
		ProgramID: bp.ProgramID,
	})
}

func add(a, b Coord) Coord { return Coord{a.X + b.X, a.Y + b.Y} }
