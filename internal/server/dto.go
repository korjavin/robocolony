// Package server is the HTTP surface that is not lobby lifecycle: the live
// world stream and the JSON DTOs the client renders from.
//
// The DTOs here are deliberately their own types. internal/sim carries no JSON
// tags and never will: the wire format is a contract with the browser, and it
// must not move every time a simulation struct is refactored. The traffic is
// strictly outbound — nothing decoded from a client ever becomes sim state
// through this package.
package server

import (
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Init is the once-per-connection frame: everything that cannot change while
// the match runs. Terrain is the reason it exists — 4096 cells are not worth
// resending ten times a second.
type Init struct {
	MatchID  int64  `json:"match_id"`
	Name     string `json:"name"`
	TickRate int    `json:"tick_rate"`
	Tick     uint64 `json:"tick"` // where the stream joined
	EndTick  uint64 `json:"end_tick"`
	Seed     int64  `json:"seed"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`

	// Terrain is one string per row, one digit per cell, indexing
	// TerrainLegend. Compact, and readable in a curl.
	//
	// ponytail: digits cap the catalogue at 10 terrain classes. Design §3.1
	// lists six, so E7.3 fits; anything past that switches to base36.
	Terrain       []string `json:"terrain"`
	TerrainLegend []string `json:"terrain_legend"`

	// Components is the catalogue, so the renderer can name a cargo variant or
	// an inventory stack without hardcoding the numbers.
	Components []Component `json:"components"`
	Colonies   []Colony    `json:"colonies"`
}

// Colony is one seat: who is playing it, under which colony id.
type Colony struct {
	ID          int    `json:"id"`
	UserID      int64  `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// Component is one catalogue row.
type Component struct {
	Variant int    `json:"variant"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Mass    int    `json:"mass"`
	Value   int    `json:"value"`
}

// Snapshot is one tick's full world state. Design §4.3: the observer has no fog
// of war, so this carries every colony's robots, bases and stats.
//
// Full snapshots, not deltas: at the POC arena size a frame is a few KB (the
// stream logs the measured size per connection). Deltas get a bead if that
// stops being true.
type Snapshot struct {
	Tick     uint64 `json:"tick"`
	EndTick  uint64 `json:"end_tick"`
	Finished bool   `json:"finished"`

	Robots   []Robot       `json:"robots"`
	Bases    []Base        `json:"bases"`
	Loose    []Loose       `json:"loose"`
	Colonies []ColonyStats `json:"colonies"`
}

// Robot is one unit as the observer sees it. Health is hp/hp_max rather than a
// precomputed fraction: one division in the renderer, and the inspector of
// design §4.4 wants the raw numbers anyway.
type Robot struct {
	ID        int    `json:"id"`
	Colony    int    `json:"colony"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Heading   int    `json:"heading"` // 0..7, clockwise from north
	HP        int    `json:"hp"`
	HPMax     int    `json:"hp_max"`
	Cargo     int    `json:"cargo"` // component variant, 0 = carrying nothing
	Archetype string `json:"archetype"`
	Blueprint string `json:"blueprint"`
	Program   string `json:"program"`
	Cooldown  int    `json:"cooldown"`

	// Memory is the three coordinate registers of design §7.4; an unset one is
	// null.
	Memory []*Point `json:"memory"`

	// Trace answers design §13's "which rule is currently active, and why". It
	// is absent for a robot that has not decided yet — one that has never had a
	// program, or that was built this tick.
	Trace *Trace `json:"trace,omitempty"`
}

// Point is a grid coordinate.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Trace is the last decision the robot's program made.
type Trace struct {
	Tick   uint64 `json:"tick"`   // tick the decision was taken
	Rule   int    `json:"rule"`   // 0-based rule index, -1 when none matched
	Action string `json:"action"` // primary action, empty when idle
	Reason string `json:"reason"`
	Idle   bool   `json:"idle"`
}

// Base is a colony's base, its stock and what it is currently assembling.
type Base struct {
	Colony     int         `json:"colony"`
	X          int         `json:"x"`
	Y          int         `json:"y"`
	Inventory  []InvEntry  `json:"inventory"` // variant order, never map order
	Blueprints []Blueprint `json:"blueprints"`
	Build      *Build      `json:"build,omitempty"`
}

// InvEntry is one component stack.
type InvEntry struct {
	Variant int `json:"variant"`
	Count   int `json:"count"`
}

// Blueprint is an approved design.
type Blueprint struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Components []int  `json:"components"`
	Program    string `json:"program"`
	Value      int    `json:"value"`
}

// Build is the current assembly job.
//
// TicksLeft counts down; there is no total here because sim does not export the
// build duration. The renderer can hold the highest value it has seen for a
// running job, which is the same number.
type Build struct {
	Blueprint  string `json:"blueprint"`
	TicksLeft  int    `json:"ticks_left"`
	Components []int  `json:"components"`
}

// Loose is a component lying in the arena.
type Loose struct {
	ID      int `json:"id"`
	X       int `json:"x"`
	Y       int `json:"y"`
	Variant int `json:"variant"`
}

// ColonyStats is a colony's headline numbers (design §4.4). Losses, kills and
// the §9 score belong here too; E5.2 owns them and adds fields.
type ColonyStats struct {
	Colony     int `json:"colony"`
	Robots     int `json:"robots"`
	Inventory  int `json:"inventory"`
	FleetValue int `json:"fleet_value"`
}

// NewInit builds the connect frame. Callers must hold the match lock over w.
func NewInit(info lobby.Info, colonies []lobby.Colony, w *sim.World) Init {
	specs := sim.TerrainSpecs()
	legend := make([]string, 0, len(specs))
	for _, s := range specs {
		legend = append(legend, s.Name)
	}

	rows := make([]string, w.Height)
	line := make([]byte, w.Width)
	for y := 0; y < w.Height; y++ {
		for x := 0; x < w.Width; x++ {
			line[x] = '0' + byte(w.At(sim.Coord{X: x, Y: y}).Terrain)
		}
		rows[y] = string(line)
	}

	cat := sim.Catalogue()
	comps := make([]Component, 0, len(cat))
	for _, c := range cat {
		comps = append(comps, Component{
			Variant: int(c.Variant), Name: c.Name, Kind: c.Kind.String(),
			Mass: c.Mass, Value: c.Value,
		})
	}

	seats := make([]Colony, 0, len(colonies))
	for _, c := range colonies {
		seats = append(seats, Colony{ID: int(c.ID), UserID: c.UserID, DisplayName: c.DisplayName})
	}

	return Init{
		MatchID: info.ID, Name: info.Name, TickRate: info.TickRate,
		Tick: w.Tick, EndTick: info.EndTick, Seed: info.Seed,
		Width: w.Width, Height: w.Height,
		Terrain: rows, TerrainLegend: legend,
		Components: comps, Colonies: seats,
	}
}

// NewSnapshot builds one tick frame. Callers must hold the match lock over w
// and rt, and must send the result after releasing it.
func NewSnapshot(w *sim.World, rt *prog.Runtime, endTick uint64) Snapshot {
	s := Snapshot{
		Tick:    w.Tick,
		EndTick: endTick,
		Robots:  make([]Robot, 0, len(w.Robots)),
		Bases:   make([]Base, 0, len(w.Bases)),
		Loose:   make([]Loose, 0, len(w.Loose)),
	}

	stats := map[sim.ColonyID]*ColonyStats{}
	stat := func(id sim.ColonyID) *ColonyStats {
		st, ok := stats[id]
		if !ok {
			st = &ColonyStats{Colony: int(id)}
			stats[id] = st
		}
		return st
	}

	for _, r := range w.Robots {
		mem := make([]*Point, sim.MemPoints)
		for i, m := range r.Memory {
			if m.Set {
				mem[i] = &Point{X: m.Coord.X, Y: m.Coord.Y}
			}
		}
		dto := Robot{
			ID: r.ID, Colony: int(r.Colony),
			X: r.Coord.X, Y: r.Coord.Y, Heading: int(r.Heading),
			HP: r.Health, HPMax: sim.StartingHealth(r.Blueprint),
			Cargo:     int(r.Cargo),
			Archetype: r.Blueprint.Name, Blueprint: r.Blueprint.ID,
			Program: r.ProgramID, Cooldown: r.Cooldown, Memory: mem,
		}
		if rt != nil {
			if tr, ok := rt.Trace(r.ID); ok {
				dto.Trace = &Trace{
					Tick: tr.Tick, Rule: tr.Rule, Action: string(tr.Action),
					Reason: tr.Reason, Idle: tr.Idle,
				}
			}
		}
		s.Robots = append(s.Robots, dto)

		st := stat(r.Colony)
		st.Robots++
		st.FleetValue += r.Blueprint.Value()
	}

	for _, b := range w.Bases {
		inv := b.SortedInventory()
		dto := Base{Colony: int(b.Colony), X: b.Coord.X, Y: b.Coord.Y,
			Inventory:  make([]InvEntry, 0, len(inv)),
			Blueprints: make([]Blueprint, 0, len(b.Blueprints)),
		}
		st := stat(b.Colony)
		for _, e := range inv {
			dto.Inventory = append(dto.Inventory, InvEntry{Variant: int(e.Variant), Count: e.Count})
			st.Inventory += e.Count
		}
		for _, bp := range b.Blueprints {
			dto.Blueprints = append(dto.Blueprints, blueprint(bp))
		}
		if b.Build.Ticks > 0 {
			dto.Build = &Build{
				Blueprint:  b.Build.Blueprint.Name,
				TicksLeft:  b.Build.Ticks,
				Components: variants(b.Build.Blueprint.Components),
			}
		}
		s.Bases = append(s.Bases, dto)
	}

	for _, l := range w.Loose {
		s.Loose = append(s.Loose, Loose{ID: l.ID, X: l.Coord.X, Y: l.Coord.Y, Variant: int(l.Variant)})
	}

	// Base order, not map order: bases are one per colony and already in colony
	// order, and a colony that lost every robot must still report zeroes.
	s.Colonies = make([]ColonyStats, 0, len(stats))
	for _, b := range w.Bases {
		s.Colonies = append(s.Colonies, *stat(b.Colony))
		delete(stats, b.Colony)
	}
	for _, r := range w.Robots {
		if st, ok := stats[r.Colony]; ok { // a colony with robots but no base
			s.Colonies = append(s.Colonies, *st)
			delete(stats, r.Colony)
		}
	}
	return s
}

func blueprint(bp sim.Blueprint) Blueprint {
	return Blueprint{
		ID: bp.ID, Name: bp.Name, Components: variants(bp.Components),
		Program: bp.ProgramID, Value: bp.Value(),
	}
}

func variants(vs []sim.Variant) []int {
	out := make([]int, 0, len(vs))
	for _, v := range vs {
		out = append(out, int(v))
	}
	return out
}
