package server

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
)

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
	// report is not telling the player anything they could act on.
	for _, i := range []int{0, 1, 2, 3} {
		if out.Rules[i].Fired == 0 || out.Rules[i].FirstTick < 0 {
			t.Errorf("rule %d never fired: %+v", i, out.Rules[i])
		}
	}
	// Every §10.7 rule ends its tick, so every one of them is traceable.
	for _, row := range out.Rules {
		if !row.Observable {
			t.Errorf("rule %d is not observable, but all of §10.7 has primary actions", row.Rule)
		}
	}
}

// TestDryRunSideEffectRuleIsNotCalledDead is the honest edge of the report: a
// rule made only of zero-tick side effects runs and evaluation continues
// (AGENTS.md action model), but the evaluator's trace names only the rule that
// took the tick. Such a rule must be marked unobservable rather than accused of
// never firing — a false "this rule is dead" is worse than saying nothing.
func TestDryRunSideEffectRuleIsNotCalledDead(t *testing.T) {
	p := prog.Program{V: prog.SchemaVersion, Rules: []prog.Rule{
		// Always matches, always writes, never ends the tick.
		{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.DoArg(prog.SaveCurrentPosition, 1)}},
		{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
	out := dryRun(p, lobby.DefaultBlueprint(), 20)

	if out.Rules[0].Observable {
		t.Error("a side-effect-only rule was reported as observable")
	}
	if !out.Rules[1].Observable {
		t.Error("a rule with a primary action was reported as unobservable")
	}
	if len(out.NeverFired) != 0 {
		t.Errorf("never_fired = %v, want the unobservable rule left out and rule 1 firing", out.NeverFired)
	}
	if out.Rules[1].Fired == 0 {
		t.Error("the rule that takes the tick never fired")
	}
}

// TestDryRunScoutNeverFires is the second acceptance case, and the reason this
// endpoint exists: design §10.8 as printed cannot act from a clean start —
// every rule needs cargo or a saved point, and nothing sets either. The report
// has to say so plainly rather than leaving the player to infer it.
func TestDryRunScoutNeverFires(t *testing.T) {
	out := dryRun(scoutProgram(), lobby.DefaultBlueprint(), dryRunTicks)

	// Rules 0 and 4 are side-effect-only (save_visible_target, clear_point), so
	// they are unobservable and cannot be listed; every rule that could have
	// been seen firing never was.
	want := []int{1, 2, 3, 5}
	if !reflect.DeepEqual(out.NeverFired, want) {
		t.Errorf("never_fired = %v, want every observable rule %v", out.NeverFired, want)
	}
	for _, i := range []int{0, 4} {
		if out.Rules[i].Observable {
			t.Errorf("§10.8 rule %d is side-effect-only and cannot be observed from a trace", i+1)
		}
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
