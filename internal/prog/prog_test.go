package prog

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// The three worked programs from design §10.7, §10.8 and §10.9.

func scavenger() Program { // §10.7
	return Program{V: SchemaVersion, Name: "component scavenger", Rules: []Rule{
		{And(Pred(AtOwnBase), Pred(CarryingComponent)), []Action{Do(DepositComponentAtBase)}},
		{Pred(CarryingComponent), []Action{Do(MoveToOwnBase)}},
		{And(Pred(ComponentInReach), Pred(CarryingNothing)), []Action{Do(PickUpComponent)}},
		{And(Pred(RadarDetectsTarget), Pred(CarryingNothing)), []Action{Do(MoveToRadarTarget)}},
		{Pred(SeesObstacle), []Action{Do(TurnRandom)}},
		{Pred(CarryingNothing), []Action{Do(MoveForward)}},
	}}
}

func scout() Program { // §10.8
	return Program{V: SchemaVersion, Name: "memory-assisted scout", Rules: []Rule{
		{And(Pred(SeesComponent), Pred(CarryingComponent)), []Action{DoArg(SaveVisibleTarget, 1)}},
		{And(Pred(AtOwnBase), Pred(CarryingComponent)), []Action{Do(DepositComponentAtBase)}},
		{Pred(CarryingComponent), []Action{Do(MoveToOwnBase)}},
		{And(PredArg(AtPoint, 1), Pred(ComponentInReach), Pred(CarryingNothing)), []Action{Do(PickUpComponent)}},
		{And(PredArg(AtPoint, 1), Pred(CarryingNothing)), []Action{DoArg(ClearPoint, 1)}},
		{And(PredArg(PointIsSet, 1), Pred(CarryingNothing)), []Action{DoArg(MoveToPoint, 1)}},
	}}
}

func responder() Program { // §10.9
	return Program{V: SchemaVersion, Name: "defensive responder", Rules: []Rule{
		{And(PredArg(HealthBelow, 25), Pred(SeesEnemyRobot)), []Action{Do(MoveAwayFromTarget)}},
		{Pred(ReceivedComeHere), []Action{DoArg(SaveSignalPosition, 2)}},
		{And(Pred(SeesEnemyRobot), Pred(VisibleTargetInWpnRange)), []Action{Do(AttackVisibleTarget)}},
		{PredArg(AtPoint, 2), []Action{DoArg(ClearPoint, 2)}},
		{PredArg(PointIsSet, 2), []Action{DoArg(MoveToPoint, 2)}},
		{Pred(RadarDetectsTarget), []Action{Do(MoveToRadarTarget)}},
		{Pred(SeesObstacle), []Action{Do(TurnRandom)}},
	}}
}

func blueprint(extra ...sim.Variant) sim.Blueprint {
	b := sim.Blueprint{ID: "bp", Name: "test", Components: append([]sim.Variant{sim.Tracks, sim.MediumArmor}, extra...)}
	if err := b.Validate(); err != nil {
		panic(err) // a broken fixture, not a test failure mode
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	for _, p := range []Program{scavenger(), scout(), responder()} {
		data, err := p.Encode()
		if err != nil {
			t.Fatalf("%s: encode: %v", p.Name, err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%s: decode: %v", p.Name, err)
		}
		if !reflect.DeepEqual(got, p) {
			t.Errorf("%s: round trip changed the program\n got %+v\nwant %+v", p.Name, got, p)
		}
		again, err := got.Encode()
		if err != nil {
			t.Fatalf("%s: re-encode: %v", p.Name, err)
		}
		if !bytes.Equal(again, data) {
			t.Errorf("%s: re-encode differs\n got %s\nwant %s", p.Name, again, data)
		}
	}
}

func TestValidate(t *testing.T) {
	full := blueprint(sim.Manipulator, sim.PartsRadar, sim.Laser)

	tests := []struct {
		name     string
		prog     Program
		bp       sim.Blueprint
		errCodes []string
		warns    []string
		notes    []string
	}{
		{"scavenger is clean", scavenger(), blueprint(sim.Manipulator, sim.PartsRadar), nil, nil, nil},
		// Neither §10.8 nor §10.9 can act on a robot that has just been built,
		// but only §10.8 is stuck: §10.9 is waiting on the world (rc-tad.5).
		{"scout warns that it cannot start", scout(), blueprint(sim.Manipulator), nil, []string{"inert_start"}, nil},
		{"responder only notes that it reacts", responder(), blueprint(sim.PartsRadar, sim.Laser), nil, nil, []string{"reactive_start"}},
		{
			"radar action without radar",
			Program{Rules: []Rule{{Pred(CarryingNothing), []Action{Do(MoveToRadarTarget)}}}},
			blueprint(sim.Manipulator),
			[]string{"missing_component"}, nil, nil,
		},
		{
			"pickup without manipulator",
			Program{Rules: []Rule{{Pred(ComponentInReach), []Action{Do(PickUpComponent)}}}},
			blueprint(),
			[]string{"missing_component"}, nil, []string{"reactive_start"},
		},
		{
			"two primary actions",
			Program{Rules: []Rule{{Pred(SeesObstacle), []Action{Do(TurnLeft), Do(MoveForward)}}}},
			full,
			[]string{"multiple_primary"}, nil, []string{"reactive_start"},
		},
		{
			"side effect plus one primary is fine",
			Program{Rules: []Rule{{Pred(SeesComponent), []Action{DoArg(SaveVisibleTarget, 1), Do(MoveForward)}}}},
			full,
			nil, []string{"forward_only"}, []string{"reactive_start"},
		},
		{
			"forward-only movement warns",
			Program{Rules: []Rule{{Pred(CarryingNothing), []Action{Do(MoveForward)}}}},
			full,
			nil, []string{"forward_only"}, nil,
		},
		{
			"dominated rule warns",
			Program{Rules: []Rule{
				{Pred(CarryingComponent), []Action{Do(MoveToOwnBase)}},
				{And(Pred(CarryingComponent), Pred(AtOwnBase)), []Action{Do(DepositComponentAtBase)}},
			}},
			full,
			nil, []string{"unreachable_rule"}, nil,
		},
		{
			// A side-effect-only rule does not end the tick, so it cannot
			// pre-empt anything below it (design §10.8 rule 1).
			"broad side-effect rule does not dominate",
			Program{Rules: []Rule{
				{Pred(CarryingComponent), []Action{DoArg(SaveCurrentPosition, 1)}},
				{And(Pred(CarryingComponent), Pred(AtOwnBase)), []Action{Do(DepositComponentAtBase)}},
			}},
			full,
			nil, []string{"inert_start"}, nil,
		},
		{
			"radar predicate without radar cannot start either",
			Program{Rules: []Rule{{Pred(RadarDetectsTarget), []Action{Do(TurnRandom)}}}},
			blueprint(),
			nil, []string{"dead_predicate", "inert_start"}, nil,
		},
		{
			"unknown identifiers are errors",
			Program{Rules: []Rule{{Pred("nope"), []Action{Do("also_nope")}}}},
			full,
			[]string{"unknown_predicate", "unknown_action"}, []string{"inert_start"}, nil,
		},
		{"empty program warns", Program{}, full, nil, []string{"empty_program"}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Validate(tt.prog, tt.bp)
			assertCodes(t, "error", r.Errors, tt.errCodes)
			assertCodes(t, "warning", r.Warnings, tt.warns)
			assertCodes(t, "note", r.Notes, tt.notes)
			if r.OK() != (len(tt.errCodes) == 0) {
				t.Errorf("OK() = %v with errors %v", r.OK(), r.Errors)
			}
			for _, w := range r.Warnings {
				if w.Severity != SevWarning {
					t.Errorf("warning %+v has severity %q", w, w.Severity)
				}
			}
			for _, n := range r.Notes {
				if n.Severity != SevNote {
					t.Errorf("note %+v has severity %q", n, n.Severity)
				}
			}
		})
	}
}

// assertCodes checks that the wanted codes are present and, when nothing is
// wanted, that the list is empty.
func assertCodes(t *testing.T, what string, got []Issue, want []string) {
	t.Helper()
	if len(want) == 0 {
		if len(got) != 0 {
			t.Fatalf("unexpected %ss: %+v", what, got)
		}
		return
	}
	for _, code := range want {
		found := false
		for _, is := range got {
			if is.Code == code {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s %q, got %+v", what, code, got)
		}
	}
}

func TestValidateWarningsNeverBlockSave(t *testing.T) {
	// A program that is all warnings must still be saveable.
	p := Program{Rules: []Rule{
		{Pred(RadarDetectsTarget), []Action{Do(MoveForward)}},
		{Pred(RadarDetectsTarget), []Action{Do(MoveForward)}},
	}}
	r := Validate(p, blueprint())
	if len(r.Warnings) == 0 {
		t.Fatal("expected warnings")
	}
	if !r.OK() {
		t.Errorf("warnings blocked the save: %+v", r.Errors)
	}
}

func TestDecodeGarbage(t *testing.T) {
	deep := strings.Repeat(`{"op":"and","of":[`, 200) + `{"op":"pred","pred":"at_own_base"}` + strings.Repeat("]}", 200)

	tests := []struct{ name, in string }{
		{"empty", ""},
		{"truncated", `{"v":1,"rules":[{"when":`},
		{"not an object", `[1,2,3]`},
		{"null", `null`},
		{"wrong types", `{"v":"one","rules":5}`},
		{"future version", `{"v":99,"rules":[]}`},
		{"unknown predicate", `{"v":1,"rules":[{"when":{"op":"pred","pred":"launch_missiles"},"then":[{"do":"stop"}]}]}`},
		{"unknown action", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[{"do":"self_destruct"}]}]}`},
		{"unknown op", `{"v":1,"rules":[{"when":{"op":"xor","of":[]},"then":[{"do":"stop"}]}]}`},
		{"null condition", `{"v":1,"rules":[{"when":null,"then":[{"do":"stop"}]}]}`},
		{"null actions", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":null}]}`},
		{"null rule", `{"v":1,"rules":[null]}`},
		{"empty group", `{"v":1,"rules":[{"when":{"op":"and","of":[]},"then":[{"do":"stop"}]}]}`},
		{"point out of range", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_point","arg":9},"then":[{"do":"stop"}]}]}`},
		{"negative point", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_point","arg":-1},"then":[{"do":"stop"}]}]}`},
		{"percent out of range", `{"v":1,"rules":[{"when":{"op":"pred","pred":"health_below","arg":9000},"then":[{"do":"stop"}]}]}`},
		{"argument on argless action", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[{"do":"stop","arg":3}]}]}`},
		{"nested too deep", `{"v":1,"rules":[{"when":` + deep + `,"then":[{"do":"stop"}]}]}`},
		{"huge nesting", `{"v":1,"rules":[{"when":` + strings.Repeat(`{"op":"and","of":[`, 200000) + `]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decode([]byte(tt.in)); err == nil {
				t.Errorf("Decode(%.60q) succeeded, want error", tt.in)
			}
		})
	}
}

func TestDecodeAcceptsOmittedVersion(t *testing.T) {
	p, err := Decode([]byte(`{"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[{"do":"stop"}]}]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.V != SchemaVersion {
		t.Errorf("V = %d, want %d", p.V, SchemaVersion)
	}
}

func TestDecodeTooWideCondition(t *testing.T) {
	// Deep nesting is not the only way to be huge: one flat group can be wide.
	of := make([]Condition, MaxCondNodes+1)
	for i := range of {
		of[i] = Pred(AtOwnBase)
	}
	p := Program{V: SchemaVersion, Rules: []Rule{{And(of...), []Action{Do(Stop)}}}}
	data, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err == nil {
		t.Error("over-wide condition decoded, want error")
	}
	// One node under the cap (the AND itself counts) still decodes.
	ok := Program{V: SchemaVersion, Rules: []Rule{{And(of[:MaxCondNodes-1]...), []Action{Do(Stop)}}}}
	data, err = ok.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err != nil {
		t.Errorf("condition at the limit rejected: %v", err)
	}
}

func TestDecodeTooManyRules(t *testing.T) {
	r := Rule{Pred(AtOwnBase), []Action{Do(Stop)}}
	p := Program{V: SchemaVersion, Rules: make([]Rule, MaxRules+1)}
	for i := range p.Rules {
		p.Rules[i] = r
	}
	data, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(data); err == nil {
		t.Error("oversized program decoded, want error")
	}
}

// TestValidateNeverPanics runs validation over the garbage corpus and over
// programs built directly in Go, on a blueprint with nothing installed.
func TestValidateNeverPanics(t *testing.T) {
	bare := blueprint()
	progs := []Program{
		{}, {Rules: []Rule{{}}},
		{Rules: []Rule{{Condition{Op: OpAnd}, nil}}},
		{Rules: []Rule{{Condition{Op: OpPred, Pred: AtPoint, Arg: -5}, []Action{{Do: MoveToPoint, Arg: 99}}}}},
		scavenger(), scout(), responder(),
	}
	for i, p := range progs {
		r := Validate(p, bare)
		for _, is := range append(append([]Issue{}, r.Errors...), r.Warnings...) {
			if is.Code == "" || is.Message == "" {
				t.Errorf("program %d: issue with empty code/message: %+v", i, is)
			}
		}
	}
}

func FuzzDecode(f *testing.F) {
	for _, p := range []Program{scavenger(), scout(), responder()} {
		if data, err := p.Encode(); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`{"v":1,"rules":[{"when":{"op":"or","of":[{"op":"pred","pred":"at_own_base"}]},"then":[]}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := Decode(data)
		if err != nil {
			return
		}
		// Anything that decodes must validate and re-encode without panicking.
		Validate(p, blueprint(sim.Manipulator, sim.PartsRadar, sim.Laser))
		if _, err := p.Encode(); err != nil {
			t.Fatalf("re-encode of a decoded program failed: %v", err)
		}
	})
}

func TestLanguageIsSerializableAndConsistent(t *testing.T) {
	l := Language()
	if l.V != SchemaVersion {
		t.Errorf("catalogue version %d, want %d", l.V, SchemaVersion)
	}
	seenP := map[PredicateID]bool{}
	for _, s := range l.Predicates {
		if seenP[s.ID] {
			t.Errorf("duplicate predicate %q", s.ID)
		}
		seenP[s.ID] = true
		// Desc is checked here so a predicate added later cannot ship
		// undocumented: the editor's inline help is served from this row.
		if s.Group == "" || s.Label == "" || s.Arg == "" || s.Desc == "" {
			t.Errorf("predicate %q has an incomplete spec: %+v", s.ID, s)
		}
	}
	seenA := map[ActionID]bool{}
	sideEffects := 0
	for _, s := range l.Actions {
		if seenA[s.ID] {
			t.Errorf("duplicate action %q", s.ID)
		}
		seenA[s.ID] = true
		if s.Group == "" || s.Label == "" || s.Arg == "" || s.Desc == "" {
			t.Errorf("action %q has an incomplete spec: %+v", s.ID, s)
		}
		if !s.Primary {
			sideEffects++
			if s.Group != GroupMemory && s.Group != GroupCommunication {
				t.Errorf("action %q is a side effect but not a memory/communication action", s.ID)
			}
		}
	}
	// The locked rule action model: exactly the memory and communication rows.
	if sideEffects != 7 {
		t.Errorf("%d side-effect actions, want 7 (5 memory + 2 broadcast)", sideEffects)
	}

	data, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("marshal catalogue: %v", err)
	}
	if !bytes.Contains(data, []byte(`"needs":["radar"]`)) {
		t.Errorf("component requirements are not serialized by name: %s", data)
	}
}

// TestPredicateWorldColumn pins the world-observable / self-state split by
// hand. World is a bool, so a row added later gets "self-state" by default and
// nothing else would notice — but the default is the answer that produces a
// warning, and a false "this program cannot act" is exactly what rc-tad.5 is
// about. Adding a predicate means deciding here.
func TestPredicateWorldColumn(t *testing.T) {
	world := map[PredicateID]bool{
		SeesComponent: true, ComponentInReach: true, SeesEnemyRobot: true,
		SeesObstacle: true, VisibleTargetInWpnRange: true, EnemyVisible: true,
		RadarDetectsTarget: true, DetectedTargetInWpnRange: true,
		ReceivedComeHere: true, ReceivedAvoidHere: true,
		// path_blocked and the target_* pair are deliberately absent: sim only
		// raises them after a move the robot itself attempted, so a robot that
		// matches no rule can never see one.
	}
	for _, s := range Language().Predicates {
		if s.World != world[s.ID] {
			t.Errorf("predicate %q: World = %v, want %v", s.ID, s.World, world[s.ID])
		}
	}
}

// TestWarnInertStart pins the three worked examples against each other, which
// is rc-tad.5 in one table: §10.7 acts on its first tick and says nothing,
// §10.9 idles until the world gives it something and gets a neutral note, and
// only §10.8 — blocked by a memory point no rule ever sets — is a warning.
//
// want is the code expected at program level: "" for neither.
func TestWarnInertStart(t *testing.T) {
	bp := blueprint(sim.Manipulator, sim.PartsRadar, sim.Laser)
	tests := []struct {
		name string
		prog Program
		want string
	}{
		{"§10.7 scavenger acts from a clean start", scavenger(), ""},
		{"§10.8 scout is stuck on a point nothing sets", scout(), "inert_start"},
		{"§10.9 responder only reacts", responder(), "reactive_start"},
		{"empty program says empty_program instead", Program{}, ""},
		{"a rule on carrying_nothing always starts", Program{Rules: []Rule{
			{Pred(CarryingNothing), []Action{Do(TurnRandom)}},
		}}, ""},
		{"at_own_base matches: robots are built at their base", Program{Rules: []Rule{
			{Pred(AtOwnBase), []Action{Do(TurnRandom)}},
		}}, ""},
		{"an OR reaching one startable branch is enough", Program{Rules: []Rule{
			{Or(Pred(SeesEnemyRobot), PredArg(PointIsEmpty, 1)), []Action{Do(TurnRandom)}},
		}}, ""},
		{"a side-effect-only rule still counts as a match", Program{Rules: []Rule{
			{Pred(CarryingNothing), []Action{DoArg(SaveCurrentPosition, 1)}},
		}}, ""},
		{"waiting on a signal is reactive, not stuck", Program{Rules: []Rule{
			{Pred(ReceivedComeHere), []Action{Do(MoveToOwnBase)}},
		}}, "reactive_start"},
		{"waiting on a point nothing writes is stuck", Program{Rules: []Rule{
			{PredArg(PointIsSet, 1), []Action{DoArg(MoveToPoint, 1)}},
		}}, "inert_start"},
		// A robot that never matches a rule never moves, so nothing can ever
		// block its path: reachability is not a stimulus the world supplies.
		{"waiting on path_blocked is stuck, not reactive", Program{Rules: []Rule{
			{Pred(PathBlocked), []Action{Do(TurnRandom)}},
		}}, "inert_start"},
		{"a world predicate ANDed with unset self-state is still stuck", Program{Rules: []Rule{
			{And(Pred(SeesEnemyRobot), Pred(CarryingComponent)), []Action{Do(AttackVisibleTarget)}},
		}}, "inert_start"},
		{"a world predicate ORed with unset self-state is reactive", Program{Rules: []Rule{
			{Or(Pred(SeesEnemyRobot), Pred(CarryingComponent)), []Action{Do(AttackVisibleTarget)}},
		}}, "reactive_start"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Validate(tt.prog, bp)
			got := ""
			for _, is := range append(append([]Issue{}, r.Warnings...), r.Notes...) {
				if is.Code != "inert_start" && is.Code != "reactive_start" {
					continue
				}
				if got != "" {
					t.Errorf("both %q and %q were reported", got, is.Code)
				}
				got = is.Code
				if is.Rule != -1 {
					t.Errorf("%s must be program level, got %+v", is.Code, is)
				}
			}
			if got != tt.want {
				t.Errorf("clean-start finding = %q, want %q (warnings %+v, notes %+v)",
					got, tt.want, r.Warnings, r.Notes)
			}
			// Only the stuck case earns a badge, and neither ever blocks a save.
			if n := len(r.Notes); tt.want == "reactive_start" && (n != 1 || len(r.Warnings) != 0) {
				t.Errorf("a reactive program must carry a note and no warning: %+v / %+v", r.Warnings, r.Notes)
			}
			if !r.OK() {
				t.Errorf("a clean-start finding blocked the save: %+v", r.Errors)
			}
		})
	}
}

func TestPrimaryTagMatchesLockedModel(t *testing.T) {
	for _, id := range []ActionID{MoveForward, TurnLeft, MoveToRadarTarget, PickUpComponent, AttackVisibleTarget} {
		if !Do(id).Primary() {
			t.Errorf("%s should be primary", id)
		}
	}
	for _, id := range []ActionID{SaveCurrentPosition, ClearPoint, BroadcastComeHere, BroadcastAvoidHere} {
		if Do(id).Primary() {
			t.Errorf("%s should be a side effect", id)
		}
	}
	if Do("nonsense").Primary() {
		t.Error("an unknown action must not count as primary")
	}
}
