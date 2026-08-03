package lobby

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// saveDesign puts a blueprint and a program in a user's library the way
// internal/server's library does: the parts list under a "components" key, the
// program as prog's own encoding.
func saveDesign(t *testing.T, database *db.DB, userID int64, name string, comps []sim.Variant, p prog.Program) (db.Blueprint, db.Program) {
	t.Helper()
	nums := make([]int, 0, len(comps))
	for _, c := range comps {
		nums = append(nums, int(c))
	}
	raw, err := json.Marshal(struct {
		Components []int `json:"components"`
	}{nums})
	if err != nil {
		t.Fatalf("marshal blueprint = %v", err)
	}
	bp, err := database.CreateBlueprint(t.Context(), userID, name, string(raw))
	if err != nil {
		t.Fatalf("CreateBlueprint() = %v", err)
	}
	encoded, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	row, err := database.CreateProgram(t.Context(), userID, name, string(encoded))
	if err != nil {
		t.Fatalf("CreateProgram() = %v", err)
	}
	return bp, row
}

// gunner is a legal armed body: heavier and dearer than the starter scavenger,
// which is what makes it the budget's test case.
var gunner = []sim.Variant{sim.Tracks, sim.HeavyArmor, sim.Laser, sim.EnemyRadar}

// TestLoadoutReachesTheArena is the bead: what a player designs is what their
// colony is built from, instead of the built-in kit everyone used to get.
func TestLoadoutReachesTheArena(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")

	bp, p := saveDesign(t, database, owner.ID, "hunter", gunner, hunterProgram())
	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: p.ID}}); err != nil {
		t.Fatalf("SetLoadout() = %v", err)
	}

	info, err := svc.Start(ctx, view.ID, owner.ID)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	m, ok := svc.Registry().Get(info.ID)
	if !ok {
		t.Fatal("the started match is not in the registry")
	}
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		if len(w.Bases[0].Blueprints) != 1 || w.Bases[0].Blueprints[0].Name != "hunter" {
			t.Errorf("base approved %+v, want just the player's hunter", w.Bases[0].Blueprints)
		}
		robots := 0
		for _, r := range w.Robots {
			if r.Colony != w.Bases[0].Colony {
				continue
			}
			robots++
			if r.Blueprint.Name != "hunter" {
				t.Errorf("robot %d is a %q, want the player's hunter", r.ID, r.Blueprint.Name)
			}
			if !r.Blueprint.Has(sim.KindWeapon) {
				t.Errorf("robot %d has no weapon, so it was not built to the chosen design", r.ID)
			}
			// A robot whose program was never installed gets no controller
			// back and idles for the whole match.
			if rt.Control(r) == nil {
				t.Errorf("robot %d runs %q, which is not installed", r.ID, r.ProgramID)
			}
		}
		if robots == 0 {
			t.Error("the colony started with no robots")
		}
	})
}

// TestLoadoutSurvivesRestart is the replay landmine: a restarted match must
// come back with the robots it actually started with, not the built-in kit and
// not whatever the player's library says now.
func TestLoadoutSurvivesRestart(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")

	bp, p := saveDesign(t, database, owner.ID, "hunter", gunner, hunterProgram())
	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: p.ID}}); err != nil {
		t.Fatalf("SetLoadout() = %v", err)
	}
	info, err := svc.Start(ctx, view.ID, owner.ID)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	m, _ := svc.Registry().Get(info.ID)
	for range 20 {
		m.step()
	}
	svc.save(m)
	var want uint64
	m.Read(func(w *sim.World, _ *prog.Runtime) { want = w.StateHash() })

	// The library moves on under the running match: a replay that resolved ids
	// would rebuild the colony from these rules instead of the ones it started
	// with.
	if _, err := database.UpdateProgram(ctx, owner.ID, p.ID, "hunter", string(mustEncode(t, foragerProgram()))); err != nil {
		t.Fatalf("UpdateProgram() = %v", err)
	}

	// A fresh process over the same database.
	restored := New(database)
	t.Cleanup(func() { _ = restored.Shutdown(t.Context()) })
	if err := restored.Restore(ctx); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	rm, ok := restored.Registry().Get(info.ID)
	if !ok {
		t.Fatal("the match was not restored")
	}
	rm.Read(func(w *sim.World, _ *prog.Runtime) {
		if got := w.StateHash(); got != want {
			t.Errorf("restored state hash %d, want %d: the replay rebuilt a different world", got, want)
		}
		for _, r := range w.Robots {
			if r.Colony == w.Bases[0].Colony && r.Blueprint.Name != "hunter" {
				t.Errorf("restored robot %d is a %q, want the player's hunter", r.ID, r.Blueprint.Name)
			}
		}
	})
}

func mustEncode(t *testing.T, p prog.Program) []byte {
	t.Helper()
	raw, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	return raw
}

// TestLoadoutOwnership: a member may only approve their own designs, and the
// refusal is the same "not found" a wrong id gets.
func TestLoadoutOwnership(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")
	other := newUser(t, database, "bob")

	mine, myProg := saveDesign(t, database, owner.ID, "mine", gunner, hunterProgram())
	theirs, theirProg := saveDesign(t, database, other.ID, "theirs", gunner, hunterProgram())

	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	_, err = svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: theirs.ID, ProgramID: myProg.ID}})
	wantStatus(t, err, http.StatusNotFound)

	_, err = svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: mine.ID, ProgramID: theirProg.ID}})
	wantStatus(t, err, http.StatusNotFound)

	// And a stranger cannot set a loadout on a lobby they are not seated in.
	_, err = svc.SetLoadout(ctx, view.ID, other.ID, []Choice{{BlueprintID: theirs.ID, ProgramID: theirProg.ID}})
	wantStatus(t, err, http.StatusConflict)
}

// TestLoadoutIsPrivateToItsPlayer: a lobby view carries your own loadout and
// nobody else's, so an opponent cannot counter-pick what you brought.
func TestLoadoutIsPrivateToItsPlayer(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")
	guest := newUser(t, database, "bob")

	bp, p := saveDesign(t, database, owner.ID, "hunter", gunner, hunterProgram())
	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Join(ctx, view.ID, guest.ID); err != nil {
		t.Fatalf("Join() = %v", err)
	}
	got, err := svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: p.ID}})
	if err != nil {
		t.Fatalf("SetLoadout() = %v", err)
	}
	for _, m := range got.forUser(owner.ID).Members {
		if m.UserID == owner.ID && len(m.Loadout) == 0 {
			t.Error("the owner cannot see their own loadout")
		}
	}
	for _, m := range got.forUser(guest.ID).Members {
		if m.UserID != guest.ID && len(m.Loadout) != 0 {
			t.Errorf("member %d's loadout is visible to another player: %s", m.UserID, m.Loadout)
		}
	}
	// The redaction must not have scribbled on the caller's own view.
	for _, m := range got.Members {
		if m.UserID == owner.ID && len(m.Loadout) == 0 {
			t.Error("forUser() cleared the loadout in the view it was called on")
		}
	}
}

// TestLoadoutRefusesUninstallablePairing: a program that cannot run on the body
// it is paired with is an error, and errors are the only findings that block.
func TestLoadoutRefusesUninstallablePairing(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")

	// The §10.7 scavenger needs a manipulator and a parts radar; a bare chassis
	// has neither, so pick_up_component and the radar rules are hardware errors.
	bare := []sim.Variant{sim.Tracks, sim.LightArmor}
	bp, p := saveDesign(t, database, owner.ID, "bare", bare, DefaultProgram())

	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	_, err = svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: p.ID}})
	wantStatus(t, err, http.StatusUnprocessableEntity)

	// A program that only warns still goes through: the same bare chassis with a
	// program that never touches the hardware it lacks. prog.Validate warns
	// about it (its only movement is move_forward) and warnings never block.
	walk := prog.Program{V: prog.SchemaVersion, Name: "walk", Rules: []prog.Rule{
		{When: prog.Pred(prog.CarryingNothing), Then: []prog.Action{prog.Do(prog.MoveForward)}},
	}}
	if res := prog.Validate(walk, sim.Blueprint{Components: bare}); res.OK() && len(res.Warnings) == 0 {
		t.Fatal("this fixture is meant to produce warnings and no errors")
	}
	walkRow, err := database.CreateProgram(ctx, owner.ID, "walk", string(mustEncode(t, walk)))
	if err != nil {
		t.Fatalf("CreateProgram() = %v", err)
	}
	if _, err := svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: walkRow.ID}}); err != nil {
		t.Fatalf("SetLoadout() with a warning-only program = %v, want it accepted", err)
	}
}

// TestLoadoutGateClosesAtStart is design §2.1 step 7 for the loadout: once the
// match is running nothing about the colony may change, and the gate is the
// same SQL one join, leave and SetAI use.
func TestLoadoutGateClosesAtStart(t *testing.T) {
	svc, database := newService(t)
	ctx := t.Context()
	owner := newUser(t, database, "ada")
	bp, p := saveDesign(t, database, owner.ID, "hunter", gunner, hunterProgram())

	view, err := svc.Create(ctx, owner.ID, "loadout match", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Start(ctx, view.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, err = svc.SetLoadout(ctx, view.ID, owner.ID, []Choice{{BlueprintID: bp.ID, ProgramID: p.ID}})
	wantStatus(t, err, http.StatusConflict)
}

// TestStartingRosterIsEqual is design §2.1 step 4: no loadout can field more
// robots, or more component value, than the built-in kit every colony used to
// start from.
func TestStartingRosterIsEqual(t *testing.T) {
	budget := startingBudget()
	cheap := sim.Blueprint{Name: "cheap", Components: []sim.Variant{sim.Tracks, sim.LightArmor}}
	rich := sim.Blueprint{Name: "rich", Components: gunner}

	cases := []struct {
		name string
		bps  []sim.Blueprint
	}{
		{"the starter scavenger spends the budget exactly", []sim.Blueprint{DefaultBlueprint()}},
		{"a cheap body does not buy extra robots", []sim.Blueprint{cheap}},
		{"an expensive body buys fewer of them", []sim.Blueprint{rich}},
		{"only the first approval is fielded", []sim.Blueprint{rich, cheap, cheap}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roster := startingRoster(tc.bps)
			if len(roster) == 0 {
				t.Fatal("startingRoster() fielded nothing")
			}
			if len(roster) > startingRobots {
				t.Errorf("startingRoster() fielded %d robots, the cap is %d", len(roster), startingRobots)
			}
			spent := 0
			for _, bp := range roster {
				spent += bp.Value()
				if bp.Name != tc.bps[0].Name {
					t.Errorf("startingRoster() fielded a %q, want only the first approval %q", bp.Name, tc.bps[0].Name)
				}
			}
			if spent > budget {
				t.Errorf("startingRoster() spent %d, the budget is %d", spent, budget)
			}
		})
	}
	// The starter scavenger is the reference: a colony approving it gets exactly
	// the opening every colony used to get, which is what makes the budget
	// "equal" rather than merely "bounded".
	if got := startingRoster([]sim.Blueprint{DefaultBlueprint()}); len(got) != startingRobots {
		t.Errorf("the starter scavenger now fields %d robots, want the unchanged %d", len(got), startingRobots)
	}
	if got := startingRoster([]sim.Blueprint{rich}); len(got) >= startingRobots {
		t.Errorf("an armed body fields %d robots, want fewer than the %d unarmed ones", len(got), startingRobots)
	}
}

// TestNoLoadoutIsTheBuiltInKit: the fallback that keeps the flow unblocked for
// a player who never opens the picker. It is also why this change leaves the
// replay fingerprint alone.
func TestNoLoadoutIsTheBuiltInKit(t *testing.T) {
	k, err := memberKit(db.Member{UserID: 1, DisplayName: "ada"})
	if err != nil {
		t.Fatalf("memberKit() = %v", err)
	}
	want := humanKit()
	if len(k.blueprints) != len(want.blueprints) || len(k.start) != len(want.start) {
		t.Errorf("memberKit() with no loadout = %d blueprints/%d robots, want %d/%d",
			len(k.blueprints), len(k.start), len(want.blueprints), len(want.start))
	}
	if len(k.start) > 0 && k.start[0].ID != DefaultBlueprintID {
		t.Errorf("memberKit() starts with %q, want the built-in %q", k.start[0].ID, DefaultBlueprintID)
	}
}
