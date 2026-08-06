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
// The bound is the init frame, which carries the whole buffer once per
// connection. Measured, not extrapolated — an extrapolation of this exact
// number was wrong by six times twice before (docs/engineering-notes.md), so
// the frame-size harness reports the feed's own share now:
//
//	ROBOCOLONY_FRAMESIZE=1 go test ./internal/server/ -run TestFrameSize -v
//
// Eight colonies, seed 0x5eed, at tick 6000:
//
//	                      events   feed bytes   init frame
//	default, M (64×64)       130       10,514       32,755
//	default, XL (128×128)    396       33,507       68,579
//	stressed, M              400       37,030       60,525
//
// The number that settled 400: an ordinary default match runs its whole 6000
// ticks and fills a third of the buffer, so nothing falls off a match anyone
// actually plays. The cap only bites on the largest arena or the stressed
// preset, where the init frame was the biggest thing on the connection anyway
// and the tick frame is the budget under pressure (maxFrameBytes), not this.
//
// ponytail: a plain ring — past the cap the oldest events fall off, so a very
// long match loses its early timeline. Upgrade path if the timeline needs the
// whole match: serve a tick range off a re-derived replay, which costs a
// rebuild rather than a bigger frame.
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
