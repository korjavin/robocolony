package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// TestHistoryServesAFinishedMatch is the wire contract the history page is
// built against: the list carries the standing, and the detail carries the
// standing and the series.
func TestHistoryServesAFinishedMatch(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	_, m := finishedMatch(t, svc, database, lobby, set, members)

	mux := http.NewServeMux()
	svc.Routes(mux, func(h http.Handler) http.Handler { return h }) // auth is main.go's job
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var list []HistoryEntry
	getJSON(t, srv.URL+"/api/history", http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("GET /api/history returned %d entries, want 1", len(list))
	}
	got := list[0]
	switch {
	case got.ID != lobby.ID:
		t.Errorf("entry id = %d, want %d", got.ID, lobby.ID)
	case got.Name != lobby.Name:
		t.Errorf("entry name = %q, want %q", got.Name, lobby.Name)
	case got.EndTick != set.durationTicks():
		t.Errorf("entry end_tick = %d, want %d", got.EndTick, set.durationTicks())
	case got.TickRate != TickRate:
		t.Errorf("entry tick_rate = %d, want %d", got.TickRate, TickRate)
	case !got.Replayable:
		t.Error("a match this build just finished is not replayable")
	case got.StartedAt == "" || got.FinishedAt == "":
		t.Errorf("entry has started_at %q and finished_at %q", got.StartedAt, got.FinishedAt)
	case len(got.Colonies) != len(m.Colonies):
		t.Fatalf("entry has %d colonies, want %d", len(got.Colonies), len(m.Colonies))
	}
	// The standing is the point of the list: a colony row with no score is a
	// row the history page cannot rank.
	for _, c := range got.Colonies {
		if c.DisplayName == "" {
			t.Errorf("colony %d has no display name", c.ID)
		}
	}
	if want := m.Info().Colonies[0].Score; got.Colonies[0].Score != want {
		t.Errorf("colony 0 scored %d in the list, %d in the match", got.Colonies[0].Score, want)
	}

	var detail HistoryDetail
	getJSON(t, srv.URL+"/api/history/"+strconv.FormatInt(lobby.ID, 10), http.StatusOK, &detail)
	if detail.Info.ID != lobby.ID {
		t.Errorf("detail info id = %d, want %d", detail.Info.ID, lobby.ID)
	}
	if detail.Info.Tick != set.durationTicks() {
		t.Errorf("detail info stops at tick %d, want the final tick %d", detail.Info.Tick, set.durationTicks())
	}
	if len(detail.History.Ticks) == 0 {
		t.Error("detail carries no series: the graph would be empty")
	}
	if !detail.Replayable || detail.Reason != "" {
		t.Errorf("detail says replayable=%v because %q", detail.Replayable, detail.Reason)
	}

	// A lobby that never finished is not history.
	getJSON(t, srv.URL+"/api/history/999999", http.StatusNotFound, nil)
}

// TestStaleRecordKeepsItsSummary: when the build no longer simulates the way
// the log was recorded, the replay is refused and the standing is still served.
// That is the whole reason the summary is stored alongside the log.
func TestStaleRecordKeepsItsSummary(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	rec, _ := finishedMatch(t, svc, database, lobby, set, members)

	rec.Fingerprint = "from-a-build-that-balanced-weapons-differently"
	if err := database.SaveMatchLog(t.Context(), rec); err != nil {
		t.Fatalf("SaveMatchLog() = %v", err)
	}

	m, err := svc.Replay(t.Context(), lobby.ID, 0)
	if !errors.Is(err, ErrStaleReplay) {
		t.Fatalf("Replay() of a stale record = %v, want ErrStaleReplay", err)
	}
	if m != nil {
		t.Error("Replay() handed out a world built by a build that simulates differently")
	}

	detail, err := svc.HistoryOf(t.Context(), lobby.ID)
	if err != nil {
		t.Fatalf("HistoryOf() = %v", err)
	}
	if detail.Replayable {
		t.Error("a stale record reports itself replayable")
	}
	if detail.Reason == "" {
		t.Error("a refusal with no reason: the page has nothing to show")
	}
	if len(detail.Info.Colonies) == 0 || len(detail.History.Ticks) == 0 {
		t.Error("the standing and the series did not survive the fingerprint change")
	}

	list, err := svc.History(t.Context())
	if err != nil {
		t.Fatalf("History() = %v", err)
	}
	if len(list) != 1 || list[0].Replayable {
		t.Errorf("history list = %+v, want one entry that is not replayable", list)
	}
}

// TestReplayStandsAtTheRequestedTick: the rebuild lands where the client asked,
// keeps stepping from there, and clamps a scrub past the end onto the end.
func TestReplayStandsAtTheRequestedTick(t *testing.T) {
	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	_, live := finishedMatch(t, svc, database, lobby, set, members)

	m, err := svc.Replay(t.Context(), lobby.ID, 120)
	if err != nil {
		t.Fatalf("Replay() = %v", err)
	}
	if got := m.Info().Tick; got != 120 {
		t.Fatalf("Replay(from=120) stands at tick %d", got)
	}
	if _, ok := svc.reg.Get(lobby.ID); ok {
		t.Error("a replay was put in the registry: it must never touch the live world")
	}
	// Stepping it forward from there is what the SSE handler does per frame.
	if err := m.ReplayTo(t.Context(), 121); err != nil {
		t.Fatalf("ReplayTo(121) = %v", err)
	}
	if got := m.Info().Tick; got != 121 {
		t.Errorf("after ReplayTo(121) the match stands at tick %d", got)
	}
	if err := m.ReplayTo(t.Context(), set.durationTicks()); err != nil {
		t.Fatalf("ReplayTo(end) = %v", err)
	}
	var got, want uint64
	m.Read(func(w *sim.World, _ *prog.Runtime) { got = w.StateHash() })
	live.Read(func(w *sim.World, _ *prog.Runtime) { want = w.StateHash() })
	if got != want {
		t.Errorf("replayed to the end the world hashes %#x, the original %#x", got, want)
	}

	past, err := svc.Replay(t.Context(), lobby.ID, 1<<40)
	if err != nil {
		t.Fatalf("Replay(from past the end) = %v, want it clamped", err)
	}
	if got := past.Info().Tick; got != set.durationTicks() {
		t.Errorf("a scrub past the end stands at tick %d, want %d", got, set.durationTicks())
	}

	// A match that is still running has no recording to replay.
	other, _ := seatedLobby(t, svc, database, set)
	if _, err := svc.Replay(t.Context(), other.ID, 0); !errors.Is(err, ErrNoRecord) {
		t.Errorf("Replay() of a match that never finished = %v, want ErrNoRecord", err)
	}
}

// TestReplayBudgetBoundsTheRebuild: the rebuild runs on a request goroutine and
// costs O(target tick) with no ceiling on match duration, so the budget is what
// stops one request occupying a goroutine indefinitely. The default budget is
// 30s and every other test here replays inside it — this one shrinks it, which
// is the only way to see it fire without a match nobody would play.
func TestReplayBudgetBoundsTheRebuild(t *testing.T) {
	defer func(prev time.Duration) { replayBudget = prev }(replayBudget)

	svc, database := newService(t)
	set := shortSettings(60)
	lobby, members := seatedLobby(t, svc, database, set)
	set.Seed = mustSettings(t, lobby).Seed
	finishedMatch(t, svc, database, lobby, set, members)

	replayBudget = time.Nanosecond
	_, err := svc.Replay(t.Context(), lobby.ID, set.durationTicks())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Replay() over budget = %v, want context.DeadlineExceeded", err)
	}

	// And the budget is a ceiling, not a cost: the same replay inside it works.
	replayBudget = 30 * time.Second
	if _, err := svc.Replay(t.Context(), lobby.ID, set.durationTicks()); err != nil {
		t.Fatalf("Replay() inside the budget = %v", err)
	}
}

func getJSON(t *testing.T, url string, code int, dst any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // httptest server, t.Context is not plumbed through http.Get
	if err != nil {
		t.Fatalf("GET %s = %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != code {
		t.Fatalf("GET %s = %d, want %d", url, resp.StatusCode, code)
	}
	if dst == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("GET %s: body does not decode: %v", url, err)
	}
}
