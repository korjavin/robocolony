package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// startMatch brings up a real service, lobby and running match: the stream is
// not worth testing against a hand-built world it will never see in production.
func startMatch(t *testing.T) (*lobby.Registry, *lobby.Match) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	svc := lobby.New(database)
	// Before the database close registered above: a tick driver must not
	// outlive the database it settles its lobby row in.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})

	owner, err := database.UpsertUser(t.Context(), "sub-owner", "owner@example.com", "Owner")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	view, err := svc.Create(t.Context(), owner.ID, "stream test", lobby.DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := svc.Start(t.Context(), view.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	m, ok := svc.Registry().Get(view.ID)
	if !ok {
		t.Fatalf("match %d is not in the registry after Start", view.ID)
	}
	return svc.Registry(), m
}

// TestTerrainLegendMatchesSpecs is what keeps the legend honest: the wire is a
// projection of the design §3.1 traversal matrix, so a rule changed in
// internal/sim must reach the client without anyone editing the client. It also
// pins every locomotion id to the component catalogue in the same frame — the
// legend names them by resolving those ids, and an unresolvable one would
// render as "variant 3".
func TestTerrainLegendMatchesSpecs(t *testing.T) {
	_, m := startMatch(t)

	info, hist := m.Info(), m.History()
	var in Init
	m.Read(func(w *sim.World, rt *prog.Runtime) { in = NewInit(info, m.Colonies, w, hist) })

	specs := sim.TerrainSpecs()
	if len(in.TerrainLegend) != len(specs) {
		t.Fatalf("init.TerrainLegend has %d classes, want %d", len(in.TerrainLegend), len(specs))
	}
	known := map[int]bool{}
	for _, c := range in.Components {
		known[c.Variant] = true
	}
	for i, s := range specs {
		got := in.TerrainLegend[i]
		if got.Name != s.Name {
			t.Errorf("init.TerrainLegend[%d].Name = %q, want %q", i, got.Name, s.Name)
		}
		if got.HardBarrier != s.HardBarrier {
			t.Errorf("%s: HardBarrier = %v, want %v", s.Name, got.HardBarrier, s.HardBarrier)
		}
		if !slices.Equal(got.Impassable, variants(s.Impassable)) {
			t.Errorf("%s: Impassable = %v, want %v", s.Name, got.Impassable, variants(s.Impassable))
		}
		if !slices.Equal(got.Favored, variants(s.Favored)) {
			t.Errorf("%s: Favored = %v, want %v", s.Name, got.Favored, variants(s.Favored))
		}
		for _, v := range slices.Concat(got.Impassable, got.Favored) {
			if !known[v] {
				t.Errorf("%s: variant %d is not in the component catalogue", s.Name, v)
			}
		}
	}
}

// TestSnapshotShape is the bead's golden-ish check: a real generated world
// produces frames with every field the renderer needs populated.
func TestSnapshotShape(t *testing.T) {
	_, m := startMatch(t)

	info, hist := m.Info(), m.History()
	var init Init
	var snap Snapshot
	m.Read(func(w *sim.World, rt *prog.Runtime) {
		init = NewInit(info, m.Colonies, w, hist)
		snap = NewSnapshot(w, rt, info.EndTick)
	})

	switch {
	case init.MatchID != m.ID:
		t.Errorf("init.MatchID = %d, want %d", init.MatchID, m.ID)
	case init.TickRate != lobby.TickRate:
		t.Errorf("init.TickRate = %d, want %d", init.TickRate, lobby.TickRate)
	case init.EndTick == 0:
		t.Error("init.EndTick = 0, want the match duration in ticks")
	case len(init.Terrain) != init.Height:
		t.Errorf("init.Terrain has %d rows, want %d", len(init.Terrain), init.Height)
	case len(init.TerrainLegend) == 0:
		t.Error("init.TerrainLegend is empty")
	case len(init.Components) == 0:
		t.Error("init.Components is empty: the renderer cannot name a variant")
	case len(init.Colonies) != len(m.Colonies):
		t.Errorf("init.Colonies has %d seats, want %d", len(init.Colonies), len(m.Colonies))
	case init.Colonies[0].DisplayName == "":
		t.Error("init.Colonies[0].DisplayName is empty")
	}
	for y, row := range init.Terrain {
		if len(row) != init.Width {
			t.Fatalf("init.Terrain[%d] is %d cells wide, want %d", y, len(row), init.Width)
		}
		for _, c := range row {
			if int(c-'0') >= len(init.TerrainLegend) {
				t.Fatalf("init.Terrain[%d] holds %q, outside the legend", y, c)
			}
		}
	}

	if snap.EndTick != info.EndTick {
		t.Errorf("snapshot.EndTick = %d, want %d", snap.EndTick, info.EndTick)
	}
	if len(snap.Robots) == 0 {
		t.Fatal("snapshot has no robots, but every colony starts with some")
	}
	r := snap.Robots[0]
	switch {
	case r.ID == 0:
		t.Error("robot.ID = 0")
	case r.HPMax == 0:
		t.Error("robot.HPMax = 0: the renderer cannot draw a health bar")
	case r.HP <= 0 || r.HP > r.HPMax:
		t.Errorf("robot.HP = %d, want 1..%d", r.HP, r.HPMax)
	case r.Archetype == "":
		t.Error("robot.Archetype is empty")
	case r.Blueprint == "":
		t.Error("robot.Blueprint is empty")
	case r.Program == "":
		t.Error("robot.Program is empty")
	case len(r.Memory) != sim.MemPoints:
		t.Errorf("robot.Memory has %d slots, want %d", len(r.Memory), sim.MemPoints)
	}

	if len(snap.Bases) != len(m.Colonies) {
		t.Fatalf("snapshot has %d bases, want %d", len(snap.Bases), len(m.Colonies))
	}
	// Blueprints ride the init frame, not the tick frame (rc-w9s.31), so every
	// robot's blueprint id has to resolve against its colony's list there —
	// that lookup is how the client names a design, draws its silhouette and
	// decides whether a program fits it.
	for _, r := range snap.Robots {
		i := slices.IndexFunc(init.Colonies, func(c Colony) bool { return c.ID == r.Colony })
		if i < 0 {
			t.Fatalf("robot %d is in colony %d, which is not on the init frame", r.ID, r.Colony)
		}
		if len(init.Colonies[i].Blueprints) == 0 {
			t.Fatalf("colony %d has no approved blueprints on the init frame", r.Colony)
		}
		if !slices.ContainsFunc(init.Colonies[i].Blueprints, func(b Blueprint) bool { return b.ID == r.Blueprint }) {
			t.Errorf("robot %d blueprint %q is not among colony %d's approved designs", r.ID, r.Blueprint, r.Colony)
		}
	}
	if len(snap.Loose) == 0 {
		t.Error("snapshot has no loose components: the default richness scatters some")
	}
	if len(snap.Colonies) != len(m.Colonies) {
		t.Fatalf("snapshot has %d colony stats, want %d", len(snap.Colonies), len(m.Colonies))
	}
	if snap.Colonies[0].Robots == 0 || snap.Colonies[0].FleetValue == 0 {
		t.Errorf("colony stats = %+v, want a non-empty fleet", snap.Colonies[0])
	}

	// Design §13 criterion 9: the client must be able to say which rule
	// controls a robot. Traces appear once a robot has decided.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var traced *Trace
		m.Read(func(w *sim.World, rt *prog.Runtime) {
			traced = NewSnapshot(w, rt, info.EndTick).Robots[0].Trace
		})
		if traced != nil {
			if traced.Rule < -1 {
				t.Errorf("trace.Rule = %d, want -1 or a rule index", traced.Rule)
			}
			if traced.Reason == "" {
				t.Error("trace.Reason is empty: the observer has nothing to show")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no robot trace after 3s of ticking")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The wire format is JSON; a type that cannot marshal is a broken contract.
	if _, err := json.Marshal(init); err != nil {
		t.Fatalf("json.Marshal(init) = %v", err)
	}
	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal(snapshot) = %v", err)
	}
	t.Logf("frame sizes: init %d bytes, tick %d bytes (%d robots, %d loose)",
		len(mustJSON(t, init)), len(body), len(snap.Robots), len(snap.Loose))
	if len(body) > maxFrameBytes {
		t.Errorf("tick frame is %d bytes, over the %d-byte full-snapshot budget", len(body), maxFrameBytes)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal() = %v", err)
	}
	return b
}

// TestStreamFrames is the curl -N case: an init frame, then tick frames.
func TestStreamFrames(t *testing.T) {
	reg, m := startMatch(t)

	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", Stream(reg, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/matches/"+strconv.FormatInt(m.ID, 10)+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no: a buffering proxy eats the stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	events := readEvents(t, resp.Body, 4)
	if events[0].name != "init" {
		t.Fatalf("first event is %q, want init", events[0].name)
	}
	var init Init
	if err := json.Unmarshal(events[0].data, &init); err != nil {
		t.Fatalf("init frame does not decode: %v", err)
	}
	if len(init.Terrain) == 0 {
		t.Error("init frame carries no terrain")
	}

	var ticks []uint64
	for _, e := range events[1:] {
		if e.name != "tick" {
			t.Fatalf("event %q after init, want tick", e.name)
		}
		var snap Snapshot
		if err := json.Unmarshal(e.data, &snap); err != nil {
			t.Fatalf("tick frame does not decode: %v", err)
		}
		if strings.Contains(string(e.data), `"terrain"`) {
			t.Error("tick frames must not resend terrain")
		}
		ticks = append(ticks, snap.Tick)
	}
	for i := 1; i < len(ticks); i++ {
		if ticks[i] <= ticks[i-1] {
			t.Errorf("ticks %v are not strictly increasing: a frame was repeated", ticks)
			break
		}
	}
}

// TestSpectateAFinishedMatch is design §12 P2's acceptance bar: a match that is
// already over is still worth opening. The world is frozen (E5.2 made Step a
// no-op once it ends) so no tick will ever come, which is exactly why the board
// travels with the init frame — without it a spectator gets terrain and an
// empty arena, and the final standing is unreadable.
//
// The trace half of the same decision is checked here too: the retained
// decisions are still served, and asking for them does not start a watch on a
// world that can no longer record one.
func TestSpectateAFinishedMatch(t *testing.T) {
	svc, database := newService(t)
	owner, err := database.UpsertUser(t.Context(), "sub-owner", "owner@example.com", "Owner")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	// A one-second match, written straight to the database: Settings.Validate
	// enforces a 60 second floor and this test will not wait a minute.
	row, err := database.CreateLobby(t.Context(), owner.ID, "sprint",
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
	robot := aRobot(t, m, m.Colonies[0].ID)
	for deadline := time.Now().Add(10 * time.Second); !m.Finished(); {
		if time.Now().After(deadline) {
			t.Fatal("the one-second match never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", Stream(svc.Registry(), nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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

	events := readEvents(t, resp.Body, 3)
	var got []string
	for _, e := range events {
		got = append(got, e.name)
	}
	if want := []string{"init", "tick", "end"}; !slices.Equal(got, want) {
		t.Fatalf("events are %q, want %q", got, want)
	}
	var board Snapshot
	if err := json.Unmarshal(events[1].data, &board); err != nil {
		t.Fatalf("final board does not decode: %v", err)
	}
	if len(board.Robots) == 0 {
		t.Error("the final board carries no robots: a spectator sees an empty arena")
	}
	if len(board.Bases) != len(m.Colonies) {
		t.Errorf("the final board carries %d bases, want %d", len(board.Bases), len(m.Colonies))
	}
	if len(board.Colonies) == 0 {
		t.Error("the final board carries no colony stats: there is no standing to show")
	}
	var end End
	if err := json.Unmarshal(events[2].data, &end); err != nil {
		t.Fatalf("end frame does not decode: %v", err)
	}
	if end.Tick != board.Tick {
		t.Errorf("end frame is at tick %d, the final board at %d", end.Tick, board.Tick)
	}

	// Trace inspection survives the end, and does not start a watch: a watch
	// created now would record nothing (the world is frozen) and could evict
	// one that still holds what a spectator came to read — prog.MaxWatched is 8
	// and a finished match may have more viewers than it had players.
	h := NewRobots(svc.Registry(), database)
	for i := range 2 {
		hist, err := h.TraceOf(m.ID, robot, 0)
		if err != nil {
			t.Fatalf("TraceOf() on a finished match = %v", err)
		}
		if !hist.Final {
			t.Error("TraceHistory.Final is false on a match that is over")
		}
		if hist.Watching {
			t.Errorf("poll %d started a watch on a frozen world", i)
		}
		if hist.Tick != board.Tick {
			t.Errorf("TraceOf() reports tick %d, the final board %d", hist.Tick, board.Tick)
		}
	}
}

// TestStreamDisconnect asserts a dropped client leaves nothing behind.
func TestStreamDisconnect(t *testing.T) {
	reg, m := startMatch(t)

	done := make(chan struct{})
	handler := Stream(reg, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		handler(w, r)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/matches/"+strconv.FormatInt(m.ID, 10)+"/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest() = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream = %v", err)
	}
	readEvents(t, resp.Body, 2) // connected and streaming

	cancel() // the client vanishes mid-stream
	_ = resp.Body.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is still running 5s after the client disconnected")
	}

	// The handler starts no goroutines of its own, so the count must come back
	// down once the server's connection goroutines are reaped.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines: %d before, %d after a disconnect", before, after)
	}
}

// TestStreamUnknownMatch: a match id nobody is running is a 404, not a stream
// that never says anything.
func TestStreamUnknownMatch(t *testing.T) {
	reg := lobby.NewRegistry(nil)
	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", Stream(reg, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/api/matches/999/stream", "/api/matches/abc/stream", "/api/matches/0/stream"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s = %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

// TestStreamShutdown is the bug rc-w9s.16 survived to production on: a stream
// held open across a shutdown. Shutdown waits for active requests, and an SSE
// stream never finishes on its own, so before the fix this drained for the full
// grace and returned context.DeadlineExceeded. The stream has to be *open and
// streaming* here — a test that opens no stream proves nothing.
func TestStreamShutdown(t *testing.T) {
	reg, m := startMatch(t)

	stopping := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("GET /api/matches/{id}/stream", Stream(reg, stopping))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// The wiring main.go uses: closed the moment Shutdown starts.
	srv.Config.RegisterOnShutdown(func() { close(stopping) })

	resp, err := http.Get(srv.URL + "/api/matches/" + strconv.FormatInt(m.ID, 10) + "/stream")
	if err != nil {
		t.Fatalf("GET stream = %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	readEvents(t, resp.Body, 2) // connected and streaming, not merely dialled

	// Well under the 30s production grace: the point is that the drain finishes
	// because the stream returns, not because the deadline ran out.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v, want nil: an open SSE stream must not stall the drain", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Shutdown() took %v with one stream open, want near-instant", took)
	}
}

type event struct {
	name string
	data []byte
}

// readEvents reads n SSE events, skipping heartbeat comments.
func readEvents(t *testing.T, body io.Reader, n int) []event {
	t.Helper()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var out []event
	var cur event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ":"): // heartbeat
		case strings.HasPrefix(line, "event: "):
			cur.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = []byte(strings.TrimPrefix(line, "data: "))
		case line == "":
			if cur.name != "" {
				out = append(out, cur)
				if len(out) == n {
					return out
				}
			}
			cur = event{}
		}
	}
	t.Fatalf("stream ended after %d events, want %d: %v", len(out), n, sc.Err())
	return nil
}
