package sim

import "math/rand"

// GenOpts are the arena generation settings (a subset of the lobby's match
// settings, design §2.3).
type GenOpts struct {
	Width, Height  int
	Colonies       int
	BarrierDensity float64 // fraction of cells turned into barriers
	Richness       float64 // loose components per cell
}

// DefaultGenOpts is the POC arena.
func DefaultGenOpts() GenOpts {
	return GenOpts{
		Width:          64,
		Height:         64,
		Colonies:       2,
		BarrierDensity: 0.08,
		Richness:       0.02,
	}
}

// minArenaSide keeps generation and base separation sane.
const minArenaSide = 8

// Terrain mix, as multiples of GenOpts.BarrierDensity. BarrierDensity keeps its
// meaning — hard barrier costs exactly the requested share of the map — and the
// two locomotion-specific classes of design §3.1 get more area than that,
// because a region has to be big to read as a region.
//
// The consequence is deliberate: at density d roughly 4d of the map is not open
// ground, but any single locomotion is blocked by only about 2.5d of it, and
// BarrierDensity 0 still means a completely clean arena.
const (
	rubblePerBarrier = 1.5 // leg-favoured rocky ground, closed to tracks
	sandPerBarrier   = 1.5 // track-favoured desert, closed to legs
)

// Region shapes. Terrain is painted as areas, not as per-cell coin flips:
// independent draws cannot cluster, so noise was noise by construction.
const (
	// regionCells is the nominal area of one sand field or rubble massif. Big
	// enough on a 64x64 arena to be a place you name, small enough that a
	// handful of them fit.
	regionCells = 110
	// massifCore is the share of a rubble massif that hardens into Barrier —
	// the impassable spine that makes it a mountain rather than a gravel pit.
	// The earliest-grown cells are the ones nearest the seed, i.e. the core.
	massifCore = 0.22
	// ridgeGap is the period of the doorways along a barrier ridge. A wall is a
	// detour, never a seal.
	ridgeGap = 5
	// ridgeJitterIn is one over the chance that a ridge step also slides
	// sideways, which is what keeps a ridge from being a drawn line.
	ridgeJitterIn = 3
	// scatterShare of each class's budget is left as single cells, so the map
	// is not purely blobby.
	scatterShare = 0.08
	// regionOverpaint compensates for shapes landing on each other: blobs
	// overlap and ridges are drawn over both, so a share of every budget is
	// spent repainting cells that were already that class or are about to stop
	// being it. Measured loss was about a quarter of the sand and rubble
	// budgets; without this the final map sits 25% under the requested mix.
	regionOverpaint = 1.33
)

// basePlacementDraws is how many candidate cells each base considers. Fixed so
// that rng consumption does not depend on how lucky the draws are.
const basePlacementDraws = 64

func (o GenOpts) normalize() GenOpts {
	o.Width = max(o.Width, minArenaSide)
	o.Height = max(o.Height, minArenaSide)
	o.Colonies = max(o.Colonies, 1)
	// Every base claims a 3x3 clearing; keep enough room for all of them.
	o.Colonies = min(o.Colonies, o.Width*o.Height/16)
	o.BarrierDensity = clamp01(o.BarrierDensity)
	o.Richness = clamp01(o.Richness)
	return o
}

func clamp01(v float64) float64 {
	if !(v > 0) { // also catches NaN
		return 0
	}
	return min(v, 1)
}

// Generate builds a world from a seed. The same (seed, opts) always yields a
// byte-identical world: every random decision comes from w.rng, in a fixed
// order, and no map iteration is involved.
func Generate(seed int64, opts GenOpts) *World {
	o := opts.normalize()
	w := &World{
		Width:  o.Width,
		Height: o.Height,
		Cells:  make([]Cell, o.Width*o.Height),
		Seed:   seed,
		rng:    rand.New(rand.NewSource(seed)),
	}

	// 1. Terrain, in regions: sand fields, rubble massifs with an impassable
	// core, and barrier ridges with doorways. Every budget scales with area and
	// BarrierDensity, so density 0 paints nothing at all.
	area := float64(len(w.Cells))
	budget := area * o.BarrierDensity * regionOverpaint
	w.paintRegions(Sand, int(budget*sandPerBarrier), 0)
	cores := w.paintRegions(Rubble, int(budget*rubblePerBarrier), massifCore)
	// The massif cores already spent part of the barrier budget; ridges spend
	// what is left, so BarrierDensity still names the total hard-barrier share.
	for left := int(area*o.BarrierDensity) - cores; left > 0; {
		n := w.ridge(max(o.Width, o.Height) / 2)
		if n == 0 {
			break
		}
		left -= n
	}

	// 2. Bases, spread apart: each colony takes the candidate cell furthest
	// from every base already placed.
	occupied := make(map[Coord]bool, o.Colonies*9)
	for i := 0; i < o.Colonies; i++ {
		best, bestDist := Coord{}, -1
		for d := 0; d < basePlacementDraws; d++ {
			c := w.randCoord()
			if dist := minBaseDist(w.Bases, c); dist > bestDist {
				best, bestDist = c, dist
			}
		}
		w.Bases = append(w.Bases, &Base{
			Colony:    ColonyID(i),
			Coord:     best,
			Inventory: map[Variant]int{},
		})
		// Clear the base's 3x3 footprint so it is never walled in on the spot.
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				c := Coord{best.X + dx, best.Y + dy}
				w.SetTerrain(c, Open)
				occupied[c] = true
			}
		}
	}

	// 3. Connectivity. Nothing above stops the terrain scatter from walling a
	// colony off, and a colony that cannot reach anything can never scavenge
	// (design §5.3 leaves it permanently inactive). Carve a direct corridor from
	// any base the first one cannot reach. Carving consumes no randomness, so a
	// seed still draws exactly as many numbers as before.
	//
	// Since E7.3 reachability is per-locomotion: rubble is closed to tracks and
	// sand to legs, so "connected" has three answers and the arena must satisfy
	// all of them. carve opens Open cells, which every locomotion can enter, so
	// one corridor repairs every locomotion at once — hence the break.
	for i := 1; i < len(w.Bases); i++ {
		for _, loco := range locomotionVariants() {
			if !w.reachable(w.Bases[0].Coord, loco)[w.index(w.Bases[i].Coord)] {
				w.carve(w.Bases[i].Coord, w.Bases[0].Coord)
				break
			}
		}
	}
	w.repairPockets()
	reach := w.commonReach(w.Bases[0].Coord)

	// 4. Loose components. A draw that lands on non-open terrain, a base
	// footprint, an already-taken cell or a pocket is dropped rather than
	// retried, so the number of rng draws stays fixed; Richness is a target,
	// not a guarantee.
	//
	// "A pocket" is now a per-locomotion question, and the answer taken here is
	// the strict one: a component is only placed where *every* locomotion can
	// reach it. A leg-only pocket full of loot would be a nice idea and a bad
	// bug — the starter blueprint runs on tracks, so a colony that has not yet
	// scavenged a pair of legs could watch a third of the map's resources sit
	// unreachable forever. Terrain still shapes play through the §6.4 speed
	// modifier and through shorter routes, which cost nothing to be wrong about.
	//
	// occupied is only ever *looked up*, never ranged: map lookups are
	// deterministic, map iteration is not.
	target := int(float64(len(w.Cells)) * o.Richness)
	for i := 0; i < target; i++ {
		c := w.randCoord()
		v := catalogue[w.rng.Intn(len(catalogue))].Variant
		if w.At(c).Terrain != Open || occupied[c] || !reach[w.index(c)] {
			continue
		}
		occupied[c] = true
		w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: c, Variant: v})
	}

	return w
}

// paintRegions paints budget cells of t as blobs of about regionCells each,
// plus a thin scattered residue, and hardens the coreFrac earliest-grown cells
// of every blob into Barrier. It returns how many cells it turned into Barrier
// that way, so the caller can keep the total hard-barrier share honest.
//
// Every blob paints at least its seed cell, so the loop always makes progress
// and terminates even when the budget exceeds the arena.
func (w *World) paintRegions(t Terrain, budget int, coreFrac float64) (cores int) {
	scatter := int(float64(budget) * scatterShare)
	for left := budget - scatter; left > 0; {
		grown := w.blob(t, min(left, regionCells))
		left -= len(grown)
		for _, c := range grown[:int(float64(len(grown))*coreFrac)] {
			// Same accounting rule as ridge: only a cell that was not already
			// Barrier spends the hard-barrier budget.
			if w.At(c).Terrain != Barrier {
				cores++
			}
			w.SetTerrain(c, Barrier)
		}
	}
	for i := 0; i < scatter; i++ {
		w.SetTerrain(w.randCoord(), t)
	}
	return cores
}

// blob paints up to area cells of t, growing outward from a random seed cell:
// pop a random frontier cell, paint it, push its unqueued neighbours. That is a
// randomised flood, so the shape is organic rather than a disc, and the cells
// come back in growth order — the front of the slice is the core.
//
// The frontier only shrinks once the arena is exhausted, so this terminates on
// an 8x8 arena with a budget larger than the arena.
func (w *World) blob(t Terrain, area int) []Coord {
	frontier := []Coord{w.randCoord()}
	queued := map[Coord]bool{frontier[0]: true} // looked up, never ranged
	grown := make([]Coord, 0, area)
	for len(grown) < area && len(frontier) > 0 {
		i := w.rng.Intn(len(frontier))
		c := frontier[i]
		frontier[i] = frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]

		w.SetTerrain(c, t)
		grown = append(grown, c)
		for h := North; h < headingCount; h++ {
			if n := add(c, h.Delta()); w.In(n) && !queued[n] {
				queued[n] = true
				frontier = append(frontier, n)
			}
		}
	}
	return grown
}

// ridge walks a barrier wall up to length cells from a random cell along one of
// the eight headings, sliding sideways now and then so it is not a drawn line,
// and skipping one cell every ridgeGap so the wall always has a doorway. It
// returns how many cells it actually painted, and stops at the arena edge.
func (w *World) ridge(length int) (painted int) {
	c := w.randCoord()
	h := Heading(w.rng.Intn(int(headingCount)))
	for i := 0; i < length && w.In(c); i++ {
		// Every ridgeGap'th cell is the doorway. Only cells that were not
		// already Barrier count: a ridge crossing a massif core or an earlier
		// ridge adds no hard barrier, and charging the budget for it would make
		// BarrierDensity undershoot.
		if i%ridgeGap != ridgeGap-1 && w.At(c).Terrain != Barrier {
			w.SetTerrain(c, Barrier)
			painted++
		}
		c = add(c, h.Delta())
		if w.rng.Intn(ridgeJitterIn) == 0 {
			c = add(c, h.Turn(2).Delta()) // one step to the right of the heading
		}
	}
	return painted
}

// repairPockets enforces the owner's rule: no place is unreachable by *every*
// locomotion. A region one chassis cannot cross is fine and is the point of the
// regions; a region nothing can enter is a generation bug.
//
// Anti-gravity is the only locomotion that enters both rubble and sand
// (terrainSpecs, world.go), so its flood from base 0 is the union of everywhere
// anything can go. Any non-Barrier cell outside it is a sealed pocket; carve
// from it to the nearest reached cell.
//
// Rng-free and independent of map iteration order: cells are scanned in index
// order and the first strictly-nearest reached cell wins. Terminates because
// carving only ever turns cells Open, which never removes anti-gravity
// passability, so each pass reaches at least the pocket cell it repaired.
func (w *World) repairPockets() {
	for {
		reach := w.reachable(w.Bases[0].Coord, AntiGrav)
		pocket, found := Coord{}, false
		for i, c := range w.Cells {
			if !reach[i] && c.Terrain != Barrier {
				pocket, found = Coord{X: i % w.Width, Y: i / w.Width}, true
				break
			}
		}
		if !found {
			return
		}
		w.carve(pocket, nearestReached(w, reach, pocket))
	}
}

// nearestReached is the reached cell closest to from, scanning in index order
// so ties resolve the same way on every run.
func nearestReached(w *World, reach []bool, from Coord) Coord {
	best, bestDist := from, int(^uint(0)>>1)
	for i, ok := range reach {
		if !ok {
			continue
		}
		c := Coord{X: i % w.Width, Y: i / w.Width}
		if d := from.Chebyshev(c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// commonReach is the set of cells reachable from a coordinate by every
// locomotion in the catalogue — the intersection of the per-locomotion floods.
// Deterministic and rng-free.
func (w *World) commonReach(from Coord) []bool {
	var out []bool
	for _, loco := range locomotionVariants() {
		r := w.reachable(from, loco)
		if out == nil {
			out = r
			continue
		}
		for i := range out {
			out[i] = out[i] && r[i]
		}
	}
	return out
}

// carve opens a one-cell corridor between two coordinates, stepping diagonally
// first. Deterministic and rng-free.
func (w *World) carve(from, to Coord) {
	for c := from; c != to; {
		c.X += sign(to.X - c.X)
		c.Y += sign(to.Y - c.Y)
		w.SetTerrain(c, Open)
	}
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

func (w *World) randCoord() Coord {
	return Coord{X: w.rng.Intn(w.Width), Y: w.rng.Intn(w.Height)}
}

// minBaseDist is the distance from c to the nearest placed base, or a value
// larger than any in-arena distance when none are placed yet.
func minBaseDist(bases []*Base, c Coord) int {
	best := int(^uint(0) >> 1)
	for _, b := range bases {
		best = min(best, c.Chebyshev(b.Coord))
	}
	return best
}
