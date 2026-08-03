package sim

import (
	"cmp"
	"slices"
)

// Ended reports whether the match clock has run out (design §9: a match ends
// after a fixed simulation duration). A Duration of zero never ends.
//
// This is the whole match-end signal: a tick driver steps until Ended and then
// stops. Step is a no-op once it is true, so an extra call cannot advance a
// finished match, and nothing here tears the world down — the final board, the
// score and the leaderboard stay readable for as long as the world is held.
func (w *World) Ended() bool { return w.Duration > 0 && w.Tick >= w.Duration }

// statsOf returns the colony's telemetry, or nil for a colony with no base.
func (w *World) statsOf(colony ColonyID) *Stats {
	b := w.baseOf(colony)
	if b == nil {
		return nil
	}
	return &b.Stats
}

// hasRobots reports whether the colony still has a unit in the arena. Note what
// this is *not* used for: design §5.3 makes the base indestructible and lets a
// colony with zero robots rebuild from inventory, so there is no elimination
// path in this package. It only feeds the time-active counter.
func (w *World) hasRobots(colony ColonyID) bool {
	return slices.ContainsFunc(w.Robots, func(r *Robot) bool { return r.Colony == colony })
}

// dropAt puts one component on the ground as an ordinary loose component —
// indistinguishable from a generated one, so any robot with a manipulator can
// collect it, including the original owner's (design §8.2).
func (w *World) dropAt(at Coord, v Variant) {
	w.Loose = append(w.Loose, &LooseComponent{ID: w.NextID(), Coord: at, Variant: v})
}

// sweepDestroyed removes every robot at zero health and leaves its salvage
// where it fell.
//
// It runs once, at a fixed point in the tick, precisely so that nothing splices
// the robot slice while Step is walking it. Order is slice order, which is
// allocation order, so the salvage rolls come off the rng in the same sequence
// on every run.
func (w *World) sweepDestroyed() {
	for _, r := range w.Robots {
		if !isDestroyed(r) {
			continue
		}
		w.salvage(r)
		if s := w.statsOf(r.Colony); s != nil {
			s.Losses++
		}
		// Design §10.6: memory disappears when the robot is destroyed. The
		// registers go away with the struct; OnDestroy is how a controller layer
		// drops the rest of its per-robot state (internal/prog's evaluator).
		if w.OnDestroy != nil {
			w.OnDestroy(r.ID)
		}
	}
	w.Robots = slices.DeleteFunc(w.Robots, isDestroyed)
}

func isDestroyed(r *Robot) bool { return r.Health <= 0 }

// salvage implements design §8.2: a random subset of the wreck's installed
// components drops, everything else disappears.
//
// Carried cargo and installed components are different things and are handled
// separately. Cargo was never part of the robot — it was being hauled — so it
// is not rolled for and always falls where the robot did; the installed modules
// each get one roll. Either way a component leaves the robot exactly once: the
// cargo slot is cleared, and the wreck itself is removed immediately after, so
// nothing can be salvaged twice.
func (w *World) salvage(r *Robot) {
	if r.Cargo != VariantNone {
		w.dropAt(r.Coord, r.Cargo)
		r.Cargo = VariantNone
	}
	dropped := 0
	for _, v := range r.Blueprint.Components {
		// The world's rng, never math/rand: this roll and the accuracy roll are
		// the two easiest places in the package to break determinism.
		if w.rng.Intn(100) >= salvageDropPercent {
			continue
		}
		w.dropAt(r.Coord, v)
		dropped++
	}
	// A wreck is always a resource site (design §8.2's stated design effect): if
	// every roll came up empty, one uniformly chosen module survives anyway.
	if dropped == 0 && len(r.Blueprint.Components) > 0 {
		w.dropAt(r.Coord, r.Blueprint.Components[w.rng.Intn(len(r.Blueprint.Components))])
	}
}

// Result is one colony's leaderboard line: the design §9 score, the two terms
// it is made of, and the telemetry beside it. The telemetry is reported whether
// or not the formula uses it — §9's remaining candidates (collected resources,
// destroyed enemy value, time active) were measured and rejected, and the next
// balance pass will want the same numbers to re-check that.
type Result struct {
	Colony ColonyID
	// Score is the design §9 score: see World.Score.
	Score          int
	FleetValue     int // §9's own term: installed component value, surviving robots
	Robots         int // surviving units
	InventoryValue int // full component value still sitting in the base
	Stats
}

// FleetValue is design §9's formula as written: the summed value of the
// installed components of every surviving robot of one colony.
func (w *World) FleetValue(colony ColonyID) int {
	total := 0
	for _, r := range w.Robots {
		if r.Colony == colony {
			total += r.Blueprint.Value()
		}
	}
	return total
}

// Score is the design §9 score, finalised by E7.8:
//
//	colony_score = fleet_value + inventoryScorePercent% * base_inventory_value
//
// §9 left the formula open and listed the candidates; these are the two that
// survived being measured over matched seeds.
//
//   - Fleet value is §9's own term and stays the dominant one. It is also the
//     survival requirement §12 P0 asks about — a destroyed robot contributes
//     nothing — which is why no separate survival gate exists: one would have to
//     zero a colony that design §5.3 says is still alive and still rebuilding.
//   - Base inventory counts, but at a fraction, because a colony ends a match
//     holding two to four times more value in stock than in robots. At full
//     weight the score becomes a hoarding contest and a wiped-out colony
//     outranks the colony that wiped it out; at zero, an hour of scavenging
//     scores the same as an hour of idling the moment the last robot dies.
//     Fielding a component is worth strictly more than stockpiling it, which is
//     the property §9 asks for, spelled the other way round from its own
//     tiny-robots example: robots cannot be inflated, and parts are discounted.
//
// Collected resources, destroyed enemy value and time active are deliberately
// not terms. Collected double-counts what is already in the inventory or in the
// fleet built from it. Time active only separates colonies that scored near
// zero anyway. A kill credit was measured to make aggression pay twice — the
// value a kill removes from the enemy fleet is already the reward — and the
// armed strategies did not need the help.
//
// Everything here is frozen state (robots, base inventory), so a finished
// match keeps answering after Ended.
func (w *World) Score(colony ColonyID) int {
	score := w.FleetValue(colony)
	if b := w.baseOf(colony); b != nil {
		score += b.InventoryValue() * inventoryScorePercent / 100
	}
	return score
}

// Leaderboard is every colony's result, best score first. It reads live state,
// so it is meaningful at any tick; after Ended it is the final result.
//
// Ties break on fleet value and then on colony id (design §12 P0 asks for a
// tie-break rule): between two colonies holding the same total, the one holding
// more of it in robots ranks higher, and beyond that the ordering is arbitrary
// but reproducible, which is all a tie can be.
func (w *World) Leaderboard() []Result {
	out := make([]Result, 0, len(w.Bases))
	for _, b := range w.Bases {
		res := Result{
			Colony:         b.Colony,
			Score:          w.Score(b.Colony),
			FleetValue:     w.FleetValue(b.Colony),
			InventoryValue: b.InventoryValue(),
			Stats:          b.Stats,
		}
		for _, r := range w.Robots {
			if r.Colony == b.Colony {
				res.Robots++
			}
		}
		out = append(out, res)
	}
	slices.SortFunc(out, func(a, b Result) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.FleetValue, a.FleetValue); c != 0 {
			return c
		}
		return cmp.Compare(a.Colony, b.Colony)
	})
	return out
}
