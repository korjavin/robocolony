package server

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Replay speed limits. A quarter rate is slow enough to watch a fight
// tick by tick; sixteen times covers an hour-long match in four minutes, and
// the frames are the same size as a live one's, so it is a sixteenth of the
// bandwidth the browser would need for sixteen live matches.
const (
	minSpeed, maxSpeed = 0.25, 16
)

// Replay is GET /api/matches/{id}/replay?from=<tick>&speed=<float>: a finished
// match played back over exactly the frames of the live stream — one init, one
// tick per simulation tick, one end. The client that renders a live match
// therefore renders a replay with no second code path, and this handler carries
// no protocol of its own.
//
// The rebuild happens here, on the request's goroutine, before the first byte:
// Service.Replay re-simulates the match from tick 0 up to `from` — about 0.3s
// for a full default match, and proportional to the target tick with no ceiling
// on match duration (see the ponytail note there). Every client control is a
// reconnect, so that is the cost of a scrub or a speed change. It is bounded by
// the replay budget, which arrives here as context.DeadlineExceeded and is a
// 503; the client hanging up mid-rebuild arrives through the same return as
// context.Canceled and is not an error at all.
//
// Like Stream, this handler owns no goroutines and selects on stopping, so a
// client that disappears leaves nothing behind and a shutdown can drain.
func Replay(svc *lobby.Service, stopping <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "no such match", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		from, speed := replayParams(r)

		ctx := r.Context()
		m, err := svc.Replay(ctx, id, from)
		switch {
		case errors.Is(err, lobby.ErrNoRecord):
			http.Error(w, "there is no recording of this match", http.StatusNotFound)
			return
		case errors.Is(err, lobby.ErrStaleReplay):
			// 409, not 404: the match exists and its standing is still served
			// by /api/history/{id}. Only the replay is refused, and never
			// silently — a log replayed under a build that simulates
			// differently is a different game (internal/lobby/persist.go).
			http.Error(w, lobby.ErrStaleReplay.Error(), http.StatusConflict)
			return
		case errors.Is(err, context.Canceled):
			// The client hung up while the rebuild was running. Not an error:
			// nothing to write to, and nothing went wrong.
			return
		case errors.Is(err, context.DeadlineExceeded):
			// The replay budget. 503, not 500: nothing is broken, the server
			// declined work it will not do on a request goroutine. The rebuild
			// is entirely before the first byte, so a real status is still
			// available here.
			slog.Warn("a replay rebuild ran out of budget", "match_id", id, "from", from)
			http.Error(w, "this match is too long to replay from here", http.StatusServiceUnavailable)
			return
		case err != nil:
			slog.Error("could not rebuild a match for replay", "match_id", id, "from", from, "err", err)
			http.Error(w, "this match could not be replayed", http.StatusInternalServerError)
			return
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		info := m.Info()
		hist := m.History() // takes the match lock, so not inside Read
		// Re-derived, not stored: the rebuild above re-ran every tick through
		// Match.step, which is the same step that fills the feed on a live
		// match (internal/lobby/events.go). Both take the match lock, so both
		// are read outside Read.
		feed := m.Events(0)
		var initFrame Init
		var board Snapshot
		m.Read(func(world *sim.World, rt *prog.Runtime) {
			initFrame = NewInit(info, m.Colonies, world, hist, feed)
			board = NewSnapshot(world, rt, info.EndTick, nil)
		})
		sentEvents := board.Tick
		if _, err := send(w, flusher, "init", initFrame); err != nil {
			return
		}
		if _, err := send(w, flusher, "tick", board); err != nil {
			return
		}
		slog.Info("replay stream opened", "match_id", id, "from", board.Tick,
			"end_tick", info.EndTick, "speed", speed, "history_points", len(hist.Ticks))

		tick := board.Tick
		if tick >= info.EndTick {
			_, _ = send(w, flusher, "end", End{Tick: tick})
			return
		}

		ticker := time.NewTicker(time.Duration(float64(time.Second/lobby.TickRate) / speed))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopping:
				// An ordinary disconnect, like the live stream: an end frame
				// would tell the client the replay is over at the tick it
				// stopped at.
				slog.Info("replay stream closed: server shutting down", "match_id", id, "tick", tick)
				return
			case <-ticker.C:
				if err := m.ReplayTo(ctx, tick+1); err != nil {
					if ctx.Err() != nil {
						// The client went away between the select and here:
						// ReplayTo returns the ctx error, which is a disconnect
						// rather than a broken record.
						return
					}
					// The record replayed this far and no further: corrupt past
					// the point already sent. Say the replay is over rather
					// than leaving the client waiting for frames.
					slog.Error("replay stopped short", "match_id", id, "tick", tick, "err", err)
					_, _ = send(w, flusher, "end", End{Tick: tick})
					return
				}
				var snap Snapshot
				m.Read(func(world *sim.World, rt *prog.Runtime) {
					snap = NewSnapshot(world, rt, info.EndTick, nil)
				})
				tick = snap.Tick
				// Nothing else drives this match, so the cursor is exact here;
				// it is advanced off the events for the same reason as the live
				// stream, which is the one code path a client sees.
				if evs := m.Events(sentEvents); len(evs) > 0 {
					sentEvents = evs[len(evs)-1].Tick + 1
					snap.Events = newEvents(evs)
				}
				if _, err := send(w, flusher, "tick", snap); err != nil {
					return
				}
				if tick >= info.EndTick {
					_, _ = send(w, flusher, "end", End{Tick: tick})
					slog.Info("replay stream closed: reached the end", "match_id", id, "tick", tick)
					return
				}
			}
		}
	}
}

// replayParams reads from and speed. Both are clamped rather than rejected: a
// timeline that scrubs past the end should land on the end, not on a 400.
func replayParams(r *http.Request) (from uint64, speed float64) {
	from, _ = strconv.ParseUint(r.URL.Query().Get("from"), 10, 64) // 0 when absent or junk
	speed = 1
	if s, err := strconv.ParseFloat(r.URL.Query().Get("speed"), 64); err == nil && !math.IsNaN(s) {
		speed = min(max(s, minSpeed), maxSpeed)
	}
	return from, speed
}
