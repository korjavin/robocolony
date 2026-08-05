package sim

import (
	"math/rand"
	"strings"
	"testing"
)

// TestTraversalMatrix is design §3.1, cell for cell: every terrain class
// against every locomotion unit, plus the favoured column. It is table-driven
// over the *whole* product deliberately — a row missing from terrainSpecs shows
// up here as a wrong answer, not as an untested combination.
func TestTraversalMatrix(t *testing.T) {
	const (
		no      = 0 // impassable
		yes     = 1 // passable, no modifier
		favored = 2 // passable and favoured (design §3.1 "passable or favored")
	)
	rows := []struct {
		terrain               Terrain
		legs, tracks, antigrv int
	}{
		{Open, yes, yes, yes},
		{Rubble, favored, no, yes},
		{Sand, no, favored, yes},
		{Barrier, no, no, no},
	}
	// The matrix must cover the catalogue, not a subset of it.
	if len(rows) != len(terrainSpecs) {
		t.Fatalf("matrix has %d terrain rows, catalogue has %d", len(rows), len(terrainSpecs))
	}
	if got := locomotionVariants(); len(got) != 3 {
		t.Fatalf("matrix has 3 locomotion columns, catalogue has %d: %v", len(got), got)
	}

	for _, row := range rows {
		cols := map[Variant]int{Legs: row.legs, Tracks: row.tracks, AntiGrav: row.antigrv}
		for _, loco := range locomotionVariants() {
			want := cols[loco]
			t.Run(row.terrain.String()+"/"+loco.String(), func(t *testing.T) {
				if got := row.terrain.Passable(loco); got != (want != no) {
					t.Errorf("Passable = %v, want %v", got, want != no)
				}
				wantBonus := 0
				if want == favored {
					wantBonus = favoredSpeedBonus
				}
				if got := row.terrain.SpeedBonus(loco); got != wantBonus {
					t.Errorf("SpeedBonus = %d, want %d", got, wantBonus)
				}
				// A locomotion that cannot enter must not also collect a bonus
				// for the terrain it cannot enter.
				if want == no && row.terrain.SpeedBonus(loco) != 0 {
					t.Error("impassable terrain hands out a speed bonus")
				}
			})
		}
	}

	// Design §3.1's closing sentence: anti-gravity crosses almost all ordinary
	// terrain but must still route around absolute barriers.
	for _, s := range terrainSpecs {
		if s.HardBarrier != !s.Terrain.Passable(AntiGrav) {
			t.Errorf("%s: anti-gravity passability disagrees with HardBarrier", s.Name)
		}
	}
}

// The traversal matrix has to reach the pathfinder, not just the predicate: a
// legs robot and a tracks robot facing the same cell must disagree.
func TestTerrainRoutesPerLocomotion(t *testing.T) {
	w := arena(9)
	// A wall of rubble across the middle, with one sand cell in it. Legs walk
	// the rubble; tracks need the sand; anti-gravity takes either.
	for x := 0; x < 9; x++ {
		w.SetTerrain(Coord{x, 4}, Rubble)
	}
	w.SetTerrain(Coord{4, 4}, Sand)

	from, to := Coord{0, 0}, Coord{0, 8}
	for _, tc := range []struct {
		loco   Variant
		viaX   int // the x the route must cross the wall at
		exactX bool
	}{
		{Tracks, 4, true},    // only the sand cell lets tracks through
		{Legs, 0, false},     // legs cross the rubble anywhere but the sand
		{AntiGrav, 0, false}, // anti-gravity crosses anywhere
	} {
		t.Run(tc.loco.String(), func(t *testing.T) {
			p := w.path(from, to, tc.loco)
			if len(p) == 0 {
				t.Fatal("no route across the wall")
			}
			for _, c := range p {
				if c.Y != 4 {
					continue
				}
				if tc.exactX && c.X != tc.viaX {
					t.Fatalf("crossed the wall at x=%d, want x=%d", c.X, tc.viaX)
				}
				if !w.Passable(c, tc.loco) {
					t.Fatalf("route enters impassable %v", c)
				}
			}
		})
	}

	// Legs must never be routed through sand, however convenient it is.
	w2 := arena(5)
	for y := 0; y < 5; y++ {
		w2.SetTerrain(Coord{2, y}, Sand)
	}
	if p := w2.path(Coord{0, 0}, Coord{4, 4}, Legs); len(p) != 0 {
		t.Fatalf("legs found a route through a sand wall: %v", p)
	}
	if p := w2.path(Coord{0, 0}, Coord{4, 4}, AntiGrav); len(p) == 0 {
		t.Fatal("anti-gravity cannot cross a sand wall")
	}
}

// Design §6.4's terrain_modifier term must show up as time, not just as a
// number: a legs robot crosses rubble faster than it crosses open ground.
func TestFavoredTerrainIsFaster(t *testing.T) {
	legs := Blueprint{Components: []Variant{Legs, LightArmor}}
	tracks := Blueprint{Components: []Variant{Tracks, LightArmor}}

	if SpeedOn(legs, Rubble) <= SpeedOn(legs, Open) {
		t.Error("legs gain nothing from rubble")
	}
	if moveTicks(legs, Rubble) >= moveTicks(legs, Open) {
		t.Errorf("legs cross rubble in %d ticks and open ground in %d",
			moveTicks(legs, Rubble), moveTicks(legs, Open))
	}
	if SpeedOn(tracks, Sand) <= SpeedOn(tracks, Open) {
		t.Error("tracks gain nothing from sand")
	}
	// Anti-gravity is favoured nowhere: that is what it pays for going anywhere.
	agrav := Blueprint{Components: []Variant{AntiGrav, LightArmor}}
	for _, terr := range []Terrain{Open, Rubble, Sand} {
		if SpeedOn(agrav, terr) != SpeedOn(agrav, Open) {
			t.Errorf("anti-gravity speed varies with terrain (%s)", terr)
		}
	}

	// The world charges the destination cell's cost, not the origin's.
	w := arena(8)
	w.SetTerrain(Coord{4, 3}, Rubble)
	r := w.addRobot(0, Coord{4, 4}, North, legs)
	if got, want := w.step(r, Coord{4, 3}), moveTicks(legs, Rubble); got != want {
		t.Fatalf("entering rubble cost %d ticks, want %d", got, want)
	}
}

// The design §6.4 balancing disadvantage for anti-gravity, spelled out as an
// assertion rather than a comment: it is the fastest chassis while light and
// the slowest under load, because its mass tolerance is the worst in the table.
func TestLocomotionIdentities(t *testing.T) {
	light := func(loco Variant) Blueprint {
		return Blueprint{Components: []Variant{loco, LightArmor}}
	}
	loaded := func(loco Variant) Blueprint {
		return Blueprint{Components: []Variant{loco, HeavyArmor, Cannon, Cannon, Manipulator}}
	}

	if !(EffectiveSpeed(light(AntiGrav)) > EffectiveSpeed(light(Tracks))) {
		t.Error("a bare anti-gravity scout must outrun a bare tracked one")
	}
	if !(EffectiveSpeed(loaded(AntiGrav)) < EffectiveSpeed(loaded(Tracks))) {
		t.Error("a loaded anti-gravity platform must be slower than a loaded tracked one")
	}
	if !(EffectiveSpeed(loaded(Legs)) >= EffectiveSpeed(loaded(Tracks))) {
		t.Error("legs must carry weight at least as well as tracks")
	}
	if !(EffectiveSpeed(light(Legs)) < EffectiveSpeed(light(Tracks))) {
		t.Error("legs must be the slowest chassis on open ground when light")
	}
	// Cost is the other half of the disadvantage: anti-gravity is the priciest
	// component in the catalogue, so it is expensive to field and a rich wreck.
	agrav, _ := Lookup(AntiGrav)
	for _, c := range catalogue {
		if c.Value > agrav.Value {
			t.Errorf("%s is worth more than the anti-gravity platform", c.Name)
		}
	}

	// Every locomotion row must be tuned; the fallback is for unknown variants
	// only, and a real row silently falling back would be invisible otherwise.
	for _, loco := range locomotionVariants() {
		s := locomotionStats(loco)
		if s.BaseSpeed == baseSpeedUnknown && s.MassPerSpeedPoint == massPerSpeedPointUnknown {
			t.Errorf("%s has no tuned locomotion row", loco)
		}
		if s.MassPerSpeedPoint <= 0 {
			t.Errorf("%s has a mass tolerance of %d", loco, s.MassPerSpeedPoint)
		}
	}
	if s := locomotionStats(Variant(200)); s.MassPerSpeedPoint <= 0 {
		t.Fatal("the unknown-locomotion fallback would divide by zero")
	}
	// No blueprint may be brought to a standstill: minSpeed is a floor.
	absurd := Blueprint{Components: []Variant{AntiGrav, HeavyArmor}}
	for i := 0; i < 50; i++ {
		absurd.Components = append(absurd.Components, Cannon)
	}
	if got := EffectiveSpeed(absurd); got != minSpeed {
		t.Fatalf("speed = %d under absurd mass, want the %d floor", got, minSpeed)
	}
}

// Turning is a fraction of a cell of movement, and never free.
func TestTurnCostScalesWithChassis(t *testing.T) {
	light := Blueprint{Components: []Variant{AntiGrav, LightArmor}}
	heavy := Blueprint{Components: []Variant{Legs, HeavyArmor, Cannon, Cannon, Manipulator}}
	if turnTicks(light) < 1 || turnTicks(heavy) < 1 {
		t.Fatal("a turn must cost at least one tick")
	}
	if turnTicks(heavy) <= turnTicks(light) {
		t.Errorf("heavy turns in %d ticks, light in %d", turnTicks(heavy), turnTicks(light))
	}
	if turnTicks(heavy) >= moveTicks(heavy, Open) {
		t.Error("turning must cost less than crossing a cell")
	}
}

// preE8LoosePerArena is how many loose components the pre-E8 generator placed
// on a mean default arena, measured over the same 100 seeds at rev 96994a2 (the
// commit before the regions landed). Regions took the non-open share of the map
// from 16% to 31%, and since a bad draw is dropped rather than retried that
// alone cost 19% of the loot (54.3 per arena) at an unchanged Richness. The
// number is a floor here, not a target: generation may place more, never fewer.
const preE8LoosePerArena = 67.10

// TestGenerationSolvable is the connectivity contract, in the owner's terms:
// **no unreachable place**, where unreachable means *no* locomotion can get
// there. A region one chassis cannot cross is not a bug — since E8 it is the
// whole point of terrain, so the old "every locomotion reaches >=50% of the
// arena" assertion is gone. What replaces it is stricter where it matters:
// every non-barrier cell of every arena must be reachable by *something*, and
// every base must reach every other base by *everything*.
//
// Loot follows the same shape since rc-rhd.2. It is no longer confined to
// ground every chassis can walk — that made the regions barren scenery — so the
// universal assertion becomes a floor plus a fraction: nothing may be placed
// where no locomotion can collect it, and the bulk must stay collectable by
// tracks, the starter locomotion (internal/lobby/starter.go), so a colony that
// has not scavenged legs yet is never starved.
func TestGenerationSolvable(t *testing.T) {
	const seeds = 100
	// The share of loot a tracks-only colony must be able to collect from every
	// one of its bases. looseCommonShare of the *target* is placed under the
	// strict rule, so this is that share as a floor on the realised placement.
	const wantTracksShare = 0.70

	opts := DefaultGenOpts()
	cells := opts.Width * opts.Height

	worstFrac := map[Variant]float64{}
	for _, loco := range locomotionVariants() {
		worstFrac[loco] = 1
	}
	worstUnion := 0.0
	placed, tracksCollectable := 0, 0
	onTerrain := map[Terrain]int{}
	for seed := int64(1); seed <= seeds; seed++ {
		w := Generate(seed, opts)
		placed += len(w.Loose)
		for _, l := range w.Loose {
			onTerrain[w.At(l.Coord).Terrain]++
		}
		// Both are per component and AND-ed over every base: a component only
		// counts as collectable if it is collectable from all of them.
		anyLoco := make([]bool, len(w.Loose))
		byTracks := make([]bool, len(w.Loose))
		for i := range w.Loose {
			anyLoco[i], byTracks[i] = true, true
		}

		// 1. No sealed pockets. Anti-gravity enters every terrain except a hard
		// barrier, so its flood from base 0 is the union of everywhere anything
		// can go; every cell that is not a Barrier must be inside it.
		union := w.reachable(w.Bases[0].Coord, AntiGrav)
		open, sealed := 0, 0
		for i, c := range w.Cells {
			if c.Terrain == Barrier {
				continue
			}
			open++
			if !union[i] {
				sealed++
				if sealed == 1 {
					t.Errorf("seed %d: sealed pocket at %v (%s), unreachable by every locomotion",
						seed, Coord{X: i % w.Width, Y: i / w.Width}, c.Terrain)
				}
			}
		}
		if sealed > 1 {
			t.Errorf("seed %d: %d sealed cells in total", seed, sealed)
		}
		if frac := float64(open) / float64(cells); frac > worstUnion {
			worstUnion = frac
		}

		for _, b := range w.Bases {
			reachedSomehow := make([]bool, len(w.Loose))
			for _, loco := range locomotionVariants() {
				reach := w.reachable(b.Coord, loco)
				n := 0
				for _, ok := range reach {
					if ok {
						n++
					}
				}
				if frac := float64(n) / float64(cells); frac < worstFrac[loco] {
					worstFrac[loco] = frac
				}
				// 2. Every base reaches every other base, by every locomotion.
				// The starter chassis is fixed, so a colony that cannot walk to
				// the fight with the chassis it starts on has no game.
				for _, other := range w.Bases {
					if !reach[w.index(other.Coord)] {
						t.Errorf("seed %d: %s cannot reach base %d from base %d",
							seed, loco, other.Colony, b.Colony)
					}
				}
				for i, l := range w.Loose {
					ok := reach[w.index(l.Coord)]
					reachedSomehow[i] = reachedSomehow[i] || ok
					if loco == Tracks {
						byTracks[i] = byTracks[i] && ok
					}
				}
			}
			// 3. Nothing generates into a pocket: every loose component is
			// collectable by *some* chassis from this base. Which chassis is
			// the player's problem — that is the point of the regions — but
			// loot no locomotion can reach is loot that never enters the game.
			for i, l := range w.Loose {
				anyLoco[i] = anyLoco[i] && reachedSomehow[i]
				if !reachedSomehow[i] {
					t.Errorf("seed %d: no locomotion reaches loose component %d at %v (%s) from base %d",
						seed, l.ID, l.Coord, w.At(l.Coord).Terrain, b.Colony)
				}
			}
		}
		for i := range w.Loose {
			if anyLoco[i] && byTracks[i] {
				tracksCollectable++
			}
		}
	}

	// 4. The tracks floor. Placement puts looseCommonShare of the target on
	// ground every chassis can walk; this is the assertion that the realised
	// arenas honour it, so a colony still on its starter chassis is never
	// locked out of the board.
	if frac := float64(tracksCollectable) / float64(placed); frac < wantTracksShare {
		t.Errorf("%d seeds: only %.1f%% of loose components are collectable by tracks from every base, want >=%.0f%%",
			seeds, frac*100, wantTracksShare*100)
	} else {
		t.Logf("%d seeds: %.1f%% of loose components are collectable by tracks from every base (floor %.0f%%)",
			seeds, frac*100, wantTracksShare*100)
	}

	// 5. The regions are stocked. Sand and rubble carrying loot is the whole
	// reason to field tracks or legs rather than to treat terrain as scenery.
	for _, terr := range []Terrain{Open, Sand, Rubble} {
		t.Logf("%d seeds: %d loose components on %s (%.1f%% of all placed, %.2f per arena)",
			seeds, onTerrain[terr], terr, 100*float64(onTerrain[terr])/float64(placed),
			float64(onTerrain[terr])/seeds)
	}
	for _, terr := range []Terrain{Sand, Rubble} {
		if onTerrain[terr] == 0 {
			t.Errorf("%d seeds at Richness %.3f placed no loose component on %s: the regions are barren",
				seeds, opts.Richness, terr)
		}
	}
	if onTerrain[Barrier] != 0 {
		t.Errorf("%d loose components generated inside a hard barrier", onTerrain[Barrier])
	}

	// 6. The count. Regions cost 19% of the loot at an unchanged Richness
	// because a draw outside the pool is dropped; drawsFor pays for the misses,
	// and this is the assertion that it kept paying.
	perArena := float64(placed) / seeds
	if perArena < preE8LoosePerArena {
		t.Errorf("%d seeds: %.2f loose components per arena at Richness %.3f, want >=%.2f (the pre-E8 count)",
			seeds, perArena, opts.Richness, preE8LoosePerArena)
	} else {
		t.Logf("%d seeds: %.2f loose components per arena at Richness %.3f, against %.2f pre-E8 and a %d target",
			seeds, perArena, opts.Richness, preE8LoosePerArena, int(float64(cells)*opts.Richness))
	}

	t.Logf("%d seeds: largest non-barrier share of an arena = %.1f%%, all of it reachable by anti-gravity",
		seeds, worstUnion*100)
	for _, loco := range locomotionVariants() {
		t.Logf("%d seeds: worst arena share reachable by %s from any base = %.1f%% (informational: regions may exclude a chassis)",
			seeds, loco, worstFrac[loco]*100)
	}
}

// Generation must actually produce the terrain classes it claims to, or the
// connectivity measurement above is measuring an empty map.
func TestGenerationScattersEveryTerrainClass(t *testing.T) {
	const seeds = 100
	opts := DefaultGenOpts()
	totals := map[Terrain]int{}
	for seed := int64(1); seed <= seeds; seed++ {
		w := Generate(seed, opts)
		counts := map[Terrain]int{}
		for _, c := range w.Cells {
			counts[c.Terrain]++
			totals[c.Terrain]++
		}
		for _, s := range terrainSpecs {
			if counts[s.Terrain] == 0 {
				t.Errorf("seed %d produced no %s cells", seed, s.Name)
			}
		}
	}
	all := float64(seeds * opts.Width * opts.Height)
	for _, s := range terrainSpecs {
		t.Logf("%-8s %.1f%% of the arena, mean over %d seeds at BarrierDensity %.2f",
			s.Name, 100*float64(totals[s.Terrain])/all, seeds, opts.BarrierDensity)
	}
	// A zero barrier density must still mean a completely clean arena: existing
	// callers outside this package generate test worlds that way.
	clean := Generate(42, GenOpts{Width: 16, Height: 16, Colonies: 1})
	for _, c := range clean.Cells {
		if c.Terrain != Open {
			t.Fatalf("BarrierDensity 0 produced %s terrain", c.Terrain)
		}
	}
}

// clustered is the spatial statistic that separates regions from noise: the
// share of cells of one class with at least minLike 8-neighbours of the same
// class. Uniform scatter at these densities lands around 5-15%; a shape lands
// far above, because almost every cell of a shape has company.
//
// minLike is per shape, not a magic number: an area (a sand field, a rubble
// massif) has 3+ like neighbours nearly everywhere, but a ridge is a *line* —
// one cell thick by design, so its interior has exactly two.
func clustered(w *World, t Terrain, minLike int) float64 {
	total, grouped := 0, 0
	for i, cell := range w.Cells {
		if cell.Terrain != t {
			continue
		}
		total++
		c := Coord{X: i % w.Width, Y: i / w.Width}
		n := 0
		for h := North; h < headingCount; h++ {
			if w.At(add(c, h.Delta())).Terrain == t {
				n++
			}
		}
		if n >= minLike {
			grouped++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(grouped) / float64(total)
}

// TestGenerationIsClusteredNotScattered is the E8 acceptance test: terrain must
// come in regions. It is written so that it FAILS on the pre-E8 generator —
// checked here against a uniform-scatter fixture built at the same densities,
// so the threshold is known to discriminate rather than merely to pass.
func TestGenerationIsClusteredNotScattered(t *testing.T) {
	// Well above what uniform scatter yields (measured 7-16% below) and well
	// below what the shapes yield (measured 61-88%).
	const wantClustered = 0.5

	shapes := []struct {
		terrain Terrain
		minLike int // 3 for an area, 2 for a one-cell-thick ridge
	}{
		{Sand, 3}, {Rubble, 3}, {Barrier, 2},
	}

	opts := DefaultGenOpts()
	for seed := int64(1); seed <= 20; seed++ {
		w := Generate(seed, opts)
		for _, s := range shapes {
			if got := clustered(w, s.terrain, s.minLike); got < wantClustered {
				t.Errorf("seed %d: only %.0f%% of %s cells have %d+ like neighbours, want >=%.0f%% — that is scatter, not a region",
					seed, got*100, s.terrain, s.minLike, wantClustered*100)
			}
		}
	}

	// The discrimination check: the old generator, verbatim — one draw per cell
	// thresholded into bands — at the same class shares this one produces. If
	// the statistic did not separate these two it would be measuring nothing.
	scatter := Generate(1, GenOpts{Width: opts.Width, Height: opts.Height, Colonies: 1})
	rng := rand.New(rand.NewSource(1))
	for i := range scatter.Cells {
		switch v := rng.Float64(); {
		case v < 0.08:
			scatter.Cells[i].Terrain = Barrier
		case v < 0.08+0.12:
			scatter.Cells[i].Terrain = Rubble
		case v < 0.08+0.24:
			scatter.Cells[i].Terrain = Sand
		}
	}
	regions := Generate(1, opts)
	for _, s := range shapes {
		got := clustered(scatter, s.terrain, s.minLike)
		t.Logf("%s with %d+ like neighbours: uniform scatter %.0f%%, generated regions %.0f%%",
			s.terrain, s.minLike, got*100, clustered(regions, s.terrain, s.minLike)*100)
		if got >= wantClustered {
			t.Errorf("the clustering statistic does not discriminate: uniform scatter already scores %.0f%% for %s",
				got*100, s.terrain)
		}
	}
}

// TestGenerationASCIIDump prints the arena so a human can decide whether it
// reads as deserts, mountains and walls. No assertions: the eye is the test.
func TestGenerationASCIIDump(t *testing.T) {
	glyph := map[Terrain]byte{Open: '.', Barrier: '#', Rubble: '^', Sand: '~'}
	for _, seed := range []int64{1, 7} {
		w := Generate(seed, DefaultGenOpts())
		rows := make([]string, 0, w.Height)
		for y := 0; y < w.Height; y++ {
			row := make([]byte, w.Width)
			for x := 0; x < w.Width; x++ {
				row[x] = glyph[w.At(Coord{x, y}).Terrain]
			}
			for _, b := range w.Bases {
				if b.Coord.Y == y {
					row[b.Coord.X] = 'B'
				}
			}
			rows = append(rows, string(row))
		}
		t.Logf("seed %d (. open  ^ rubble  ~ sand  # barrier  B base)\n%s",
			seed, strings.Join(rows, "\n"))
	}
}

// Each radar reports exactly one target class (design §7.2), and a blueprint
// may carry only one radar (design §6.3) — so the radar choice *is* the
// perception choice, and each variant must be blind to the other two classes.
func TestRadarVariantsSeeOneClassEach(t *testing.T) {
	// One world, one arrangement, three robots differing only in their radar.
	// Everything is inside every radar's range and behind the robots, so
	// forward vision cannot account for any of it.
	newWorld := func(radar Variant) (*World, *Robot) {
		w := arena(40)
		w.addBase(0, Coord{20, 20})
		w.addBase(1, Coord{20, 34}) // 14 cells: inside every radar range
		w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{20, 26}, Variant: Laser})
		bp := Blueprint{Components: []Variant{Tracks, LightArmor, radar}}
		r := w.addRobot(0, Coord{20, 20}, North, bp)
		w.addRobot(1, Coord{20, 30}, North, bp)
		return w, r
	}

	for _, tc := range []struct {
		radar Variant
		want  SightingKind
	}{
		{PartsRadar, SightComponent},
		{EnemyRadar, SightRobot},
		{BaseRadar, SightBase},
	} {
		t.Run(tc.radar.String(), func(t *testing.T) {
			w, r := newWorld(tc.radar)
			got := w.radar(r)
			if len(got) != 1 {
				t.Fatalf("radar reported %d contacts, want 1: %+v", len(got), got)
			}
			if got[0].Kind != tc.want {
				t.Fatalf("contact kind = %d, want %d", got[0].Kind, tc.want)
			}
			if got[0].Distance <= 0 {
				t.Fatal("radar reported a contact on the robot's own cell")
			}
		})
	}

	// No radar, nothing reported — the choice is real in both directions.
	w := arena(40)
	w.addBase(1, Coord{20, 30})
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: Coord{20, 26}, Variant: Laser})
	blind := w.addRobot(0, Coord{20, 20}, North, Blueprint{Components: []Variant{Tracks, LightArmor}})
	if got := w.radar(blind); len(got) != 0 {
		t.Fatalf("a radarless blueprint sees %d contacts", len(got))
	}
}

// A base contact must be distinguishable from a robot contact. internal/prog
// picks an attack target out of RadarTargets and bases are indestructible
// (design §5.3), so a base that looked like a robot would be an attack order
// that can never land.
func TestBaseContactsAreDistinguishable(t *testing.T) {
	w := arena(40)
	w.addBase(0, Coord{20, 20})
	w.addBase(1, Coord{20, 30})
	r := w.addRobot(0, Coord{20, 20}, North, Blueprint{Components: []Variant{Tracks, LightArmor, BaseRadar}})
	enemy := w.addRobot(1, Coord{20, 30}, North, Blueprint{Components: []Variant{Tracks, LightArmor}})

	got := w.radar(r)
	if len(got) != 1 || got[0].Kind != SightBase {
		t.Fatalf("base radar reported %+v", got)
	}
	// The manufactured base id must not collide with any live entity id.
	if got[0].ID >= 0 || got[0].ID == enemy.ID {
		t.Fatalf("base contact id %d collides with entity ids", got[0].ID)
	}
	// Until internal/prog learns to filter on Kind, an attack aimed at a base
	// contact must at worst waste the tick: enemyAt refuses a base cell that
	// holds no hostile robot, so this can never panic or mis-credit a kill.
	enemy.Coord = Coord{0, 0}
	if cost := w.attack(r, got[0].Coord); cost != idleTicks {
		t.Fatalf("attacking a base cost %d ticks, want %d", cost, idleTicks)
	}
}

// Enemy-robot radar must obey the same wreck rule as forward vision: a robot
// already at zero health this tick is not a contact.
func TestEnemyRadarIgnoresWrecks(t *testing.T) {
	w := arena(40)
	bp := Blueprint{Components: []Variant{Tracks, LightArmor, EnemyRadar}}
	r := w.addRobot(0, Coord{20, 20}, North, bp)
	dead := w.addRobot(1, Coord{20, 28}, North, bp)
	if len(w.radar(r)) != 1 {
		t.Fatal("enemy radar does not see a live enemy")
	}
	dead.Health = 0
	if got := w.radar(r); len(got) != 0 {
		t.Fatalf("enemy radar reports a wreck: %+v", got)
	}
}
