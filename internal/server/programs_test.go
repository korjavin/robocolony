package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

func newLibrary(t *testing.T) (*Library, *db.DB) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewLibrary(database), database
}

func newUser(t *testing.T, database *db.DB, name string) db.User {
	t.Helper()
	u, err := database.UpsertUser(t.Context(), "sub-"+name, name+"@example.com", name)
	if err != nil {
		t.Fatalf("UpsertUser(%s) = %v", name, err)
	}
	return u
}

// wantLibStatus asserts an error carries a particular HTTP status.
func wantLibStatus(t *testing.T, err error, code int) statusError {
	t.Helper()
	var se statusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a status error, want %d", err, code)
	}
	if se.code != code {
		t.Fatalf("error %q has status %d, want %d", se.msg, se.code, code)
	}
	return se
}

func encode(t *testing.T, p prog.Program) json.RawMessage {
	t.Helper()
	raw, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	return raw
}

// TestProgramOwnership is the bead's headline security case: an id is not a
// capability. B knows A's program id and still cannot read, overwrite or delete
// it — every query is scoped by (user_id, id).
func TestProgramOwnership(t *testing.T) {
	lib, database := newLibrary(t)
	alice := newUser(t, database, "alice")
	bob := newUser(t, database, "bob")

	raw := encode(t, lobby.DefaultProgram())
	saved, err := lib.SaveProgram(t.Context(), alice.ID, 0, "alice's scavenger", raw, 0)
	if err != nil {
		t.Fatalf("SaveProgram() = %v", err)
	}

	if _, err := lib.GetProgram(t.Context(), bob.ID, saved.ID); err == nil {
		t.Error("bob read alice's program")
	} else {
		wantLibStatus(t, err, http.StatusNotFound)
	}

	if _, err := lib.SaveProgram(t.Context(), bob.ID, saved.ID, "bob's now", raw, 0); err == nil {
		t.Error("bob overwrote alice's program")
	} else {
		wantLibStatus(t, err, http.StatusNotFound)
	}

	if err := lib.DeleteProgram(t.Context(), bob.ID, saved.ID); err == nil {
		t.Error("bob deleted alice's program")
	} else {
		wantLibStatus(t, err, http.StatusNotFound)
	}

	list, err := lib.ListPrograms(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("bob's library has %d programs, want 0", len(list))
	}

	// And after all that, alice's program is untouched.
	got, err := lib.GetProgram(t.Context(), alice.ID, saved.ID)
	if err != nil {
		t.Fatalf("GetProgram() = %v", err)
	}
	if got.Name != "alice's scavenger" {
		t.Errorf("program name = %q, want %q", got.Name, "alice's scavenger")
	}
}

// TestBlueprintOwnership: a blueprint id from another library must not become a
// validation context, or a program could be checked against hardware its owner
// never designed.
func TestBlueprintOwnership(t *testing.T) {
	lib, database := newLibrary(t)
	alice := newUser(t, database, "alice")
	bob := newUser(t, database, "bob")

	bp, err := lib.CreateBlueprint(t.Context(), alice.ID, "gunner",
		[]int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Laser)})
	if err != nil {
		t.Fatalf("CreateBlueprint() = %v", err)
	}
	if _, err := lib.ValidateProgram(t.Context(), bob.ID, encode(t, lobby.DefaultProgram()), bp.ID); err == nil {
		t.Error("bob validated against alice's blueprint")
	} else {
		wantLibStatus(t, err, http.StatusNotFound)
	}

	list, err := lib.ListBlueprints(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("ListBlueprints() = %v", err)
	}
	for _, b := range list {
		if b.ID == bp.ID {
			t.Errorf("alice's blueprint %d showed up in bob's library", bp.ID)
		}
	}
}

// TestValidateEndpoint: errors refuse the save, warnings do not (design §10.10).
func TestValidateEndpoint(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	// A blueprint with no radar, so the design §10.7 program's radar rule is an
	// error rather than a matter of opinion.
	blind, err := lib.CreateBlueprint(t.Context(), user.ID, "blind",
		[]int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Manipulator)})
	if err != nil {
		t.Fatalf("CreateBlueprint() = %v", err)
	}

	raw := encode(t, lobby.DefaultProgram())
	res, err := lib.ValidateProgram(t.Context(), user.ID, raw, blind.ID)
	if err != nil {
		t.Fatalf("ValidateProgram() = %v", err)
	}
	if res.OK() {
		t.Fatal("a radar program validated clean against a blueprint with no radar")
	}
	if _, err := lib.SaveProgram(t.Context(), user.ID, 0, "doomed", raw, blind.ID); err == nil {
		t.Fatal("an invalid program was saved")
	} else {
		se := wantLibStatus(t, err, http.StatusUnprocessableEntity)
		if se.result == nil || len(se.result.Errors) == 0 {
			t.Error("the refusal carried no errors")
		}
	}
	if list, err := lib.ListPrograms(t.Context(), user.ID); err != nil || len(list) != 0 {
		t.Fatalf("ListPrograms() = %v, %v; want an empty library", list, err)
	}

	// Warning-only: an empty program is legal, just idle. It must save, and it
	// must come back with an empty rule list rather than a null one — every
	// reader, the editor included, would otherwise need its own guard.
	empty := encode(t, prog.Program{V: prog.SchemaVersion, Rules: nil})
	res, err = lib.ValidateProgram(t.Context(), user.ID, empty, blind.ID)
	if err != nil {
		t.Fatalf("ValidateProgram() = %v", err)
	}
	if !res.OK() || len(res.Warnings) == 0 {
		t.Fatalf("empty program: errors %v, warnings %v; want warnings only", res.Errors, res.Warnings)
	}
	saved, err := lib.SaveProgram(t.Context(), user.ID, 0, "idle", empty, blind.ID)
	if err != nil {
		t.Fatalf("a warning-only program was refused: %v", err)
	}
	var stored struct {
		Rules []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(saved.Program, &stored); err != nil {
		t.Fatalf("stored program does not parse: %v", err)
	}
	if stored.Rules == nil {
		t.Errorf("stored program is %s, want an empty rule list, not null", saved.Program)
	}
}

// TestMalformedProgramIsRefused: the parser never panics on user input, and a
// program that cannot be represented at all still comes back as findings the
// editor can render.
func TestMalformedProgramIsRefused(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{"not json", `{`},
		{"null", `null`},
		{"empty", ``},
		{"future version", `{"v":99,"rules":[]}`},
		{"unknown predicate", `{"v":1,"rules":[{"when":{"op":"pred","pred":"nope"},"then":[{"do":"stop"}]}]}`},
		{"unknown action", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[{"do":"nuke"}]}]}`},
		{"empty group", `{"v":1,"rules":[{"when":{"op":"and","of":[]},"then":[{"do":"stop"}]}]}`},
		{"no action", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_own_base"},"then":[]}]}`},
		{"bad argument", `{"v":1,"rules":[{"when":{"op":"pred","pred":"at_point","arg":99},"then":[{"do":"stop"}]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := lib.ValidateProgram(t.Context(), user.ID, json.RawMessage(tt.raw), 0)
			if err != nil {
				t.Fatalf("ValidateProgram() = %v", err)
			}
			if res.OK() {
				t.Fatalf("%s validated clean", tt.name)
			}
			if _, err := lib.SaveProgram(t.Context(), user.ID, 0, "x", json.RawMessage(tt.raw), 0); err == nil {
				t.Fatalf("%s was saved", tt.name)
			}
		})
	}
}

// TestRoundTrip is the manual acceptance case, automated: save the design §10.7
// scavenger and read it back rule for rule.
func TestRoundTrip(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	want := lobby.DefaultProgram()
	saved, err := lib.SaveProgram(t.Context(), user.ID, 0, "scavenger", encode(t, want), 0)
	if err != nil {
		t.Fatalf("SaveProgram() = %v", err)
	}
	got, err := lib.GetProgram(t.Context(), user.ID, saved.ID)
	if err != nil {
		t.Fatalf("GetProgram() = %v", err)
	}
	p, err := prog.Decode(got.Program)
	if err != nil {
		t.Fatalf("the stored program does not decode: %v", err)
	}
	if len(p.Rules) != len(want.Rules) {
		t.Fatalf("read back %d rules, want %d", len(p.Rules), len(want.Rules))
	}
	for i := range want.Rules {
		a, _ := json.Marshal(want.Rules[i])
		b, _ := json.Marshal(p.Rules[i])
		if string(a) != string(b) {
			t.Errorf("rule %d = %s, want %s", i+1, b, a)
		}
	}
	if p.Name != "scavenger" {
		t.Errorf("stored program name = %q, want the library name", p.Name)
	}

	// A second program of the same name is a conflict, not a silent overwrite.
	if _, err := lib.SaveProgram(t.Context(), user.ID, 0, "scavenger", encode(t, want), 0); err == nil {
		t.Error("a duplicate name was accepted")
	} else {
		wantLibStatus(t, err, http.StatusConflict)
	}
}

// TestStarterTemplatesValidate: every template ships paired with a blueprint it
// runs clean on. The §10.7 scavenger needs a parts radar and the §10.9
// responder needs a weapon, so a mismatch here would greet a new player with a
// red error on a program they did not write.
func TestStarterTemplatesValidate(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	blueprints, err := lib.ListBlueprints(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListBlueprints() = %v", err)
	}
	byName := map[string]int64{}
	for _, b := range blueprints {
		byName[b.Name] = b.ID
	}
	if len(byName) != len(starterBlueprints()) {
		t.Fatalf("a fresh library has %d blueprints, want the %d starters", len(byName), len(starterBlueprints()))
	}

	for _, tmpl := range LanguageDoc().Templates {
		t.Run(tmpl.Name, func(t *testing.T) {
			id, ok := byName[tmpl.Blueprint]
			if !ok {
				t.Fatalf("template names blueprint %q, which is not a starter", tmpl.Blueprint)
			}
			res, err := lib.ValidateProgram(t.Context(), user.ID, tmpl.Program, id)
			if err != nil {
				t.Fatalf("ValidateProgram() = %v", err)
			}
			if !res.OK() {
				t.Errorf("template %q has errors on blueprint %q: %v", tmpl.Name, tmpl.Blueprint, res.Errors)
			}
			if _, err := lib.SaveProgram(t.Context(), user.ID, 0, tmpl.Name, tmpl.Program, id); err != nil {
				t.Errorf("template %q could not be saved: %v", tmpl.Name, err)
			}
		})
	}
}

// TestSeedingIsIdempotent: the starter blueprints are seeded on first read, so
// a second read must not double them.
func TestSeedingIsIdempotent(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	first, err := lib.ListBlueprints(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListBlueprints() = %v", err)
	}
	second, err := lib.ListBlueprints(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListBlueprints() = %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("second read has %d blueprints, first had %d", len(second), len(first))
	}
}

// TestBlueprintConstraints: design §6.3 is enforced on the server, whatever the
// editor sent.
func TestBlueprintConstraints(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	for _, tt := range []struct {
		name       string
		components []int
	}{
		{"no locomotion", []int{int(sim.MediumArmor)}},
		{"no armor", []int{int(sim.Tracks)}},
		{"two radars", []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.PartsRadar), int(sim.PartsRadar)}},
		{"three weapons", []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Laser), int(sim.Laser), int(sim.Cannon)}},
		{"unknown component", []int{int(sim.Tracks), int(sim.MediumArmor), 200}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := lib.CreateBlueprint(t.Context(), user.ID, tt.name, tt.components); err == nil {
				t.Fatalf("%s was accepted", tt.name)
			} else {
				wantLibStatus(t, err, http.StatusBadRequest)
			}
		})
	}
}
