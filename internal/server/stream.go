package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// heartbeatEvery is how often an SSE comment goes out when nothing else does.
// Tick frames already flow ten times a second on a live match; this is for the
// gaps — a match that has just been registered, or a proxy that would otherwise
// reap an idle connection.
const heartbeatEvery = 15 * time.Second

// maxFrameBytes is the size above which full snapshots stop being a reasonable
// answer. Exceeding it is logged, not worked around: deltas are a bead, not
// something to invent inside a stream handler.
//
// Measured, not extrapolated (rc-w9s.31, re-measured for the arena presets in
// rc-rys): eight colonies, seed 0x5eed, replayed to the tick shown, tick frame
// in bytes. framesize_test.go is the harness and re-runs this. "stressed" is
// richness 0.25, spawn 120/min, budget 3450 — a chosen worst case, not a legal
// ceiling: richness and budget have had no ceiling since rc-8hu.
//
//	                       tick 600  tick 1800  tick 3600  tick 6000
//	default, M (64×64)       10,593     10,784     11,297     11,037
//	  robots                     22         22         24         24
//	default, XL (128×128)    19,351     20,233     19,996     20,015
//	  robots                     25         31         33         38
//	stressed, M              46,692     54,178     59,361     63,633
//	  robots                     57        116        160        172
//	stressed, XL            148,792    147,558    152,256    156,396
//	  robots                     55        117        238        363
//
// The two extrapolations this budget was argued against — 45 KB in E4.1, then
// 75 KB while adding statistics — were both pessimistic by about six times:
// they assumed 8 colonies × 20 robots, and a default board sustains about 25
// robots in total, because the loose components run out and production stalls.
// An ordinary match sits at a sixth of this budget, and the largest arena a
// host can pick only doubles that: a bigger board is more ground, and richness
// is per cell, so the frame grows with the cell count rather than with the
// side.
//
// The stressed rows are what this WARN is for. Before the rc-w9s.31 trim (the
// approved designs moved to the init frame — 11 KB a frame of data that cannot
// change while a match runs) the M row crossed the budget by tick 6000; the XL
// row sits at two and a half times it throughout. Neither is a bound this code
// enforces, and neither is new: the same harness measures 97 KB on a *64×64*
// board at richness 1.0, which any host can create today. The knob is the loose
// component count, and the arena preset is one of three ways to turn it. When
// deltas get built, that is the reason — with a real deployment behind it
// rather than an extrapolation.
//
// The init frame grows with the arena too, and it is the one that scales
// cleanly: one terrain char per cell puts XL at 31–36 KB against M's 18–23 KB.
// It is sent once per connection, not per tick.
const maxFrameBytes = 64 << 10

// pollInterval is how often the stream checks for a new tick. Deliberately
// faster than the tick itself: two independent tickers at the same rate drift
// into sending a tick twice and skipping the next one, which the renderer sees
// as a stutter. Polling at twice the rate and sending only on a tick change
// puts exactly one frame on the wire per tick.
const pollInterval = time.Second / lobby.TickRate / 2

// Stream is GET /api/matches/{id}/stream: the authoritative world, as SSE.
//
// One event: init frame on connect carries the terrain and everything else that
// cannot change, then one event: tick frame per simulation tick, then a single
// event: end when the match is over. Design §4.3 — no fog of war for the
// observer, so every colony is in every frame.
//
// The handler owns no goroutines: everything runs on the request's own, so a
// client that disappears mid-frame leaves nothing behind but a failed write.
//
// stopping is closed when the process is shutting down. An SSE stream is an
// active request that by design never finishes, so http.Server.Shutdown can
// only ever time out waiting for it; the handler selects on this alongside the
// request context and returns, which lets the drain complete. A nil channel
// blocks forever, which is the right behaviour for a server that never stops.
func Stream(reg *lobby.Registry, stopping <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "no such match", http.StatusNotFound)
			return
		}
		m, ok := reg.Get(id)
		if !ok {
			http.Error(w, "match not found", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok { // no http.Server in this repo strips it; a middleware that wraps
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		// Traefik, nginx and friends buffer a response body by default, which
		// turns a 10Hz stream into one big delivery at disconnect.
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // headers out now, so the client's onopen fires

		ctx := r.Context()

		// The board goes out with the init frame rather than on the next tick.
		// That is what makes a finished match watchable (design §12 P2): E5.2
		// froze Step once a match ends, so its world never ticks again, and a
		// spectator who connects after the end would otherwise be sent terrain
		// with nothing standing on it — no robots, no bases, no standing. On a
		// live match it costs nothing: the loop below only sends a frame when
		// the tick moves, so this is the frame it would have sent 100ms later.
		info := m.Info()
		// Outside Read: History takes the match lock itself, and this mutex is
		// not reentrant. It may therefore be one sample ahead of the board
		// below, which the client handles by appending only ticks newer than
		// the last one in the series.
		hist := m.History()
		// The whole feed so far, on the init frame beside the history and for
		// the same reason. Outside Read: Events takes the match lock itself.
		feed := m.Events(0)
		var initFrame Init
		var board Snapshot
		m.Read(func(world *sim.World, rt *prog.Runtime) {
			initFrame = NewInit(info, m.Colonies, world, hist, feed)
			board = NewSnapshot(world, rt, info.EndTick, nil)
		})
		// An event is stamped with the tick it happened *during*, so everything
		// the init frame could carry is stamped below board.Tick however far the
		// world moved between the two reads. This is the cursor the loop below
		// advances.
		sentEvents := board.Tick
		n, err := send(w, flusher, "init", initFrame)
		if err != nil {
			return
		}
		tn, err := send(w, flusher, "tick", board)
		if err != nil {
			return
		}
		level := slog.LevelInfo
		if tn > maxFrameBytes {
			// File a bead for deltas; do not invent a protocol here.
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "world stream opened", "match_id", id, "tick", initFrame.Tick,
			"init_bytes", n, "tick_bytes", tn, "robots", len(board.Robots), "loose", len(board.Loose),
			"history_points", len(hist.Ticks))

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		beat := time.NewTicker(heartbeatEvery)
		defer beat.Stop()

		last := board.Tick
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopping:
				// Just close the body: to the client this is an ordinary
				// disconnect, which EventSource retries with backoff. An "end"
				// frame would tell it the match is over and stop the retries.
				slog.Info("world stream closed: server shutting down", "match_id", id, "tick", last)
				return
			case <-beat.C:
				if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case <-ticker.C:
				// Read before the snapshot: a match that finishes in between
				// has a frozen world, so the snapshot below is still the final
				// tick and the end frame after it is still true. The other
				// order would announce the end at a tick that is about to move.
				finished := m.Finished()

				var snap Snapshot
				fresh := false
				m.Read(func(world *sim.World, rt *prog.Runtime) {
					if world.Tick == last {
						return // no new tick yet; nothing to say
					}
					snap = NewSnapshot(world, rt, info.EndTick, nil)
					fresh = true
				})
				if fresh {
					last = snap.Tick
					// Outside Read, so the world may already have moved on and
					// handed us an event from a tick past this frame's.
					// Advancing the cursor off the events themselves rather
					// than off snap.Tick is what makes that harmless: nothing
					// is sent twice and nothing is skipped, and one tick's
					// events are appended in a single critical section, so a
					// cut can never split a tick.
					if evs := m.Events(sentEvents); len(evs) > 0 {
						sentEvents = evs[len(evs)-1].Tick + 1
						snap.Events = newEvents(evs)
					}
					if _, err := send(w, flusher, "tick", snap); err != nil {
						return
					}
				}
				if finished {
					_, _ = send(w, flusher, "end", End{Tick: last})
					slog.Info("world stream closed: match finished", "match_id", id, "tick", last)
					return
				}
			}
		}
	}
}

// send marshals and writes one SSE event, then flushes, and reports the payload
// size. json.Marshal never emits a raw newline, so no data-line splitting is
// needed.
func send(w io.Writer, f http.Flusher, event string, payload any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
		return len(body), err
	}
	f.Flush()
	return len(body), nil
}
