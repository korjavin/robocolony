package sim

import (
	"cmp"
	"slices"
)

// index is the row-major offset of an in-bounds cell.
func (w *World) index(c Coord) int { return c.Y*w.Width + c.X }

// headingTo is the facing that points from a to an adjacent cell b. Non-adjacent
// or identical cells return North; callers only ask about single steps.
func headingTo(a, b Coord) Heading {
	d := Coord{b.X - a.X, b.Y - a.Y}
	for h := North; h < headingCount; h++ {
		if h.Delta() == d {
			return h
		}
	}
	return North
}

// inCone implements design §7.1: the robot sees only a short-range wedge in the
// direction it is facing, so a program must turn to scan. Deliberately not a
// radius — a robot facing away from something never sees it.
//
// The test is exact integer arithmetic: with d the offset to the target and h
// the heading vector, the target is inside the cone when
// cos²(angle) = dot² / (|d|²|h|²) is at least the constant, and dot > 0 keeps
// the reflected half of that equality out.
func inCone(from Coord, h Heading, to Coord) (dist int, ok bool) {
	dist = from.Chebyshev(to)
	if dist == 0 || dist > visionRange {
		return dist, false
	}
	d := Coord{to.X - from.X, to.Y - from.Y}
	hd := h.Delta()
	dot := d.X*hd.X + d.Y*hd.Y
	if dot <= 0 {
		return dist, false
	}
	dsq := d.X*d.X + d.Y*d.Y
	hsq := hd.X*hd.X + hd.Y*hd.Y
	return dist, dot*dot*visionCosSqDen >= dsq*hsq*visionCosSqNum
}

// look returns what forward vision reports, nearest first: loose components and
// robots of other colonies. Terrain does not occlude sight in the POC.
//
// ponytail: no line-of-sight test, so a robot sees past a barrier inside its
// cone. Add a Bresenham walk here when terrain gets interesting enough for
// hiding to be a tactic.
func (w *World) look(r *Robot) (components, enemies []Sighting) {
	for _, l := range w.Loose {
		if d, ok := inCone(r.Coord, r.Heading, l.Coord); ok {
			components = append(components, Sighting{ID: l.ID, Kind: SightComponent, Coord: l.Coord, Variant: l.Variant, Distance: d})
		}
	}
	for _, o := range w.Robots {
		// A robot destroyed earlier in this tick is out of the fight even though
		// the end-of-tick sweep has not reached it yet: enemyAt refuses it as a
		// target, so reporting it here would only make later robots spend their
		// turn aiming at a wreck.
		if o.Colony == r.Colony || isDestroyed(o) {
			continue
		}
		if d, ok := inCone(r.Coord, r.Heading, o.Coord); ok {
			enemies = append(enemies, Sighting{ID: o.ID, Kind: SightRobot, Coord: o.Coord, Colony: o.Colony, Distance: d})
		}
	}
	return sortSightings(components), sortSightings(enemies)
}

// radar returns what the installed radar reports (design §7.2): longer range,
// omnidirectional, and exactly one target class — which is the whole point of
// the radar choice, since a blueprint may carry only one (design §6.3).
//
// Each variant is a case over a different slice of the world; nothing else in
// perception changes when a radar row is added.
func (w *World) radar(r *Robot) []Sighting {
	var out []Sighting
	for _, v := range r.Blueprint.Components {
		if k, ok := v.Kind(); !ok || k != KindRadar {
			continue
		}
		switch v {
		case PartsRadar:
			for _, l := range w.Loose {
				if d := r.Coord.Chebyshev(l.Coord); d > 0 && d <= radarRange {
					out = append(out, Sighting{ID: l.ID, Kind: SightComponent, Coord: l.Coord, Variant: l.Variant, Distance: d})
				}
			}
		case EnemyRadar:
			for _, o := range w.Robots {
				// Same rule as forward vision: a robot shot to pieces earlier
				// this tick is not a contact, so nobody spends a turn aiming at
				// a wreck the end-of-tick sweep is about to remove.
				if o.Colony == r.Colony || isDestroyed(o) {
					continue
				}
				if d := r.Coord.Chebyshev(o.Coord); d > 0 && d <= radarRange {
					out = append(out, Sighting{ID: o.ID, Kind: SightRobot, Coord: o.Coord, Colony: o.Colony, Distance: d})
				}
			}
		case BaseRadar:
			for _, b := range w.Bases {
				if b.Colony == r.Colony {
					continue
				}
				if d := r.Coord.Chebyshev(b.Coord); d > 0 && d <= baseRadarRange {
					out = append(out, Sighting{ID: baseSightingID(b.Colony), Kind: SightBase, Coord: b.Coord, Colony: b.Colony, Distance: d})
				}
			}
		}
		break // design §6.3: at most one radar
	}
	return sortSightings(out)
}

// baseSightingID is the contact id of an enemy base. Bases have no entity id —
// a colony has exactly one and it never moves — so this manufactures a stable
// negative one, which cannot collide with a robot or component id because those
// come from World.NextID and start at 1.
func baseSightingID(c ColonyID) int { return -1 - int(c) }

// sortSightings orders nearest first, breaking ties on id so the result never
// depends on slice order.
func sortSightings(s []Sighting) []Sighting {
	slices.SortFunc(s, func(a, b Sighting) int {
		if c := cmp.Compare(a.Distance, b.Distance); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return s
}

// path is a BFS shortest route from `from` to `to` over cells the locomotion
// can enter, excluding `from` and including `to`. It returns nil when there is
// no route. Neighbours are visited in heading order, so of several equally
// short routes the same one always wins.
func (w *World) path(from, to Coord, locomotion Variant) []Coord {
	if !w.In(from) || !w.Passable(to, locomotion) || from == to {
		return nil
	}
	prev := make([]int, len(w.Cells))
	for i := range prev {
		prev[i] = -1
	}
	start := w.index(from)
	prev[start] = start
	queue := []Coord{from}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for h := North; h < headingCount; h++ {
			n := add(c, h.Delta())
			if !w.Passable(n, locomotion) || prev[w.index(n)] >= 0 {
				continue
			}
			prev[w.index(n)] = w.index(c)
			if n == to {
				return unwind(prev, w.Width, start, w.index(n))
			}
			queue = append(queue, n)
		}
	}
	return nil
}

func unwind(prev []int, width, start, end int) []Coord {
	var out []Coord
	for i := end; i != start; i = prev[i] {
		out = append(out, Coord{X: i % width, Y: i / width})
	}
	slices.Reverse(out)
	return out
}

// reachable flood-fills from a cell and reports, per cell, whether the
// locomotion can get there. Used by generation to keep colonies from being
// walled off; the tick loop uses path instead.
func (w *World) reachable(from Coord, locomotion Variant) []bool {
	seen := make([]bool, len(w.Cells))
	if !w.Passable(from, locomotion) {
		return seen
	}
	seen[w.index(from)] = true
	queue := []Coord{from}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for h := North; h < headingCount; h++ {
			n := add(c, h.Delta())
			if !w.Passable(n, locomotion) || seen[w.index(n)] {
				continue
			}
			seen[w.index(n)] = true
			queue = append(queue, n)
		}
	}
	return seen
}
