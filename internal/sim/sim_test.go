package sim

import (
	"errors"
	"reflect"
	"testing"
)

// sampleWorld is a generated world with a robot and a stocked base bolted on,
// so that every branch of StateHash has something to chew on.
func sampleWorld(t *testing.T, seed int64) *World {
	t.Helper()
	w := Generate(seed, DefaultGenOpts())
	bp := Blueprint{
		ID:         "bp-scout",
		Name:       "scout",
		Components: []Variant{Tracks, MediumArmor, Manipulator, PartsRadar},
		ProgramID:  "prog-scavenge",
	}
	if err := bp.Validate(); err != nil {
		t.Fatalf("sample blueprint invalid: %v", err)
	}
	w.Bases[0].Blueprints = append(w.Bases[0].Blueprints, bp)
	for _, v := range []Variant{Laser, Tracks, MediumArmor, Manipulator, PartsRadar} {
		w.Bases[0].Inventory[v] += 2
	}
	w.Bases[0].Build = BuildOrder{Blueprint: bp, Ticks: 7}
	w.signals = []Signal{{Kind: ComeHere, From: 1, Colony: 0, Coord: Coord{3, 4}}}
	w.Robots = append(w.Robots, &Robot{
		ID:          w.NextID(),
		Colony:      0,
		Coord:       Coord{X: 5, Y: 7},
		Heading:     SouthWest,
		Health:      100,
		Cargo:       Laser,
		Blueprint:   bp,
		ProgramID:   bp.ProgramID,
		Cooldown:    2,
		PathBlocked: true,
		Memory:      [MemPoints]MemPoint{{Coord: Coord{1, 2}, Set: true}},
	})
	return w
}

func TestDeterminism(t *testing.T) {
	a, b := Generate(42, DefaultGenOpts()), Generate(42, DefaultGenOpts())

	if !reflect.DeepEqual(a, b) {
		t.Fatal("Generate(42) produced two structurally different worlds")
	}
	if a.StateHash() != b.StateHash() {
		t.Fatalf("hash mismatch after Generate: %d != %d", a.StateHash(), b.StateHash())
	}

	// A hash that ranges over a map is unstable within a single process: Go
	// randomizes map iteration order per range. Repeat enough that an
	// unsorted inventory or entity walk cannot survive.
	a = sampleWorld(t, 42)
	b = sampleWorld(t, 42)
	want := a.StateHash()
	for i := 0; i < 200; i++ {
		if got := a.StateHash(); got != want {
			t.Fatalf("StateHash is not stable across calls: %d != %d (call %d)", got, want, i)
		}
	}
	if b.StateHash() != want {
		t.Fatalf("two identically built worlds hash differently: %d != %d", b.StateHash(), want)
	}

	for i := 0; i < 1000; i++ {
		a.Step()
		b.Step()
	}
	if a.Tick != 1000 {
		t.Fatalf("Tick = %d after 1000 steps, want 1000", a.Tick)
	}
	if a.StateHash() != b.StateHash() {
		t.Fatalf("hash mismatch after 1000 ticks: %d != %d", a.StateHash(), b.StateHash())
	}

	// StateHash cannot see the rng's internal state, so compare it directly:
	// this catches a divergence that consumed randomness without (yet)
	// changing visible state.
	for i := 0; i < 16; i++ {
		if x, y := a.Rand().Int63(), b.Rand().Int63(); x != y {
			t.Fatalf("rng streams diverged at draw %d: %d != %d", i, x, y)
		}
	}
}

func TestGenerateDiffersBySeed(t *testing.T) {
	if a, b := Generate(42, DefaultGenOpts()), Generate(43, DefaultGenOpts()); a.StateHash() == b.StateHash() {
		t.Fatal("seeds 42 and 43 produced the same world hash")
	}
}

// TestStateHashCoversState is the other half of the determinism guard: a hash
// that ignored most of the world would pass TestDeterminism trivially.
func TestStateHashCoversState(t *testing.T) {
	// retexture rewrites the first cell that is not already this class, so the
	// mutation is a real change whatever the generated arena looks like.
	retexture := func(want Terrain) func(*World) {
		return func(w *World) {
			for i := range w.Cells {
				if w.Cells[i].Terrain != want {
					w.Cells[i].Terrain = want
					return
				}
			}
			t.Fatalf("the whole arena is already %s", want)
		}
	}
	mutations := []struct {
		name string
		f    func(*World)
	}{
		{"tick", func(w *World) { w.Step() }},
		{"terrain", func(w *World) { w.SetTerrain(Coord{0, 0}, Barrier^w.At(Coord{0, 0}).Terrain) }},
		// Every terrain class must hash distinctly, not just "barrier or not":
		// rubble and sand differ only in which locomotion they stop, so a hash
		// that collapsed them would let two different arenas look identical.
		{"terrain rubble", retexture(Rubble)},
		{"terrain sand", retexture(Sand)},
		{"base coord", func(w *World) { w.Bases[0].Coord.X++ }},
		{"base inventory count", func(w *World) { w.Bases[0].Inventory[Laser]++ }},
		{"base inventory variant", func(w *World) { w.Bases[0].Inventory[Tracks+100] = 1 }},
		{"approved blueprint", func(w *World) { w.Bases[0].Blueprints[0].ProgramID = "other" }},
		{"blueprint components", func(w *World) {
			w.Bases[0].Blueprints[0].Components = append(w.Bases[0].Blueprints[0].Components, Laser)
		}},
		{"robot added", func(w *World) { w.Robots = append(w.Robots, &Robot{ID: w.NextID()}) }},
		{"robot coord", func(w *World) { w.Robots[0].Coord.Y++ }},
		{"robot heading", func(w *World) { w.Robots[0].Heading = w.Robots[0].Heading.Turn(1) }},
		{"robot health", func(w *World) { w.Robots[0].Health-- }},
		{"robot cargo", func(w *World) { w.Robots[0].Cargo = VariantNone }},
		{"robot colony", func(w *World) { w.Robots[0].Colony++ }},
		{"robot blueprint", func(w *World) { w.Robots[0].Blueprint.ID = "bp-other" }},
		{"robot memory set", func(w *World) { w.Robots[0].Memory[1].Set = true }},
		{"robot memory coord", func(w *World) { w.Robots[0].Memory[0].Coord.X++ }},
		{"robot program", func(w *World) { w.Robots[0].ProgramID += "!" }},
		{"robot cooldown", func(w *World) { w.Robots[0].Cooldown++ }},
		{"robot weapon cooldown", func(w *World) { w.Robots[0].WeaponCooldown[0]++ }},
		{"robot second weapon cooldown", func(w *World) { w.Robots[0].WeaponCooldown[MaxWeapons-1]++ }},
		{"robot path blocked", func(w *World) { w.Robots[0].PathBlocked = !w.Robots[0].PathBlocked }},
		{"robot target reached", func(w *World) { w.Robots[0].TargetReached = !w.Robots[0].TargetReached }},
		{"robot target unreachable", func(w *World) {
			w.Robots[0].TargetUnreachable = !w.Robots[0].TargetUnreachable
		}},
		{"robot recalled", func(w *World) { w.Robots[0].Recalled = !w.Robots[0].Recalled }},
		{"match duration", func(w *World) { w.Duration = 500 }},
		{"colony collected", func(w *World) { w.Bases[0].Stats.Collected++ }},
		{"colony losses", func(w *World) { w.Bases[0].Stats.Losses++ }},
		{"colony kills", func(w *World) { w.Bases[0].Stats.Kills++ }},
		{"colony ticks active", func(w *World) { w.Bases[0].Stats.TicksActive++ }},
		{"base build timer", func(w *World) { w.Bases[0].Build.Ticks++ }},
		{"base build blueprint", func(w *World) { w.Bases[0].Build.Blueprint.ID = "bp-other" }},
		{"signal added", func(w *World) {
			w.signals = append(w.signals, Signal{Kind: ComeHere, From: 99, Coord: Coord{2, 3}})
		}},
		{"signal kind", func(w *World) { w.signals[0].Kind = AvoidHere }},
		{"signal sender", func(w *World) { w.signals[0].From++ }},
		{"signal colony", func(w *World) { w.signals[0].Colony++ }},
		{"signal coord", func(w *World) { w.signals[0].Coord.X++ }},
		{"loose coord", func(w *World) { w.Loose[0].Coord.X++ }},
		{"loose variant", func(w *World) { w.Loose[0].Variant = VariantNone }},
		{"loose removed", func(w *World) { w.Loose = w.Loose[1:] }},
		{"id allocator", func(w *World) { w.NextID() }},
	}
	base := sampleWorld(t, 7).StateHash()
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			w := sampleWorld(t, 7)
			if w.StateHash() != base {
				t.Fatalf("sample world is not reproducible")
			}
			m.f(w)
			if w.StateHash() == base {
				t.Fatalf("StateHash ignores %s", m.name)
			}
		})
	}
}

// Slice order is not state: entities are identified by id.
func TestStateHashIgnoresSliceOrder(t *testing.T) {
	w := sampleWorld(t, 11)
	w.Robots = append(w.Robots, &Robot{ID: w.NextID(), Coord: Coord{3, 3}})
	want := w.StateHash()
	w.Robots[0], w.Robots[1] = w.Robots[1], w.Robots[0]
	w.Loose[0], w.Loose[len(w.Loose)-1] = w.Loose[len(w.Loose)-1], w.Loose[0]
	if got := w.StateHash(); got != want {
		t.Fatalf("StateHash depends on slice order: %d != %d", got, want)
	}
}

func TestGenerate(t *testing.T) {
	// Sweep seeds: the invariants below must hold for every arena, not just a
	// lucky one.
	for seed := int64(1); seed <= 50; seed++ {
		checkGenerated(t, seed)
	}
}

func checkGenerated(t *testing.T, seed int64) {
	t.Helper()
	opts := DefaultGenOpts()
	w := Generate(seed, opts)

	if len(w.Cells) != w.Width*w.Height {
		t.Fatalf("cells = %d, want %d", len(w.Cells), w.Width*w.Height)
	}
	if len(w.Bases) != opts.Colonies {
		t.Fatalf("bases = %d, want %d", len(w.Bases), opts.Colonies)
	}
	if len(w.Loose) == 0 {
		t.Fatal("no loose components scattered")
	}
	if len(w.Robots) != 0 {
		t.Fatal("Generate must not spawn robots; production is E1.2")
	}

	seen := map[Coord]bool{}
	for _, l := range w.Loose {
		if !w.In(l.Coord) {
			t.Fatalf("loose component %d outside arena at %v", l.ID, l.Coord)
		}
		if w.At(l.Coord).Terrain != Open {
			t.Fatalf("loose component %d on %s terrain", l.ID, w.At(l.Coord).Terrain)
		}
		if seen[l.Coord] {
			t.Fatalf("two loose components stacked at %v", l.Coord)
		}
		seen[l.Coord] = true
		if _, ok := Lookup(l.Variant); !ok {
			t.Fatalf("loose component %d has variant %d outside the catalogue", l.ID, l.Variant)
		}
	}

	for i, b := range w.Bases {
		if ColonyID(i) != b.Colony {
			t.Fatalf("base %d has colony %d", i, b.Colony)
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				c := Coord{b.Coord.X + dx, b.Coord.Y + dy}
				if w.In(c) && w.At(c).Terrain != Open {
					t.Fatalf("base %d is walled in at %v", i, c)
				}
			}
		}
	}
	// Two colonies on a 64x64 arena should land nowhere near each other.
	if d := w.Bases[0].Coord.Chebyshev(w.Bases[1].Coord); d < w.Width/3 {
		t.Fatalf("bases only %d cells apart", d)
	}
}

func TestGenerateNormalizesOpts(t *testing.T) {
	w := Generate(3, GenOpts{Width: -5, Height: 0, Colonies: 0, BarrierDensity: 9, Richness: -1})
	if w.Width != minArenaSide || w.Height != minArenaSide {
		t.Fatalf("arena %dx%d, want %dx%d", w.Width, w.Height, minArenaSide, minArenaSide)
	}
	if len(w.Bases) != 1 {
		t.Fatalf("bases = %d, want 1", len(w.Bases))
	}
	if len(w.Loose) != 0 {
		t.Fatalf("loose = %d, want 0 at zero richness", len(w.Loose))
	}
	// Full barrier density still leaves the base's clearing open.
	if w.At(w.Bases[0].Coord).Terrain != Open {
		t.Fatal("base cell is not open")
	}
}

func TestBlueprintValidate(t *testing.T) {
	tests := []struct {
		name       string
		components []Variant
		want       error
	}{
		{"minimum valid robot", []Variant{Tracks, MediumArmor}, nil},
		{"scavenger", []Variant{Tracks, MediumArmor, Manipulator, PartsRadar}, nil},
		{"two weapons allowed", []Variant{Tracks, MediumArmor, Laser, Laser}, nil},
		{"full loadout", []Variant{Tracks, MediumArmor, Manipulator, PartsRadar, Laser, Laser}, nil},
		{"empty", nil, ErrLocomotion},
		{"no locomotion", []Variant{MediumArmor, Laser}, ErrLocomotion},
		{"two locomotion", []Variant{Tracks, Tracks, MediumArmor}, ErrLocomotion},
		{"no armor", []Variant{Tracks, Manipulator}, ErrArmor},
		{"two armor", []Variant{Tracks, MediumArmor, MediumArmor}, ErrArmor},
		{"two radars", []Variant{Tracks, MediumArmor, PartsRadar, PartsRadar}, ErrRadarLimit},
		{"three weapons", []Variant{Tracks, MediumArmor, Laser, Laser, Laser}, ErrWeaponLimit},
		{"unknown component", []Variant{Tracks, MediumArmor, Variant(200)}, ErrUnknownComponent},
		{"none is not a component", []Variant{Tracks, MediumArmor, VariantNone}, ErrUnknownComponent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Blueprint{ID: "bp", Components: tc.components}.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBlueprintHasAndTotals(t *testing.T) {
	bp := Blueprint{Components: []Variant{Tracks, MediumArmor, Manipulator}}
	if !bp.Has(KindManipulator) {
		t.Error("scavenger blueprint should have a manipulator")
	}
	if bp.Has(KindWeapon) {
		t.Error("unarmed blueprint should not report a weapon")
	}
	if want := 30 + 40 + 10; bp.Mass() != want {
		t.Errorf("Mass() = %d, want %d", bp.Mass(), want)
	}
	if want := 30 + 40 + 20; bp.Value() != want {
		t.Errorf("Value() = %d, want %d", bp.Value(), want)
	}
}

func TestHeading(t *testing.T) {
	for h := North; h < headingCount; h++ {
		d := h.Delta()
		if d == (Coord{}) {
			t.Fatalf("heading %d has a zero delta", h)
		}
		if h.Turn(4).Delta() != (Coord{X: -d.X, Y: -d.Y}) {
			t.Fatalf("heading %d reversed is not the opposite delta", h)
		}
		if h.Turn(8) != h || h.Turn(-8) != h {
			t.Fatalf("heading %d does not survive a full turn", h)
		}
	}
	if North.Turn(2) != East || North.Turn(-1) != NorthWest {
		t.Fatal("turns are not clockwise from north")
	}
}

func TestTerrainPassable(t *testing.T) {
	if !Open.Passable(Tracks) {
		t.Error("open terrain must be passable")
	}
	if Barrier.Passable(Tracks) {
		t.Error("barrier must be impassable")
	}
	// A locomotion variant that does not exist yet must not sneak through a
	// hard barrier: that is the whole point of the HardBarrier flag.
	if Barrier.Passable(Variant(200)) {
		t.Error("hard barrier must block unknown locomotion")
	}
	if Terrain(200).Passable(Tracks) {
		t.Error("unknown terrain must not be passable")
	}
	w := Generate(5, DefaultGenOpts())
	if w.Passable(Coord{-1, 0}, Tracks) || w.Passable(Coord{w.Width, 0}, Tracks) {
		t.Error("out-of-bounds cells must not be passable")
	}
}
