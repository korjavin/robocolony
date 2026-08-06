package sim

import (
	"math/rand"
	"reflect"
	"testing"
)

// stepAndCollect steps the world n times and returns every event emitted.
//
// It also checks the stamping invariant on the way, once for every event any
// test in this file produces: an event is stamped with the tick the world was
// standing on while it happened, which is one less than the Tick of the
// snapshot that shows the result. Get that wrong and every timeline mark in the
// UI sits one tick off its cause.
func stepAndCollect(t *testing.T, w *World, n int) []Event {
	t.Helper()
	var out []Event
	for range n {
		at := w.Tick
		w.Step()
		for _, e := range w.Events() {
			if e.Tick != at {
				t.Fatalf("a %s event is stamped tick %d but was emitted during tick %d", e.Kind, e.Tick, at)
			}
			out = append(out, e)
		}
	}
	return out
}

// ofKind filters a feed and blanks Tick, which stepAndCollect has already
// checked and which a table cannot state without pinning a balance number.
func ofKind(evs []Event, k EventKind) []Event {
	var out []Event
	for _, e := range evs {
		if e.Kind == k {
			e.Tick = 0
			out = append(out, e)
		}
	}
	return out
}

// TestEventFeed is rc-pt6.8's acceptance at the point of emission: the sim
// reports what it already knows, with enough identity on it that a timeline can
// name the robot — and, for a loss, the robot that did it.
func TestEventFeed(t *testing.T) {
	gunner, scav := gunnerBlueprint(), scavengerBlueprint()

	tests := []struct {
		name  string
		kind  EventKind
		ticks int
		setup func() *World
		want  []Event
	}{
		{
			// The attribution case the bead calls out: kill credit is a scalar
			// counter on Stats, and this is the same fact with both identities
			// still attached.
			name: "a loss names the victim and the robot that killed it",
			kind: EventLoss, ticks: 60,
			setup: func() *World {
				w := arena(12)
				w.rng = rand.New(rand.NewSource(4))
				w.addRobot(0, Coord{3, 5}, East, gunner)           // id 1
				victim := w.addRobot(1, Coord{6, 5}, West, gunner) // id 2
				// One hit ends it, so the fight resolves inside the window
				// whatever the accuracy rolls do.
				victim.Health = 1
				w.driveAll(duellist)
				return w
			},
			want: []Event{{
				Kind: EventLoss, Colony: 1, Robot: 2, Blueprint: gunner.ID,
				Attacker: 1, AttackerColony: 0,
			}},
		},
		{
			name: "a build names the robot the base released",
			kind: EventBuild, ticks: 80,
			setup: func() *World {
				w := arena(12)
				b := w.addBase(0, Coord{5, 5})
				b.Blueprints = []Blueprint{scav}
				for _, v := range scav.Components {
					b.Inventory[v]++
				}
				return w
			},
			want: []Event{{Kind: EventBuild, Colony: 0, Robot: 1, Blueprint: scav.ID}},
		},
		{
			name: "a deposit names the robot that banked it",
			kind: EventDeposit, ticks: 4,
			setup: func() *World {
				w := arena(12)
				w.addBase(1, Coord{5, 5})
				r := w.addRobot(1, Coord{5, 5}, North, scav)
				r.Cargo = Laser
				w.driveAll(funcController(func(RobotView) Action { return Action{Kind: ActDeposit} }))
				return w
			},
			// One event, not four: the second attempt has nothing to carry, and
			// a deposit that does nothing must not mark the timeline.
			want: []Event{{Kind: EventDeposit, Colony: 1, Robot: 1, Blueprint: scav.ID}},
		},
		{
			// Ten a second for the rest of the match is not a feed, it is a
			// flood: the stall is an edge.
			name: "an idle stall is reported once, not every tick",
			kind: EventIdle, ticks: 20,
			setup: func() *World {
				w := arena(12)
				w.addBase(3, Coord{5, 5}) // nothing approved, nothing in stock
				return w
			},
			want: []Event{{Kind: EventIdle, Colony: 3}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ofKind(stepAndCollect(t, tc.setup(), tc.ticks), tc.kind)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s events =\n%+v\nwant\n%+v", tc.kind, got, tc.want)
			}
		})
	}
}

// TestIdleStallRearms: a base that gets back to work stops being stalled, so
// the next stall is reported again. Without the reset a match would show its
// first stall and never another — and the interesting stall is the one that
// happens after the colony was rebuilding fine.
func TestIdleStallRearms(t *testing.T) {
	scav := scavengerBlueprint()
	w := arena(12)
	b := w.addBase(0, Coord{5, 5})
	b.Blueprints = []Blueprint{scav}

	if got := ofKind(stepAndCollect(t, w, 3), EventIdle); len(got) != 1 {
		t.Fatalf("a base approved but unstocked emitted %d idle events, want 1", len(got))
	}
	// Exactly one build's worth of parts: the base works, then runs dry again.
	for _, v := range scav.Components {
		b.Inventory[v]++
	}
	got := stepAndCollect(t, w, buildTicks(scav)+3)
	if n := len(ofKind(got, EventBuild)); n != 1 {
		t.Fatalf("the stocked base emitted %d build events, want 1", n)
	}
	if n := len(ofKind(got, EventIdle)); n != 1 {
		t.Fatalf("the base emitted %d idle events after running dry again, want 1", n)
	}
}

// TestEventsAreNotState states the rc-pt6.8 decision as a test: events are
// observation, so they are deliberately absent from StateHash, exactly like
// traces, the sampled history and IdleReason. The corollary in
// docs/engineering-notes.md — "any new mutable field needs a StateHash entry" —
// is answered here rather than in TestStateHashCoversState, which is the list of
// fields that must be *in* the hash.
//
// Nothing about a robot's fate depends on whether an event describing it exists,
// so two worlds that agree on state but not on their event buffers are the same
// world, and a replay that rebuilt the feed differently would still be faithful.
func TestEventsAreNotState(t *testing.T) {
	w := sampleWorld(t, 42)
	w.Step()
	before := w.StateHash()

	w.events = append(w.events, Event{Kind: EventLoss, Robot: 99, Attacker: 98})
	w.Bases[0].stalled = !w.Bases[0].stalled

	if after := w.StateHash(); after != before {
		t.Fatalf("the event buffer moved the state hash: %#x != %#x", after, before)
	}
	// And the buffer is one tick deep: a match-length feed belongs to the layer
	// that owns the match (internal/lobby/events.go), not to a world.
	w.Step()
	for _, e := range w.Events() {
		if e.Tick != w.Tick-1 {
			t.Fatalf("Step did not reset the buffer: %+v survived into tick %d", e, w.Tick)
		}
	}
}

// TestEventsCopyOut: Events hands out a copy. The buffer's backing array is
// reused every tick, and a caller holding the live slice would find it
// rewritten under them — the aliasing bug RobotView.Blueprint.Components
// already paid for once (docs/engineering-notes.md).
func TestEventsCopyOut(t *testing.T) {
	w := arena(12)
	w.addBase(0, Coord{5, 5})
	w.Step()
	held := w.Events()
	if len(held) == 0 {
		t.Fatal("the unstocked base emitted no idle event to hold on to")
	}
	first := held[0]
	for range 5 {
		w.Step()
	}
	if held[0] != first {
		t.Fatalf("a held event changed under the caller: %+v became %+v", first, held[0])
	}
}
