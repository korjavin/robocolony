package prog

import "github.com/korjavin/robocolony/internal/sim"

// Trace history for one robot (design §10.10). Trace itself is a fixed-size
// struct overwritten every tick, which is what keeps a 72000-tick match from
// leaking; this file adds the *past* without giving that up.
//
// Two properties do all the work here:
//
//   - Bounded. A watch is a fixed-size ring of HistoryTicks events, and at most
//     MaxWatched robots may be watched at once. The whole feature's memory is
//     therefore a constant, not a function of match length or fleet size.
//   - Opt-in. Nothing is recorded until someone calls Watch, so the 159 robots
//     nobody has selected cost exactly what they cost today: one Trace each.
//
// A watch is observation, never world state. It is not in sim.StateHash, it is
// not in the match's replay log, and Decide's behaviour does not change by one
// branch when a robot is watched — Event is filled from values Decide already
// computed.

const (
	// HistoryTicks is how far back a watched robot remembers: 128 ticks is 12.8
	// seconds at the locked 10Hz. The point is answering "why did it do that"
	// about a moment that has just scrolled past, not archiving a match.
	HistoryTicks = 128

	// MaxWatched caps concurrent watches per runtime. A match seats at most 8
	// players and a player inspects one robot at a time, so this is headroom,
	// and it is the ceiling on this feature's memory: MaxWatched * HistoryTicks
	// events, a few hundred KB, whatever the fleet does.
	MaxWatched = 8

	// watchIdleTicks is how long a watch outlives its last refresh. The client
	// re-asserts its watch on every poll, so a browser tab that closes stops
	// being recorded a few seconds later without needing to say goodbye.
	watchIdleTicks = 100

	// maxHeard is how many of a tick's signals an Event keeps verbatim. The
	// friendly channel is global (AGENTS.md), so a colony of 20 broadcasting
	// robots can deliver 20 signals in one tick; Event stores the first few and
	// counts the rest, which keeps it fixed-size.
	maxHeard = 4
)

// MemDelta is one memory point's change during a tick, or its absence.
type MemDelta struct {
	Written bool      // the tick wrote this point
	Clear   bool      // ... and the write was clear_point
	Coord   sim.Coord // ... otherwise, the coordinate stored
}

// Event is one watched tick: the whole decision, not just its outcome.
//
// Fixed size on purpose. Memory changes collapse to the final state of each of
// the three points — a later write to a point supersedes an earlier one, so the
// last one is what the robot actually ended up holding — and signals are capped
// with a total count, so neither can grow with the program's shape.
type Event struct {
	Trace                          // decision, including the matched-rule bitset
	Writes [sim.MemPoints]MemDelta // memory changes this tick
	Heard  [maxHeard]sim.Signal    // signals received, first maxHeard
	HeardN int                     // signals received in total

	// Target is the cell a navigation or attack action aimed at, and Targeted
	// says whether there was one — 0,0 is a real cell. Design §10.10 asks for
	// the target by name, and this is the only place it still exists: the
	// action resolves it from a sighting or a memory point that will have moved
	// or been overwritten by the time anyone reads the history back.
	Target   sim.Coord
	Targeted bool
}

// history is one robot's ring. Writes are O(1) and allocate nothing after the
// first Watch.
type history struct {
	buf  [HistoryTicks]Event
	n    uint64 // events ever appended; n-1 indexes the newest
	seen uint64 // tick of the last Watch call, for idle eviction
}

func (h *history) add(e Event) {
	h.buf[h.n%HistoryTicks] = e
	h.n++
}

// since returns the retained events with Tick > after, oldest first.
func (h *history) since(after uint64) []Event {
	first := uint64(0)
	if h.n > HistoryTicks {
		first = h.n - HistoryTicks
	}
	out := make([]Event, 0, HistoryTicks)
	for i := first; i < h.n; i++ {
		if e := h.buf[i%HistoryTicks]; e.Tick > after {
			out = append(out, e)
		}
	}
	return out
}

// Watch starts or refreshes recording for one robot, at the world's current
// tick. Calling it repeatedly is how a client says "I am still looking at
// this"; a watch that stops being refreshed is evicted.
//
// Callers must hold whatever lock guards the runtime — internal/lobby's
// Match.Read, in this server.
func (rt *Runtime) Watch(robotID int, tick uint64) {
	if h, ok := rt.watched[robotID]; ok {
		h.seen = tick
		return
	}
	rt.evict(tick)
	h := &history{seen: tick}
	rt.watched[robotID] = h
	// Attach to the running evaluator too, so recording starts on the very next
	// tick rather than after Control next rebuilds one.
	if e, ok := rt.robots[robotID]; ok {
		e.hist = h
	}
}

// evict makes room for one more watch: idle watches first, and if none are
// idle and the map is full, the least recently refreshed. Ties break on robot
// id so the choice does not depend on map order — nothing here reaches world
// state, but a deterministic runtime is easier to reason about than one that
// is only accidentally deterministic.
func (rt *Runtime) evict(now uint64) {
	for id, h := range rt.watched {
		// Written this way round rather than now-h.seen: tick is unsigned, and a
		// watch refreshed on a match that has since been rewound or frozen would
		// underflow into "idle for four billion ticks".
		if h.seen+watchIdleTicks < now {
			rt.drop(id)
		}
	}
	if len(rt.watched) < MaxWatched {
		return
	}
	oldest, at := -1, uint64(0)
	for id, h := range rt.watched {
		if oldest < 0 || h.seen < at || (h.seen == at && id < oldest) {
			oldest, at = id, h.seen
		}
	}
	if oldest >= 0 {
		rt.drop(oldest)
	}
}

// drop stops recording a robot and releases its ring.
func (rt *Runtime) drop(robotID int) {
	delete(rt.watched, robotID)
	if e, ok := rt.robots[robotID]; ok {
		e.hist = nil
	}
}

// History returns a watched robot's retained events with a tick greater than
// after, oldest first, and whether the robot is being recorded at all. An
// unwatched robot returns (nil, false) — the caller asked for history that was
// never kept, which is not the same as a robot that has done nothing.
func (rt *Runtime) History(robotID int, after uint64) ([]Event, bool) {
	h, ok := rt.watched[robotID]
	if !ok {
		return nil, false
	}
	return h.since(after), true
}

// record appends one tick to the ring, if this evaluator is being watched.
// Called at the end of Decide, from values it already has.
func (e *Evaluator) record(tr Trace, v sim.RobotView, act sim.Action) {
	if e.hist == nil {
		return
	}
	ev := Event{
		Trace:    tr,
		HeardN:   len(v.Signals),
		Targeted: act.Kind == sim.ActMoveTo || act.Kind == sim.ActAttack,
	}
	if ev.Targeted {
		ev.Target = act.Coord
	}
	for _, w := range act.Memory {
		if w.Point < 0 || w.Point >= sim.MemPoints {
			continue
		}
		ev.Writes[w.Point] = MemDelta{Written: true, Clear: w.Clear, Coord: w.Coord}
	}
	for i := 0; i < len(v.Signals) && i < maxHeard; i++ {
		ev.Heard[i] = v.Signals[i]
	}
	e.hist.add(ev)
}
