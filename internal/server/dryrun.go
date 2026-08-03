package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// DryRunner answers "does this program do anything at all?" without a match.
//
// It is the editor's smoke test: generate a throwaway arena, put one robot on
// the chosen blueprint in it, install the program, step a bounded number of
// ticks and report what happened. Nothing here can reach a live match — the
// world is built per request from a fixed seed and dropped when the response is
// written — and the execution path is exactly sim.Generate + sim.World.Step +
// prog.Runtime, so a preview can never disagree with the real simulation.
//
// The program is untrusted input, so the run is bounded on every axis: fixed
// arena, capped ticks, one robot, prog.Validate first, and one run per user per
// dryRunInterval.
type DryRunner struct {
	lib *Library

	mu   sync.Mutex
	last map[int64]time.Time
}

// NewDryRunner wires the dry runner to the library it borrows blueprint lookup
// and program validation from.
func NewDryRunner(lib *Library) *DryRunner {
	return &DryRunner{lib: lib, last: map[int64]time.Time{}}
}

// Bounds. The arena is fixed rather than caller-chosen: 16x16 is inside radar
// range from anywhere (design §7.2), which is what keeps design §10.7 from
// degenerating into a random walk in the one place a player would notice.
const (
	dryRunSeed     = 7   // fixed and reported: two runs must be comparable
	dryRunSide     = 16  // arena width and height
	dryRunTicks    = 200 // default when the caller does not ask
	maxDryRunTicks = 500 // hard cap; a larger request is refused, not clamped

	dryRunBarriers = 0.08
	dryRunRichness = 0.05

	// dryRunProgramID is the id the throwaway robot runs under. The caller's
	// blueprint may name a program that is not the one being previewed, so the
	// preview always installs under an id of its own.
	dryRunProgramID = "dry-run"
)

// dryRunInterval is the per-user floor between runs. A run is a few
// milliseconds of CPU, so this is about a caller in a loop, not about one run
// being expensive.
const dryRunInterval = time.Second

// dryRunClients bounds the rate-limiter map. Past it, entries older than the
// interval are swept; they can only be stale.
const dryRunClients = 4096

// DryRunEvent is one thing the program either did or never did. FirstTick is
// -1 when Count is zero, so "never happened" is a value, not an absence.
type DryRunEvent struct {
	Count     int `json:"count"`
	FirstTick int `json:"first_tick"`
}

func (e *DryRunEvent) record(tick int) {
	if e.Count == 0 {
		e.FirstTick = tick
	}
	e.Count++
}

// DryRunRule is one rule's activity. There is a row for every rule in the
// program, in program order, whether or not it ever matched — Fired == 0 is the
// single most useful thing this endpoint reports.
type DryRunRule struct {
	Rule      int `json:"rule"` // 0-based, as prog.Trace numbers them
	Fired     int `json:"fired"`
	FirstTick int `json:"first_tick"` // -1 when it never fired

	// Observable is false for a rule whose actions are all zero-tick side
	// effects. Such a rule matches, runs, and evaluation continues down the
	// list (AGENTS.md action model), so the evaluator's trace — which names
	// only the rule that took the tick — never mentions it. Fired is then not
	// evidence either way, and the rule is left out of NeverFired rather than
	// reported as dead. Making it observable needs prog.Trace to record every
	// matched rule, not just the primary one.
	Observable bool `json:"observable"`
}

// DryRunResult is the whole report.
type DryRunResult struct {
	Seed   int64 `json:"seed"`
	Ticks  int   `json:"ticks"` // ticks simulated
	Width  int   `json:"width"`
	Height int   `json:"height"`

	// Decisions is how many ticks the program was actually consulted on. It is
	// less than Ticks because movement and interaction cost more than one tick.
	Decisions int `json:"decisions"`

	Acted      DryRunEvent  `json:"acted"`       // decisions that produced a primary action
	Idle       DryRunEvent  `json:"idle"`        // decisions that produced none
	PickedUp   DryRunEvent  `json:"picked_up"`   // cargo actually acquired
	Deposited  DryRunEvent  `json:"deposited"`   // cargo actually delivered to base
	Rules      []DryRunRule `json:"rules"`       // one row per rule, program order
	NeverFired []int        `json:"never_fired"` // observable rule indices with Fired == 0

	// IdleReason is the evaluator's phrase for the last idle decision, e.g.
	// "no rule matched". Empty when the program never idled.
	IdleReason string `json:"idle_reason"`
}

// DryRun validates the program against the blueprint and, if it is legal,
// previews it. An invalid program comes back as a validation error with the
// findings attached, exactly as a refused save does: running a program that
// could not be installed would report on something the player cannot use.
func (d *DryRunner) DryRun(ctx context.Context, userID int64, raw json.RawMessage, blueprintID int64, ticks int) (DryRunResult, error) {
	switch {
	case ticks == 0:
		ticks = dryRunTicks
	case ticks < 1 || ticks > maxDryRunTicks:
		return DryRunResult{}, libErrf(http.StatusBadRequest, "ticks must be between 1 and %d", maxDryRunTicks)
	}
	if err := d.allow(userID, time.Now()); err != nil {
		return DryRunResult{}, err
	}
	bp, err := d.lib.blueprint(ctx, userID, blueprintID)
	if err != nil {
		return DryRunResult{}, err
	}
	p, res, ok := parseProgram(raw)
	if !ok {
		return DryRunResult{}, libValidationError(res)
	}
	if res := prog.Validate(p, bp); !res.OK() {
		return DryRunResult{}, libValidationError(res)
	}
	return dryRun(p, bp, ticks), nil
}

// allow enforces the per-user interval. The map is swept rather than reaped on
// a timer: entries are two words each and only a live caller can add one.
func (d *DryRunner) allow(userID int64, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, seen := d.last[userID]; seen && now.Sub(at) < dryRunInterval {
		return libErrf(http.StatusTooManyRequests, "one dry run per second; try again in a moment")
	}
	if len(d.last) >= dryRunClients {
		for id, at := range d.last {
			if now.Sub(at) >= dryRunInterval {
				delete(d.last, id)
			}
		}
		// Everything left is younger than the interval, so the sweep freed
		// nothing and the map would grow past its cap. Drop it: this is a
		// throttle, not a ledger, and the whole cost of resetting it is one
		// extra run per active caller — cheaper than either growing without a
		// bound or refusing a legitimate player.
		if len(d.last) >= dryRunClients {
			clear(d.last)
		}
	}
	d.last[userID] = now
	return nil
}

// dryRun is the simulation itself, free of HTTP and the database so a test can
// call it directly. It is prog's TestScavengerExample parameterised: one
// generated arena, one robot at its base, one installed program, N steps.
func dryRun(p prog.Program, bp sim.Blueprint, ticks int) DryRunResult {
	w := sim.Generate(dryRunSeed, sim.GenOpts{
		Width: dryRunSide, Height: dryRunSide, Colonies: 1,
		BarrierDensity: dryRunBarriers, Richness: dryRunRichness,
	})
	base := w.Bases[0]
	r := &sim.Robot{
		ID:        w.NextID(),
		Colony:    base.Colony,
		Coord:     base.Coord,
		Heading:   sim.North,
		Health:    sim.StartingHealth(bp),
		Blueprint: bp,
		ProgramID: dryRunProgramID,
	}
	w.Robots = append(w.Robots, r)

	rt := prog.NewRuntime()
	rt.Install(dryRunProgramID, p)
	w.Control = rt.Control

	out := DryRunResult{
		Seed: dryRunSeed, Ticks: ticks, Width: w.Width, Height: w.Height,
		Acted:      DryRunEvent{FirstTick: -1},
		Idle:       DryRunEvent{FirstTick: -1},
		PickedUp:   DryRunEvent{FirstTick: -1},
		Deposited:  DryRunEvent{FirstTick: -1},
		Rules:      make([]DryRunRule, len(p.Rules)),
		NeverFired: []int{},
	}
	for i := range out.Rules {
		out.Rules[i] = DryRunRule{Rule: i, FirstTick: -1, Observable: hasPrimary(p.Rules[i])}
	}

	for i := 0; i < ticks; i++ {
		tick := int(w.Tick)
		cargo, collected := r.Cargo, base.Stats.Collected
		w.Step()

		// A trace stamped with the tick just stepped is a decision made this
		// tick; anything older means the robot was mid-action and the program
		// was not consulted at all.
		if tr, ok := rt.Trace(r.ID); ok && int(tr.Tick) == tick {
			out.Decisions++
			if tr.Idle {
				out.Idle.record(tick)
				out.IdleReason = tr.Reason
			} else {
				out.Acted.record(tick)
			}
			if tr.Rule >= 0 && tr.Rule < len(out.Rules) {
				row := &out.Rules[tr.Rule]
				if row.Fired == 0 {
					row.FirstTick = tick
				}
				row.Fired++
			}
		}

		// Outcomes are read off world state, not off the chosen action: a rule
		// that issues pick_up_component with nothing in reach did not pick
		// anything up, and saying otherwise is the one lie this must not tell.
		if cargo == sim.VariantNone && r.Cargo != sim.VariantNone {
			out.PickedUp.record(tick)
		}
		if base.Stats.Collected > collected {
			out.Deposited.record(tick)
		}
	}

	for i := range out.Rules {
		if out.Rules[i].Observable && out.Rules[i].Fired == 0 {
			out.NeverFired = append(out.NeverFired, i)
		}
	}
	return out
}

// hasPrimary reports whether a rule contains an action that ends the tick. A
// rule that does is the one the evaluator names in its trace when it matches;
// a rule that does not is invisible to the trace entirely. Unknown actions are
// not primary — prog.Validate has already refused the program by the time this
// runs, so they cannot appear here anyway.
func hasPrimary(r prog.Rule) bool {
	for _, a := range r.Then {
		if spec, ok := prog.LookupAction(a.Do); ok && spec.Primary {
			return true
		}
	}
	return false
}

// HTTP surface. One route, behind RequireAuth like everything else in this
// package.

// Routes registers the dry-run endpoint. requireAuth is
// auth.Handler.RequireAuth.
func (d *DryRunner) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST /api/programs/dryrun", requireAuth(http.HandlerFunc(d.handleDryRun)))
}

func (d *DryRunner) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Program     json.RawMessage `json:"program"`
		BlueprintID int64           `json:"blueprint_id"`
		Ticks       int             `json:"ticks"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	out, err := d.DryRun(r.Context(), user.ID, body.Program, body.BlueprintID, body.Ticks)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
