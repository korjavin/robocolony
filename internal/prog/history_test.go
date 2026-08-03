package prog

import (
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// watchProgram writes a memory point when it hears a signal and otherwise walks
// forward, so one program exercises every field of an Event.
func watchProgram() Program {
	return Program{V: SchemaVersion, Name: "watched", Rules: []Rule{
		{When: Pred(ReceivedComeHere), Then: []Action{DoArg(SaveSignalPosition, 2)}},
		{When: Pred(CarryingNothing), Then: []Action{Do(MoveForward)}},
	}}
}

// TestHistoryOnlyForWatchedRobots is the constraint the bead is about: a robot
// nobody selected records nothing, however long the match runs.
func TestHistoryOnlyForWatchedRobots(t *testing.T) {
	w := flatWorld(t, 3, 12)
	rt := NewRuntime()
	rt.Install("prog-scavenge", scavengerProgram())
	w.Control, w.OnDestroy = rt.Control, rt.Forget

	quiet := addRobot(w, sim.Coord{X: 3, Y: 3}, sim.North, scavengerBlueprint())
	seen := addRobot(w, sim.Coord{X: 5, Y: 5}, sim.North, scavengerBlueprint())
	rt.Watch(seen.ID, w.Tick)

	for i := 0; i < 30; i++ {
		w.Step()
	}

	if _, ok := rt.History(quiet.ID, 0); ok {
		t.Fatal("an unwatched robot accumulated trace history")
	}
	if e, ok := rt.robots[quiet.ID]; !ok || e.hist != nil {
		t.Fatal("an unwatched robot's evaluator holds a ring")
	}
	events, ok := rt.History(seen.ID, 0)
	if !ok || len(events) == 0 {
		t.Fatalf("watched robot recorded %d events, ok=%v", len(events), ok)
	}
	for i, e := range events {
		if i > 0 && e.Tick <= events[i-1].Tick {
			t.Fatalf("events not in tick order: %d after %d", e.Tick, events[i-1].Tick)
		}
	}
}

// TestHistoryRingIsBounded is the leak guard: a long match must not make the
// ring grow, and the oldest ticks must fall off the front.
func TestHistoryRingIsBounded(t *testing.T) {
	h := &history{}
	for i := 1; i <= HistoryTicks*3; i++ {
		h.add(Event{Trace: Trace{Tick: uint64(i)}})
	}
	got, want := h.since(0), HistoryTicks
	if len(got) != want {
		t.Fatalf("ring retained %d events, want %d", len(got), want)
	}
	if first := got[0].Tick; first != uint64(HistoryTicks*3-HistoryTicks+1) {
		t.Fatalf("oldest retained tick is %d, want %d", first, HistoryTicks*2+1)
	}
	if last := got[len(got)-1].Tick; last != uint64(HistoryTicks*3) {
		t.Fatalf("newest retained tick is %d, want %d", last, HistoryTicks*3)
	}
	// since() is what keeps a poll small: only the ticks the client lacks.
	if n := len(h.since(uint64(HistoryTicks*3 - 5))); n != 5 {
		t.Fatalf("since(newest-5) returned %d events, want 5", n)
	}
	if n := len(h.since(uint64(HistoryTicks * 3))); n != 0 {
		t.Fatalf("since(newest) returned %d events, want none", n)
	}
}

// TestHistoryRecordsWholeDecision covers what design §10.10 asks for: every
// rule that matched, the one that took the tick, and the memory change and the
// signal that caused it.
func TestHistoryRecordsWholeDecision(t *testing.T) {
	p := watchProgram()
	mustValidate(t, p, defenderBlueprint())
	e := New(p)
	e.hist = &history{}

	v := sim.RobotView{
		Tick:  41,
		Cargo: sim.VariantNone,
		Signals: []sim.Signal{
			{Kind: sim.ComeHere, From: 9, Coord: sim.Coord{X: 4, Y: 7}},
			{Kind: sim.AvoidHere, From: 11, Coord: sim.Coord{X: 1, Y: 1}},
		},
	}
	e.Decide(v)

	events, _ := (&Runtime{watched: map[int]*history{1: e.hist}}).History(1, 0)
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Tick != 41 {
		t.Fatalf("event tick %d, want 41", ev.Tick)
	}
	// Rule 1 fired as a pure side effect and evaluation continued; rule 2 took
	// the tick. Both must be visible, which is exactly what the live trace's
	// single Rule field cannot say.
	if !ev.Matched(0) || !ev.Matched(1) {
		t.Fatalf("matched bitset lost a rule: 0=%v 1=%v", ev.Matched(0), ev.Matched(1))
	}
	if ev.Rule != 1 || ev.Action != MoveForward {
		t.Fatalf("tick taken by rule %d action %q, want rule 1 move_forward", ev.Rule, ev.Action)
	}
	if w := ev.Writes[1]; !w.Written || w.Clear || w.Coord != (sim.Coord{X: 4, Y: 7}) {
		t.Fatalf("memory point 2 delta = %+v, want the come-here sender's position", w)
	}
	if ev.Writes[0].Written || ev.Writes[2].Written {
		t.Fatal("an untouched memory point was reported as written")
	}
	if ev.HeardN != 2 || ev.Heard[0].From != 9 || ev.Heard[1].From != 11 {
		t.Fatalf("signals recorded = %d %+v, want both", ev.HeardN, ev.Heard[:2])
	}
	if ev.Targeted {
		t.Fatalf("move_forward reported a target: %+v", ev.Target)
	}
}

// TestHistoryKeepsTheTarget: design §10.10 asks for the target, and it is only
// knowable at decision time — the memory point behind it can be overwritten and
// the sighting behind it can walk away.
func TestHistoryKeepsTheTarget(t *testing.T) {
	e := New(Program{V: SchemaVersion, Rules: []Rule{
		{When: PredArg(PointIsSet, 1), Then: []Action{DoArg(MoveToPoint, 1)}},
	}})
	e.hist = &history{}
	want := sim.Coord{X: 12, Y: 30}
	e.Decide(sim.RobotView{Tick: 4, Memory: [sim.MemPoints]sim.MemPoint{{Coord: want, Set: true}}})

	ev := e.hist.since(0)[0]
	if !ev.Targeted || ev.Target != want {
		t.Fatalf("target = %+v targeted=%v, want %+v", ev.Target, ev.Targeted, want)
	}
}

// TestHistorySurvivesReprogramming: Control builds a fresh evaluator on a
// program swap, and the observer watching that robot must keep recording.
func TestHistorySurvivesReprogramming(t *testing.T) {
	w := flatWorld(t, 5, 12)
	rt := NewRuntime()
	rt.Install("prog-scavenge", scavengerProgram())
	w.Control, w.OnDestroy = rt.Control, rt.Forget
	r := addRobot(w, sim.Coord{X: 4, Y: 4}, sim.North, scavengerBlueprint())
	rt.Watch(r.ID, w.Tick)

	for i := 0; i < 5; i++ {
		w.Step()
	}
	before, _ := rt.History(r.ID, 0)

	rt.Install("prog-scavenge", scavengerProgram()) // same id, new revision
	for i := 0; i < 5; i++ {
		w.Step()
	}
	after, _ := rt.History(r.ID, 0)
	if len(after) <= len(before) {
		t.Fatalf("recording stopped at the reprogram: %d events before, %d after",
			len(before), len(after))
	}
}

// TestWatchesAreCapped: the map itself is the other thing that could grow, so
// it has a hard ceiling and evicts the least recently refreshed watch.
func TestWatchesAreCapped(t *testing.T) {
	rt := NewRuntime()
	for i := 1; i <= MaxWatched*3; i++ {
		rt.Watch(i, uint64(i))
	}
	if n := len(rt.watched); n > MaxWatched {
		t.Fatalf("%d concurrent watches, cap is %d", n, MaxWatched)
	}
	if _, ok := rt.History(1, 0); ok {
		t.Fatal("the least recently refreshed watch was not evicted")
	}
	if _, ok := rt.History(MaxWatched*3, 0); !ok {
		t.Fatal("the newest watch was evicted")
	}
}

// TestWatchExpires: a browser tab that stops polling stops being recorded.
func TestWatchExpires(t *testing.T) {
	rt := NewRuntime()
	rt.Watch(1, 10)
	rt.Watch(2, 10+watchIdleTicks+1) // any new watch prunes the idle ones
	if _, ok := rt.History(1, 0); ok {
		t.Fatal("an idle watch outlived watchIdleTicks")
	}
	// A refreshed watch must not be pruned alongside it.
	rt.Watch(2, 10+watchIdleTicks+2)
	rt.Watch(3, 10+watchIdleTicks+2)
	if _, ok := rt.History(2, 0); !ok {
		t.Fatal("a freshly refreshed watch was pruned")
	}
}

// TestForgetReleasesHistory: a destroyed robot must not keep its ring alive.
func TestForgetReleasesHistory(t *testing.T) {
	rt := NewRuntime()
	rt.Watch(7, 0)
	rt.Forget(7)
	if _, ok := rt.History(7, 0); ok {
		t.Fatal("history survived the robot")
	}
}
