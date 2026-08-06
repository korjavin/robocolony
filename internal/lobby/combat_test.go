package lobby

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/korjavin/robocolony/internal/sim"
)

// combatFixture runs a match far enough to have four per-minute buckets. The
// caller then replaces its feed, so the derivation is exercised against events
// the test chose rather than whatever the economy happened to produce. The
// world is real: the bucket count comes from its tick, and the seats from
// newMatch.
func combatFixture(t *testing.T) *Match {
	t.Helper()
	m := testMatch(t, shortSettings(600), 2)
	for range 3 * combatBucketTicks {
		if !m.step() {
			t.Fatal("the fixture match ended before it was long enough to bucket")
		}
	}
	return m
}

// loss builds the one event kind that carries an attacker.
func loss(tick uint64, victim sim.ColonyID, attacker int, attackerColony sim.ColonyID) sim.Event {
	return sim.Event{
		Tick: tick, Kind: sim.EventLoss, Colony: victim, Robot: 900 + int(tick),
		Attacker: attacker, AttackerColony: attackerColony,
	}
}

// TestCombatDerivesAttribution is the half of rc-pt6.10 that comes off the
// event feed: who killed whom, and in which minute. Colony ids are read off the
// match rather than assumed, since the seat-to-ColonyID mapping is newMatch's.
func TestCombatDerivesAttribution(t *testing.T) {
	// Placeholders, substituted for the real colony ids once the fixture
	// exists: a table cannot name what newMatch has not assigned yet.
	const a, b sim.ColonyID = 0, 1

	tests := []struct {
		name          string
		events        []sim.Event
		wantKilledBy  [][]AttackerLosses
		wantLossesPer [][]int
		wantKillsPer  [][]int
	}{
		{
			name:          "no combat at all",
			events:        nil,
			wantKilledBy:  [][]AttackerLosses{{}, {}},
			wantLossesPer: [][]int{{0, 0, 0, 0}, {0, 0, 0, 0}},
			wantKillsPer:  [][]int{{0, 0, 0, 0}, {0, 0, 0, 0}},
		},
		{
			name: "one robot does all the killing",
			events: []sim.Event{
				loss(10, a, 7, b),
				loss(20, a, 7, b),
				loss(30, a, 7, b),
			},
			wantKilledBy: [][]AttackerLosses{
				{{Robot: 7, Colony: b, Losses: 3}},
				{},
			},
			wantLossesPer: [][]int{{3, 0, 0, 0}, {0, 0, 0, 0}},
			wantKillsPer:  [][]int{{0, 0, 0, 0}, {3, 0, 0, 0}},
		},
		{
			name: "the ranking is by count, most first",
			events: []sim.Event{
				loss(10, a, 4, b),
				loss(11, a, 7, b),
				loss(12, a, 7, b),
				loss(13, a, 9, b),
				loss(14, a, 7, b),
				loss(15, a, 9, b),
			},
			wantKilledBy: [][]AttackerLosses{
				{
					{Robot: 7, Colony: b, Losses: 3},
					{Robot: 9, Colony: b, Losses: 2},
					{Robot: 4, Colony: b, Losses: 1},
				},
				{},
			},
			wantLossesPer: [][]int{{6, 0, 0, 0}, {0, 0, 0, 0}},
			wantKillsPer:  [][]int{{0, 0, 0, 0}, {6, 0, 0, 0}},
		},
		{
			// One event is two facts: the victim's loss and the attacker's
			// kill. Nothing else may be double-counted off it.
			name: "both sides take losses",
			events: []sim.Event{
				loss(10, a, 7, b),
				loss(20, b, 3, a),
			},
			wantKilledBy: [][]AttackerLosses{
				{{Robot: 7, Colony: b, Losses: 1}},
				{{Robot: 3, Colony: a, Losses: 1}},
			},
			wantLossesPer: [][]int{{1, 0, 0, 0}, {1, 0, 0, 0}},
			wantKillsPer:  [][]int{{1, 0, 0, 0}, {1, 0, 0, 0}},
		},
		{
			// The per-minute chart: an event at tick 600 is minute 1, not the
			// tail of minute 0. The last bucket is the one the match ended in.
			name: "events land in the minute they happened",
			events: []sim.Event{
				loss(0, a, 7, b),
				loss(combatBucketTicks-1, a, 7, b),
				loss(combatBucketTicks, a, 7, b),
				loss(2*combatBucketTicks+5, a, 7, b),
				loss(3*combatBucketTicks, a, 7, b),
			},
			wantKilledBy: [][]AttackerLosses{
				{{Robot: 7, Colony: b, Losses: 5}},
				{},
			},
			wantLossesPer: [][]int{{2, 1, 1, 1}, {0, 0, 0, 0}},
			wantKillsPer:  [][]int{{0, 0, 0, 0}, {2, 1, 1, 1}},
		},
	}

	// One fixture for the whole table: Combat only reads, so every case can
	// rewrite the feed of the same match. Building one per case cost four
	// seconds each for nothing.
	m := combatFixture(t)
	ids := [2]sim.ColonyID{m.Colonies[0].ID, m.Colonies[1].ID}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.events = make([]sim.Event, len(tt.events))
			for i, e := range tt.events {
				e.Colony, e.AttackerColony = ids[e.Colony], ids[e.AttackerColony]
				m.events[i] = e
			}
			got := m.Combat()

			if got.BucketTicks != combatBucketTicks {
				t.Errorf("bucket_ticks = %d, want %d", got.BucketTicks, combatBucketTicks)
			}
			if len(got.Colonies) != 2 {
				t.Fatalf("combat covers %d colonies, want 2", len(got.Colonies))
			}
			for i, c := range got.Colonies {
				want := make([]AttackerLosses, len(tt.wantKilledBy[i]))
				for j, w := range tt.wantKilledBy[i] {
					w.Colony = ids[w.Colony]
					want[j] = w
				}
				if c.Colony != ids[i] {
					t.Errorf("colony %d is out of seat order: got id %d, want %d", i, c.Colony, ids[i])
				}
				if !reflect.DeepEqual(c.KilledBy, want) {
					t.Errorf("colony %d killed_by = %+v, want %+v", i, c.KilledBy, want)
				}
				if !reflect.DeepEqual(c.LossesPer, tt.wantLossesPer[i]) {
					t.Errorf("colony %d losses_per = %v, want %v", i, c.LossesPer, tt.wantLossesPer[i])
				}
				if !reflect.DeepEqual(c.KillsPer, tt.wantKillsPer[i]) {
					t.Errorf("colony %d kills_per = %v, want %v", i, c.KillsPer, tt.wantKillsPer[i])
				}
			}
		})
	}
}

// TestCombatTotalsSurviveADroppedFeed is the edge the design turns on: the feed
// is a ring capped at eventCap, so a long match loses its earliest losses, but
// the totals come from sim.Stats and are counted for the whole match. The
// totals must stay complete while the attribution goes short — and the shortfall
// must be exactly what a client gets by subtracting, because that difference is
// the only thing standing in for a "partial" flag this code deliberately does
// not store.
func TestCombatTotalsSurviveADroppedFeed(t *testing.T) {
	m := testMatch(t, shortSettings(600), 2)
	victim, killer := m.Colonies[0].ID, m.Colonies[1].ID

	// What the simulation counted over the whole match.
	for _, b := range m.world.Bases {
		switch b.Colony {
		case victim:
			b.Stats.Losses, b.Stats.TicksActive = 10, 5555
		case killer:
			b.Stats.Kills = 10
		}
	}
	// What is left in the feed: three of the ten, the rest having fallen off
	// the oldest end.
	m.events = []sim.Event{
		loss(10, victim, 7, killer),
		loss(20, victim, 7, killer),
		loss(30, victim, 4, killer),
	}

	c := m.Combat()
	lost, killed := c.Colonies[0], c.Colonies[1]
	if lost.Losses != 10 || killed.Kills != 10 {
		t.Errorf("totals came from the feed, not the simulation: losses %d, kills %d, want 10 and 10",
			lost.Losses, killed.Kills)
	}
	if lost.TicksActive != 5555 {
		t.Errorf("ticks_active = %d, want 5555", lost.TicksActive)
	}
	attributed := 0
	for _, a := range lost.KilledBy {
		attributed += a.Losses
	}
	if attributed != 3 {
		t.Errorf("attributed %d losses, want the 3 still in the feed", attributed)
	}
	if got := lost.Losses - attributed; got != 7 {
		t.Errorf("losses minus attribution = %d, want 7 unattributed", got)
	}
	bucketed := 0
	for _, n := range lost.LossesPer {
		bucketed += n
	}
	if bucketed != attributed {
		t.Errorf("the series counts %d losses and the ranking %d: they come from one feed and must agree",
			bucketed, attributed)
	}
}

// TestFinishedMatchAnswersKillsAndLosses is rc-pt6.10's acceptance criterion,
// end to end and through the real endpoints: a match that is over answers how
// many kills and losses each colony had and who did the killing — and a process
// that restarts answers the same, because the answer is in the stored summary
// and not in any memory that died with it.
func TestFinishedMatchAnswersKillsAndLosses(t *testing.T) {
	svc, database := newService(t)
	set := hunterSettings()
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	_, m := finishedMatch(t, svc, database, lobby, set, members)

	live := m.Combat()
	fought := 0
	for _, c := range live.Colonies {
		fought += c.Losses
	}
	if fought == 0 {
		t.Fatal("the fixture produced no losses at all: two aggressive colonies over a full match " +
			"must fight, so this is a broken fixture rather than a passing test")
	}

	// The restart. A brand-new Service on the same database, holding nothing
	// the finished match left in memory.
	restarted := New(database)
	mux := http.NewServeMux()
	restarted.Routes(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var detail HistoryDetail
	getJSON(t, srv.URL+"/api/history/"+strconv.FormatInt(lobby.ID, 10), http.StatusOK, &detail)

	if len(detail.Combat.Colonies) != len(m.Colonies) {
		t.Fatalf("the stored combat record covers %d colonies, the match had %d",
			len(detail.Combat.Colonies), len(m.Colonies))
	}
	if detail.Combat.BucketTicks != combatBucketTicks {
		t.Errorf("stored bucket_ticks = %d, want %d", detail.Combat.BucketTicks, combatBucketTicks)
	}
	if !reflect.DeepEqual(detail.Combat, live) {
		t.Errorf("the restarted combat record differs from the one the match ended with:\n got %+v\nwant %+v",
			detail.Combat, live)
	}

	// Every kill is somebody's loss: the two totals are counted independently
	// by the simulation and must balance over the whole match.
	kills, losses := 0, 0
	for i, c := range detail.Combat.Colonies {
		kills, losses = kills+c.Kills, losses+c.Losses
		if got, want := len(c.LossesPer), int(set.durationTicks()/combatBucketTicks)+1; got != want {
			t.Errorf("colony %d has %d minute buckets, want %d for a %d-tick match",
				i, got, want, set.durationTicks())
		}
		attributed := 0
		for _, a := range c.KilledBy {
			attributed += a.Losses
			if a.Colony == c.Colony {
				t.Errorf("colony %d is credited with killing its own robot %d", c.Colony, a.Robot)
			}
		}
		if attributed > c.Losses {
			t.Errorf("colony %d attributed %d losses but only lost %d", i, attributed, c.Losses)
		}
	}
	if kills != losses {
		t.Errorf("the match recorded %d kills and %d losses: every kill is a loss", kills, losses)
	}

	// The standing carries the totals too, so the history LIST ranks on them
	// without opening each match.
	var list []HistoryEntry
	getJSON(t, srv.URL+"/api/history", http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("GET /api/history returned %d entries, want 1", len(list))
	}
	for i, c := range list[0].Colonies {
		if c.Kills != detail.Combat.Colonies[i].Kills || c.Losses != detail.Combat.Colonies[i].Losses {
			t.Errorf("list colony %d says %d kills / %d losses, the detail says %d / %d",
				i, c.Kills, c.Losses, detail.Combat.Colonies[i].Kills, detail.Combat.Colonies[i].Losses)
		}
	}
}

// TestSummaryWithoutCombatStillDecodes: rows written before rc-pt6.10 have no
// combat key. The history page must serve them rather than 500, because a
// deployed database is full of them.
func TestSummaryWithoutCombatStillDecodes(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	rec, _ := finishedMatch(t, svc, database, lobby, set, members)

	// The exact shape the previous build wrote.
	var old struct {
		Info    Info    `json:"info"`
		History History `json:"history"`
	}
	if err := json.Unmarshal([]byte(rec.Summary), &old); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	legacy, err := json.Marshal(old)
	if err != nil {
		t.Fatalf("marshal legacy summary: %v", err)
	}
	rec.Summary = string(legacy)
	if err := database.SaveMatchLog(t.Context(), rec); err != nil {
		t.Fatalf("SaveMatchLog() = %v", err)
	}

	detail, err := svc.HistoryOf(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("HistoryOf() on a pre-rc-pt6.10 summary = %v", err)
	}
	if len(detail.Combat.Colonies) != 0 {
		t.Errorf("a summary with no combat key decoded %d colonies of it", len(detail.Combat.Colonies))
	}
	if len(detail.Info.Colonies) == 0 {
		t.Error("the standing was lost decoding an older summary")
	}
	var list []HistoryEntry
	if list, err = svc.History(t.Context()); err != nil || len(list) != 1 {
		t.Errorf("History() = %v entries, %v; the list dropped a row it could not fully decode", len(list), err)
	}
}
