package lobby

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/korjavin/robocolony/internal/db"
)

// Match history, E9: a match that ends keeps its record instead of deleting it,
// so it can be listed, read, and watched back.
//
// Two things are stored, and they answer different questions:
//
//   - the command log, which already existed for restarts (persist.go). It is
//     what a *replay* runs off, and it only replays under a build that
//     simulates identically — see fingerprint.
//   - a summary, written once at the end. It is redundant with the log, and it
//     is stored anyway: this project deploys on every push, so a balance change
//     invalidates every log in flight, and without the summary a redeploy would
//     empty the history page. The standing and the graph do not depend on the
//     fingerprint, so they survive one.

// Why a replay was refused. internal/server maps these to 404 and 409; they are
// sentinels rather than statusError because that type is unexported and the
// SSE handler lives in another package.
var (
	// ErrNoRecord: no finished match with a replay record under this id.
	ErrNoRecord = errors.New("lobby: no replay record for this match")
	// ErrStaleReplay is the message the UI shows verbatim.
	ErrStaleReplay = errors.New("this match was recorded by an older build and can no longer be replayed")
)

// Summary is what a finished match keeps: the final standing and the sampled
// series behind the graph. Both are the shapes the live match already serves —
// Info is GET /api/matches/{id}, History rides the stream's init frame — so the
// history page renders them with the code it already has.
type Summary struct {
	Info    Info    `json:"info"`
	History History `json:"history"`
}

// HistoryEntry is one row of GET /api/history: the standing, and enough to
// decide whether the match is worth opening. No series — that is per-match.
type HistoryEntry struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	StartedAt  string   `json:"started_at"`
	FinishedAt string   `json:"finished_at"`
	EndTick    uint64   `json:"end_tick"`
	TickRate   int      `json:"tick_rate"`
	Replayable bool     `json:"replayable"`
	Colonies   []Status `json:"colonies"`
}

// HistoryDetail is GET /api/history/{id}: the summary, plus whether this build
// can still replay the match and why not when it cannot.
type HistoryDetail struct {
	Summary
	Replayable bool   `json:"replayable"`
	Reason     string `json:"reason"`
}

// History lists the finished matches, newest first.
//
// A row whose summary does not decode is skipped rather than failing the list:
// one unreadable match must not take the page down with it.
func (s *Service) History(ctx context.Context) ([]HistoryEntry, error) {
	recs, err := s.db.ListFinishedMatchLogs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, 0, len(recs))
	for _, rec := range recs {
		var sum Summary
		if err := json.Unmarshal([]byte(rec.Summary), &sum); err != nil {
			slog.Warn("skipping a finished match whose summary does not decode",
				"lobby_id", rec.LobbyID, "err", err)
			continue
		}
		out = append(out, HistoryEntry{
			ID:         rec.LobbyID,
			Name:       sum.Info.Name,
			StartedAt:  rec.StartedAt.Format(time.RFC3339),
			FinishedAt: rec.FinishedAt.Format(time.RFC3339),
			EndTick:    sum.Info.EndTick,
			TickRate:   sum.Info.TickRate,
			Replayable: rec.Fingerprint == fingerprint(),
			Colonies:   sum.Info.Colonies,
		})
	}
	return out, nil
}

// HistoryOf returns one finished match's standing and series.
func (s *Service) HistoryOf(ctx context.Context, id int64) (HistoryDetail, error) {
	rec, err := s.finishedRecord(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNoRecord) {
			return HistoryDetail{}, errf(http.StatusNotFound, "no finished match with this id")
		}
		return HistoryDetail{}, err
	}
	var sum Summary
	if err := json.Unmarshal([]byte(rec.Summary), &sum); err != nil {
		return HistoryDetail{}, fmt.Errorf("lobby: match %d: decode summary: %w", id, err)
	}
	detail := HistoryDetail{Summary: sum, Replayable: rec.Fingerprint == fingerprint()}
	if !detail.Replayable {
		detail.Reason = ErrStaleReplay.Error()
	}
	return detail, nil
}

// Replay rebuilds a finished match at tick from and hands it out. from is
// clamped to [0, end tick].
//
// The match it returns is private to the caller: it is not in the registry, no
// tick driver is behind it, and nothing else can see it — a replay must never
// touch the live world. ReplayTo advances it a tick at a time.
//
// ponytail: a rebuild per connection, and the client's every control (pause,
// scrub, speed) is a reconnect, so this is the cost of a control action.
// Measured at 0.23-0.32s for a full default match on a free core (6000 ticks, 4
// colonies — TestRebuildCost logs it), which is what settled the design.
//
// It is O(target tick), and since rc-8hu dropped maxDurationSec there is no
// ceiling on match duration and therefore none on this: a match ten times the
// default length costs ten times as much to scrub in. replayBudget is what
// stops that from being unbounded work on a request goroutine. The upgrade path
// when the budget starts firing on matches people actually play is a warm
// session cached per (user, match) with a TTL, so a pause/resume or a forward
// seek reuses the world instead of rebuilding it. Not worth the eviction rules
// until a long match actually exists.
func (s *Service) Replay(ctx context.Context, id int64, from uint64) (*Match, error) {
	rec, err := s.finishedRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.Fingerprint != fingerprint() {
		return nil, ErrStaleReplay
	}
	lobbyRow, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return nil, notFound(err, "match")
	}
	set, err := decodeSettings(lobbyRow.SettingsJSON)
	if err != nil {
		return nil, err
	}
	members, err := s.db.LobbyMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := newReplay(lobbyRow, set, members, rec)
	if err != nil {
		return nil, err
	}
	if end := set.durationTicks(); from > end {
		from = end
	}
	// The one bound on the rebuild. A tick cap would not be one: a bigger board
	// is slower per tick, so only the clock bounds how long the goroutine is
	// occupied. It doubles as the hang-up path — the caller's ctx is the
	// parent, so a client that goes away stops the loop through the same check.
	ctx, cancel := context.WithTimeout(ctx, replayBudget)
	defer cancel()
	if err := m.ReplayTo(ctx, from); err != nil {
		return nil, err
	}
	return m, nil
}

// replayBudget is a ceiling on how long one request goroutine may be occupied
// rebuilding, not a latency target: the honest number for a default match is a
// fraction of a second (TestRebuildCost logs 0.23-0.32s for a full 600s match
// on a free core, and up to 6.9s on an oversubscribed one). 30s is set well
// clear of that so it cannot fire on a legitimate default match on a loaded
// box; what it refuses is the match long enough that rebuilding it is an
// unbounded cost per scrub.
//
// A var so a test can shrink it. Nothing writes it outside tests.
var replayBudget = 30 * time.Second

// finishedRecord reads the record of a match that is over. A record without
// finished_at belongs to a match that is still running, whose endpoint is the
// live stream, so it reads as absent here.
func (s *Service) finishedRecord(ctx context.Context, id int64) (db.MatchLog, error) {
	rec, err := s.db.MatchLogByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.MatchLog{}, ErrNoRecord
	}
	if err != nil {
		return db.MatchLog{}, err
	}
	if rec.FinishedAt.IsZero() {
		return db.MatchLog{}, ErrNoRecord
	}
	return rec, nil
}

func (s *Service) handleHistory(w http.ResponseWriter, r *http.Request) {
	list, err := s.History(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Service) handleHistoryOf(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	detail, err := s.HistoryOf(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
