package lobby

import (
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// TestHistorySamplesOnInterval pins the sampling rule on the live path: a real
// match samples at tick 0 and then once per historyEvery ticks, and nowhere in
// between.
func TestHistorySamplesOnInterval(t *testing.T) {
	m := testMatch(t, shortSettings(120), 2)

	h := m.History()
	if len(h.Ticks) != 1 || h.Ticks[0] != 0 {
		t.Fatalf("history at tick 0 = %v, want exactly [0]", h.Ticks)
	}
	if h.Interval != historyEvery {
		t.Fatalf("history.Interval = %d, want %d", h.Interval, historyEvery)
	}
	if len(h.Colonies) != len(m.Colonies) {
		t.Fatalf("history has %d colonies, want %d", len(h.Colonies), len(m.Colonies))
	}

	for i := 0; i < 2*historyEvery+5; i++ {
		if !m.step() {
			t.Fatalf("match ended at tick %d", i)
		}
	}
	h = m.History()
	want := []uint64{0, historyEvery, 2 * historyEvery}
	if len(h.Ticks) != len(want) {
		t.Fatalf("history ticks = %v, want %v", h.Ticks, want)
	}
	for i, w := range want {
		if h.Ticks[i] != w {
			t.Fatalf("history ticks = %v, want %v", h.Ticks, want)
		}
	}
	for _, c := range h.Colonies {
		if len(c.Score) != len(h.Ticks) || len(c.Robots) != len(h.Ticks) || len(c.Collected) != len(h.Ticks) {
			t.Fatalf("colony %d series lengths %d/%d/%d, want %d",
				c.Colony, len(c.Score), len(c.Robots), len(c.Collected), len(h.Ticks))
		}
		if c.Score[0] <= 0 || c.Robots[0] <= 0 {
			t.Errorf("colony %d starts with score %d and %d robots, want a fielded kit",
				c.Colony, c.Score[0], c.Robots[0])
		}
	}
}

// TestHistoryIsBounded is the memory bound, exercised through the real sampler
// rather than through a copy of its rules: however long the match runs, the
// series never exceeds historyCap, and it still spans the whole match at an
// interval the samples are actually spaced on.
//
// The world is a bare one stepped by hand — the sampler only reads Tick, Robots
// and Bases, and 200000 real ticks would be a minute of test time to prove
// arithmetic.
func TestHistoryIsBounded(t *testing.T) {
	m := &Match{
		world:   &sim.World{Bases: []*sim.Base{{Colony: 0, Inventory: map[sim.Variant]int{}}}},
		history: History{Interval: historyEvery, Colonies: []ColonyHistory{{Colony: 0}}},
	}
	const ticks = 200_000 // ~5.5 hours of match time, far past any usual match
	for tick := uint64(0); tick <= ticks; tick++ {
		m.world.Tick = tick
		m.sample()
		if n := len(m.history.Ticks); n > historyCap {
			t.Fatalf("history holds %d samples at tick %d, cap is %d", n, tick, historyCap)
		}
	}

	h := m.history
	if h.Ticks[0] != 0 {
		t.Errorf("history starts at tick %d, want the start of the match", h.Ticks[0])
	}
	if last := h.Ticks[len(h.Ticks)-1]; last < ticks-h.Interval {
		t.Errorf("history ends at tick %d, want within one interval of %d", last, ticks)
	}
	for i := 1; i < len(h.Ticks); i++ {
		if h.Ticks[i]-h.Ticks[i-1] != h.Interval {
			t.Fatalf("samples %d and %d are %d ticks apart, interval is %d",
				i-1, i, h.Ticks[i]-h.Ticks[i-1], h.Interval)
		}
	}
	for _, c := range h.Colonies {
		if len(c.Score) != len(h.Ticks) {
			t.Errorf("colony %d has %d scores for %d ticks", c.Colony, len(c.Score), len(h.Ticks))
		}
	}
}
