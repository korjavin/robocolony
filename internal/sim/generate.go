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
// meaning — hard barriers are scattered at exactly the requested density — and
// the two locomotion-specific classes of design §3.1 are scattered on top of it
// in the same proportion to each other.
//
// The consequence is deliberate: at density d, *every* locomotion is blocked by
// d*(1 + one of the two shares) of the map rather than the flat d it used to
// face, so the arena is more obstructed overall but never for everyone at once,
// and BarrierDensity 0 still means a completely clean arena.
const (
	rubblePerBarrier = 0.5 // leg-favoured, closed to tracks
	sandPerBarrier   = 0.5 // track-favoured, closed to legs
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

	// 1. Terrain, scattered uniformly: one draw per cell, split into bands, so
	// adding classes did not change how much randomness a seed consumes.
	hardBand := o.BarrierDensity
	rubbleBand := hardBand + o.BarrierDensity*rubblePerBarrier
	sandBand := rubbleBand + o.BarrierDensity*sandPerBarrier
	for i := range w.Cells {
		switch v := w.rng.Float64(); {
		case v < hardBand:
			w.Cells[i].Terrain = Barrier
		case v < rubbleBand:
			w.Cells[i].Terrain = Rubble
		case v < sandBand:
			w.Cells[i].Terrain = Sand
		}
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
