package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Robots is design §4.2 — recall and reprogramming, the only direct commands a
// player has over a robot. Everything else a robot does comes from its program.
//
// The two commands are deliberately asymmetric: recall takes effect at once but
// only *starts* a walk home, and installing a program is refused anywhere but
// the robot's own base. That travel delay is the design (§4.2 constraint), so
// there is no path here that moves a robot or swaps its rules in the field.
type Robots struct {
	reg *lobby.Registry
	db  *db.DB
}

// NewRobots wires the robot commands to the live match registry and the
// player's program library.
func NewRobots(reg *lobby.Registry, database *db.DB) *Robots {
	return &Robots{reg: reg, db: database}
}

// Routes registers the two commands. requireAuth is auth.Handler.RequireAuth;
// there is no unauthenticated path to either.
func (h *Robots) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("POST /api/matches/{id}/robots/{robotId}/recall", requireAuth(http.HandlerFunc(h.handleRecall)))
	mux.Handle("POST /api/matches/{id}/robots/{robotId}/program", requireAuth(http.HandlerFunc(h.handleProgram)))
}

// RobotState is what a command reports back: enough for the inspector to show
// "returning" versus "at base, awaiting program", and to prove the memory clear
// of design §4.2 step 4 without waiting for the next stream frame.
type RobotState struct {
	ID       int      `json:"id"`
	Colony   int      `json:"colony"`
	X        int      `json:"x"`
	Y        int      `json:"y"`
	Program  string   `json:"program"`
	Recalled bool     `json:"recalled"`
	AtBase   bool     `json:"at_base"`
	Memory   []*Point `json:"memory"`
}

func robotState(w *sim.World, r *sim.Robot) RobotState {
	mem := make([]*Point, sim.MemPoints)
	for i, m := range r.Memory {
		if m.Set {
			mem[i] = &Point{X: m.Coord.X, Y: m.Coord.Y}
		}
	}
	return RobotState{
		ID: r.ID, Colony: int(r.Colony), X: r.Coord.X, Y: r.Coord.Y,
		Program: r.ProgramID, Recalled: r.Recalled, AtBase: w.AtOwnBase(r), Memory: mem,
	}
}

// Recall issues design §4.2 step 1 on one robot of the caller's own colony. It
// only raises the flag: the walk home happens in the simulation, tick by tick.
func (h *Robots) Recall(ctx context.Context, userID, matchID int64, robotID int) (RobotState, error) {
	m, colony, err := h.own(matchID, userID)
	if err != nil {
		return RobotState{}, err
	}
	// Apply rather than Read: a recall is a player input, and the match's input
	// log is what a restart replays it from (internal/lobby/persist.go).
	var out RobotState
	err = m.Apply(lobby.Command{Kind: lobby.CmdRecall, Robot: robotID}, func(w *sim.World, _ *prog.Runtime) error {
		r, err := robotOf(w, colony, robotID)
		if err != nil {
			return err
		}
		r.Recalled = true
		out = robotState(w, r)
		return nil
	})
	return out, err
}

// InstallProgram is design §4.2 steps 3-5: a program from the caller's own
// library, onto a robot of the caller's own colony, and only while that robot
// is at its own base.
func (h *Robots) InstallProgram(ctx context.Context, userID, matchID int64, robotID int, programID int64) (RobotState, error) {
	m, colony, err := h.own(matchID, userID)
	if err != nil {
		return RobotState{}, err
	}
	// Scoped by (user_id, id): another player's program is not a 403 to probe
	// with, it simply does not exist.
	row, err := h.db.ProgramByID(ctx, userID, programID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RobotState{}, errf(http.StatusNotFound, "program not found in your library")
		}
		return RobotState{}, err
	}
	p, err := prog.Decode([]byte(row.JSON))
	if err != nil {
		return RobotState{}, errf(http.StatusBadRequest, "program %d does not load: %s", programID, err)
	}

	// The program travels in the log by value, not as a library id: the player
	// may edit that library row later, and a replay must install the rules the
	// robot actually ran.
	id := installID(programID, robotID)
	cmd := lobby.Command{
		Kind:      lobby.CmdProgram,
		Robot:     robotID,
		ProgramID: id,
		Program:   json.RawMessage(row.JSON),
	}
	var out RobotState
	err = m.Apply(cmd, func(w *sim.World, rt *prog.Runtime) error {
		r, err := robotOf(w, colony, robotID)
		if err != nil {
			return err
		}
		// The travel delay is the whole point of design §4.2: no install in the
		// field, however long the player has been waiting.
		if !w.AtOwnBase(r) {
			return errf(http.StatusConflict, "robot %d is not at its base; recall it first", robotID)
		}
		if res := prog.Validate(p, r.Blueprint); !res.OK() {
			return validationError(res)
		}
		rt.Install(id, p)
		// Reprogram clears all three memory points (design §10.6) here and now,
		// rather than leaving it to Runtime.Control on the robot's next tick: the
		// inspector must be able to show the clean state immediately.
		r.Reprogram(id)
		out = robotState(w, r)
		return nil
	})
	return out, err
}

// installID is the runtime id a library program gets when it is installed on
// one robot. It is per robot on purpose: installing under the shared library id
// would bump Runtime's revision for every other robot already running that
// program and reprogram them where they stand — the exact instant reprogramming
// design §4.2 forbids.
//
// ponytail: one runtime entry per install, never collected. A match is minutes
// long and an install needs a robot to walk home first, so the map cannot grow
// far. Collect on Forget if that ever stops being true.
func installID(programID int64, robotID int) string {
	return fmt.Sprintf("lib-%d-r%d", programID, robotID)
}

// own resolves the match and the caller's colony in it. A player with no seat
// in the match has no robot to command, which is a 403 and not a hint about
// which matches exist.
func (h *Robots) own(matchID, userID int64) (*lobby.Match, sim.ColonyID, error) {
	m, ok := h.reg.Get(matchID)
	if !ok {
		return nil, 0, errf(http.StatusNotFound, "match not found")
	}
	// A finished match stays in the registry so it can still be observed, and
	// its world stops stepping. Commanding it would edit the final state after
	// the fact, so both commands stop here.
	//
	// Checked outside the match lock because Finished takes that same lock, and
	// therefore one command can still land on a match that finishes in the gap.
	// That is one tick's worth of race on a match that just ended, which is no
	// different from the command having arrived a tick earlier.
	if m.Finished() {
		return nil, 0, errf(http.StatusConflict, "the match is over")
	}
	// Colonies are fixed when the match starts and never written again, so this
	// needs no lock.
	for _, c := range m.Colonies {
		if c.UserID == userID {
			return m, c.ID, nil
		}
	}
	return nil, 0, errf(http.StatusForbidden, "you have no colony in this match")
}

// robotOf finds a robot the caller is allowed to command. Caller holds the
// match lock. A robot of another colony is a 403 whether or not it exists, so
// guessing ids reveals nothing beyond what the observer stream already shows.
func robotOf(w *sim.World, colony sim.ColonyID, robotID int) (*sim.Robot, error) {
	r := w.RobotByID(robotID)
	if r == nil {
		return nil, errf(http.StatusNotFound, "no robot %d in this match", robotID)
	}
	if r.Colony != colony {
		return nil, errf(http.StatusForbidden, "robot %d belongs to another colony", robotID)
	}
	return r, nil
}

// HTTP surface. Thin: every rule above is enforced in the domain methods, which
// are testable without a session.

func (h *Robots) handleRecall(w http.ResponseWriter, r *http.Request) {
	user, matchID, robotID, err := commandTarget(r)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	state, err := h.Recall(r.Context(), user.ID, matchID, robotID)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	writeCmdJSON(w, http.StatusOK, state)
}

func (h *Robots) handleProgram(w http.ResponseWriter, r *http.Request) {
	user, matchID, robotID, err := commandTarget(r)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	var body struct {
		ProgramID int64 `json:"program_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeCmdErr(w, r, errf(http.StatusBadRequest, "invalid request body: %s", err))
		return
	}
	if body.ProgramID <= 0 {
		writeCmdErr(w, r, errf(http.StatusBadRequest, "program_id is required"))
		return
	}
	state, err := h.InstallProgram(r.Context(), user.ID, matchID, robotID, body.ProgramID)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	writeCmdJSON(w, http.StatusOK, state)
}

// commandTarget pulls the caller and the path ids out of a request.
func commandTarget(r *http.Request) (db.User, int64, int, error) {
	user, ok := auth.UserFrom(r.Context())
	if !ok { // unreachable behind RequireAuth
		return db.User{}, 0, 0, errf(http.StatusUnauthorized, "unauthenticated")
	}
	matchID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || matchID <= 0 {
		return db.User{}, 0, 0, errf(http.StatusNotFound, "no such match")
	}
	robotID, err := strconv.Atoi(r.PathValue("robotId"))
	if err != nil || robotID <= 0 {
		return db.User{}, 0, 0, errf(http.StatusNotFound, "no such robot")
	}
	return user, matchID, robotID, nil
}

// cmdError carries the status a command failure maps to. Named for the robot
// commands specifically: internal/server is a shared package and this is not a
// general-purpose error type.
type cmdError struct {
	code   int
	msg    string
	issues []prog.Issue
}

func (e cmdError) Error() string { return e.msg }

func errf(code int, format string, a ...any) error {
	return cmdError{code: code, msg: fmt.Sprintf(format, a...)}
}

// validationError reports design §10.10's blocking issues, so the editor can
// point at the offending rules instead of showing one flattened string.
func validationError(res prog.Result) error {
	return cmdError{
		code:   http.StatusBadRequest,
		msg:    "the program is not valid for this robot's blueprint",
		issues: res.Errors,
	}
}

func writeCmdJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeCmdErr(w http.ResponseWriter, r *http.Request, err error) {
	var ce cmdError
	if errors.As(err, &ce) {
		body := map[string]any{"error": ce.msg}
		if len(ce.issues) > 0 {
			body["issues"] = ce.issues
		}
		writeCmdJSON(w, ce.code, body)
		return
	}
	slog.Error("robot command failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeCmdJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
