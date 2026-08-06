package lobby

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/sim"
)

// eventfulMatch runs a match far enough that its feed has something in it —
// builds, deposits, and with hunters seated the occasional loss — and saves the
// replay record, which is all a restart or a replay ever gets.
func eventfulMatch(t *testing.T, svc *Service, lobby db.Lobby, set Settings, members []db.Member, ticks int) *Match {
	t.Helper()
	m, err := newMatch(lobby, set, members)
	if err != nil {
		t.Fatalf("newMatch() = %v", err)
	}
	for range ticks {
		if !m.step() {
			t.Fatalf("match ended early at tick %d", m.world.Tick)
		}
	}
	svc.save(m)
	return m
}

// eventsBefore keeps the events stamped before tick, so a feed can be compared
// against one whose match is still ticking.
func eventsBefore(evs []sim.Event, tick uint64) []sim.Event {
	var out []sim.Event
	for _, e := range evs {
		if e.Tick < tick {
			out = append(out, e)
		}
	}
	return out
}

// hunterSettings seats two aggressive colonies, so the feed has a chance of
// carrying losses as well as economy events.
func hunterSettings() Settings {
	set := shortSettings(600)
	set.AI = []Profile{ProfileAggressive, ProfileAggressive}
	set.MaxPlayers = maxPlayers - len(set.AI)
	return set
}

// checkFeed asserts a feed is worth comparing at all, and that any loss in it
// carries an attacker who is not the victim's own colony. The attribution
// assertion is conditional because the seed is drawn per run and a short match
// need not contain a fight; internal/sim's TestEventFeed is where attribution
// is proven unconditionally.
func checkFeed(t *testing.T, evs []sim.Event) {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("the match produced no events at all: nothing was compared")
	}
	kinds := map[sim.EventKind]int{}
	for _, e := range evs {
		kinds[e.Kind]++
		if e.Kind != sim.EventLoss {
			continue
		}
		if e.Attacker == 0 {
			t.Errorf("loss of robot %d at tick %d has no attacker", e.Robot, e.Tick)
		}
		if e.AttackerColony == e.Colony {
			t.Errorf("loss of robot %d at tick %d is attributed to its own colony %d",
				e.Robot, e.Tick, e.Colony)
		}
	}
	t.Logf("feed: %d events, %d losses, %d builds, %d deposits, %d stalls", len(evs),
		kinds[sim.EventLoss], kinds[sim.EventBuild], kinds[sim.EventDeposit], kinds[sim.EventIdle])
}

// TestEventsAreIdenticalLiveAndReplayed is rc-pt6.8's acceptance criterion: a
// running match and its replay report the same events over the same tick range.
//
// Nothing about the feed is stored — the record on disk is still seed plus
// command log. This passes because a replay re-runs Match.step, which is the
// same step that fills the feed, so the events are re-derived rather than
// restored. That is the whole argument for not persisting them, and this is
// what would fail if the feed ever depended on something a replay does not
// reproduce (wall time, a map walk, a counter that survives a process).
func TestEventsAreIdenticalLiveAndReplayed(t *testing.T) {
	svc, database := newService(t)
	set := hunterSettings()
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	live := eventfulMatch(t, svc, lobby, set, members, 900)

	rec, err := database.MatchLogByID(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v", err)
	}
	restored, err := replay(t.Context(), lobby, set, members, rec)
	if err != nil {
		t.Fatalf("replay() = %v", err)
	}

	want, got := live.Events(0), restored.Events(0)
	checkFeed(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the replayed feed has %d events, the live one %d; first difference at %s",
			len(got), len(want), firstDiff(got, want))
	}
}

// TestEventsSurviveARestart is the other half: a match whose process died comes
// back with its feed. Through the real Restore path, so what is being tested is
// the thing a deploy does, not a helper.
func TestEventsSurviveARestart(t *testing.T) {
	svc, database := newService(t)
	set := hunterSettings()
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed

	live := eventfulMatch(t, svc, lobby, set, members, 900)
	liveTick := live.Info().Tick

	restarted := New(database)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	// Restore puts a tick driver behind the match, so freeze it before reading:
	// otherwise the restored feed keeps growing past the live one while the
	// comparison is being made.
	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := restarted.Shutdown(stop); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	m, ok := restarted.Registry().Get(lobby.ID)
	if !ok {
		t.Fatalf("match %d is not running after Restore", lobby.ID)
	}

	want := eventsBefore(live.Events(0), liveTick)
	got := eventsBefore(m.Events(0), liveTick)
	checkFeed(t, want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the restarted feed has %d events before tick %d, the live one %d; first difference at %s",
			len(got), liveTick, len(want), firstDiff(got, want))
	}
}

// TestEventBufferIsBounded: the feed rides the init frame, so it cannot grow
// with match length. Past the cap the oldest events fall off and the newest
// survive — the opposite of a ring that drops what just happened.
func TestEventBufferIsBounded(t *testing.T) {
	m := testMatch(t, shortSettings(600), 2)
	// Filled directly rather than simulated: reaching the cap through the
	// economy is thousands of ticks of a busy match, and what is under test is
	// the trim and the tick-range query, not the economy that feeds them.
	for i := range eventCap * 2 {
		m.events = append(m.events, sim.Event{Tick: uint64(i), Kind: sim.EventDeposit, Robot: i + 1})
	}
	m.collectEvents()

	evs := m.Events(0)
	if len(evs) != eventCap {
		t.Fatalf("the feed holds %d events, cap is %d", len(evs), eventCap)
	}
	// The newest survive and the oldest fall off, not the other way round.
	if got, want := evs[len(evs)-1].Tick, uint64(eventCap*2-1); got != want {
		t.Fatalf("the newest event is at tick %d, want %d: the trim dropped the wrong end", got, want)
	}
	if got, want := evs[0].Tick, uint64(eventCap); got != want {
		t.Fatalf("the oldest surviving event is at tick %d, want %d", got, want)
	}

	// Events(since) is the tick range the stream uses as a cursor: it must
	// start exactly at since and drop nothing after it.
	mid := uint64(eventCap + eventCap/2)
	tail := m.Events(mid)
	if len(tail) != int(uint64(eventCap*2)-mid) {
		t.Fatalf("Events(%d) returned %d events, want %d", mid, len(tail), uint64(eventCap*2)-mid)
	}
	if tail[0].Tick != mid {
		t.Fatalf("Events(%d) starts at tick %d", mid, tail[0].Tick)
	}
}

// firstDiff names the first position two feeds disagree at, so a failure says
// which event went wrong rather than dumping two thousand lines.
func firstDiff(got, want []sim.Event) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Sprintf("index %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	return fmt.Sprintf("index %d: one feed is longer than the other", min(len(got), len(want)))
}
