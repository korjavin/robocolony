package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// twoColonies starts a real two-player match: the ownership rules are only
// meaningful when there is somebody else's colony to try to command.
func twoColonies(t *testing.T) (*Robots, *lobby.Match, db.User, db.User) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	svc := lobby.New(database)
	// Before the database close registered above: a tick driver must not outlive
	// the database it settles its lobby row in.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})

	owner, err := database.UpsertUser(t.Context(), "sub-owner", "owner@example.com", "Owner")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	rival, err := database.UpsertUser(t.Context(), "sub-rival", "rival@example.com", "Rival")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	view, err := svc.Create(t.Context(), owner.ID, "recall test", lobby.DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Join(t.Context(), view.ID, rival.ID); err != nil {
		t.Fatalf("Join() = %v", err)
	}
	if _, err := svc.Start(t.Context(), view.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	m, ok := svc.Registry().Get(view.ID)
	if !ok {
		t.Fatalf("match %d is not in the registry after Start", view.ID)
	}
	if len(m.Colonies) != 2 {
		t.Fatalf("match has %d colonies, want 2", len(m.Colonies))
	}
	return NewRobots(svc.Registry(), database), m, owner, rival
}

// colonyOf returns a user's colony id in the match.
func colonyOf(t *testing.T, m *lobby.Match, user db.User) sim.ColonyID {
	t.Helper()
	for _, c := range m.Colonies {
		if c.UserID == user.ID {
			return c.ID
		}
	}
	t.Fatalf("user %d has no colony in match %d", user.ID, m.ID)
	return 0
}

// aRobot returns the id of one robot of the given colony.
func aRobot(t *testing.T, m *lobby.Match, colony sim.ColonyID) int {
	t.Helper()
	id := 0
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, r := range w.Robots {
			if r.Colony == colony {
				id = r.ID
				return
			}
		}
	})
	if id == 0 {
		t.Fatalf("colony %d has no robots", colony)
	}
	return id
}

// parkAtBase puts a robot at its base and recalls it, which is how it stays
// there while the tick driver keeps running under the test. The coordinate is
// test scaffolding — the product never moves a robot like this.
func parkAtBase(t *testing.T, m *lobby.Match, robotID int) {
	t.Helper()
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		r := w.RobotByID(robotID)
		if r == nil {
			t.Errorf("robot %d is gone", robotID)
			return
		}
		for _, b := range w.Bases {
			if b.Colony == r.Colony {
				r.Coord = b.Coord
			}
		}
		r.Recalled = true
	})
}

// sendFarFromBase drops a robot in a corner far enough that it cannot walk home
// within the milliseconds a test takes.
func sendFarFromBase(t *testing.T, m *lobby.Match, robotID int) {
	t.Helper()
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		r := w.RobotByID(robotID)
		if r == nil {
			t.Errorf("robot %d is gone", robotID)
			return
		}
		var base sim.Coord
		for _, b := range w.Bases {
			if b.Colony == r.Colony {
				base = b.Coord
			}
		}
		best, bestDist := r.Coord, 0
		for _, c := range []sim.Coord{{X: 1, Y: 1}, {X: w.Width - 2, Y: 1}, {X: 1, Y: w.Height - 2}, {X: w.Width - 2, Y: w.Height - 2}} {
			if d := c.Chebyshev(base); d > bestDist {
				best, bestDist = c, d
			}
		}
		r.Coord = best
		if w.AtOwnBase(r) {
			t.Errorf("robot %d is still at its base at %v", robotID, r.Coord)
		}
	})
}

// saveProgram puts a runnable program in a user's library and returns its id.
func saveProgram(t *testing.T, h *Robots, user db.User, name string, p prog.Program) int64 {
	t.Helper()
	encoded, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	row, err := h.db.CreateProgram(t.Context(), user.ID, name, string(encoded))
	if err != nil {
		t.Fatalf("CreateProgram() = %v", err)
	}
	return row.ID
}

func wantStatus(t *testing.T, err error, code int) {
	t.Helper()
	var ce cmdError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want a cmdError with status %d", err, code)
	}
	if ce.code != code {
		t.Fatalf("status = %d (%s), want %d", ce.code, ce.msg, code)
	}
}

// TestForeignRobotRejected is the ownership gate on both commands: a robot id
// is not a capability. Guessing one belonging to another colony gets a 403, and
// so does a player with no seat in the match at all.
func TestForeignRobotRejected(t *testing.T) {
	h, m, owner, rival := twoColonies(t)
	victim := aRobot(t, m, colonyOf(t, m, owner))
	programID := saveProgram(t, h, rival, "scavenger", lobby.DefaultProgram())

	if _, err := h.Recall(t.Context(), rival.ID, m.ID, victim); err == nil {
		t.Fatal("Recall on another colony's robot succeeded")
	} else {
		wantStatus(t, err, http.StatusForbidden)
	}

	parkAtBase(t, m, victim)
	if _, err := h.InstallProgram(t.Context(), rival.ID, m.ID, victim, programID); err == nil {
		t.Fatal("InstallProgram on another colony's robot succeeded")
	} else {
		wantStatus(t, err, http.StatusForbidden)
	}

	// A stranger with no colony here is refused before any robot is looked at,
	// so the same id tells them nothing.
	outsider, err := h.db.UpsertUser(t.Context(), "sub-outsider", "out@example.com", "Outsider")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	if _, err := h.Recall(t.Context(), outsider.ID, m.ID, victim); err == nil {
		t.Fatal("Recall by a non-member succeeded")
	} else {
		wantStatus(t, err, http.StatusForbidden)
	}
}

// TestRecallCommand is design §4.2 step 1 through the endpoint: it flags the
// robot and reports the state the inspector shows, without moving anything.
func TestRecallCommand(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	sendFarFromBase(t, m, id)

	var was sim.Coord
	m.Read(func(w *sim.World, _ *prog.Runtime) { was = w.RobotByID(id).Coord })

	state, err := h.Recall(t.Context(), owner.ID, m.ID, id)
	if err != nil {
		t.Fatalf("Recall() = %v", err)
	}
	if !state.Recalled {
		t.Error("Recall did not report the robot as recalled")
	}
	if state.AtBase {
		t.Error("a robot recalled in the field is reported as at base")
	}
	if (sim.Coord{X: state.X, Y: state.Y}) != was {
		t.Errorf("Recall moved the robot from %v to %v: recall is a walk home, not a teleport", was, sim.Coord{X: state.X, Y: state.Y})
	}
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if !w.RobotByID(id).Recalled {
			t.Error("the recall did not reach the live world")
		}
	})
}

// TestInstallRequiresBase is design §4.2's travel delay: no reprogramming in
// the field, ever.
func TestInstallRequiresBase(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	programID := saveProgram(t, h, owner, "scavenger", lobby.DefaultProgram())

	sendFarFromBase(t, m, id)
	var before string
	m.Read(func(w *sim.World, _ *prog.Runtime) { before = w.RobotByID(id).ProgramID })

	if _, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID); err == nil {
		t.Fatal("installed a program on a robot in the field")
	} else {
		wantStatus(t, err, http.StatusConflict)
	}
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if got := w.RobotByID(id).ProgramID; got != before {
			t.Errorf("the rejected install still changed the program: %q -> %q", before, got)
		}
	})

	// Recalled but not home yet is still the field: waiting does not help.
	if _, err := h.Recall(t.Context(), owner.ID, m.ID, id); err != nil {
		t.Fatalf("Recall() = %v", err)
	}
	if _, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID); err == nil {
		t.Fatal("installed a program on a robot that is still walking home")
	} else {
		wantStatus(t, err, http.StatusConflict)
	}
}

// TestInstallClearsMemory is design §4.2 steps 4-5 and §10.6: all three
// coordinate points are empty afterwards and the robot leaves base under the
// new program.
func TestInstallClearsMemory(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	programID := saveProgram(t, h, owner, "scavenger", lobby.DefaultProgram())

	parkAtBase(t, m, id)
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		r := w.RobotByID(id)
		for i := range r.Memory {
			r.Memory[i] = sim.MemPoint{Coord: sim.Coord{X: i + 1, Y: i + 1}, Set: true}
		}
	})

	state, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID)
	if err != nil {
		t.Fatalf("InstallProgram() = %v", err)
	}
	if len(state.Memory) != sim.MemPoints {
		t.Fatalf("reported memory has %d points, want %d", len(state.Memory), sim.MemPoints)
	}
	for i, p := range state.Memory {
		if p != nil {
			t.Errorf("reported memory point %d is %+v, want empty", i+1, *p)
		}
	}
	if state.Recalled {
		t.Error("the robot is still recalled after being reprogrammed; it must leave base")
	}
	if !strings.Contains(state.Program, "lib-") {
		t.Errorf("program = %q, want the installed library program", state.Program)
	}

	m.Read(func(w *sim.World, rt *prog.Runtime) {
		r := w.RobotByID(id)
		for i, p := range r.Memory {
			if p.Set || p.Coord != (sim.Coord{}) {
				t.Errorf("memory point %d survived the install: %+v", i+1, p)
			}
		}
		if r.Recalled {
			t.Error("the recall override survived the install")
		}
		if r.ProgramID != state.Program {
			t.Errorf("live program = %q, want %q", r.ProgramID, state.Program)
		}
		// The runtime must actually be able to drive the robot with it,
		// otherwise the robot idles forever with a program installed.
		if rt.Control(r) == nil {
			t.Error("the installed program has no controller in the runtime")
		}
	})
}

// A second install on a robot at base must not disturb its colony mates: their
// reprogramming is delayed by their own travel (design §4.2).
func TestInstallLeavesOtherRobotsAlone(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	colony := colonyOf(t, m, owner)
	id := aRobot(t, m, colony)
	programID := saveProgram(t, h, owner, "scavenger", lobby.DefaultProgram())

	// A sibling in the field, with memory it has every right to keep.
	sibling := 0
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, r := range w.Robots {
			if r.Colony == colony && r.ID != id {
				sibling = r.ID
				r.Memory[0] = sim.MemPoint{Coord: sim.Coord{X: 9, Y: 9}, Set: true}
				return
			}
		}
	})
	if sibling == 0 {
		t.Skip("colony has only one robot")
	}
	sendFarFromBase(t, m, sibling)

	parkAtBase(t, m, id)
	if _, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID); err != nil {
		t.Fatalf("InstallProgram() = %v", err)
	}

	m.Read(func(w *sim.World, _ *prog.Runtime) {
		r := w.RobotByID(sibling)
		if r == nil {
			t.Skip("the sibling was destroyed mid-test")
		}
		if !r.Memory[0].Set {
			t.Error("reprogramming one robot wiped a colony mate's memory in the field")
		}
		if r.Recalled {
			t.Error("reprogramming one robot recalled a colony mate")
		}
	})
}

// A program that does not fit the robot's blueprint is refused before anything
// is installed (design §10.10).
func TestInstallValidatesAgainstBlueprint(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	// The starting scavenger has no weapon.
	gunner := prog.Program{V: prog.SchemaVersion, Name: "gunner", Rules: []prog.Rule{
		{When: prog.Pred(prog.EnemyVisible), Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
	}}
	programID := saveProgram(t, h, owner, "gunner", gunner)

	parkAtBase(t, m, id)
	var before string
	m.Read(func(w *sim.World, _ *prog.Runtime) { before = w.RobotByID(id).ProgramID })

	_, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID)
	if err == nil {
		t.Fatal("installed a program the blueprint cannot run")
	}
	wantStatus(t, err, http.StatusBadRequest)
	var ce cmdError
	if errors.As(err, &ce) && len(ce.issues) == 0 {
		t.Error("validation failure carried no issues for the editor to show")
	}
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if got := w.RobotByID(id).ProgramID; got != before {
			t.Errorf("the rejected install still changed the program: %q -> %q", before, got)
		}
	})
}

// Another player's program is not installable, and does not read as forbidden
// either: the library lookup is scoped by user, so it simply is not there.
func TestInstallForeignProgramNotFound(t *testing.T) {
	h, m, owner, rival := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	programID := saveProgram(t, h, rival, "rival scavenger", lobby.DefaultProgram())

	parkAtBase(t, m, id)
	if _, err := h.InstallProgram(t.Context(), owner.ID, m.ID, id, programID); err == nil {
		t.Fatal("installed another player's program")
	} else {
		wantStatus(t, err, http.StatusNotFound)
	}
}

// A match that is not running (or died with a restart) is a 404, not a panic.
func TestCommandsOnUnknownMatch(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	id := aRobot(t, m, colonyOf(t, m, owner))
	if _, err := h.Recall(t.Context(), owner.ID, m.ID+9999, id); err == nil {
		t.Fatal("recalled a robot in a match that does not exist")
	} else {
		wantStatus(t, err, http.StatusNotFound)
	}
	if _, err := h.Recall(t.Context(), owner.ID, m.ID, 999999); err == nil {
		t.Fatal("recalled a robot that does not exist")
	} else {
		wantStatus(t, err, http.StatusNotFound)
	}
}

// The routes exist, are behind the auth middleware, and a request without a
// session gets nothing back but a 401.
func TestRoutesRequireASession(t *testing.T) {
	h, m, _, _ := twoColonies(t)
	mux := http.NewServeMux()
	// A pass-through "middleware": the real one puts a user in the context, so
	// this exercises the handler's own no-user guard.
	h.Routes(mux, func(next http.Handler) http.Handler { return next })

	for _, path := range []string{"/recall", "/program"} {
		url := "/api/matches/" + strconv.FormatInt(m.ID, 10) + "/robots/1" + path
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"program_id":1}`)))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s = %d, want %d", url, rec.Code, http.StatusUnauthorized)
		}
	}
}
