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
// The arena is not empty. Since rc-tad.14 it holds a hostile sparring partner
// and a friendly scout, both pinned below, because with nobody in it every
// combat, defensive and signal rule a player can write is unreachable and
// reports as never fired — a false negative indistinguishable from a mistake.
//
// The program is untrusted input, so the run is bounded on every axis: fixed
// arena, capped ticks, three robots and no production, prog.Validate first, and
// one run per user per dryRunInterval.
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

	// dryRunEnemyProgramID is the sparring partner's install id. Same runtime,
	// same evaluator, same rule language — see dryRunEnemyProgram.
	dryRunEnemyProgramID = "dry-run-sparring"

	// dryRunSpotterProgramID is the friendly scout's install id. See
	// dryRunSpotterProgram for why the preview fields one.
	dryRunSpotterProgramID = "dry-run-spotter"
)

// The sparring partner (rc-tad.14). An opponent is not optional: with an empty
// arena, sees_enemy_robot, visible_target_in_weapon_range, attack_visible_target,
// attack_radar_target and every enemy radar contact are unreachable, so a
// combat or defensive program reports *every* one of its rules as never fired —
// which a player cannot tell apart from having written them wrong. Design
// §10.9's responder, a shipped template, was entirely untestable that way.
//
// It is pinned here rather than borrowed from internal/lobby's AI profiles on
// purpose. Those four are a measured difficulty ladder and retuning one is a
// balance decision; if the preview referenced one, a balance pass would silently
// change what a player's dry run says about a program they did not touch. That
// is precisely the property the fixed seed exists to protect.
//
// The kit is deliberately light: the catalogue's weakest weapon (autogun,
// design §8.1 — range 4, 45% accuracy, 4 damage) on light armor. It hunts on
// enemy radar, whose 16-cell range covers the whole practice arena, so contact
// is guaranteed rather than lucky; it can put a dent in the player's robot, so
// health_below and §10.9's flee rule are reachable; it is soft enough that an
// armed design can actually destroy it; and it is far too weak to reliably
// delete an unarmed starter scavenger inside the default 200 ticks, so a
// scavenging preview still previews scavenging.
func dryRunEnemyBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         "bp-dry-run-sparring",
		Name:       "sparring partner",
		Components: []sim.Variant{sim.Tracks, sim.LightArmor, sim.AutoGun, sim.EnemyRadar},
		ProgramID:  dryRunEnemyProgramID,
	}
}

// dryRunEnemyProgram drives it: shoot what is in range, close on radar
// contacts, patrol otherwise. Ordinary rules over the ordinary evaluator — the
// sparring partner perceives exactly what sim.RobotView gives any robot and
// there is no branch anywhere that knows it is not a player.
func dryRunEnemyProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "sparring partner", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.SeesEnemyRobot), prog.Pred(prog.VisibleTargetInWpnRange)),
			Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
		{When: prog.Pred(prog.DetectedTargetInWpnRange),
			Then: []prog.Action{prog.Do(prog.AttackRadarTarget)}},
		{When: prog.Pred(prog.RadarDetectsTarget),
			Then: []prog.Action{prog.Do(prog.MoveToRadarTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}

// The friendly scout. Design §10.9's responder is a *responder*: its engage
// path is received_come_here → save_signal_position → move_to_point, because a
// robot with a 90° forward vision cone (design §7.1) and no rule that turns
// towards a threat cannot find one on its own. Measured: on its own shipped
// blueprint the §10.9 template took 192 damage over 500 ticks and never fired
// back once, because the sparring partner kept arriving outside its cone. That
// report is truthful and completely useless.
//
// So the preview fields a squadmate, exactly as internal/lobby's defensive
// profile pairs spotters with responders. It carries no manipulator and no
// weapon: it competes for nothing the player's robot wants, collects nothing
// that would inflate a scavenging report, and cannot land a hit the player's
// program would be credited with. All it does is walk around and shout.
func dryRunSpotterBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         "bp-dry-run-spotter",
		Name:       "scout",
		Components: []sim.Variant{sim.Tracks, sim.LightArmor},
		ProgramID:  dryRunSpotterProgramID,
	}
}

// dryRunSpotterProgram is internal/lobby's spotter idiom with the scavenging
// removed: see an enemy, call the colony in, keep walking. broadcast_come_here
// is a zero-tick side effect under the locked action model, so rule 1 costs the
// scout nothing and the patrol rules below it still run in the same tick.
func dryRunSpotterProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "scout", Rules: []prog.Rule{
		{When: prog.Pred(prog.SeesEnemyRobot),
			Then: []prog.Action{prog.Do(prog.BroadcastComeHere)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
		{When: prog.Pred(prog.CarryingNothing),
			Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
}

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
//
// Fired counts every tick the rule matched, including a rule of nothing but
// zero-tick side effects that ran and let evaluation continue (AGENTS.md action
// model). prog.Trace records every matched rule, so no rule is invisible here
// and every row is evidence.
type DryRunRule struct {
	Rule      int `json:"rule"` // 0-based, as prog.Trace numbers them
	Fired     int `json:"fired"`
	FirstTick int `json:"first_tick"` // -1 when it never fired
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
	NeverFired []int        `json:"never_fired"` // rule indices with Fired == 0

	// Combat outcomes. An opponent in the arena is only half the fix: a program
	// that fights still cannot tell whether it *worked* unless the report says
	// so. All three events keep the {count, first_tick} shape above so the
	// client renders them exactly like the rest.
	//
	// Attacked is decisions that chose an attack — the shot the program meant to
	// take. Hit is the subset that took health off an enemy, so Attacked without
	// Hit is a program aiming at something it cannot actually reach or reload
	// for. TookDamage is ticks the robot lost health, which is what makes
	// health_below and design §10.9's flee rule reachable at all.
	Attacked   DryRunEvent `json:"attacked"`
	Hit        DryRunEvent `json:"hit"`
	TookDamage DryRunEvent `json:"took_damage"`

	DamageDealt int `json:"damage_dealt"`
	DamageTaken int `json:"damage_taken"`
	Kills       int `json:"kills"` // sparring partners destroyed

	// Survived is false when the robot was destroyed before the run ended;
	// DestroyedTick is the tick it happened on, or -1. A short run that stops
	// reporting halfway is information, not a bug, and the client has to be able
	// to say which it was.
	Survived      bool `json:"survived"`
	DestroyedTick int  `json:"destroyed_tick"`
	Health        int  `json:"health"`     // at the end of the run
	MaxHealth     int  `json:"max_health"` // at the start

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
// generated arena, one robot at its base, one sparring partner at the other
// base, one installed program, N steps.
//
// Two colonies rather than one is the whole of rc-tad.14's cost, and it is one
// extra robot: the generated bases carry no approved blueprints, so neither
// colony ever produces, and the tick cap holds unchanged.
func dryRun(p prog.Program, bp sim.Blueprint, ticks int) DryRunResult {
	w := sim.Generate(dryRunSeed, sim.GenOpts{
		Width: dryRunSide, Height: dryRunSide, Colonies: 2,
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
	sbp := dryRunSpotterBlueprint()
	spotter := &sim.Robot{
		ID:        w.NextID(),
		Colony:    base.Colony,
		Coord:     base.Coord,
		Heading:   sim.South,
		Health:    sim.StartingHealth(sbp),
		Blueprint: sbp,
		ProgramID: dryRunSpotterProgramID,
	}
	ebp := dryRunEnemyBlueprint()
	enemyBase := w.Bases[1]
	enemy := &sim.Robot{
		ID:        w.NextID(),
		Colony:    enemyBase.Colony,
		Coord:     enemyBase.Coord,
		Heading:   sim.South,
		Health:    sim.StartingHealth(ebp),
		Blueprint: ebp,
		ProgramID: dryRunEnemyProgramID,
	}
	w.Robots = append(w.Robots, r, spotter, enemy)

	rt := prog.NewRuntime()
	rt.Install(dryRunProgramID, p)
	rt.Install(dryRunSpotterProgramID, dryRunSpotterProgram())
	rt.Install(dryRunEnemyProgramID, dryRunEnemyProgram())
	// Forget is the pairing prog.Runtime documents. It also makes "the robot
	// stopped deciding" unambiguous below: a destroyed robot has no trace left.
	w.Control, w.OnDestroy = rt.Control, rt.Forget

	out := DryRunResult{
		Seed: dryRunSeed, Ticks: ticks, Width: w.Width, Height: w.Height,
		Acted:         DryRunEvent{FirstTick: -1},
		Idle:          DryRunEvent{FirstTick: -1},
		PickedUp:      DryRunEvent{FirstTick: -1},
		Deposited:     DryRunEvent{FirstTick: -1},
		Attacked:      DryRunEvent{FirstTick: -1},
		Hit:           DryRunEvent{FirstTick: -1},
		TookDamage:    DryRunEvent{FirstTick: -1},
		Rules:         make([]DryRunRule, len(p.Rules)),
		NeverFired:    []int{},
		Survived:      true,
		DestroyedTick: -1,
		MaxHealth:     r.Health,
	}
	for i := range out.Rules {
		out.Rules[i] = DryRunRule{Rule: i, FirstTick: -1}
	}

	for i := 0; i < ticks; i++ {
		tick := int(w.Tick)
		cargo, collected := r.Cargo, base.Stats.Collected
		health, enemyHealth := r.Health, hostileHealth(w, base.Colony)
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
				if tr.Action == prog.AttackVisibleTarget || tr.Action == prog.AttackRadarTarget {
					out.Attacked.record(tick)
				}
			}
			// Every rule that matched, not just the one that took the tick: a
			// side-effect-only rule fires and evaluation continues past it.
			for j := range out.Rules {
				if !tr.Matched(j) {
					continue
				}
				row := &out.Rules[j]
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

		// Combat outcomes are read off health, for the same reason: an attack
		// action is what the program *meant*, a health drop is what happened.
		// A destroyed robot is swept at the end of the tick it died on, and it
		// is at zero health by then, so the difference is the damage either way.
		if dealt := enemyHealth - hostileHealth(w, base.Colony); dealt > 0 {
			out.DamageDealt += dealt
			out.Hit.record(tick)
		}
		if taken := health - r.Health; taken > 0 {
			out.DamageTaken += taken
			out.TookDamage.record(tick)
		}
		if r.Health <= 0 && out.Survived {
			out.Survived, out.DestroyedTick = false, tick
		}
	}
	out.Kills = base.Stats.Kills
	out.Health = max(r.Health, 0)

	for i := range out.Rules {
		if out.Rules[i].Fired == 0 {
			out.NeverFired = append(out.NeverFired, i)
		}
	}
	return out
}

// hostileHealth is the total health of every robot not in the given colony. It
// is the only way to measure damage dealt without a per-shot hook in sim: a
// robot destroyed this tick is at zero before it is swept, so the difference
// across a Step is the damage regardless of whether anything died.
func hostileHealth(w *sim.World, colony sim.ColonyID) int {
	total := 0
	for _, o := range w.Robots {
		if o.Colony != colony {
			total += max(o.Health, 0)
		}
	}
	return total
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
