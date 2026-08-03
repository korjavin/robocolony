package prog

import "github.com/korjavin/robocolony/internal/sim"

// Runtime plugs programs into a world. Assign Runtime.Control to
// sim.World.Control and every robot is driven by the program its ProgramID
// names.
//
// It also holds each robot's last Trace, which is the only place an observer
// can ask "why did that robot do that" (E4.3). Maps are used for lookup only —
// nothing here ever ranges one, so world state never depends on map order.
type Runtime struct {
	programs map[string]installed
	robots   map[int]*Evaluator
}

// installed is a program plus the revision it was installed at. A player who
// edits a program keeps its id (design §4.2 reprogramming does not mint a new
// one), so the id alone cannot tell a running robot that its rules changed.
type installed struct {
	program  Program
	revision int
}

// NewRuntime returns an empty runtime.
func NewRuntime() *Runtime {
	return &Runtime{programs: map[string]installed{}, robots: map[int]*Evaluator{}}
}

// Install registers a program under the id blueprints and robots refer to.
// Installing over an existing id bumps its revision, so robots already running
// it pick the new rules up on their next tick.
func (rt *Runtime) Install(id string, p Program) {
	rt.programs[id] = installed{program: p, revision: rt.programs[id].revision + 1}
}

// Control resolves a robot's controller, as sim.World.Control requires. A robot
// whose program id or revision changed has been reprogrammed (design §4.2), so
// it gets a fresh evaluator and, per design §10.6, its coordinate memory wiped.
// A robot with no installed program returns nil and idles.
func (rt *Runtime) Control(r *sim.Robot) sim.Controller {
	cur, ok := rt.programs[r.ProgramID]
	if !ok {
		delete(rt.robots, r.ID)
		return nil
	}
	old, seen := rt.robots[r.ID]
	if seen && old.programID == r.ProgramID && old.revision == cur.revision {
		return old
	}
	if seen {
		ClearMemory(r)
	}
	e := New(cur.program)
	e.programID, e.revision = r.ProgramID, cur.revision
	rt.robots[r.ID] = e
	return e
}

// Trace returns a robot's last decision, or false if it has never decided.
func (rt *Runtime) Trace(robotID int) (Trace, bool) {
	e, ok := rt.robots[robotID]
	if !ok {
		return Trace{}, false
	}
	return e.Trace(), true
}

// Forget drops a destroyed robot's evaluator. Without it the runtime would grow
// for the length of a match, and a recycled id would inherit a dead robot's
// state. Assign it to sim.World.OnDestroy and the world calls it on every
// removal:
//
//	w.Control, w.OnDestroy = rt.Control, rt.Forget
func (rt *Runtime) Forget(robotID int) { delete(rt.robots, robotID) }
