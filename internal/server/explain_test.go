package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// alwaysDraft is a program every live robot matches on every blueprint:
// health_above 0 is true for anything that has not been destroyed, and neither
// rule needs a component. Both rules match, so rule 1 is always shadowed by
// rule 0 whatever the world is doing under the test.
func alwaysDraft() json.RawMessage {
	p := prog.Program{V: prog.SchemaVersion, Name: "draft", Rules: []prog.Rule{
		{When: prog.PredArg(prog.HealthAbove, 0), Then: []prog.Action{prog.Do(prog.Stop)}},
		{When: prog.PredArg(prog.HealthAbove, 0), Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
	raw, err := p.Encode()
	if err != nil {
		panic(err)
	}
	return raw
}

// TestShadowTestAnswersWhichRuleWins is the editor's question: given this draft
// rule list and that live robot, which rule takes the tick and which rules
// would have matched but never got it.
func TestShadowTestAnswersWhichRuleWins(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	robot := aRobot(t, m, colonyOf(t, m, owner))

	got, err := h.ShadowTest(m.ID, robot, alwaysDraft())
	if err != nil {
		t.Fatalf("ShadowTest() = %v", err)
	}
	if got.Robot != robot {
		t.Errorf("answer is for robot %d, asked about %d", got.Robot, robot)
	}
	if got.Rule != 0 || got.Action != string(prog.Stop) {
		t.Errorf("winner = rule %d action %q, want rule 0 %q", got.Rule, got.Action, prog.Stop)
	}
	want := []RuleVerdict{
		{Rule: 0, Verdict: string(prog.VerdictWon), ShadowedBy: -1},
		{Rule: 1, Verdict: string(prog.VerdictShadowed), ShadowedBy: 0},
	}
	if len(got.Rules) != len(want) {
		t.Fatalf("got %d verdicts, want %d", len(got.Rules), len(want))
	}
	for i, rv := range got.Rules {
		if rv != want[i] {
			t.Errorf("verdict %d = %+v, want %+v", i, rv, want[i])
		}
	}
	assertTable(t, got.Explanation)

	// The one caveat of evaluating from outside a tick: the signal inbox is
	// consumed inside sim.World.Step, so the communication rows say "unknown"
	// rather than a confident false.
	for _, row := range got.Conditions {
		if row.Group == prog.GroupCommunication && !row.Unknown {
			t.Errorf("communication row %s claims a truth value the shadow test cannot know", row.Pred)
		}
	}
}

// TestShadowTestDoesNotMutate is the invariant: a draft is evaluated, never
// installed. The world under the test is stepping at 10Hz, so the check is
// paired by tick — a hash taken either side of the call within one tick must be
// identical.
func TestShadowTestDoesNotMutate(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	robot := aRobot(t, m, colonyOf(t, m, owner))

	program := ""
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if r := w.RobotByID(robot); r != nil {
			program = r.ProgramID
		}
	})

	state := func() (uint64, uint64) {
		var tick, hash uint64
		m.Read(func(w *sim.World, _ *prog.Runtime) { tick, hash = w.Tick, w.StateHash() })
		return tick, hash
	}

	paired := 0
	for i := 0; i < 40 && paired < 3; i++ {
		beforeTick, beforeHash := state()
		if _, err := h.ShadowTest(m.ID, robot, alwaysDraft()); err != nil {
			t.Fatalf("ShadowTest() = %v", err)
		}
		afterTick, afterHash := state()
		if beforeTick != afterTick {
			continue // the world stepped on its own; nothing to compare
		}
		paired++
		if beforeHash != afterHash {
			t.Fatalf("state hash changed across a shadow test within tick %d: %d -> %d",
				beforeTick, beforeHash, afterHash)
		}
	}
	if paired == 0 {
		t.Fatal("never observed a shadow test inside a single tick")
	}

	// And the draft was not installed on the way past.
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		r := w.RobotByID(robot)
		if r == nil {
			t.Fatal("the robot vanished during the test")
		}
		if r.ProgramID != program {
			t.Errorf("robot now runs %q, was running %q", r.ProgramID, program)
		}
		if ex, ok := rt.Explanation(robot); ok && len(ex.Rules) == 2 {
			t.Error("the runtime recorded the two-rule draft as the robot's program")
		}
	})
}

// TestShadowTestRefusesADraftThatCouldNotBeInstalled: answering verdicts for a
// program the player cannot save would be answering the wrong question, so the
// same validation an install runs happens here, with the rule-indexed issues
// the editor highlights.
func TestShadowTestRefusesADraftThatCouldNotBeInstalled(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	robot := aRobot(t, m, colonyOf(t, m, owner))

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"unknown predicate", json.RawMessage(
			`{"v":1,"rules":[{"when":{"op":"pred","pred":"telepathy"},"then":[{"do":"stop"}]}]}`)},
		{"rule with no action", json.RawMessage(
			`{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[]}]}`)},
		{"not a program", json.RawMessage(`"hello"`)},
		{"nothing at all", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := h.ShadowTest(m.ID, robot, c.raw)
			var ce cmdError
			if !errors.As(err, &ce) {
				t.Fatalf("ShadowTest() = %v, want a command error", err)
			}
			if ce.code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", ce.code, http.StatusBadRequest)
			}
			if len(ce.issues) == 0 {
				t.Error("the refusal carried no issues for the editor to point at")
			}
		})
	}

	if _, err := h.ShadowTest(m.ID, 999999, alwaysDraft()); err == nil {
		t.Error("a shadow test against a robot that does not exist was answered")
	}
	if _, err := h.ShadowTest(m.ID+9999, robot, alwaysDraft()); err == nil {
		t.Error("a shadow test against a match that does not exist was answered")
	}
}

// TestTraceCarriesTheConditionTable: the same answer for the *installed*
// program, recorded on the tick the evaluator decided and delivered on the poll
// the client already makes.
func TestTraceCarriesTheConditionTable(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	robot := aRobot(t, m, colonyOf(t, m, owner))

	first, err := h.TraceOf(m.ID, robot, 0)
	if err != nil {
		t.Fatalf("TraceOf() = %v", err)
	}
	if first.Explain != nil {
		t.Error("a robot recorded a condition table before anyone was watching it")
	}

	deadline := time.Now().Add(3 * time.Second)
	var got TraceHistory
	for time.Now().Before(deadline) {
		got, err = h.TraceOf(m.ID, robot, 0)
		if err != nil {
			t.Fatalf("TraceOf() = %v", err)
		}
		if got.Explain != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.Explain == nil {
		t.Fatal("a watched robot recorded no condition table after 3s")
	}
	assertTable(t, *got.Explain)

	// Stamped with the tick it was decided on: a robot mid-move is not
	// consulted every tick, and a table dated now would be a lie about a
	// decision taken several ticks ago.
	if got.Explain.Tick > got.Tick {
		t.Errorf("condition table is from tick %d, the world is at %d", got.Explain.Tick, got.Tick)
	}
	for _, e := range got.Events {
		if e.Tick != got.Explain.Tick {
			continue
		}
		if e.Rule != got.Explain.Rule {
			t.Errorf("tick %d: the event says rule %d, the table says rule %d",
				e.Tick, e.Rule, got.Explain.Rule)
		}
	}
}

// assertTable holds the properties every condition table has, wherever it came
// from: the whole catalogue is covered, groups are filled in, an impossible row
// never claims to be true, and an unknown row never claims either.
func assertTable(t *testing.T, ex Explanation) {
	t.Helper()
	cat := prog.Language().Predicates
	if len(ex.Conditions) < len(cat) {
		t.Errorf("table has %d rows for a catalogue of %d predicates", len(ex.Conditions), len(cat))
	}
	seen := map[string]bool{}
	for _, row := range ex.Conditions {
		seen[row.Pred] = true
		if row.Group == "" {
			t.Errorf("row %s has no group to file it under", row.Pred)
		}
		if row.True && (row.Impossible || row.Unknown) {
			t.Errorf("row %s is true and also impossible=%v unknown=%v", row.Pred, row.Impossible, row.Unknown)
		}
	}
	for _, spec := range cat {
		if !seen[string(spec.ID)] {
			t.Errorf("catalogue predicate %s has no row", spec.ID)
		}
	}
	for _, rv := range ex.Rules {
		if rv.Verdict != string(prog.VerdictShadowed) && rv.ShadowedBy != -1 {
			t.Errorf("rule %d verdict %q carries shadowed_by %d", rv.Rule, rv.Verdict, rv.ShadowedBy)
		}
	}
}
