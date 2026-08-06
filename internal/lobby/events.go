package lobby

import (
	"cmp"
	"slices"

	"github.com/korjavin/robocolony/internal/sim"
)

// The match-wide event feed (rc-pt6.8). internal/sim emits one tick's events
// and keeps no history (internal/sim/events.go); this is where they accumulate
// for the length of a match, and it is the same shape as history.go beside it:
// observation, sampled off a world that is never written back to.
//
// Nothing here is persisted, and that is the design decision, not an omission.
// A match is stored as seed + command log and rebuilt by re-running Match.step
// (persist.go) — the same step that fills this buffer — so a restart, a
// restore, and a replay all re-derive the identical feed for free. Storing it
// as well would be a second copy of a derived thing that has to agree with the
// first, under a fingerprint that already refuses any log this build would
// simulate differently.
//
// The consequence to be honest about: a match whose log has gone stale under a
// new build keeps its standing and its graph (the stored summary) but loses its
// events along with its replay. rc-pt6.10 is the bead that puts losses and
// their attribution into the finished-match record for exactly that reason.

// eventCap is how many events one match keeps.
//
// ponytail: a plain ring — past the cap the oldest events fall off, so a long
// match loses its early timeline. The bound is the init frame, which carries
// the whole buffer once per connection: 400 events is about 28 KB of JSON,
// beside the 18-36 KB of terrain already on it and the ~58 KB ceiling
// history.go allows itself. Deposits dominate the feed (roughly one every two
// ticks on a default board), so 400 covers the last ~800 ticks of a busy match.
// Upgrade path if the timeline needs the whole match: serve a tick range from
// a re-derived replay rather than growing the frame.
const eventCap = 400

// collectEvents drains the tick's events into the match buffer. Caller holds mu.
func (m *Match) collectEvents() {
	m.events = append(m.events, m.world.Events()...)
	if n := len(m.events); n > eventCap {
		m.events = slices.Delete(m.events, 0, n-eventCap)
	}
}

// Events returns the buffered events stamped at or after since, oldest first.
// A copy, safe to marshal outside the match lock.
//
// since is a tick, so this is the queryable tick range the feed needs: Events(0)
// is the whole buffer for an init frame, and Events(t) is the tail a stream that
// has already sent everything before t still owes its client. Events of one tick
// are appended in a single critical section, so a cut can never split a tick.
func (m *Match) Events(since uint64) []sim.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, _ := slices.BinarySearchFunc(m.events, since, func(e sim.Event, t uint64) int {
		return cmp.Compare(e.Tick, t)
	})
	return slices.Clone(m.events[i:])
}
