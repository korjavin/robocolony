package lobby

import (
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// spawnTestWorld is a small arena painted with one of every terrain class in a
// fixed pattern, so a spawn test knows exactly what each cell is. The base cell
// is forced Open: otherwise a base sitting on a barrier would make the
// base-exclusion rule and the barrier filter indistinguishable.
func spawnTestWorld(t *testing.T) *sim.World {
	t.Helper()
	w := sim.Generate(7, sim.GenOpts{Width: 8, Height: 8, Colonies: 1})
	if len(w.Bases) != 1 {
		t.Fatalf("want 1 base, got %d", len(w.Bases))
	}
	classes := []sim.Terrain{sim.Open, sim.Barrier, sim.Rubble, sim.Sand}
	for y := 0; y < w.Height; y++ {
		for x := 0; x < w.Width; x++ {
			w.SetTerrain(sim.Coord{X: x, Y: y}, classes[x%len(classes)])
		}
	}
	w.SetTerrain(w.Bases[0].Coord, sim.Open)
	w.Loose = nil
	return w
}

// TestSpawnResourcesReachesEveryEnterableTerrain is rc-rhd.3: mid-match spawns
// used to filter on sim.Tracks, which meant a rubble massif never received a
// component all match. Anything some locomotion can enter is a legal spawn; a
// hard barrier and a base cell are not.
func TestSpawnResourcesReachesEveryEnterableTerrain(t *testing.T) {
	w := spawnTestWorld(t)
	base := w.Bases[0].Coord
	// spawnEvery clamps to 1 tick, so every call spawns and tick 0 qualifies.
	m := &Match{Settings: Settings{SpawnPerMin: 60 * TickRate}, world: w}
	if got := m.Settings.spawnEvery(); got != 1 {
		t.Fatalf("spawnEvery = %d, want 1", got)
	}
	for i := 0; i < 2000; i++ {
		m.spawnResources()
	}

	seen := map[sim.Terrain]int{}
	for _, l := range w.Loose {
		if l.Coord == base {
			t.Fatalf("spawned on the base cell %v", base)
		}
		seen[w.At(l.Coord).Terrain]++
	}
	if n := seen[sim.Barrier]; n != 0 {
		t.Errorf("%d components spawned on a hard barrier", n)
	}
	for _, ter := range []sim.Terrain{sim.Open, sim.Rubble, sim.Sand} {
		if seen[ter] == 0 {
			t.Errorf("no component ever spawned on %v", ter)
		}
	}
}

// TestSpawnResourcesRngConsumptionIsFixed pins the replay contract: a persisted
// match is a seed plus a command log, so a spawn tick must always consume the
// same three draws whether or not the cell it picked was usable.
func TestSpawnResourcesRngConsumptionIsFixed(t *testing.T) {
	const ticks = 500
	w := spawnTestWorld(t)
	m := &Match{Settings: Settings{SpawnPerMin: 60 * TickRate}, world: w}
	for i := 0; i < ticks; i++ {
		m.spawnResources()
	}

	ref := sim.Generate(7, sim.GenOpts{Width: 8, Height: 8, Colonies: 1})
	cat := sim.Catalogue()
	for i := 0; i < ticks; i++ {
		ref.Rand().Intn(ref.Width)
		ref.Rand().Intn(ref.Height)
		ref.Rand().Intn(len(cat))
	}
	if got, want := w.Rand().Int63(), ref.Rand().Int63(); got != want {
		t.Fatalf("spawn ticks consumed a different number of draws: next draw %d, want %d", got, want)
	}
}
