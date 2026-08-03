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

	// watched is the trace history of the robots an observer has selected, and
	// only those (design §10.10, history.go). Empty in a match nobody is
	// inspecting, which is the normal case.
	watched map[int]*history
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
	return &Runtime{
		programs: map[string]installed{},
		robots:   map[int]*Evaluator{},
		watched:  map[int]*history{},
	}
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
	// A watch outlives the evaluator: a reprogrammed robot builds a new one
	// below, and the observer watching it must not silently stop recording. nil
	// for every unwatched robot, which is the whole fleet in most matches.
	h := rt.watched[r.ID]
	old, seen := rt.robots[r.ID]
	if seen && old.programID == r.ProgramID && old.revision == cur.revision {
		old.hist = h
		return old
	}
	if seen {
		ClearMemory(r)
	}
	e := New(cur.program)
	e.programID, e.revision, e.hist = r.ProgramID, cur.revision, h
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
//
// It also releases any retained trace history: a destroyed robot cannot be
// inspected further, and the ring is the one part of this runtime that is not
// a handful of bytes.
func (rt *Runtime) Forget(robotID int) {
	delete(rt.robots, robotID)
	delete(rt.watched, robotID)
}
