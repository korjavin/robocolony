package lobby

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"reflect"
	"slices"
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

// TestLoadoutIgnoresLibraryEdits is the other half of the same argument, for
// blueprint edit: an approval keeps the parts the design had when it was
// approved, so re-equipping the library row cannot re-equip a colony that
// already chose it.
func TestLoadoutIgnoresLibraryEdits(t *testing.T) {
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

	// The library row loses its weapon and its name after the approval.
	disarmed, err := json.Marshal(storedBlueprint{Components: []int{int(sim.Tracks), int(sim.LightArmor)}})
	if err != nil {
		t.Fatalf("marshal = %v", err)
	}
	if _, err := database.UpdateBlueprint(ctx, owner.ID, bp.ID, "pacifist", string(disarmed)); err != nil {
		t.Fatalf("UpdateBlueprint() = %v", err)
	}

	got, err := svc.Get(ctx, view.ID)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	for _, m := range got.Members {
		if m.UserID != owner.ID {
			continue
		}
		var stored Loadout
		if err := json.Unmarshal(m.Loadout, &stored); err != nil {
			t.Fatalf("stored loadout does not parse: %v", err)
		}
		if len(stored.Entries) != 1 {
			t.Fatalf("the seat has %d approvals, want 1", len(stored.Entries))
		}
		e := stored.Entries[0]
		if e.BlueprintName != "hunter" {
			t.Errorf("approval is named %q, want the %q it was approved as", e.BlueprintName, "hunter")
		}
		if !e.blueprint().Has(sim.KindWeapon) {
			t.Errorf("approval lost its weapon to a later library edit: %v", e.Components)
		}
	}
}

// TestLoadoutSurvivesLibraryDelete is the whole safety argument behind blueprint
// delete: an approval is a frozen snapshot of the parts list, not a library id,
// so deleting the row it was copied from cannot reach a lobby that already
// approved it — nor the match that lobby starts.
func TestLoadoutSurvivesLibraryDelete(t *testing.T) {
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
	if err := database.DeleteBlueprint(ctx, owner.ID, bp.ID); err != nil {
		t.Fatalf("DeleteBlueprint() = %v", err)
	}

	info, err := svc.Start(ctx, view.ID, owner.ID)
	if err != nil {
		t.Fatalf("Start() after deleting the library row = %v", err)
	}
	m, ok := svc.Registry().Get(info.ID)
	if !ok {
		t.Fatal("the started match is not in the registry")
	}
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		if len(w.Bases[0].Blueprints) != 1 || w.Bases[0].Blueprints[0].Name != "hunter" {
			t.Errorf("base approved %+v, want the hunter it approved before the delete", w.Bases[0].Blueprints)
		}
		for _, r := range w.Robots {
			if r.Colony != w.Bases[0].Colony {
				continue
			}
			if !r.Blueprint.Has(sim.KindWeapon) {
				t.Errorf("robot %d lost its weapon, so it was not built to the approved design", r.ID)
			}
			if rt.Control(r) == nil {
				t.Errorf("robot %d runs %q, which is not installed", r.ID, r.ProgramID)
			}
		}
	})
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
	budget := defaultStartingBudget()
	cheap := sim.Blueprint{Name: "cheap", Components: []sim.Variant{sim.Tracks, sim.LightArmor}}
	rich := sim.Blueprint{Name: "rich", Components: gunner}

	cases := []struct {
		name string
		bps  []sim.Blueprint
	}{
		{"the starter scavenger spends the budget exactly", []sim.Blueprint{DefaultBlueprint()}},
		{"a cheap body does not buy extra robots", []sim.Blueprint{cheap}},
		{"an expensive body buys fewer of them", []sim.Blueprint{rich}},
		{"a mixed approval set stays inside both bounds", []sim.Blueprint{rich, cheap, DefaultBlueprint()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Several seeds: the draw is random, so one roster proves nothing
			// about the bounds it has to hold under every draw.
			for seed := range int64(8) {
				roster := startingRoster(rand.New(rand.NewSource(seed)), tc.bps, budget)
				if len(roster) == 0 {
					t.Fatal("startingRoster() fielded nothing")
				}
				if len(roster) > startingRobots {
					t.Errorf("startingRoster() fielded %d robots, the cap is %d", len(roster), startingRobots)
				}
				spent := 0
				for _, bp := range roster {
					spent += bp.Value()
					if !slices.ContainsFunc(tc.bps, func(a sim.Blueprint) bool { return a.Name == bp.Name }) {
						t.Errorf("startingRoster() fielded a %q, which the colony never approved", bp.Name)
					}
				}
				if spent > budget {
					t.Errorf("startingRoster() spent %d, the budget is %d", spent, budget)
				}
			}
		})
	}
	// The starter scavenger is the reference: a colony approving it gets exactly
	// the opening every colony used to get, which is what makes the budget
	// "equal" rather than merely "bounded".
	rng := rand.New(rand.NewSource(1))
	if got := startingRoster(rng, []sim.Blueprint{DefaultBlueprint()}, budget); len(got) != startingRobots {
		t.Errorf("the starter scavenger now fields %d robots, want the unchanged %d", len(got), startingRobots)
	}
	if got := startingRoster(rng, []sim.Blueprint{rich}, budget); len(got) >= startingRobots {
		t.Errorf("an armed body fields %d robots, want fewer than the %d unarmed ones", len(got), startingRobots)
	}
}

// TestStartingRosterDrawsFromTheWholeLoadout is rc-w9s.36: approving a mixed set
// used to buy nothing at tick 0, because the opening was the first approval
// repeated. Now every robot is drawn from what still fits the remaining budget,
// so a two-design loadout fields both designs over a handful of seeds — and the
// same seed still fields exactly the same colony, which is what a replay rests
// on (persist.go rebuilds a running match from seed + command log alone).
//
// Both designs cost under a third of the budget, so both stay affordable for
// every one of the three draws: a fixture where only one option is ever
// affordable makes Intn(1) return 0 from any rand source and would hide a
// math/rand regression entirely (see docs/engineering-notes.md).
func TestStartingRosterDrawsFromTheWholeLoadout(t *testing.T) {
	seat := []db.Member{{UserID: 1, DisplayName: "ada", Loadout: twoDesignLoadout(t)}}
	budget := defaultStartingBudget()

	seen := map[string]bool{}
	for seed := range int64(8) {
		set := shortSettings(60)
		set.Seed = seed
		set.MaxPlayers = 1
		m, err := newMatch(db.Lobby{ID: 1, Name: "draw"}, set, seat)
		if err != nil {
			t.Fatalf("newMatch() = %v", err)
		}
		roster := colonyRoster(m)
		if len(roster) == 0 || len(roster) > startingRobots {
			t.Fatalf("seed %d fielded %d robots, want 1..%d", seed, len(roster), startingRobots)
		}
		spent := 0
		for _, name := range roster {
			seen[name] = true
		}
		for _, r := range m.world.Robots {
			if r.Colony == m.world.Bases[0].Colony {
				spent += r.Blueprint.Value()
			}
		}
		if spent > budget {
			t.Errorf("seed %d fielded %d in component value, the budget is %d", seed, spent, budget)
		}
	}
	if len(seen) < 2 {
		t.Errorf("eight seeds fielded only %v, want both approved designs to come up", seen)
	}

	// Same seed, same members, same settings: the same opening roster.
	set := shortSettings(60)
	set.Seed = 7
	set.MaxPlayers = 1
	a, err := newMatch(db.Lobby{ID: 1, Name: "draw"}, set, seat)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	b, err := newMatch(db.Lobby{ID: 1, Name: "draw"}, set, seat)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	if x, y := colonyRoster(a), colonyRoster(b); !reflect.DeepEqual(x, y) {
		t.Errorf("the same seed fielded %v in one match and %v in another: the draw is not on the world rng", x, y)
	}
}

// colonyRoster is the first colony's starting robots, in world order.
func colonyRoster(m *Match) []string {
	var out []string
	for _, r := range m.world.Robots {
		if r.Colony == m.world.Bases[0].Colony {
			out = append(out, r.Blueprint.Name)
		}
	}
	return out
}

// twoDesignLoadout approves two scavengers that differ only in armor. Both are
// affordable at every step of the draw, which is what makes it a fixture the
// determinism guard can see through.
func twoDesignLoadout(t *testing.T) json.RawMessage {
	t.Helper()
	entry := func(id int64, name string, armor sim.Variant) LoadoutEntry {
		return LoadoutEntry{
			BlueprintID: id, BlueprintName: name, ProgramID: 1, ProgramName: "scavenger",
			Components: []int{int(sim.Tracks), int(armor), int(sim.Manipulator), int(sim.PartsRadar)},
			Program:    mustEncode(t, DefaultProgram()),
		}
	}
	l := Loadout{Entries: []LoadoutEntry{
		entry(1, "medium scavenger", sim.MediumArmor),
		entry(2, "light scavenger", sim.LightArmor),
	}}
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatalf("encode loadout: %v", err)
	}
	return raw
}

// TestLoadoutTooExpensiveToFieldFails: the pre-generation guard. A loadout whose
// cheapest approval costs more than the whole budget would field no robots, and
// a colony with neither robots nor stock can never act again (design §5.3), so
// the match must refuse to start rather than generate an arena for it.
func TestLoadoutTooExpensiveToFieldFails(t *testing.T) {
	raw, err := json.Marshal(Loadout{Entries: []LoadoutEntry{{
		BlueprintID: 1, BlueprintName: "gunner", ProgramID: 1, ProgramName: "scavenger",
		Components: []int{int(sim.Tracks), int(sim.HeavyArmor), int(sim.Laser), int(sim.EnemyRadar)},
		Program:    mustEncode(t, DefaultProgram()),
	}}})
	if err != nil {
		t.Fatalf("encode loadout: %v", err)
	}
	m := db.Member{UserID: 1, DisplayName: "ada", Loadout: raw}
	if _, err := memberKit(m, 10); err == nil {
		t.Fatal("memberKit() accepted a loadout no approval of which fits the budget")
	}
	if _, err := memberKit(m, defaultStartingBudget()); err != nil {
		t.Fatalf("memberKit() with an affordable approval = %v", err)
	}
}

// TestNoLoadoutIsTheBuiltInKit: the fallback that keeps the flow unblocked for
// a player who never opens the picker. It is also why this change leaves the
// replay fingerprint alone.
func TestNoLoadoutIsTheBuiltInKit(t *testing.T) {
	k, err := memberKit(db.Member{UserID: 1, DisplayName: "ada"}, defaultStartingBudget())
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

// TestStartingBudgetIsSpentInFull is design §12 P0's answer: the host sets the
// budget, and whatever the opening roster does not spend arrives as parts in
// the base rather than being lost.
//
// It is also the equal-strength check (design §2.1): every *player* colony,
// lean loadout or expensive one, leaves the start worth the same budget, up to
// less than the cheapest component in the catalogue, which is the finest
// granularity the conversion can reach. An AI colony is deliberately outside
// it — see the kit.budget comment in starter.go — so this checks the human
// seats and separately checks that the AI opening never moved.
func TestStartingBudgetIsSpentInFull(t *testing.T) {
	const seats = 2
	cheapest := cheapestComponentValue(t)
	aiWorth := map[int]int{}
	// 3450 is just a big budget, not a ceiling: the settings have none.
	for _, budget := range []int{minStartingBudget, defaultStartingBudget(), 500, 3450} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			set := shortSettings(60)
			set.StartingBudget = budget
			set.MaxPlayers = seats
			set.AI = Profiles()[:1]
			if err := set.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
			m := testMatch(t, set, seats)

			for i, b := range m.world.Bases {
				worth, robots := b.InventoryValue(), 0
				for _, r := range m.world.Robots {
					if r.Colony == b.Colony {
						worth += r.Blueprint.Value()
						robots++
					}
				}
				if robots == 0 {
					// Even at the floor: a colony with no robot collects
					// nothing (design §5.3), so no legal budget may field one.
					t.Errorf("colony %d starts with no robots at budget %d", b.Colony, budget)
				}
				if i >= seats {
					// The AI seats: the same opening at every budget, or the
					// measured ladder in ai.go stops meaning anything.
					if prev, seen := aiWorth[i]; seen && prev != worth {
						t.Errorf("AI colony %d starts worth %d at budget %d but %d at another: the budget re-priced it",
							b.Colony, worth, budget, prev)
					}
					aiWorth[i] = worth
					continue
				}
				if worth > budget || worth <= budget-cheapest {
					t.Errorf("colony %d starts worth %d, want the budget %d spent down to under %d left",
						b.Colony, worth, budget, cheapest)
				}
				for _, e := range b.SortedInventory() {
					if _, ok := sim.Lookup(e.Variant); !ok {
						t.Errorf("colony %d holds %d of unknown variant %d", b.Colony, e.Count, e.Variant)
					}
				}
			}
		})
	}

	// The leftover is drawn from the world's rng, so the same seed must produce
	// the same stock. This is what a replay rests on.
	set := shortSettings(60)
	set.StartingBudget = 500
	a, b := testMatch(t, set, 2), testMatch(t, set, 2)
	for i := range a.world.Bases {
		x, y := a.world.Bases[i].SortedInventory(), b.world.Bases[i].SortedInventory()
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("colony %d starts with %v in one match and %v in another from the same seed", i, x, y)
		}
	}

	// A budget at the floor still fields a robot, whatever the host sets: a
	// colony with neither robots nor stock can never act again (design §5.3).
	if minStartingBudget < DefaultBlueprint().Value() {
		t.Fatalf("minStartingBudget %d is below the built-in scavenger's %d, so a legal lobby could not be started",
			minStartingBudget, DefaultBlueprint().Value())
	}
}

func cheapestComponentValue(t *testing.T) int {
	t.Helper()
	cheapest := 0
	for _, c := range sim.Catalogue() {
		if c.Value > 0 && (cheapest == 0 || c.Value < cheapest) {
			cheapest = c.Value
		}
	}
	if cheapest == 0 {
		t.Fatal("no priced component in the catalogue")
	}
	return cheapest
}
