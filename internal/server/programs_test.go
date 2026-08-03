package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

	// Bob's library is his own seeded starters and nothing of alice's.
	list, err := lib.ListPrograms(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(list) != len(starterPrograms()) {
		t.Errorf("bob's library has %d programs, want the %d starters", len(list), len(starterPrograms()))
	}
	for _, p := range list {
		if p.ID == saved.ID {
			t.Errorf("alice's program %d is in bob's library", saved.ID)
		}
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
	// The refused save left nothing behind: the library is still just the rows
	// seeded on first read.
	if list, err := lib.ListPrograms(t.Context(), user.ID); err != nil || len(list) != len(starterPrograms()) {
		t.Fatalf("ListPrograms() = %d programs, %v; want the %d seeded starters",
			len(list), err, len(starterPrograms()))
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

// TestBlueprintPreviewAgreesWithSave: the editor draws its live §6.3 verdict
// from the preview endpoint and its save gate from CreateBlueprint. If those
// two ever disagree the editor either refuses a legal design or offers one the
// save will bounce, so pin them to the same answer over the same cases.
func TestBlueprintPreviewAgreesWithSave(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	twoWeapons := []int{int(sim.Tracks), int(sim.MediumArmor), int(sim.Laser), int(sim.AutoGun)}
	for _, tt := range []struct {
		name       string
		components []int
	}{
		{"two weapons", twoWeapons},
		{"three weapons", append(slices.Clone(twoWeapons), int(sim.Cannon))},
		{"no locomotion", []int{int(sim.MediumArmor)}},
		{"unknown component", []int{int(sim.Tracks), int(sim.MediumArmor), 200}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stats, err := lib.PreviewBlueprint(tt.components)
			if err != nil {
				t.Fatalf("PreviewBlueprint() = %v", err)
			}
			_, saveErr := lib.CreateBlueprint(t.Context(), user.ID, tt.name, tt.components)
			if stats.OK != (saveErr == nil) {
				t.Fatalf("preview ok = %v (%q) but save error = %v", stats.OK, stats.Error, saveErr)
			}
			if !stats.OK {
				return
			}
			bp := sim.Blueprint{Components: toVariants(tt.components)}
			if stats.Mass != bp.Mass() || stats.Value != bp.Value() ||
				stats.Health != sim.StartingHealth(bp) || stats.Speed != sim.EffectiveSpeed(bp) {
				t.Fatalf("preview %+v does not match sim", stats)
			}
		})
	}
}

// TestBlueprintPreviewRefusesOversized: the preview runs no simulation, but it
// is still untrusted input and must not take an unbounded parts list.
func TestBlueprintPreviewRefusesOversized(t *testing.T) {
	lib, _ := newLibrary(t)
	if _, err := lib.PreviewBlueprint(make([]int, maxComponents+1)); err == nil {
		t.Fatal("an oversized parts list was accepted")
	} else {
		wantLibStatus(t, err, http.StatusBadRequest)
	}
}

// TestProgramLibraryIsSeeded is rc-tad.8: a player who has never opened the
// editor recalls a robot and finds an empty install picker. The library seeds
// the design's three worked programs on first read, exactly as ListBlueprints
// seeds the starter blueprints.
func TestProgramLibraryIsSeeded(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	first, err := lib.ListPrograms(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(first) != len(starterPrograms()) {
		t.Fatalf("a fresh library has %d programs, want the %d starters", len(first), len(starterPrograms()))
	}

	// The seeded set is the template set: a template the library does not hold
	// is a program the player can see in the editor and cannot install.
	seeded := map[string]json.RawMessage{}
	for _, p := range first {
		seeded[p.Name] = p.Program
	}
	for _, tmpl := range LanguageDoc().Templates {
		raw, ok := seeded[tmpl.Name]
		if !ok {
			t.Fatalf("template %q is not in the seeded library", tmpl.Name)
		}
		if string(raw) != string(tmpl.Program) {
			t.Errorf("seeded %q is not the template:\n got %s\nwant %s", tmpl.Name, raw, tmpl.Program)
		}
	}

	// Idempotent: seeding on read must not double the rows on the second read.
	second, err := lib.ListPrograms(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(second) != len(first) {
		t.Fatalf("second read has %d programs, first had %d", len(second), len(first))
	}

	// Every seeded row is one InstallProgram can resolve and decode, and at
	// least one runs clean on the blueprint a *starting* robot carries — that is
	// the recall-and-install flow the bug dead-ended. The §10.9 responder needs
	// a weapon the starter scavenger does not have, which is the component-aware
	// check doing its job, not a seeding failure.
	clean := 0
	for _, p := range second {
		decoded, err := prog.Decode(p.Program)
		if err != nil {
			t.Fatalf("seeded program %q does not decode: %v", p.Name, err)
		}
		if prog.Validate(decoded, lobby.DefaultBlueprint()).OK() {
			clean++
		}
	}
	if clean == 0 {
		t.Fatal("no seeded program installs on a starting robot's blueprint")
	}

	// A warning never blocks a save, and it must not block seeding either: the
	// §10.8 scout is seeded despite the finding it carries on this blueprint.
	scout, err := prog.Decode(seeded["memory-assisted scout"])
	if err != nil {
		t.Fatalf("seeded scout does not decode: %v", err)
	}
	if res := prog.Validate(scout, lobby.DefaultBlueprint()); len(res.Warnings)+len(res.Notes) == 0 {
		t.Error("the scout carries neither a warning nor a note; this assertion no longer tests anything")
	}

	// A deleted program stays deleted: only a wholly empty library re-seeds.
	if err := lib.DeleteProgram(t.Context(), user.ID, first[0].ID); err != nil {
		t.Fatalf("DeleteProgram() = %v", err)
	}
	third, err := lib.ListPrograms(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(third) != len(first)-1 {
		t.Fatalf("after deleting one, the library has %d programs, want %d", len(third), len(first)-1)
	}
}

// TestSeededLibraryIsPerUser: seeded rows are owned like every other row, and
// one player's library is never another's.
func TestSeededLibraryIsPerUser(t *testing.T) {
	lib, database := newLibrary(t)
	alice := newUser(t, database, "alice")
	bob := newUser(t, database, "bob")

	mine, err := lib.ListPrograms(t.Context(), alice.ID)
	if err != nil {
		t.Fatalf("ListPrograms(alice) = %v", err)
	}
	theirs, err := lib.ListPrograms(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("ListPrograms(bob) = %v", err)
	}
	if len(mine) != len(theirs) || len(mine) == 0 {
		t.Fatalf("alice has %d programs, bob %d", len(mine), len(theirs))
	}
	for _, p := range mine {
		if _, err := lib.GetProgram(t.Context(), bob.ID, p.ID); err == nil {
			t.Errorf("bob read alice's seeded program %d", p.ID)
		}
	}
}

// TestImportedProgramIsValidatedServerSide is rc-tad.10's landmine: an imported
// document is untrusted input from an arbitrary source, and the editor's own
// check is not a gate. Every one of these reaches the database only through
// SaveProgram, which refuses it.
func TestImportedProgramIsValidatedServerSide(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "player")

	for _, tt := range []struct {
		name, doc, want string
	}{
		{"unknown version", `{"v":99,"rules":[]}`, "unsupported schema version"},
		{"not json", `{"v":1,"rules":`, "not valid JSON"},
		{"null", `null`, "no program"},
		{"unknown predicate", `{"v":1,"rules":[{"when":{"op":"pred","pred":"sees_ghost"},"then":[{"do":"turn_random"}]}]}`, "unknown predicate"},
		{"unknown action", `{"v":1,"rules":[{"when":{"op":"pred","pred":"sees_obstacle"},"then":[{"do":"self_destruct"}]}]}`, "unknown action"},
		{"hardware it does not have", `{"v":1,"rules":[{"when":{"op":"pred","pred":"sees_enemy_robot"},"then":[{"do":"attack_visible_target"}]}]}`, "the blueprint has none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := lib.SaveProgram(t.Context(), user.ID, 0, tt.name, json.RawMessage(tt.doc), 0)
			if err == nil {
				t.Fatal("the document was saved")
			}
			se := wantLibStatus(t, err, http.StatusUnprocessableEntity)
			if se.result == nil || len(se.result.Errors) == 0 {
				t.Fatalf("refusal carries no findings: %+v", se)
			}
			var joined string
			for _, is := range se.result.Errors {
				joined += is.Message + "\n"
			}
			if !strings.Contains(joined, tt.want) {
				t.Errorf("refusal says %q, want it to mention %q", joined, tt.want)
			}
		})
	}

	// Nothing was half-saved along the way: the library is only the seeded set.
	list, err := lib.ListPrograms(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	if len(list) != len(starterPrograms()) {
		t.Fatalf("library has %d programs after the refused imports, want the %d starters",
			len(list), len(starterPrograms()))
	}
}

// TestExportedProgramRoundTrips is rc-tad.10's acceptance: the exported file is
// the stored wire format, and importing it into another account gives back the
// same program rule for rule.
//
// The import is checked against a blueprint the *importing* player picked, not
// one named in the file: the §10.9 responder needs a weapon, and on the wrong
// blueprint this same call is a refusal with the missing component named. That
// is the decision the export carries no blueprint hint — see web/js/editor.js.
func TestExportedProgramRoundTrips(t *testing.T) {
	lib, database := newLibrary(t)
	alice := newUser(t, database, "alice")
	bob := newUser(t, database, "bob")

	mine, err := lib.ListPrograms(t.Context(), alice.ID)
	if err != nil {
		t.Fatalf("ListPrograms() = %v", err)
	}
	blueprints, err := lib.ListBlueprints(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("ListBlueprints() = %v", err)
	}
	bpID := map[string]int64{}
	for _, b := range blueprints {
		bpID[b.Name] = b.ID
	}
	pairedWith := map[string]int64{}
	for _, s := range starterPrograms() {
		pairedWith[s.name] = bpID[s.blueprint]
	}
	for _, p := range mine {
		// What the editor writes into the file is exactly ProgramView.Program.
		imported, err := lib.SaveProgram(t.Context(), bob.ID, 0, p.Name+" (imported)", p.Program, pairedWith[p.Name])
		if err != nil {
			t.Fatalf("importing %q = %v", p.Name, err)
		}
		want, err := prog.Decode(p.Program)
		if err != nil {
			t.Fatalf("Decode(%q) = %v", p.Name, err)
		}
		got, err := prog.Decode(imported.Program)
		if err != nil {
			t.Fatalf("Decode(imported %q) = %v", p.Name, err)
		}
		want.Name = got.Name // the library name is the player's, not the file's
		if !reflect.DeepEqual(got, want) {
			t.Errorf("imported %q is not the exported program:\n got %+v\nwant %+v", p.Name, got, want)
		}
	}
}
