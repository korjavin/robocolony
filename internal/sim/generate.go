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

	// 1. Barriers, scattered uniformly.
	for i := range w.Cells {
		if w.rng.Float64() < o.BarrierDensity {
			w.Cells[i].Terrain = Barrier
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

	// 3. Loose components. A draw that lands on a barrier, a base footprint or
	// an already-taken cell is dropped rather than retried, so the number of
	// rng draws stays fixed; Richness is a target, not a guarantee.
	//
	// occupied is only ever *looked up*, never ranged: map lookups are
	// deterministic, map iteration is not.
	target := int(float64(len(w.Cells)) * o.Richness)
	for i := 0; i < target; i++ {
		c := w.randCoord()
		v := catalogue[w.rng.Intn(len(catalogue))].Variant
		if w.At(c).Terrain != Open || occupied[c] {
			continue
		}
		occupied[c] = true
		w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: c, Variant: v})
	}

	return w
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
