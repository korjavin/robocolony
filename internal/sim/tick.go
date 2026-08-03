package sim

import "slices"

// Balance constants. Rebalancing the simulation is editing the numbers in this
// block and the tables in component.go and world.go; no logic below should need
// restructuring to retune.
const (
	// Speed model, design §6.4, all three terms:
	//
	//   effective_speed = locomotion_base_speed
	//                   - total_mass / locomotion_mass_tolerance
	//                   + terrain_modifier(terrain, locomotion)
	//
	// The first two terms are per-locomotion and live in locomotionSpecs
	// (component.go); the third is per-(terrain, locomotion) and lives in
	// terrainSpecs (world.go). Only the shared scaling is here.
	baseSpeedUnknown         = 8  // locomotion variants with no tuned row
	massPerSpeedPointUnknown = 20 // ditto, mass tolerance
	minSpeed                 = 2
	// favoredSpeedBonus is what design §3.1's "passable or favored" is worth:
	// enough that a legs robot crossing rubble beats the same chassis on open
	// ground, which is the whole reason to take legs.
	favoredSpeedBonus = 4
	// speedScale converts speed into time: ticks to cross one cell is
	// ceil(speedScale / speed). Large enough that the ~16..2 speed range maps
	// onto distinct tick costs instead of collapsing the fast end into 1.
	speedScale = 24

	// Action durations, in ticks. Every action costs at least one tick.
	//
	// turnTickDivisor makes turning a fraction of a cell of movement, so a slow
	// heavy robot is also slow to look around — scanning with forward vision
	// (design §7.1) costs a big chassis real time.
	turnTickDivisor = 3
	interactTicks   = 2
	idleTicks       = 1
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
	// The enemy-base radar reaches further than the other two: design §6.2 sells
	// it as a *navigation landmark*, and a landmark you have to be sixteen cells
	// from is not one. It also has the least to report — one static contact per
	// opponent — so the extra reach buys no extra information density.
	baseRadarRange = 28

	// Reach for pick up and deposit: the robot's own cell or one cell away.
	interactRange = 1

	// Signal reach, design §7.5. The friendly channel is not global: a signal
	// reaches only friendly robots within signalRadius of the sender, measured
	// in the package's one distance, Chebyshev. Expressed as a divisor of the
	// longer arena side so "about half the board" holds on any map — 32 cells
	// on the default 64×64 arena, which is longer than any radar (a radio must
	// outreach the sensors) but short enough that a scout on the far side of
	// the map is genuinely out of touch.
	signalRadiusDivisor = 2

	// Base production, design §5.2. Build time is mass-dependent, decided in
	// rc-w9s.22: a big design now costs tempo as well as parts, so the cheap
	// colony gets more attempts.
	//
	// Linear in mass, with a floor. The extremes are what picked the numbers,
	// on the design §6.3 legal range (mass 44 for legs + light armor up to 190
	// for tracks + heavy armor + two cannons + manipulator + radar):
	//
	//	lightest legal   44 mass →  26 ticks   (2.6 s: cheap, not free)
	//	starter scavenger 88 mass →  41 ticks   (was 40 — the measured baseline
	//	                                         keeps its tempo)
	//	heavy gunner     145 mass →  60 ticks
	//	heaviest legal   190 mass →  75 ticks   (~3× the lightest, and still a
	//	                                         eightieth of a match)
	//
	// Component count is deliberately no longer a term: it is highly correlated
	// with mass, and charging both taxed the same thing twice.
	buildTicksBase   = 12
	massPerBuildTick = 3
)

// SignalKind is one of the two shared-channel signals (design §7.5).
type SignalKind uint8

const (
	ComeHere SignalKind = iota
	AvoidHere
)

// Signal is one broadcast on the friendly channel. Per the locked decision in
// AGENTS.md the channel carries about half the board: every robot of the
// sender's colony within World.signalRadius of Coord hears it, and one further
// out hears nothing. A signal exists for exactly one tick, the tick after it
// was sent, and never interrupts the receiver by itself (design §7.5).
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
	// actionKindCount closes the enum. New kinds are appended *above* it, never
	// inserted: the numbers are wire values. It exists so a test can sweep every
	// kind — that is how "recall is not expressible as a program rule"
	// (design §4.2) stays true as kinds are added.
	actionKindCount
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

// SightingKind says what a perceived entity is. It exists because the design
// §6.2 enemy-base radar puts a *place* into the same contact list as robots and
// loose components, and "attack the nearest radar contact" must be able to tell
// a base apart from a robot.
type SightingKind uint8

const (
	SightComponent SightingKind = iota
	SightRobot
	SightBase
)

// Sighting is one perceived entity. Variant is the component type for a loose
// component and VariantNone for a robot or a base; Kind is the reliable
// discriminator.
type Sighting struct {
	ID       int
	Kind     SightingKind
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

// SpeedOn implements design §6.4 on a given terrain class: heavier robots are
// slower, the locomotion unit sets the ceiling, the terrain modifier is the
// favoured-terrain bonus, and minSpeed is the floor nothing falls through.
func SpeedOn(bp Blueprint, t Terrain) int {
	s := locomotionStats(locomotionOf(bp))
	return max(s.BaseSpeed-bp.Mass()/s.MassPerSpeedPoint+t.SpeedBonus(s.Variant), minSpeed)
}

// EffectiveSpeed is SpeedOn open ground: the blueprint's speed with no terrain
// modifier, which is what a designer comparing two blueprints wants to see.
func EffectiveSpeed(bp Blueprint) int { return SpeedOn(bp, Open) }

// moveTicks is how many ticks entering a cell of this terrain costs.
func moveTicks(bp Blueprint, t Terrain) int {
	s := SpeedOn(bp, t)
	return max(1, (speedScale+s-1)/s)
}

// turnTicks is what one eighth-turn costs: a fraction of a cell of movement on
// open ground, never less than a tick. Terrain does not enter it — the robot
// stays where it is.
func turnTicks(bp Blueprint) int {
	return max(1, moveTicks(bp, Open)/turnTickDivisor)
}

// StartingHealth derives durability from the armoured body (design §6.1),
// reading the armor tier table rather than a formula so heavy armor can be made
// tough without also being made heavy. It is exported because it is also
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

// AtOwnBase reports whether the robot is within reach of its own base. It is
// the one definition: the at_own_base a program sees and the "only at its own
// base" gate on reprogramming (design §4.2 step 3) can never disagree.
func (w *World) AtOwnBase(r *Robot) bool {
	b := w.baseOf(r.Colony)
	return b != nil && r.Coord.Chebyshev(b.Coord) <= interactRange
}

// RobotByID returns the live robot with this id, or nil. Destroyed robots are
// swept at the end of every tick, so between steps this only ever yields one
// that is still in the fight.
func (w *World) RobotByID(id int) *Robot {
	for _, r := range w.Robots {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// recallAction is what a recalled robot does this tick: walk home over the
// world's own BFS navigation, then hold position at base. It reuses ActMoveTo
// rather than a second pathfinder, so recall obeys terrain, speed and
// path_blocked exactly like any other movement.
func (w *World) recallAction(r *Robot) Action {
	b := w.baseOf(r.Colony)
	if b == nil || w.AtOwnBase(r) {
		return Action{Kind: ActStop}
	}
	return Action{Kind: ActMoveTo, Coord: b.Coord}
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
		// A robot shot to pieces earlier in this same loop is still in the slice
		// until the sweep below. It does not get a turn: no reload, no decision,
		// and above all no shot back from a wreck.
		if isDestroyed(r) {
			continue
		}
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
		// Recall is a system-level override (design §4.2 step 2): the installed
		// program is suspended — not consulted at all — and the robot navigates
		// home over ordinary movement, at ordinary speed. That is the point:
		// reprogramming is delayed by travel, and a blocked or threatened robot
		// may never arrive.
		if r.Recalled {
			w.apply(r, w.recallAction(r))
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
		w.produce(b)
		// Counted after production, so the tick a wiped-out colony is released
		// back into play counts as active.
		if w.hasRobots(b.Colony) {
			b.Stats.TicksActive++
		}
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
		v.AtBase = w.AtOwnBase(r)
	}

	v.VisibleComponents, v.VisibleEnemies = w.look(r)
	v.RadarTargets = w.radar(r)
	v.WeaponReady, v.WeaponRange = weaponry(r)
	v.ObstacleAhead = !w.Passable(add(r.Coord, r.Heading.Delta()), locomotionOf(r.Blueprint))
	v.ComponentInReach = w.componentInReach(r) != nil

	radius := w.signalRadius()
	for _, s := range inbox {
		if s.Colony == r.Colony && s.From != r.ID && s.Coord.Chebyshev(r.Coord) <= radius {
			v.Signals = append(v.Signals, s)
		}
	}
	return v
}

// signalRadius is how far a broadcast carries (design §7.5), from the sender's
// position at send time. Derived from the arena rather than fixed, so the reach
// stays "about half the board" whatever size the map is generated at.
//
// Derived, not stored: it is a function of Width and Height, both already
// hashed, so nothing here belongs in StateHash.
func (w *World) signalRadius() int {
	return max(w.Width, w.Height) / signalRadiusDivisor
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
		return turnTicks(r.Blueprint)

	case ActTurnRight:
		r.Heading = r.Heading.Turn(1)
		return turnTicks(r.Blueprint)

	case ActTurnRandom:
		r.Heading = Heading(w.rng.Intn(int(headingCount)))
		return turnTicks(r.Blueprint)

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
	return moveTicks(r.Blueprint, w.At(to).Terrain)
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
// Slot order, not "best weapon for the shot": design §12 P1's selection
// question is settled as first ready in slot order (docs/decisions.md). A
// player who wants the cannon to fire first installs it first.
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
// to this: design §12 P1's friendly-fire question is settled as no friendly
// fire (docs/decisions.md). Bases are indestructible (§5.3), not targets.
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
// and the robot carries at most one component — design §12 P0's capacity
// question is settled at exactly one (docs/decisions.md).
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

// buildTicks is how long a blueprint takes to assemble: design §5.2's open
// question, settled mass-dependent in rc-w9s.22. See the balance block for the
// shape and the extremes it was checked against.
func buildTicks(bp Blueprint) int {
	return buildTicksBase + bp.Mass()/massPerBuildTick
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

// IdleReason says why the base is assembling nothing, and "" while it is
// assembling something. §5.2 step 3 — "wait for the inventory to change" — is
// a legitimate state, not a failure, but it is indistinguishable from a broken
// simulation unless the base says so: a colony sitting on hundreds of salvaged
// parts that fit no approved blueprint looks exactly like a stuck build queue.
//
// Derived, not stored: nothing here is state, so nothing here belongs in
// StateHash.
func (b *Base) IdleReason() string {
	switch {
	case b.Build.Ticks > 0:
		return ""
	case len(b.Blueprints) == 0:
		return "no approved blueprints"
	default:
		return "no approved blueprint is fully covered by the inventory"
	}
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
