package server

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// defenderBlueprint is the seeded "defender" starter (programs.go), the
// blueprint the §10.9 template is offered against. Spelled out here so a change
// to the seed is a visible test failure rather than a silent change of subject.
func defenderBlueprint() sim.Blueprint {
	return sim.Blueprint{
		ID:         "bp-test-defender",
		Name:       starterDefender,
		Components: []sim.Variant{sim.Tracks, sim.HeavyArmor, sim.Laser, sim.PartsRadar},
	}
}

// TestDryRunScavengerScavenges is the bead's first acceptance case: the design
// §10.7 program, unmodified, on the starter blueprint, reports that it actually
// got a component into base inventory — the whole point of running it blind.
func TestDryRunScavengerScavenges(t *testing.T) {
	out := dryRun(lobby.DefaultProgram(), lobby.DefaultBlueprint(), dryRunTicks)

	if out.PickedUp.Count == 0 {
		t.Errorf("the §10.7 scavenger never picked anything up in %d ticks", out.Ticks)
	}
	if out.Deposited.Count == 0 {
		t.Errorf("the §10.7 scavenger never deposited in %d ticks", out.Ticks)
	}
	if out.PickedUp.FirstTick < 0 || out.Deposited.FirstTick < out.PickedUp.FirstTick {
		t.Errorf("deposited at %d before picking up at %d", out.Deposited.FirstTick, out.PickedUp.FirstTick)
	}
	if out.Acted.Count == 0 {
		t.Error("the program never acted at all")
	}
	if out.Decisions == 0 || out.Decisions > out.Ticks {
		t.Errorf("decisions = %d over %d ticks", out.Decisions, out.Ticks)
	}
	if len(out.Rules) != len(lobby.DefaultProgram().Rules) {
		t.Errorf("rules = %d rows, want one per rule", len(out.Rules))
	}
	// The rules that carry the scavenge loop must all show activity, or the
	// report is not telling the player anything they could act on. That is the
	// first three now, not the first four: the deposit rule §10.7 opens with is
	// gone, because the evaluator deposits by reflex (rc-tad.13).
	for _, i := range []int{0, 1, 2} {
		if out.Rules[i].Fired == 0 || out.Rules[i].FirstTick < 0 {
			t.Errorf("rule %d never fired: %+v", i, out.Rules[i])
		}
	}
	// rc-tad.14 put a hostile in the arena, and the reason it is the weakest
	// weapon in the catalogue is right here: an unarmed scavenger has to still
	// be scavenging at the end of the default run. It gets shot at — that is the
	// point, and the report has to say so rather than looking like an empty
	// arena — but being shot at must not be the same thing as being deleted.
	if out.DamageTaken == 0 || out.TookDamage.Count == 0 {
		t.Error("the scavenger was never shot at, so the arena has no opponent in it")
	}
	if !out.Survived {
		t.Errorf("the starter scavenger was destroyed at tick %d of %d: the sparring partner is too strong",
			out.DestroyedTick, out.Ticks)
	}
	if out.Attacked.Count != 0 || out.DamageDealt != 0 {
		t.Errorf("an unarmed scavenger reports attacking: %+v, %d damage", out.Attacked, out.DamageDealt)
	}
}

// TestDryRunResponderFights is rc-tad.14's acceptance case: design §10.9's
// defensive responder — a shipped template — used to report every one of its
// rules as never fired, because the practice arena had nobody in it to fight.
// Its combat rule has to fire now, and the report has to carry the outcome, or
// the player still cannot tell a working attack rule from a broken one.
func TestDryRunResponderFights(t *testing.T) {
	p := responderProgram()
	out := dryRun(p, defenderBlueprint(), dryRunTicks)

	// Rule 2 (§10.9's "enemy in sight and in weapon range → attack") is the one
	// the empty arena made unreachable.
	const attackRule = 2
	if out.Rules[attackRule].Fired == 0 {
		t.Errorf("the attack rule still never fires: %+v", out.Rules[attackRule])
	}
	for _, i := range out.NeverFired {
		if i == attackRule {
			t.Error("the attack rule is still reported as never fired")
		}
	}
	if out.Attacked.Count == 0 || out.Attacked.FirstTick < 0 {
		t.Errorf("attacked = %+v, want shots taken", out.Attacked)
	}
	if out.Hit.Count == 0 || out.DamageDealt == 0 {
		t.Errorf("hit = %+v for %d damage: the shots never landed", out.Hit, out.DamageDealt)
	}
	if out.Hit.Count > out.Attacked.Count {
		t.Errorf("hit %d times on %d attacks", out.Hit.Count, out.Attacked.Count)
	}
	if out.TookDamage.Count == 0 || out.DamageTaken == 0 {
		t.Error("the responder was never shot at, so health_below can never hold")
	}
	if out.Health != out.MaxHealth-out.DamageTaken {
		t.Errorf("health %d of %d after taking %d damage does not add up",
			out.Health, out.MaxHealth, out.DamageTaken)
	}
	// The whole program must not have gone dark either: this is the case the
	// bead calls a false negative, so "meaningfully" means most rules report.
	if len(out.NeverFired) >= len(p.Rules)-1 {
		t.Errorf("%d of %d rules still never fired: %v", len(out.NeverFired), len(p.Rules), out.NeverFired)
	}
}

// TestDryRunKillIsReported is the other half: a design that can actually win
// has to be told that it won. The sparring partner is deliberately soft enough
// for this to be reachable — an armed program that destroys it and survives is
// the clearest "yes, this works" the endpoint can give.
func TestDryRunKillIsReported(t *testing.T) {
	// A laser gunner on enemy radar: the same shape as the sparring partner,
	// one armor tier up.
	bp := sim.Blueprint{ID: "bp-test-gunner", Name: "gunner",
		Components: []sim.Variant{sim.Tracks, sim.MediumArmor, sim.Laser, sim.EnemyRadar}}
	out := dryRun(dryRunEnemyProgram(), bp, dryRunTicks)

	if out.Kills == 0 {
		t.Errorf("a laser gunner never destroyed the sparring partner in %d ticks (%d damage dealt)",
			out.Ticks, out.DamageDealt)
	}
	if !out.Survived || out.DestroyedTick != -1 {
		t.Errorf("survived = %v, destroyed at %d", out.Survived, out.DestroyedTick)
	}
	if len(out.NeverFired) != 0 {
		t.Errorf("never_fired = %v, want nothing: every rule of a hunter has a target now", out.NeverFired)
	}
}

// TestDryRunFieldedKit pins the two robots the preview supplies. They are
// deliberately not internal/lobby's AI profiles: those four are a measured
// difficulty ladder, and retuning one must never silently change what a
// player's dry run says about a program they did not touch.
func TestDryRunFieldedKit(t *testing.T) {
	for _, tc := range []struct {
		what string
		bp   sim.Blueprint
		p    prog.Program
		id   string
	}{
		{"sparring partner", dryRunEnemyBlueprint(), dryRunEnemyProgram(), dryRunEnemyProgramID},
		{"scout", dryRunSpotterBlueprint(), dryRunSpotterProgram(), dryRunSpotterProgramID},
	} {
		if err := tc.bp.Validate(); err != nil {
			t.Errorf("the %s is not a legal design: %v", tc.what, err)
		}
		if res := prog.Validate(tc.p, tc.bp); !res.OK() {
			t.Errorf("the %s's program does not validate on its own hardware: %+v", tc.what, res.Errors)
		}
		if tc.bp.ProgramID != tc.id {
			t.Errorf("%s blueprint program id = %q, want %q", tc.what, tc.bp.ProgramID, tc.id)
		}
		if tc.bp.ID == lobby.DefaultBlueprint().ID || tc.bp.ProgramID == dryRunProgramID {
			t.Errorf("the %s shares an id with the player's install", tc.what)
		}
	}
	// The scout is a witness, not a second player: it must never collect
	// anything the player's report would then take credit for, and never land a
	// hit the player's colony would be credited with.
	scout := dryRunSpotterBlueprint()
	if scout.Has(sim.KindManipulator) {
		t.Error("the scout carries a manipulator, so it competes for components")
	}
	if len(scout.Weapons()) != 0 {
		t.Error("the scout is armed, so a kill in the report may not be the player's")
	}
}

// TestDryRunIsReproducible is the landmine PR #22 left behind: adding a second
// colony must not have made the run depend on anything but the seed. The
// endpoint-level test below covers two users; this one covers the same call
// twice, including everything the opponent contributes.
func TestDryRunIsReproducible(t *testing.T) {
	for _, ticks := range []int{dryRunTicks, maxDryRunTicks} {
		a := dryRun(responderProgram(), defenderBlueprint(), ticks)
		b := dryRun(responderProgram(), defenderBlueprint(), ticks)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("two runs at %d ticks differ:\n%+v\n%+v", ticks, a, b)
		}
	}
}

// TestDryRunSideEffectRuleIsNotCalledDead is rc-tad.6's acceptance: a rule made
// only of zero-tick side effects runs and evaluation continues past it
// (AGENTS.md action model). It used to be invisible to the trace and had to be
// excused as "unobservable"; now it is reported as having fired, because it did.
func TestDryRunSideEffectRuleIsNotCalledDead(t *testing.T) {
	p := prog.Program{V: prog.SchemaVersion, Rules: []prog.Rule{
		// Always matches, always writes, never ends the tick.
		{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.DoArg(prog.SaveCurrentPosition, 1)}},
		{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
	out := dryRun(p, lobby.DefaultBlueprint(), 20)

	if out.Rules[0].Fired == 0 || out.Rules[0].FirstTick < 0 {
		t.Errorf("the side-effect-only rule matched every tick but reports %+v", out.Rules[0])
	}
	if out.Rules[1].Fired == 0 {
		t.Error("the rule that takes the tick never fired")
	}
	// Both rules match on the same tick, so the side effect can never be rarer
	// than the rule below it.
	if out.Rules[0].Fired != out.Rules[1].Fired {
		t.Errorf("rule 1 fired %d times, rule 2 %d; both match on the same ticks",
			out.Rules[0].Fired, out.Rules[1].Fired)
	}
	if len(out.NeverFired) != 0 {
		t.Errorf("never_fired = %v, want nothing: both rules fire", out.NeverFired)
	}
}

// TestDryRunScoutNeverFires is the second acceptance case, and the reason this
// endpoint exists: design §10.8 as printed cannot act from a clean start —
// every rule needs cargo or a saved point, and nothing sets either. The report
// has to say so plainly rather than leaving the player to infer it.
func TestDryRunScoutNeverFires(t *testing.T) {
	out := dryRun(scoutProgram(), lobby.DefaultBlueprint(), dryRunTicks)

	// Every rule, including the two side-effect-only ones (rules 1 and 5 of
	// §10.8: save_visible_target, clear_point). Those used to be excused as
	// unobservable; the trace now proves they genuinely never matched.
	want := []int{0, 1, 2, 3, 4, 5}
	if !reflect.DeepEqual(out.NeverFired, want) {
		t.Errorf("never_fired = %v, want every rule %v", out.NeverFired, want)
	}
	for _, row := range out.Rules {
		if row.Fired != 0 || row.FirstTick != -1 {
			t.Errorf("rule %d fired from a clean start: %+v", row.Rule, row)
		}
	}
	if out.Acted.Count != 0 || out.Acted.FirstTick != -1 {
		t.Errorf("acted = %+v, want never", out.Acted)
	}
	if out.Idle.Count == 0 {
		t.Error("the robot never idled, but no rule ever fired")
	}
	if out.IdleReason == "" {
		t.Error("an idle run carries no reason")
	}
	if out.PickedUp.Count != 0 || out.Deposited.Count != 0 {
		t.Errorf("a program that never fired picked up %+v and deposited %+v", out.PickedUp, out.Deposited)
	}
}

// TestDryRunIsDeterministic is the third acceptance case, through the full
// endpoint path and for two different users: "try it" twice must give the same
// answer, or a player cannot tell what their edit changed.
func TestDryRunIsDeterministic(t *testing.T) {
	lib, database := newLibrary(t)
	d := NewDryRunner(lib)
	raw := encode(t, lobby.DefaultProgram())

	var first DryRunResult
	for i, name := range []string{"alice", "bob"} {
		u := newUser(t, database, name)
		out, err := d.DryRun(t.Context(), u.ID, raw, 0, 0)
		if err != nil {
			t.Fatalf("DryRun(%s) = %v", name, err)
		}
		if out.Seed != dryRunSeed {
			t.Errorf("seed = %d, want the fixed %d reported back", out.Seed, dryRunSeed)
		}
		if i == 0 {
			first = out
			continue
		}
		if !reflect.DeepEqual(out, first) {
			t.Errorf("two runs of the same program differ:\n%+v\n%+v", first, out)
		}
	}
}

// TestDryRunBounds is the fourth acceptance case plus the rest of the resource
// envelope: this executes untrusted input, so an oversized request is refused
// rather than honoured, and a caller in a loop is throttled.
func TestDryRunBounds(t *testing.T) {
	lib, database := newLibrary(t)
	raw := encode(t, lobby.DefaultProgram())

	t.Run("oversized tick count is refused", func(t *testing.T) {
		d := NewDryRunner(lib)
		u := newUser(t, database, "greedy")
		for _, ticks := range []int{maxDryRunTicks + 1, 1 << 30, -1} {
			if _, err := d.DryRun(t.Context(), u.ID, raw, 0, ticks); err == nil {
				t.Errorf("DryRun(ticks=%d) was honoured", ticks)
			} else {
				wantLibStatus(t, err, http.StatusBadRequest)
			}
		}
		// Refusing must not have consumed the rate-limit budget, and the cap
		// itself must still be runnable.
		out, err := d.DryRun(t.Context(), u.ID, raw, 0, maxDryRunTicks)
		if err != nil {
			t.Fatalf("DryRun(ticks=%d) = %v", maxDryRunTicks, err)
		}
		if out.Ticks != maxDryRunTicks {
			t.Errorf("ticks = %d, want %d", out.Ticks, maxDryRunTicks)
		}
	})

	t.Run("a caller in a loop is throttled", func(t *testing.T) {
		d := NewDryRunner(lib)
		u := newUser(t, database, "hammer")
		if _, err := d.DryRun(t.Context(), u.ID, raw, 0, 0); err != nil {
			t.Fatalf("first DryRun() = %v", err)
		}
		if _, err := d.DryRun(t.Context(), u.ID, raw, 0, 0); err == nil {
			t.Error("a second immediate run was allowed")
		} else {
			wantLibStatus(t, err, http.StatusTooManyRequests)
		}
		// Another player is not collateral damage.
		other := newUser(t, database, "bystander")
		if _, err := d.DryRun(t.Context(), other.ID, raw, 0, 0); err != nil {
			t.Errorf("a second user was throttled by the first: %v", err)
		}
		// And the throttle lifts.
		d.mu.Lock()
		d.last[u.ID] = time.Now().Add(-2 * dryRunInterval)
		d.mu.Unlock()
		if _, err := d.DryRun(t.Context(), u.ID, raw, 0, 0); err != nil {
			t.Errorf("the throttle never lifted: %v", err)
		}
	})

	// The rate limiter is per-user state keyed by an attacker-influenced count
	// of identities, so it has to be bounded whatever the traffic looks like.
	t.Run("the rate-limiter map stays bounded", func(t *testing.T) {
		d := NewDryRunner(lib)
		now := time.Now()
		for i := int64(0); i < dryRunClients*3; i++ {
			if err := d.allow(i, now); err != nil { // every caller distinct and fresh
				t.Fatalf("allow(%d) = %v", i, err)
			}
			if len(d.last) > dryRunClients {
				t.Fatalf("after %d callers the map holds %d entries, cap is %d", i+1, len(d.last), dryRunClients)
			}
		}
	})

	t.Run("an invalid program is reported, not run", func(t *testing.T) {
		d := NewDryRunner(lib)
		u := newUser(t, database, "invalid")
		// move_to_radar_target needs a parts radar, which the blueprint has;
		// an unknown action is illegal on any blueprint.
		bad := encode(t, prog.Program{V: prog.SchemaVersion, Rules: []prog.Rule{
			{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.Do("no_such_action")}},
		}})
		_, err := d.DryRun(t.Context(), u.ID, bad, 0, 0)
		if err == nil {
			t.Fatal("an invalid program was run")
		}
		se := wantLibStatus(t, err, http.StatusUnprocessableEntity)
		if se.result == nil || len(se.result.Errors) == 0 {
			t.Errorf("no validation findings came back: %+v", se.result)
		}
	})

	t.Run("malformed JSON is reported, not run", func(t *testing.T) {
		d := NewDryRunner(lib)
		u := newUser(t, database, "malformed")
		if _, err := d.DryRun(t.Context(), u.ID, []byte(`{"rules":`), 0, 0); err == nil {
			t.Error("malformed JSON was run")
		} else {
			wantLibStatus(t, err, http.StatusUnprocessableEntity)
		}
	})
}
