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
	}{
		{"scavenger is clean", scavenger(), blueprint(sim.Manipulator, sim.PartsRadar), nil, nil},
		{"scout is clean", scout(), blueprint(sim.Manipulator), nil, nil},
		{"responder is clean", responder(), blueprint(sim.PartsRadar, sim.Laser), nil, nil},
		{
			"radar action without radar",
			Program{Rules: []Rule{{Pred(CarryingNothing), []Action{Do(MoveToRadarTarget)}}}},
			blueprint(sim.Manipulator),
			[]string{"missing_component"}, nil,
		},
		{
			"pickup without manipulator",
			Program{Rules: []Rule{{Pred(ComponentInReach), []Action{Do(PickUpComponent)}}}},
			blueprint(),
			[]string{"missing_component"}, nil,
		},
		{
			"two primary actions",
			Program{Rules: []Rule{{Pred(SeesObstacle), []Action{Do(TurnLeft), Do(MoveForward)}}}},
			full,
			[]string{"multiple_primary"}, nil,
		},
		{
			"side effect plus one primary is fine",
			Program{Rules: []Rule{{Pred(SeesComponent), []Action{DoArg(SaveVisibleTarget, 1), Do(MoveForward)}}}},
			full,
			nil, []string{"forward_only"},
		},
		{
			"forward-only movement warns",
			Program{Rules: []Rule{{Pred(CarryingNothing), []Action{Do(MoveForward)}}}},
			full,
			nil, []string{"forward_only"},
		},
		{
			"dominated rule warns",
			Program{Rules: []Rule{
				{Pred(CarryingComponent), []Action{Do(MoveToOwnBase)}},
				{And(Pred(CarryingComponent), Pred(AtOwnBase)), []Action{Do(DepositComponentAtBase)}},
			}},
			full,
			nil, []string{"unreachable_rule"},
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
			nil, nil,
		},
		{
			"radar predicate without radar only warns",
			Program{Rules: []Rule{{Pred(RadarDetectsTarget), []Action{Do(TurnRandom)}}}},
			blueprint(),
			nil, []string{"dead_predicate"},
		},
		{
			"unknown identifiers are errors",
			Program{Rules: []Rule{{Pred("nope"), []Action{Do("also_nope")}}}},
			full,
			[]string{"unknown_predicate", "unknown_action"}, nil,
		},
		{"empty program warns", Program{}, full, nil, []string{"empty_program"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Validate(tt.prog, tt.bp)
			assertCodes(t, "error", r.Errors, tt.errCodes)
			assertCodes(t, "warning", r.Warnings, tt.warns)
			if r.OK() != (len(tt.errCodes) == 0) {
				t.Errorf("OK() = %v with errors %v", r.OK(), r.Errors)
			}
			for _, w := range r.Warnings {
				if w.Severity != SevWarning {
					t.Errorf("warning %+v has severity %q", w, w.Severity)
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
		if s.Group == "" || s.Label == "" || s.Arg == "" {
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
		if s.Group == "" || s.Label == "" || s.Arg == "" {
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
