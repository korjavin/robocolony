// Package sim is the pure, deterministic simulation core: world model, arena
// generation, and (from E1.2 on) the tick loop.
//
// Purity rules, per AGENTS.md:
//   - no net/http, no database/sql, no wall-clock time. Time is w.Tick.
//   - no global math/rand. Every world owns an explicit *rand.Rand.
//   - nothing that affects state may depend on map iteration order.
package sim

import (
	"cmp"
	"encoding/binary"
	"hash/fnv"
	"math/rand"
	"slices"
)

// Coord is an integer grid cell. +X is east, +Y is south.
type Coord struct {
	X, Y int
}

// Chebyshev is the 8-way movement distance between two cells.
func (c Coord) Chebyshev(o Coord) int {
	return max(abs(c.X-o.X), abs(c.Y-o.Y))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Heading is one of the eight facings, clockwise from north.
type Heading uint8

const (
	North Heading = iota
	NorthEast
	East
	SouthEast
	South
	SouthWest
	West
	NorthWest
	headingCount
)

var headingDelta = [headingCount]Coord{
	North:     {0, -1},
	NorthEast: {1, -1},
	East:      {1, 0},
	SouthEast: {1, 1},
	South:     {0, 1},
	SouthWest: {-1, 1},
	West:      {-1, 0},
	NorthWest: {-1, -1},
}

// Delta is the one-cell offset in this heading's direction.
func (h Heading) Delta() Coord {
	if h >= headingCount {
		return Coord{}
	}
	return headingDelta[h]
}

// Turn rotates by steps eighths of a circle; positive is clockwise.
func (h Heading) Turn(steps int) Heading {
	n := int(headingCount)
	return Heading(((int(h)+steps)%n + n) % n)
}

// Terrain is a terrain class. The POC generates only Open and Barrier; the
// rest of design §3.1 arrives in E7.3 as extra rows in terrainSpecs.
type Terrain uint8

const (
	Open Terrain = iota
	Barrier
)

// TerrainSpec is one row of the design §3.1 traversal matrix. HardBarrier
// terrain is impassable to every locomotion, present and future; otherwise
// Impassable lists the locomotion variants that cannot enter.
type TerrainSpec struct {
	Terrain     Terrain
	Name        string
	HardBarrier bool
	Impassable  []Variant
}

var terrainSpecs = []TerrainSpec{
	{Terrain: Open, Name: "open"},
	{Terrain: Barrier, Name: "barrier", HardBarrier: true},
}

// TerrainSpecs returns the terrain catalogue.
func TerrainSpecs() []TerrainSpec { return slices.Clone(terrainSpecs) }

func terrainSpec(t Terrain) (TerrainSpec, bool) {
	for _, s := range terrainSpecs {
		if s.Terrain == t {
			return s, true
		}
	}
	return TerrainSpec{}, false
}

func (t Terrain) String() string {
	if s, ok := terrainSpec(t); ok {
		return s.Name
	}
	return "unknown"
}

// Passable reports whether the given locomotion variant may enter this terrain.
func (t Terrain) Passable(locomotion Variant) bool {
	s, ok := terrainSpec(t)
	if !ok || s.HardBarrier {
		return false
	}
	return !slices.Contains(s.Impassable, locomotion)
}

// Cell is one arena cell. Later epics extend it; At/SetTerrain keep working.
type Cell struct {
	Terrain Terrain
}

// MemPoint is one of a robot's three coordinate registers (design §7.4).
type MemPoint struct {
	Coord Coord
	Set   bool
}

// ColonyID identifies a player's colony within a match.
type ColonyID int

// MemPoints is the number of coordinate registers every robot has.
const MemPoints = 3

// Robot is one active unit.
//
// Every mutable field here must also be covered by StateHash, or the
// determinism guard silently stops guarding.
type Robot struct {
	ID        int
	Colony    ColonyID
	Coord     Coord
	Heading   Heading
	Health    int
	Cargo     Variant // VariantNone when carrying nothing
	Blueprint Blueprint
	ProgramID string // installed program; cleared and replaced on reprogram
	Memory    [MemPoints]MemPoint

	// Cooldown is how many further ticks the current action occupies. A robot
	// with a cooldown does not perceive or decide; this is how speed (§6.4)
	// turns into time.
	Cooldown int

	// WeaponCooldown is the reload left on each weapon module, by slot order
	// in Blueprint.Weapons(). Independent of Cooldown: weapons reload while
	// the robot is doing something else.
	WeaponCooldown [MaxWeapons]int

	// Perception flags for the next evaluation cycle (design §10.3).
	PathBlocked       bool
	TargetReached     bool
	TargetUnreachable bool

	// Recalled is the player's one and only direct command (design §4.2): the
	// robot suspends its installed program and walks home on its own. It is
	// deliberately not an ActionKind — no rule can set it, and nothing clears it
	// but Reprogram, so the travel delay cannot be shortcut.
	Recalled bool
}

// Reprogram installs a new program id and wipes everything a robot must not
// carry across a reprogram: all three coordinate memory points (design §4.2
// step 4, §10.6), the recall override, and the navigation flags the suspended
// program left behind — so the robot leaves base from a clean state (§4.2
// step 5).
//
// It does not check where the robot is: design §4.2 only allows this at the
// robot's own base, and the caller enforces that with AtOwnBase.
func (r *Robot) Reprogram(programID string) {
	r.ProgramID = programID
	r.Memory = [MemPoints]MemPoint{}
	r.Recalled = false
	r.PathBlocked, r.TargetReached, r.TargetUnreachable = false, false, false
}

// InvEntry is one component stack in a base inventory.
type InvEntry struct {
	Variant Variant
	Count   int
}

// Stats is one colony's running telemetry. Design §9's score is provisional and
// E7.8 revisits it; these are the inputs the candidate formulas listed there
// need (collected resources, destroyed enemy value, survival, time active), so
// they are recorded during the match rather than reconstructed from it later.
//
// Mutable state: every field here is in StateHash.
type Stats struct {
	Collected   int    // components deposited into this colony's base
	Losses      int    // own robots destroyed
	Kills       int    // enemy robots this colony destroyed
	TicksActive uint64 // ticks this colony ended with at least one live robot
}

// Base is a colony's single indestructible base (design §5). It is also where
// per-colony bookkeeping lives, because it is the colony's only singleton — a
// colony with zero robots still has one (design §5.3).
type Base struct {
	Colony     ColonyID
	Coord      Coord
	Inventory  map[Variant]int
	Blueprints []Blueprint // approved for automatic production
	Build      BuildOrder  // current assembly job; zero when idle
	Stats      Stats
}

// SortedInventory returns the inventory in variant order. Anything that feeds
// simulation state must use this rather than ranging over Inventory: raw map
// order is the classic determinism bug in this package.
func (b *Base) SortedInventory() []InvEntry {
	out := make([]InvEntry, 0, len(b.Inventory))
	for v, n := range b.Inventory {
		out = append(out, InvEntry{Variant: v, Count: n})
	}
	slices.SortFunc(out, func(a, b InvEntry) int { return cmp.Compare(a.Variant, b.Variant) })
	return out
}

// LooseComponent is a component lying in the arena, waiting to be scavenged.
type LooseComponent struct {
	ID      int
	Coord   Coord
	Variant Variant
}

// World is the complete state of one match.
//
// Robots, Bases and Loose are slices, not maps, precisely so that iteration
// order is fixed. StateHash sorts defensively anyway.
type World struct {
	Width, Height int
	Cells         []Cell // row-major, len == Width*Height
	Robots        []*Robot
	Bases         []*Base
	Loose         []*LooseComponent
	Tick          uint64
	Seed          int64

	// Duration is the match length in ticks (design §9: a match ends after a
	// fixed simulation duration). Zero is an open-ended sandbox that never
	// ends. Set it before the first Step; it is hashed, so two worlds with
	// different clocks are different worlds.
	Duration uint64

	// Control resolves a robot's controller each tick. It is an *input*, not
	// state: StateHash ignores it, and two worlds driven by equivalent
	// controllers must still hash equal. A nil Control, or a nil result, idles
	// the robot. E3 plugs the program runtime in here.
	Control func(*Robot) Controller

	// OnDestroy is called with the id of every robot removed from the world,
	// once, just before it goes. Like Control it is an input, not state: it
	// exists so a controller layer can drop per-robot bookkeeping (internal/prog
	// wires Runtime.Forget here) without sim importing that package.
	OnDestroy func(robotID int)

	// signals are the broadcasts heard this tick — sent during the previous
	// one. State, and hashed.
	signals []Signal

	rng    *rand.Rand
	nextID int
}

// Rand is the world's only randomness source. Never use global math/rand.
func (w *World) Rand() *rand.Rand { return w.rng }

// NextID allocates the next entity id.
func (w *World) NextID() int {
	w.nextID++
	return w.nextID
}

// In reports whether a coordinate lies inside the arena.
func (w *World) In(c Coord) bool {
	return c.X >= 0 && c.Y >= 0 && c.X < w.Width && c.Y < w.Height
}

// At returns the cell at c. Out-of-bounds reads as hard Barrier so callers do
// not need a bounds check before a traversal test.
func (w *World) At(c Coord) Cell {
	if !w.In(c) {
		return Cell{Terrain: Barrier}
	}
	return w.Cells[c.Y*w.Width+c.X]
}

// SetTerrain writes a cell's terrain class. Out-of-bounds writes are ignored.
func (w *World) SetTerrain(c Coord, t Terrain) {
	if w.In(c) {
		w.Cells[c.Y*w.Width+c.X].Terrain = t
	}
}

// Passable reports whether a robot with the given locomotion may occupy c.
func (w *World) Passable(c Coord, locomotion Variant) bool {
	return w.In(c) && w.At(c).Terrain.Passable(locomotion)
}

// StateHash is a stable hash over the whole world state. Two worlds with equal
// state hash equal, in any process, on any run: entities are hashed in sorted
// id order and inventories in sorted variant order.
//
// It deliberately does not cover the *rand.Rand's internal state, which is not
// readable; tests compare a draw from each rng to catch divergent consumption.
func (w *World) StateHash() uint64 {
	h := fnv.New64a()
	var buf [8]byte
	putU := func(v uint64) {
		binary.LittleEndian.PutUint64(buf[:], v)
		_, _ = h.Write(buf[:])
	}
	putI := func(v int) { putU(uint64(v)) }
	putS := func(s string) {
		putI(len(s))
		_, _ = h.Write([]byte(s))
	}
	putC := func(c Coord) { putI(c.X); putI(c.Y) }
	putB := func(b bool) {
		if b {
			putI(1)
		} else {
			putI(0)
		}
	}
	putBP := func(b Blueprint) {
		putS(b.ID)
		putS(b.Name)
		putS(b.ProgramID)
		putI(len(b.Components))
		for _, v := range b.Components {
			putI(int(v))
		}
	}

	putU(w.Tick)
	putU(w.Duration)
	putI(w.Width)
	putI(w.Height)
	putU(uint64(w.Seed))
	// The id allocator is state too: two worlds that allocated a different
	// number of ids will hand out different ids next, even if no entity of
	// theirs survives to show it.
	putI(w.nextID)

	putI(len(w.Cells))
	for _, c := range w.Cells {
		putI(int(c.Terrain))
	}

	bases := slices.Clone(w.Bases)
	slices.SortFunc(bases, func(a, b *Base) int { return cmp.Compare(a.Colony, b.Colony) })
	putI(len(bases))
	for _, b := range bases {
		putI(int(b.Colony))
		putC(b.Coord)
		inv := b.SortedInventory()
		putI(len(inv))
		for _, e := range inv {
			putI(int(e.Variant))
			putI(e.Count)
		}
		putI(len(b.Blueprints))
		for _, bp := range b.Blueprints {
			putBP(bp)
		}
		putI(b.Build.Ticks)
		putBP(b.Build.Blueprint)
		putI(b.Stats.Collected)
		putI(b.Stats.Losses)
		putI(b.Stats.Kills)
		putU(b.Stats.TicksActive)
	}

	robots := slices.Clone(w.Robots)
	slices.SortFunc(robots, func(a, b *Robot) int { return cmp.Compare(a.ID, b.ID) })
	putI(len(robots))
	for _, r := range robots {
		putI(r.ID)
		putI(int(r.Colony))
		putC(r.Coord)
		putI(int(r.Heading))
		putI(r.Health)
		putI(int(r.Cargo))
		putBP(r.Blueprint)
		putS(r.ProgramID)
		putI(r.Cooldown)
		for _, cd := range r.WeaponCooldown {
			putI(cd)
		}
		putB(r.PathBlocked)
		putB(r.TargetReached)
		putB(r.TargetUnreachable)
		putB(r.Recalled)
		for _, m := range r.Memory {
			putC(m.Coord)
			putB(m.Set)
		}
	}

	loose := slices.Clone(w.Loose)
	slices.SortFunc(loose, func(a, b *LooseComponent) int { return cmp.Compare(a.ID, b.ID) })
	putI(len(loose))
	for _, l := range loose {
		putI(l.ID)
		putC(l.Coord)
		putI(int(l.Variant))
	}

	// Signals in flight are state: they decide what robots perceive next tick.
	// Sorted, because the channel is a set, not a sequence.
	sigs := slices.Clone(w.signals)
	slices.SortFunc(sigs, func(a, b Signal) int {
		if c := cmp.Compare(a.From, b.From); c != 0 {
			return c
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	putI(len(sigs))
	for _, s := range sigs {
		putI(int(s.Kind))
		putI(s.From)
		putI(int(s.Colony))
		putC(s.Coord)
	}

	return h.Sum64()
}
