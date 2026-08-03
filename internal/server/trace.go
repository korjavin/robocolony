package server

import (
	"net/http"
	"strconv"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Trace history for a selected robot: design §10.10's "evaluated rules, chosen
// rule, target, memory changes, and received signals" for ticks that have
// already passed. The tick frame carries only the *current* decision, and by
// the time a player notices a robot misbehaving that decision is long gone.
//
// It is a separate polled endpoint rather than more fields on the tick frame,
// and that is the whole design:
//
//   - The tick frame is unchanged, byte for byte. E4.1 measured it at ~3.7 KB
//     and extrapolated ~45 KB at 8 colonies × 20 robots against a 64 KB warning
//     threshold; a per-robot rule bitset, memory delta and signal list on every
//     robot of every frame would have spent that headroom to serve the one
//     robot a player has actually selected.
//   - Asking is what starts recording. GET marks the robot watched (prog.Watch),
//     so the 159 robots nobody is looking at record nothing at all. A client
//     that closes its tab stops asking and the watch is evicted.
//   - The client sends the last tick it holds as ?since=, so a poll carries the
//     handful of ticks since the previous one rather than the whole window.
//
// Nothing here writes world state: a watch is not in sim.StateHash and not in
// the match's replay log, so a match observed and a match ignored simulate
// identically.

// TraceHistory is one robot's recent decisions, oldest first.
type TraceHistory struct {
	Robot  int    `json:"robot"`
	Tick   uint64 `json:"tick"`   // world tick this was read at
	Window int    `json:"window"` // how many ticks the server retains

	// Watching is false on the very first request for a robot: the watch was
	// created by that request, so there is nothing behind it yet. The client
	// renders "recording from here" rather than an empty list that looks broken.
	Watching bool         `json:"watching"`
	Events   []TraceEvent `json:"events"`
}

// TraceEvent is one tick of one robot's evaluation.
type TraceEvent struct {
	Tick   uint64 `json:"tick"`
	Rule   int    `json:"rule"`             // rule that took the tick, -1 when none did
	Action string `json:"action,omitempty"` // primary action, empty when idle
	Reason string `json:"reason,omitempty"`
	Idle   bool   `json:"idle,omitempty"`

	// Target is the cell the action aimed at, absent for the actions that do
	// not aim at one. Design §10.10 asks for it by name, and it cannot be
	// reconstructed later: the sighting or memory point it came from has moved
	// or been overwritten by then.
	Target *Point `json:"target,omitempty"`

	// Matched is every rule that matched this tick, 0-based and in order —
	// including side-effect-only rules that ran and let evaluation continue
	// (AGENTS.md action model), which Rule alone never names. Capped at
	// maxMatchedShown with MatchedTotal carrying the true count: a rule list
	// may hold prog.MaxRules entries and the reader can only take in a few.
	Matched      []int `json:"matched,omitempty"`
	MatchedTotal int   `json:"matched_total,omitempty"`

	Memory []MemChange `json:"memory,omitempty"`

	Signals      []SignalHeard `json:"signals,omitempty"`
	SignalsTotal int           `json:"signals_total,omitempty"`
}

// MemChange is one coordinate register written during the tick. Point is
// 1-based, the way the editor and the inspector number them.
type MemChange struct {
	Point   int  `json:"point"`
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Cleared bool `json:"cleared,omitempty"`
}

// SignalHeard is one signal delivered to the robot on that tick.
type SignalHeard struct {
	Kind string `json:"kind"`
	From int    `json:"from"` // sender robot id
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// maxMatchedShown bounds the rule indices one event puts on the wire.
const maxMatchedShown = 12

// signalNames maps sim.SignalKind onto the design §7.5 names the player sees.
// A table rather than a String method on the sim type: the wire format is this
// package's contract with the browser (see dto.go).
var signalNames = map[sim.SignalKind]string{
	sim.ComeHere:  "come here",
	sim.AvoidHere: "avoid here",
}

// TraceOf marks a robot watched and returns its retained decisions after the
// given tick. Watching is idempotent: every poll refreshes it, and a watch that
// stops being refreshed is dropped by the runtime.
func (h *Robots) TraceOf(matchID int64, robotID int, since uint64) (TraceHistory, error) {
	m, ok := h.reg.Get(matchID)
	if !ok {
		return TraceHistory{}, errf(http.StatusNotFound, "match not found")
	}
	out := TraceHistory{Robot: robotID, Window: prog.HistoryTicks}
	var fail error
	// Read, not Apply: a watch is observation. Putting it in the command log
	// would make a replayed match differ from the one that was played.
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		if w.RobotByID(robotID) == nil {
			fail = errf(http.StatusNotFound, "no robot %d in this match", robotID)
			return
		}
		out.Tick = w.Tick
		if rt == nil {
			return
		}
		events, watching := rt.History(robotID, since)
		out.Watching = watching
		for _, e := range events {
			out.Events = append(out.Events, traceEvent(e))
		}
		rt.Watch(robotID, w.Tick)
	})
	return out, fail
}

func traceEvent(e prog.Event) TraceEvent {
	out := TraceEvent{
		Tick: e.Tick, Rule: e.Rule, Action: string(e.Action),
		Reason: e.Reason, Idle: e.Idle,
	}
	if e.Targeted {
		out.Target = &Point{X: e.Target.X, Y: e.Target.Y}
	}
	for i := 0; i < prog.MaxRules; i++ {
		if !e.Matched(i) {
			continue
		}
		out.MatchedTotal++
		if len(out.Matched) < maxMatchedShown {
			out.Matched = append(out.Matched, i)
		}
	}
	for i, d := range e.Writes {
		if !d.Written {
			continue
		}
		out.Memory = append(out.Memory, MemChange{
			Point: i + 1, X: d.Coord.X, Y: d.Coord.Y, Cleared: d.Clear,
		})
	}
	out.SignalsTotal = e.HeardN
	for i := 0; i < e.HeardN && i < len(e.Heard); i++ {
		s := e.Heard[i]
		name, ok := signalNames[s.Kind]
		if !ok {
			name = "signal"
		}
		out.Signals = append(out.Signals, SignalHeard{
			Kind: name, From: s.From, X: s.Coord.X, Y: s.Coord.Y,
		})
	}
	return out
}

func (h *Robots) handleTrace(w http.ResponseWriter, r *http.Request) {
	_, matchID, robotID, err := commandTarget(r)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	// No colony check: design §4.3 gives the observer no fog of war, and the
	// stream already shows every colony's robots to every session.
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	out, err := h.TraceOf(matchID, robotID, since)
	if err != nil {
		writeCmdErr(w, r, err)
		return
	}
	writeCmdJSON(w, http.StatusOK, out)
}
