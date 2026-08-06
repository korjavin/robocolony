package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

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

// TestStreamDeliversEveryEventOnce is the delivery invariant of the feed, and
// the check behind the fix the codex review pass found: the init frame carries
// the backlog and the tick frames carry the rest, with nothing sent twice and —
// the part that is easy to get wrong — nothing skipped.
//
// The trap: the feed and the board are two reads of the match lock, and the
// tick driver can advance the world between them. A cursor set from the board's
// tick would then declare the events of the ticks in between already sent, and
// they would never reach this client. Silent, and invisible to any assertion
// that only looks at what did arrive — so this compares the delivered feed
// against the match's own buffer over the ticks that were fully delivered.
func TestStreamDeliversEveryEventOnce(t *testing.T) {
	reg, m := startMatch(t)

	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", Stream(reg, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/matches/"+strconv.FormatInt(m.ID, 10)+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	frames := readEvents(t, resp.Body, 25)
	var got []Event
	for i, f := range frames {
		switch f.name {
		case "init":
			var in Init
			if err := json.Unmarshal(f.data, &in); err != nil {
				t.Fatalf("init frame does not decode: %v", err)
			}
			checkPhase(t, in.Tick, in.Events)
			got = append(got, in.Events...)
		case "tick":
			var snap Snapshot
			if err := json.Unmarshal(f.data, &snap); err != nil {
				t.Fatalf("tick frame does not decode: %v", err)
			}
			checkPhase(t, snap.Tick, snap.Events)
			got = append(got, snap.Events...)
		default:
			t.Fatalf("frame %d is %q, want init or tick", i, f.name)
		}
	}
	if len(got) == 0 {
		t.Skip("this match produced no events in the window watched; nothing to compare")
	}
	for i := 1; i < len(got); i++ {
		if got[i].Tick < got[i-1].Tick {
			t.Fatalf("delivered out of order at %d: tick %d after %d", i, got[i].Tick, got[i-1].Tick)
		}
	}

	// Ground truth. Every event of a tick is buffered in one critical section,
	// so a tick that delivered anything delivered all of it: everything the
	// match holds up to the last delivered tick had to arrive, in order, once.
	last := got[len(got)-1].Tick
	var buffered []sim.Event
	for _, e := range m.Events(0) {
		if e.Tick <= last {
			buffered = append(buffered, e)
		}
	}
	want := newEvents(buffered)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the stream delivered %d events up to tick %d, the match holds %d:\ngot  %+v\nwant %+v",
			len(got), last, len(want), got, want)
	}
}

// checkPhase: an event is stamped with the tick it happened *during*, so a
// frame may only carry events from ticks it has already advanced past. One that
// rode a frame too early would mark the timeline against a board that does not
// show the loss yet — and would then be missing from the frame that does.
func checkPhase(t *testing.T, frameTick uint64, evs []Event) {
	t.Helper()
	for _, e := range evs {
		if e.Tick >= frameTick {
			t.Fatalf("a frame at tick %d carries a %s event stamped tick %d, which it does not show yet",
				frameTick, e.Kind, e.Tick)
		}
	}
}
