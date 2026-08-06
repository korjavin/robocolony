package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// TestProgramVersionsCreateApproveInstall walks the whole of rc-pt6.11 once, in
// the order a player meets it: save, save again, approve an older version,
// install it on a robot, and save again while that robot is fielding it.
//
// It is one narrative rather than five cases because the interesting facts are
// all about *sequence* — what a save does depends on what is in the field, and
// what an install hands over depends on what was approved.
func TestProgramVersionsCreateApproveInstall(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	// The registry is what makes "in use by" answerable, and with it the
	// difference between a save that approves itself and one that drafts.
	lib := NewLibrary(h.db, h.reg)

	v1, err := lib.SaveProgram(t.Context(), owner.ID, 0, "harvest", encode(t, lobby.DefaultProgram()), 0)
	if err != nil {
		t.Fatalf("SaveProgram(create) = %v", err)
	}
	if v1.Version != 1 || v1.ApprovedVersion != 1 {
		t.Fatalf("created program is v%d approved v%d, want v1 approved v1", v1.Version, v1.ApprovedVersion)
	}

	// Nothing of the player's is running it, so the save approves itself: SAVE
	// still means "this is what my robots would get".
	v2, err := lib.SaveProgram(t.Context(), owner.ID, v1.ID, "harvest", encode(t, scoutProgram()), 0)
	if err != nil {
		t.Fatalf("SaveProgram(second) = %v", err)
	}
	if v2.Version != 2 || v2.ApprovedVersion != 2 {
		t.Errorf("second save is v%d approved v%d, want v2 approved v2", v2.Version, v2.ApprovedVersion)
	}

	// Going back to v1 is the whole point of keeping them.
	back, err := lib.ApproveProgram(t.Context(), owner.ID, v1.ID, 1)
	if err != nil {
		t.Fatalf("ApproveProgram(1) = %v", err)
	}
	if back.Version != 2 || back.ApprovedVersion != 1 {
		t.Errorf("after approving v1: v%d approved v%d, want v2 approved v1", back.Version, back.ApprovedVersion)
	}

	// An install hands over the approved version, not the head, and says so in
	// the runtime id — which is the only record of what a robot was given.
	robot := aRobot(t, m, colonyOf(t, m, owner))
	parkAtBase(t, m, robot)
	state, err := h.InstallProgram(t.Context(), owner.ID, m.ID, robot, v1.ID)
	if err != nil {
		t.Fatalf("InstallProgram() = %v", err)
	}
	wantID := fmt.Sprintf("lib-%d-v1-r%d", v1.ID, robot)
	if state.Program != wantID {
		t.Errorf("installed program = %q, want %q (the approved version, not the head)", state.Program, wantID)
	}
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		if rt.Control(w.RobotByID(robot)) == nil {
			t.Error("the installed version has no controller in the runtime")
		}
	})

	// Now the design's topbar: the approved version is in the field, and the
	// VERSIONS panel says which one and where.
	open, err := lib.GetProgram(t.Context(), owner.ID, v1.ID)
	if err != nil {
		t.Fatalf("GetProgram() = %v", err)
	}
	if open.InUse != 1 || open.InUseMatch != m.ID {
		t.Errorf("in use by %d robots in match %d, want 1 in match %d", open.InUse, open.InUseMatch, m.ID)
	}
	if len(open.Versions) != 2 {
		t.Fatalf("VERSIONS lists %d rows, want 2", len(open.Versions))
	}
	for _, v := range open.Versions {
		switch v.Version {
		case 1:
			if !v.Approved || v.Robots != 1 || v.Match != m.ID {
				t.Errorf("v1 = %+v, want approved with 1 robot in match %d", v, m.ID)
			}
		case 2:
			if v.Approved || v.Robots != 0 {
				t.Errorf("v2 = %+v, want unapproved and unfielded", v)
			}
		}
	}

	// A save while robots are fielding it is a draft: the approval stays where
	// it is, and APPROVE is the click that moves it.
	// scoutProgram again: what changes between v2 and v3 is not the point, and
	// the §10.9 responder needs a weapon this colony's scavenger does not carry.
	v3, err := lib.SaveProgram(t.Context(), owner.ID, v1.ID, "harvest", encode(t, scoutProgram()), 0)
	if err != nil {
		t.Fatalf("SaveProgram(while fielded) = %v", err)
	}
	if v3.Version != 3 || v3.ApprovedVersion != 1 {
		t.Errorf("save while fielded is v%d approved v%d, want v3 approved v1", v3.Version, v3.ApprovedVersion)
	}

	// And approving it changes nothing that is already running: design §4.2 has
	// rules reach a robot through an install and nowhere else.
	if _, err := lib.ApproveProgram(t.Context(), owner.ID, v1.ID, 3); err != nil {
		t.Fatalf("ApproveProgram(3) = %v", err)
	}
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		if r := w.RobotByID(robot); r.ProgramID != wantID {
			t.Errorf("the fielded robot moved to %q on approve, want it still on %q", r.ProgramID, wantID)
		}
	})
}

// TestApproveRefusesWhatIsNotThere covers the ways an approval can miss. A
// version that was never saved and somebody else's program are the same answer
// on purpose: an id is not a way to learn what exists.
func TestApproveRefusesWhatIsNotThere(t *testing.T) {
	lib, database := newLibrary(t)
	alice := newUser(t, database, "alice")
	bob := newUser(t, database, "bob")

	p, err := lib.SaveProgram(t.Context(), alice.ID, 0, "harvest", encode(t, lobby.DefaultProgram()), 0)
	if err != nil {
		t.Fatalf("SaveProgram() = %v", err)
	}

	for _, tc := range []struct {
		name    string
		user    int64
		id      int64
		version int
		code    int
	}{
		{"version zero", alice.ID, p.ID, 0, http.StatusBadRequest},
		{"negative version", alice.ID, p.ID, -1, http.StatusBadRequest},
		{"unsaved version", alice.ID, p.ID, 9, http.StatusNotFound},
		{"somebody else's program", bob.ID, p.ID, 1, http.StatusNotFound},
		{"no such program", alice.ID, p.ID + 500, 1, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := lib.ApproveProgram(t.Context(), tc.user, tc.id, tc.version)
			wantLibStatus(t, err, tc.code)
		})
	}

	// None of that moved the approval.
	after, err := lib.GetProgram(t.Context(), alice.ID, p.ID)
	if err != nil {
		t.Fatalf("GetProgram() = %v", err)
	}
	if after.ApprovedVersion != 1 {
		t.Errorf("approved version = %d after the refusals, want 1", after.ApprovedVersion)
	}
}
