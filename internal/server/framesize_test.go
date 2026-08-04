package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// The frame size harness (rc-w9s.31). maxFrameBytes is a budget, and a budget
// nobody can re-measure is a guess: E4.1 extrapolated the 8-colony frame from a
// 2-colony one, PR #51 re-extrapolated it, and both numbers were arithmetic
// rather than observation. This runs the largest match the lobby permits and
// marshals the frames the stream would actually send.
//
// It asserts nothing and is opt-in: a full-size match has to be replayed
// thousands of ticks to have robots in it, which is far too slow for every
// `go test ./...`.
//
//	ROBOCOLONY_FRAMESIZE=1 go test ./internal/server/ -run TestFrameSize -v
//
// The measurement it printed is recorded at maxFrameBytes in stream.go.
// frameSizeSeed is the arena every run of the harness measures, so two runs
// are comparable and the numbers recorded at maxFrameBytes can be checked.
const frameSizeSeed = 0x5eed

func TestFrameSize(t *testing.T) {
	if os.Getenv("ROBOCOLONY_FRAMESIZE") == "" {
		t.Skip("set ROBOCOLONY_FRAMESIZE=1 to run the frame size harness")
	}
	// Two lobbies: the defaults every match is created with, and the legal
	// ceiling — richness, spawn rate and starting budget all at the maximum
	// Settings.Validate accepts, which is the largest frame a player can ask
	// this build for without changing it.
	for _, cfg := range []struct {
		name string
		max  bool
	}{{"default", false}, {"maxsettings", true}} {
		for _, tick := range []int64{600, 1800, 3600, 6000} {
			t.Run(cfg.name+"/"+strconv.FormatInt(tick, 10)+"ticks", func(t *testing.T) {
				m := replayLargeMatch(t, tick, cfg.max)
				info, hist := m.Info(), m.History()
				var in Init
				var snap Snapshot
				m.Read(func(w *sim.World, rt *prog.Runtime) {
					in = NewInit(info, m.Colonies, w, hist)
					snap = NewSnapshot(w, rt, info.EndTick)
				})
				initBytes, _ := json.Marshal(in)
				tickBytes, _ := json.Marshal(snap)
				t.Logf("tick %d: colonies %d robots %d loose %d | tick frame %d B (%.1f KB, budget %d KB) | init frame %d B",
					snap.Tick, len(snap.Colonies), len(snap.Robots), len(snap.Loose),
					len(tickBytes), float64(len(tickBytes))/1024, maxFrameBytes>>10, len(initBytes))
			})
		}
	}
}

// replayLargeMatch returns the biggest match this build can field — eight
// colonies on the full arena — fast-forwarded to tick.
//
// It gets there through the restore path rather than by waiting: the tick
// driver runs at wall-clock rate, and lobby.replay steps a match to a recorded
// tick as fast as the CPU allows. That is the production restart path, so the
// world it produces is the one a player would be watching.
//
// The AI seats are the two profiles that do not hunt: an aggressive colony
// clears the board, and an empty board is the small frame, not the big one.
func replayLargeMatch(t *testing.T, tick int64, maxSettings bool) *lobby.Match {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "framesize.db")
	database, err := db.Open(ctx, path)
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	svc := lobby.New(database)
	owner, err := database.UpsertUser(ctx, "sub-owner", "owner@example.com", "Owner")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	set := lobby.DefaultSettings()
	set.MaxPlayers = 1
	set.DurationSec = 3600 // 36000 ticks: every measured tick is mid-match
	if maxSettings {
		set.Richness, set.SpawnPerMin, set.StartingBudget = 0.25, 120, 3450
	}
	view, err := svc.Create(ctx, owner.ID, "frame size", set)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	ai := []lobby.Profile{}
	for len(ai) < 7 {
		ai = append(ai, lobby.ProfilePeaceful, lobby.ProfileTutorial)
	}
	if _, err := svc.SetAI(ctx, view.ID, owner.ID, ai[:7]); err != nil {
		t.Fatalf("SetAI() = %v", err)
	}
	// Create draws the seed, on purpose: a player must not be able to shop for
	// an arena. A measurement must be re-runnable, so it is pinned here, in the
	// stored settings the match is built from, while the lobby is still open.
	stored, err := database.LobbyByID(ctx, view.ID)
	if err != nil {
		t.Fatalf("LobbyByID() = %v", err)
	}
	var pinned lobby.Settings
	if err := json.Unmarshal([]byte(stored.SettingsJSON), &pinned); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	pinned.Seed = frameSizeSeed
	encoded, err := json.Marshal(pinned)
	if err != nil {
		t.Fatalf("encode settings: %v", err)
	}
	if err := database.UpdateLobbySettings(ctx, view.ID, owner.ID, string(encoded)); err != nil {
		t.Fatalf("UpdateLobbySettings() = %v", err)
	}
	if _, err := svc.Start(ctx, view.ID, owner.ID); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	// Shutdown flushes the replay record; the tick it records is whatever the
	// driver reached, and the line below moves it to the tick we want.
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v", err)
	}
	rec, err := database.MatchLogByID(ctx, view.ID)
	if err != nil {
		t.Fatalf("MatchLogByID() = %v", err)
	}
	rec.Tick = tick
	if err := database.SaveMatchLog(ctx, rec); err != nil {
		t.Fatalf("SaveMatchLog() = %v", err)
	}

	restored := lobby.New(database)
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := restored.Shutdown(stop); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})
	started := time.Now()
	if err := restored.Restore(ctx); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	m, ok := restored.Registry().Get(view.ID)
	if !ok {
		t.Fatalf("match %d is not running after Restore", view.ID)
	}
	t.Logf("replayed %d ticks in %s", tick, time.Since(started).Round(time.Millisecond))
	return m
}
