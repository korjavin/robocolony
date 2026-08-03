package sim

import "testing"

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

// TestGenerationSolvablePerLocomotion is the E1.2 connectivity measurement,
// redone per locomotion because E7.3 made "impassable" a per-locomotion answer.
// The failure it guards against is a robot sealed into a pocket by terrain its
// chassis cannot cross.
func TestGenerationSolvablePerLocomotion(t *testing.T) {
	const seeds = 100
	opts := DefaultGenOpts()
	cells := opts.Width * opts.Height

	worstFrac := map[Variant]float64{}
	for _, loco := range locomotionVariants() {
		worstFrac[loco] = 1
	}
	for seed := int64(1); seed <= seeds; seed++ {
		w := Generate(seed, opts)
		for _, loco := range locomotionVariants() {
			for _, b := range w.Bases {
				reach := w.reachable(b.Coord, loco)
				n := 0
				for _, ok := range reach {
					if ok {
						n++
					}
				}
				// 1. No base is sealed in: every locomotion reaches a
				// substantial share of the arena from every base.
				if frac := float64(n) / float64(cells); frac < 0.5 {
					t.Errorf("seed %d: %s reaches only %.1f%% of the arena from base %d",
						seed, loco, frac*100, b.Colony)
				} else if frac < worstFrac[loco] {
					worstFrac[loco] = frac
				}
				// 2. Every base reaches every other base.
				for _, other := range w.Bases {
					if !reach[w.index(other.Coord)] {
						t.Errorf("seed %d: %s cannot reach base %d from base %d",
							seed, loco, other.Colony, b.Colony)
					}
				}
				// 3. Every loose component is collectable by every chassis
				// from every base — nothing generates into a pocket.
				for _, l := range w.Loose {
					if !reach[w.index(l.Coord)] {
						t.Errorf("seed %d: %s cannot reach loose component %d at %v from base %d",
							seed, loco, l.ID, l.Coord, b.Colony)
					}
				}
			}
		}
	}
	for _, loco := range locomotionVariants() {
		t.Logf("%d seeds: worst arena share reachable by %s from any base = %.1f%%",
			seeds, loco, worstFrac[loco]*100)
	}
}

// Generation must actually produce the terrain classes it claims to, or the
// connectivity measurement above is measuring an empty map.
func TestGenerationScattersEveryTerrainClass(t *testing.T) {
	w := Generate(42, DefaultGenOpts())
	counts := map[Terrain]int{}
	for _, c := range w.Cells {
		counts[c.Terrain]++
	}
	for _, s := range terrainSpecs {
		if counts[s.Terrain] == 0 {
			t.Errorf("generation produced no %s cells", s.Name)
		}
		t.Logf("%-8s %5d cells (%.1f%%)", s.Name, counts[s.Terrain],
			100*float64(counts[s.Terrain])/float64(len(w.Cells)))
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
