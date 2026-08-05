package lobby

import (
	"slices"

	"github.com/korjavin/robocolony/internal/sim"
)

// History is a match's telemetry sampled over time: the score-over-time graph
// of design §4.4 ("production history"), and the only thing in the wire format
// that answers "who was ahead, and when did that change".
//
// Three constraints shaped it.
//
//   - It rides the init frame, never the tick frame. E4.1 measured ~3.7 KB per
//     tick against a 64 KB warn threshold, ten times a second; a growing series
//     on that frame would blow it. On the init frame it is sent once per
//     connection, which is also what makes a page reload and a late joiner both
//     get the history the client could not have observed.
//   - It is bounded by historyCap, a constant, not by match length. When the
//     series fills, decimate drops every second sample and doubles Interval:
//     the whole match stays in view at half the resolution, rather than the
//     start of it falling out of a ring.
//   - It is observation, not state. Sampling reads the world, never writes it,
//     and never touches the rng — so it is invisible to sim.StateHash and to the
//     determinism guard. A replay rebuilds it because it rebuilds every tick
//     (persist.go), not because anything about it is persisted.
type History struct {
	Interval uint64          `json:"interval"` // ticks between two samples
	Ticks    []uint64        `json:"ticks"`    // sample ticks, ascending
	Colonies []ColonyHistory `json:"colonies"` // one per seat, in colony order
}

// ColonyHistory is one colony's series. Each slice is parallel to History.Ticks.
//
// Parallel arrays rather than a struct per point: the JSON is a fraction of the
// size with no repeated keys, and the client feeds one of them straight into an
// SVG polyline.
type ColonyHistory struct {
	Colony sim.ColonyID `json:"colony"`
	// Score is the design §9 score (World.Score), the standing itself.
	Score []int `json:"score"`
	// Robots is the live robot count: the fight, as opposed to its result.
	Robots []int `json:"robots"`
	// Collected is components banked in the base, cumulative: the economy.
	Collected []int `json:"collected"`
}

const (
	// historyEvery is the base sampling interval: one sample per ten seconds of
	// match time. Sampling every tick would be 36000 points for an hour-long
	// match and tell nobody anything a graph 600 pixels wide can show.
	historyEvery = 10 * TickRate

	// historyCap is the most samples kept, ever. At historyEvery it covers a
	// 30000-tick (50 minute) match outright; past that the series decimates, so
	// a 7200s match — 72000 ticks — costs at most two decimations and lands at
	// 40 seconds per sample. Duration has no ceiling; longer just decimates more.
	//
	// The bound: (historyCap+1) samples x maxPlayers colonies x 3 int series is
	// about 58 KB per match, plus 2.4 KB of ticks, and it does not grow with
	// match length.
	historyCap = 300
)

// sample appends one point to the history if this tick is a sampling tick.
// Caller holds mu.
func (m *Match) sample() {
	h := &m.history
	if h.Interval == 0 || m.world.Tick%h.Interval != 0 {
		return
	}
	h.Ticks = append(h.Ticks, m.world.Tick)
	for i := range h.Colonies {
		c := &h.Colonies[i]
		robots := 0
		for _, r := range m.world.Robots {
			if r.Colony == c.Colony {
				robots++
			}
		}
		collected := 0
		for _, b := range m.world.Bases {
			if b.Colony == c.Colony {
				collected = b.Stats.Collected
			}
		}
		c.Score = append(c.Score, m.world.Score(c.Colony))
		c.Robots = append(c.Robots, robots)
		c.Collected = append(c.Collected, collected)
	}
	if len(h.Ticks) > historyCap {
		h.decimate()
	}
}

// decimate halves the resolution in place: every second sample is dropped and
// the interval doubles. The kept samples stay evenly spaced on the new interval,
// so the next sample lands in step with them.
func (h *History) decimate() {
	h.Interval *= 2
	h.Ticks = everySecond(h.Ticks)
	for i := range h.Colonies {
		c := &h.Colonies[i]
		c.Score = everySecond(c.Score)
		c.Robots = everySecond(c.Robots)
		c.Collected = everySecond(c.Collected)
	}
}

// everySecond keeps elements 0, 2, 4, … in place. It reuses the backing array:
// the series never reallocates past the size it reached at the cap.
func everySecond[T any](s []T) []T {
	out := s[:0]
	for i := 0; i < len(s); i += 2 {
		out = append(out, s[i])
	}
	return out
}

// History returns a copy of the sampled telemetry, safe to marshal outside the
// match lock. It is a deep copy: the series keep growing under the tick driver.
func (m *Match) History() History {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.history
	h.Ticks = slices.Clone(h.Ticks)
	h.Colonies = slices.Clone(h.Colonies)
	for i := range h.Colonies {
		c := &h.Colonies[i]
		c.Score = slices.Clone(c.Score)
		c.Robots = slices.Clone(c.Robots)
		c.Collected = slices.Clone(c.Collected)
	}
	return h
}
