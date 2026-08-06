package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// What a design *means*, for the blueprint configurator.
//
// BlueprintStats (programs.go) says what a parts list costs and buys in
// numbers. A number is not a decision: "speed 9" does not tell a player their
// scavenger now takes three ticks a cell and can no longer cross the rubble
// between it and the far quadrant. This file turns the numbers into the
// sentences the configurator's consequence column is made of.
//
// Every one of them is derived from internal/sim's own tables — the §3.1
// traversal matrix, the §8.1 weapon table, the §6.4 speed model — and none is
// written in JavaScript. E7.3 retunes the game by editing those tables, and a
// sentence in the browser would go quietly wrong the day it did.

// BlueprintPreview is the configurator's whole answer for one parts list: the
// numbers, what they mean, which of the caller's programs will run on it, and
// what one more part off the catalogue would do to all of it.
type BlueprintPreview struct {
	BlueprintStats
	Programs []ProgramFit `json:"programs"`
	Marginal []Marginal   `json:"marginal"`
}

// Marginal prices one catalogue row against the design on screen. A part's cost
// is not its mass, it is what its mass does to *this* robot: the same laser is
// free on a light scavenger and costs a whole robot off the opening fleet on a
// heavy gunner, and the catalogue row can say neither.
//
// Every row is answered in one request. The configurator used to ask
// /api/blueprints/preview once per hypothetical, which is a round trip per
// palette button per keystroke — a stampede, not a preview.
type Marginal struct {
	Variant int    `json:"variant"`
	OK      bool   `json:"ok"`              // §6.3 allows the part list with this added
	Error   string `json:"error,omitempty"` // and if it does not, why

	// What the design becomes with it fitted. Both are the rail's two gauges
	// one part further on; Fleet is 0 for a design the starting budget cannot
	// field at all, which is the budget half of "does this fit".
	TicksPerCell int `json:"ticks_per_cell"`
	Fleet        int `json:"fleet"`

	// Unlocks is how many rows of the rule language this part switches on:
	// predicates and actions whose required hardware the design would then
	// carry. It is what the *language* opens up, not a verdict on the player's
	// saved programs — that would be a prog.Validate pass over the library per
	// catalogue row, and this block is answered on every keystroke.
	Unlocks int `json:"unlocks"`
}

// marginals answers every catalogue row at once. Adding a component never
// removes one, so Unlocks cannot go negative.
func marginals(bp sim.Blueprint) []Marginal {
	budget := lobby.DefaultStartingBudget()
	lang := prog.Language()
	have := languageRows(lang, bp)
	cat := sim.Catalogue()
	out := make([]Marginal, 0, len(cat))
	for _, c := range cat {
		with := sim.Blueprint{Components: append(append(make([]sim.Variant, 0, len(bp.Components)+1),
			bp.Components...), c.Variant)}
		m := Marginal{Variant: int(c.Variant), OK: true, Unlocks: languageRows(lang, with) - have}
		if err := with.Validate(); err != nil {
			// An illegal design has no pace and no fleet — §6.3 has not decided
			// what it is. The reason is the whole answer the palette needs.
			m.OK, m.Error = false, err.Error()
		} else {
			m.TicksPerCell = sim.TicksPerCell(with, sim.Open)
			m.Fleet = lobby.StartingFleet(with, budget)
		}
		out = append(out, m)
	}
	return out
}

// languageRows counts the predicates and actions this hardware can satisfy.
// The Needs lists are prog's own (catalogue.go), so a predicate that grows a
// hardware requirement is priced here with no change to this function.
func languageRows(lang prog.Catalogue, bp sim.Blueprint) int {
	n := 0
	for _, p := range lang.Predicates {
		if carries(bp, p.Needs) {
			n++
		}
	}
	for _, a := range lang.Actions {
		if carries(bp, a.Needs) {
			n++
		}
	}
	return n
}

func carries(bp sim.Blueprint, needs []sim.ComponentKind) bool {
	for _, k := range needs {
		if !bp.Has(k) {
			return false
		}
	}
	return true
}

// ProgramFit is one library program judged against the design on screen.
// Blocked carries prog.Validate's first blocking error, because that is the
// part the player has to fix; Dead counts the predicates that are legal but can
// never be true on this hardware, which is a design smell rather than a fault.
type ProgramFit struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Blocked string `json:"blocked,omitempty"`
	Dead    int    `json:"dead"`
}

// deadPredicate is prog.Validate's code for a condition the blueprint's
// hardware can never satisfy (validate.go). Matched by code, not by message.
const deadPredicate = "dead_predicate"

// programFit validates every program in the caller's library against a design.
// The library is a page's worth of rows and Validate is pure, so this is one
// query and no I/O per program.
func (l *Library) programFit(ctx context.Context, userID int64, bp sim.Blueprint) ([]ProgramFit, error) {
	rows, err := l.ListPrograms(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ProgramFit, 0, len(rows))
	for _, row := range rows {
		fit := ProgramFit{ID: row.ID, Name: row.Name}
		p, err := prog.Decode(row.Program)
		if err != nil {
			// A row that no longer decodes is reported as unfit rather than
			// failing the whole preview: one bad program must not hide the
			// verdict on the other nine.
			fit.Blocked = "this program does not load"
			out = append(out, fit)
			continue
		}
		res := prog.Validate(p, bp)
		fit.OK = res.OK()
		if len(res.Errors) > 0 {
			fit.Blocked = res.Errors[0].Message
		}
		for _, w := range res.Warnings {
			if w.Code == deadPredicate {
				fit.Dead++
			}
		}
		out = append(out, fit)
	}
	return out, nil
}

// consequences is the design's own receipt: what this robot will and will not
// be able to do in the arena, in the order a player cares about it — how fast,
// where it may go, how much it survives, what it can do on contact, what it can
// perceive, and how many of it the opening budget buys.
//
// An illegal parts list gets none: design §6.3 has not decided what it is yet,
// and a sentence about a robot that cannot be built is worse than silence.
func consequences(bp sim.Blueprint) []string {
	if bp.Validate() != nil {
		return nil
	}
	var out []string
	out = append(out, speedLines(bp)...)
	out = append(out, durabilityLine(bp))
	out = append(out, armamentLines(bp)...)
	out = append(out, perceptionLine(bp))
	out = append(out, budgetLine(bp))
	return out
}

// speedLines is the §6.4 speed model and the §3.1 traversal matrix read
// together: the pace on open ground, then every terrain class this chassis is
// shut out of or favoured on. Both lists come from sim.TerrainSpecs, so a new
// terrain class appears here with no change to this function.
func speedLines(bp sim.Blueprint) []string {
	loco := locomotionName(bp)
	out := []string{fmt.Sprintf("One cell every %d ticks on open ground, on %s.",
		sim.TicksPerCell(bp, sim.Open), loco)}
	var blocked, favoured []string
	for _, s := range sim.TerrainSpecs() {
		if s.Terrain == sim.Open || s.HardBarrier {
			continue
		}
		switch {
		case !s.Terrain.Passable(locomotionVariant(bp)):
			blocked = append(blocked, s.Name)
		case sim.TicksPerCell(bp, s.Terrain) < sim.TicksPerCell(bp, sim.Open):
			favoured = append(favoured, fmt.Sprintf("%s (%d ticks a cell)",
				s.Name, sim.TicksPerCell(bp, s.Terrain)))
		}
	}
	if len(blocked) > 0 {
		out = append(out, fmt.Sprintf("Cannot enter %s — every route around it is a longer route.",
			join(blocked)))
	}
	if len(favoured) > 0 {
		out = append(out, fmt.Sprintf("Faster on %s.", join(favoured)))
	}
	return out
}

// durabilityLine prices the armoured body in the only currency that matters
// under fire: how many hits of each weapon in the catalogue it takes. Accuracy
// is deliberately not folded in — these are hits landed, not shots fired, and a
// player comparing two armour tiers wants the number that does not move.
func durabilityLine(bp sim.Blueprint) string {
	health := sim.StartingHealth(bp)
	var hits []string
	for _, c := range sim.Catalogue() {
		w, ok := sim.WeaponStats(c.Variant)
		if !ok || w.Damage <= 0 {
			continue
		}
		hits = append(hits, fmt.Sprintf("%d %s hits", (health+w.Damage-1)/w.Damage, c.Name))
	}
	if len(hits) == 0 {
		return fmt.Sprintf("%d health.", health)
	}
	return fmt.Sprintf("%d health — %s to destroy it.", health, join(hits))
}

// armamentLines says what the robot can do the moment something hostile is in
// front of it. The unarmed case is the important one: it is not a missing
// number, it is a whole branch of the rule language that will never fire.
func armamentLines(bp sim.Blueprint) []string {
	var out []string
	weapons := bp.Weapons()
	if len(weapons) == 0 {
		out = append(out, "Carries no weapon: no rule can make it attack, and it reads as unarmed "+
			"to every other player. Moving away is its only answer to contact.")
	}
	for _, v := range weapons {
		w, ok := sim.WeaponStats(v)
		if !ok {
			continue
		}
		out = append(out, fmt.Sprintf("%s: %d damage at %d cells, a shot every %d ticks, %d%% accurate.",
			v, w.Damage, w.Range, w.Cooldown, w.Accuracy))
	}
	if !bp.Has(sim.KindManipulator) {
		out = append(out, "No manipulator: it can neither pick a component up nor deliver one, "+
			"so it can add nothing to what the base builds from.")
	}
	return out
}

// perceptionLine is design §7.1 against §7.2: the wedge every robot has, and
// what the one radar slot buys on top of it.
func perceptionLine(bp sim.Blueprint) string {
	wedge := fmt.Sprintf("Sees a 90° wedge %d cells deep in front of it", sim.VisionRange)
	rng := sim.BlueprintRadarRange(bp)
	if rng == 0 {
		return wedge + " and nothing else — anything behind it may as well not exist."
	}
	return fmt.Sprintf("%s, plus %s contacts out to %d cells in every direction.",
		wedge, radarName(bp), rng)
}

// budgetLine is the trade the two meters are really about. The count comes from
// lobby.StartingFleet, which is the code that decides it: "approving a cheap
// body cannot buy a bigger colony" is a rule of that package, not arithmetic
// this one may repeat.
func budgetLine(bp sim.Blueprint) string {
	budget := lobby.DefaultStartingBudget()
	n := lobby.StartingFleet(bp, budget)
	if n == 0 {
		return fmt.Sprintf("Costs %d of the %d starting budget: too expensive to open with even one. "+
			"The base can still build it once parts arrive.", bp.Value(), budget)
	}
	if left := budget - n*bp.Value(); left > 0 {
		return fmt.Sprintf("The %d starting budget opens with %d of these and turns the remaining %d "+
			"into base inventory — spares, not robots.", budget, n, left)
	}
	return fmt.Sprintf("The %d starting budget opens with %d of these, to the last point.", budget, n)
}

func locomotionVariant(bp sim.Blueprint) sim.Variant {
	for _, v := range bp.Components {
		if k, ok := v.Kind(); ok && k == sim.KindLocomotion {
			return v
		}
	}
	return sim.VariantNone
}

func locomotionName(bp sim.Blueprint) string { return locomotionVariant(bp).String() }

func radarName(bp sim.Blueprint) string {
	for _, v := range bp.Components {
		if k, ok := v.Kind(); ok && k == sim.KindRadar {
			return v.String()
		}
	}
	return "radar"
}

// join renders a list the way a sentence needs it, which is not what
// strings.Join does with the last separator.
func join(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
