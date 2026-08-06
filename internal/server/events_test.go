package server

import (
	"encoding/json"
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// TestEventWireShape pins the one branch in the projection: a loss carries an
// attacker object and nothing else does. The nesting exists because colony 0 is
// a real colony, so a flat attacker_colony has no value that could mean "none"
// — and a renderer that read a zero there would attribute every build and
// deposit to the first seat.
func TestEventWireShape(t *testing.T) {
	tests := []struct {
		name string
		in   sim.Event
		want string
	}{
		{
			name: "a loss carries its attacker",
			in: sim.Event{
				Tick: 41, Kind: sim.EventLoss, Colony: 1, Robot: 7, Blueprint: "bp-gunner",
				Attacker: 3, AttackerColony: 0,
			},
			want: `{"tick":41,"kind":"loss","colony":1,"robot":7,"blueprint":"bp-gunner","attacker":{"robot":3,"colony":0}}`,
		},
		{
			// Attacker and AttackerColony are zero on every other kind, and
			// colony 0 must not surface as an attacker because of it.
			name: "a build carries no attacker",
			in:   sim.Event{Tick: 12, Kind: sim.EventBuild, Colony: 0, Robot: 4, Blueprint: "bp-scavenger"},
			want: `{"tick":12,"kind":"build","colony":0,"robot":4,"blueprint":"bp-scavenger"}`,
		},
		{
			// A base is not a robot: no id, no design.
			name: "an idle stall is a base-level event",
			in:   sim.Event{Tick: 300, Kind: sim.EventIdle, Colony: 2},
			want: `{"tick":300,"kind":"idle","colony":2}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(newEvents([]sim.Event{tc.in})[0])
			if err != nil {
				t.Fatalf("Marshal() = %v", err)
			}
			if string(body) != tc.want {
				t.Fatalf("event JSON =\n%s\nwant\n%s", body, tc.want)
			}
		})
	}
}

// An empty feed must not put an events key on the tick frame at all: the frame
// goes out ten times a second against the maxFrameBytes budget, and most ticks
// have nothing to report.
func TestEmptyFeedIsAbsentFromTheTickFrame(t *testing.T) {
	body, err := json.Marshal(Snapshot{Tick: 1, Events: newEvents(nil)})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal() = %v", err)
	}
	if _, ok := decoded["events"]; ok {
		t.Fatalf("an empty feed still put an events key on the tick frame: %s", body)
	}
}
