package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// TestTraceOfStartsUnwatchedAndThenRecords is the contract the client relies
// on: nothing is retained until somebody asks, and asking is what turns
// recording on.
func TestTraceOfStartsUnwatchedAndThenRecords(t *testing.T) {
	h, m, owner, _ := twoColonies(t)
	robot := aRobot(t, m, colonyOf(t, m, owner))

	first, err := h.TraceOf(m.ID, robot, 0)
	if err != nil {
		t.Fatalf("TraceOf() = %v", err)
	}
	if first.Watching {
		t.Fatal("the robot was already being recorded before anyone asked")
	}
	if len(first.Events) != 0 {
		t.Fatalf("first poll returned %d events, want none", len(first.Events))
	}
	if first.Window != prog.HistoryTicks {
		t.Fatalf("window = %d, want %d", first.Window, prog.HistoryTicks)
	}

	// A second robot nobody asked about must still record nothing.
	other := 0
	m.Read(func(w *sim.World, _ *prog.Runtime) {
		for _, r := range w.Robots {
			if r.ID != robot {
				other = r.ID
				return
			}
		}
	})

	deadline := time.Now().Add(3 * time.Second)
	var got TraceHistory
	for time.Now().Before(deadline) {
		got, err = h.TraceOf(m.ID, robot, 0)
		if err != nil {
			t.Fatalf("TraceOf() = %v", err)
		}
		if len(got.Events) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !got.Watching || len(got.Events) == 0 {
		t.Fatalf("watched robot recorded nothing after 3s: watching=%v events=%d",
			got.Watching, len(got.Events))
	}
	for i, e := range got.Events {
		if i > 0 && e.Tick <= got.Events[i-1].Tick {
			t.Fatalf("events out of order: %d after %d", e.Tick, got.Events[i-1].Tick)
		}
		if e.MatchedTotal < len(e.Matched) {
			t.Fatalf("matched_total %d is below the %d indices sent", e.MatchedTotal, len(e.Matched))
		}
		if len(e.Matched) > maxMatchedShown {
			t.Fatalf("event carried %d rule indices, cap is %d", len(e.Matched), maxMatchedShown)
		}
	}

	if other != 0 {
		m.Read(func(_ *sim.World, rt *prog.Runtime) {
			if _, ok := rt.History(other, 0); ok {
				t.Error("a robot nobody asked about accumulated history")
			}
		})
	}

	// since= is what keeps a poll small: the client asks only for what it lacks.
	last := got.Events[len(got.Events)-1].Tick
	tail, err := h.TraceOf(m.ID, robot, last)
	if err != nil {
		t.Fatalf("TraceOf(since=%d) = %v", last, err)
	}
	for _, e := range tail.Events {
		if e.Tick <= last {
			t.Fatalf("since=%d returned tick %d", last, e.Tick)
		}
	}
}

// TestTraceOfUnknownTargets: a robot or a match that is not there is a 404, not
// a watch on nothing.
func TestTraceOfUnknownTargets(t *testing.T) {
	h, m, _, _ := twoColonies(t)

	if _, err := h.TraceOf(m.ID, 999999, 0); !isStatus(err, http.StatusNotFound) {
		t.Fatalf("TraceOf(unknown robot) = %v, want 404", err)
	}
	if _, err := h.TraceOf(m.ID+9999, 1, 0); !isStatus(err, http.StatusNotFound) {
		t.Fatalf("TraceOf(unknown match) = %v, want 404", err)
	}
}

func isStatus(err error, code int) bool {
	var ce cmdError
	return errors.As(err, &ce) && ce.code == code
}
