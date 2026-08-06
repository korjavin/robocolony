package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
)

// finishedMatch runs a real one-second match to its end, driver and all, so the
// finishing save writes the record the replay endpoint reads.
func finishedMatch(t *testing.T) (*lobby.Service, *db.DB, int64) {
	t.Helper()
	svc, database := newService(t)
	owner, err := database.UpsertUser(t.Context(), "sub-owner", "owner@example.com", "Owner")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	// A one-second match, written straight to the database: Settings.Validate
	// enforces a 60 second floor and this test will not wait a minute.
	row, err := database.CreateLobby(t.Context(), owner.ID, "replay me",
		`{"duration_sec":1,"richness":0.02,"spawn_per_min":6,"max_players":4}`)
	if err != nil {
		t.Fatalf("CreateLobby() = %v", err)
	}
	if _, err := svc.Start(t.Context(), row.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	m, ok := svc.Registry().Get(row.ID)
	if !ok {
		t.Fatalf("match %d is not in the registry after Start", row.ID)
	}
	for deadline := time.Now().Add(10 * time.Second); !m.Finished(); {
		if time.Now().After(deadline) {
			t.Fatal("the one-second match never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Finished() flips inside the step; the driver writes the record just
	// after, so wait for the record rather than for the flag.
	for deadline := time.Now().Add(10 * time.Second); ; {
		rec, err := database.MatchLogByID(t.Context(), row.ID)
		if err == nil && !rec.FinishedAt.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the finished match never got its record: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return svc, database, row.ID
}

func replayServer(t *testing.T, svc *lobby.Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/replay", Replay(svc, nil))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func replayGet(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s = %v", path, err)
	}
	return resp
}

// TestReplayStreamFrames is the acceptance case: a finished match plays back
// over exactly the live stream's frames — init, ticks, end — so the client
// renders it with no second code path.
func TestReplayStreamFrames(t *testing.T) {
	svc, _, id := finishedMatch(t)
	srv := replayServer(t, svc)

	resp := replayGet(t, srv, "/api/matches/"+strconv.FormatInt(id, 10)+"/replay?from=0&speed=16")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET replay = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// init, the board at tick 0, one frame per tick to the end of the
	// ten-tick match, and the end frame: 13 events.
	events := readEvents(t, resp.Body, 13)
	if events[0].name != "init" {
		t.Fatalf("first event is %q, want init", events[0].name)
	}
	var in Init
	if err := json.Unmarshal(events[0].data, &in); err != nil {
		t.Fatalf("init frame does not decode: %v", err)
	}
	if in.MatchID != id || len(in.Terrain) == 0 {
		t.Errorf("init frame is for match %d with %d terrain rows", in.MatchID, len(in.Terrain))
	}
	if in.Tick != 0 {
		t.Errorf("init frame joined at tick %d, want 0", in.Tick)
	}

	var last uint64
	sawEnd := false
	for _, e := range events[1:] {
		switch e.name {
		case "tick":
			var snap Snapshot
			if err := json.Unmarshal(e.data, &snap); err != nil {
				t.Fatalf("tick frame does not decode: %v", err)
			}
			if snap.Tick < last {
				t.Errorf("replay went backwards: tick %d after %d", snap.Tick, last)
			}
			last = snap.Tick
			if len(snap.Robots) == 0 {
				t.Error("a replayed tick carries no robots")
			}
		case "end":
			var end End
			if err := json.Unmarshal(e.data, &end); err != nil {
				t.Fatalf("end frame does not decode: %v", err)
			}
			if end.Tick != in.EndTick {
				t.Errorf("end frame at tick %d, want the final tick %d", end.Tick, in.EndTick)
			}
			sawEnd = true
		default:
			t.Fatalf("unexpected event %q", e.name)
		}
	}
	if !sawEnd {
		t.Errorf("the replay reached tick %d of %d without an end frame", last, in.EndTick)
	}
}

// TestReplayFromLatestTick: a scrub to the end still opens, and closes right
// away with the final board and an end frame.
func TestReplayFromTheEnd(t *testing.T) {
	svc, _, id := finishedMatch(t)
	srv := replayServer(t, svc)

	resp := replayGet(t, srv, "/api/matches/"+strconv.FormatInt(id, 10)+"/replay?from=99999")
	defer func() { _ = resp.Body.Close() }()

	events := readEvents(t, resp.Body, 3)
	for i, want := range []string{"init", "tick", "end"} {
		if events[i].name != want {
			t.Fatalf("event %d is %q, want %q", i, events[i].name, want)
		}
	}
	var snap Snapshot
	if err := json.Unmarshal(events[1].data, &snap); err != nil {
		t.Fatalf("board does not decode: %v", err)
	}
	if snap.Tick != snap.EndTick {
		t.Errorf("a scrub past the end stands at tick %d of %d", snap.Tick, snap.EndTick)
	}
}

// TestReplayRefusesAForeignRecord: a log recorded by a build that simulates
// differently must be refused loudly, and must not stream a world.
func TestReplayRefusesAForeignRecord(t *testing.T) {
	svc, database, id := finishedMatch(t)
	rec, err := database.MatchLogByID(t.Context(), id)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v", err)
	}
	rec.Fingerprint = "from-a-build-that-balanced-weapons-differently"
	if err := database.SaveMatchLog(t.Context(), rec); err != nil {
		t.Fatalf("SaveMatchLog() = %v", err)
	}

	srv := replayServer(t, svc)
	resp := replayGet(t, srv, "/api/matches/"+strconv.FormatInt(id, 10)+"/replay")
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("GET replay of a stale record = %d, want 409", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got == "text/event-stream" {
		t.Error("a refused replay opened a stream anyway")
	}
	body := make([]byte, 512)
	n, _ := resp.Body.Read(body)
	if n == 0 {
		t.Error("the refusal has no body for the UI to show")
	}
}

// TestReplayUnknownMatch: nothing recorded, nothing to replay.
func TestReplayUnknownMatch(t *testing.T) {
	svc, _, _ := finishedMatch(t)
	srv := replayServer(t, svc)
	resp := replayGet(t, srv, "/api/matches/999999/replay")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET replay of an unknown match = %d, want 404", resp.StatusCode)
	}
}
