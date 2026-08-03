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
	programs map[string]Program
	robots   map[int]*Evaluator
}

// NewRuntime returns an empty runtime.
func NewRuntime() *Runtime {
	return &Runtime{programs: map[string]Program{}, robots: map[int]*Evaluator{}}
}

// Install registers a program under the id blueprints and robots refer to.
// Installing over an existing id takes effect on each robot's next tick.
func (rt *Runtime) Install(id string, p Program) { rt.programs[id] = p }

// Control resolves a robot's controller, as sim.World.Control requires. A robot
// whose ProgramID changed has been reprogrammed at its base (design §4.2), so
// it gets a fresh evaluator and, per design §10.6, its coordinate memory wiped.
// A robot with no installed program returns nil and idles.
func (rt *Runtime) Control(r *sim.Robot) sim.Controller {
	if e, ok := rt.robots[r.ID]; ok && e.programID == r.ProgramID {
		return e
	}
	p, ok := rt.programs[r.ProgramID]
	if !ok {
		delete(rt.robots, r.ID)
		return nil
	}
	if _, seen := rt.robots[r.ID]; seen {
		ClearMemory(r)
	}
	e := New(p)
	e.programID = r.ProgramID
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
// for the length of a match; E5.2 calls it when a robot is destroyed.
func (rt *Runtime) Forget(robotID int) { delete(rt.robots, robotID) }
