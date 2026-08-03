// Package lobby is how a player gets from "logged in" to "a match is running":
// lobby lifecycle (create, join, leave, start), server-side validation of the
// match settings, the registry of running matches, and the per-match tick
// driver.
//
// The live world lives in memory, but it is not lost on a restart: every match
// records the seed it was generated from and the player commands applied since
// (persist.go), and Restore replays them at startup. Design §2.2 has a colony
// keep running while its player is away, and a deploy is not an exception.
package lobby

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/db"
)

// Service is the lobby feature: its persistence, its match registry and its
// HTTP surface.
type Service struct {
	db  *db.DB
	reg *Registry
}

// New wires a lobby service to the database. Nothing ticks until a match
// starts, and nothing is restored until Restore runs.
func New(database *db.DB) *Service {
	s := &Service{db: database}
	s.reg = NewRegistry(s.save, s.forget)
	return s
}

// Registry exposes the running matches, for E4.1's world stream.
func (s *Service) Registry() *Registry { return s.reg }

// Shutdown stops every running match's tick driver.
func (s *Service) Shutdown(ctx context.Context) error { return s.reg.Shutdown(ctx) }

// Restore brings back the matches a previous process was running, replaying
// each one's recorded input log (persist.go). Call it once, before the
// listener: a request that started a match into a half-restored registry could
// collide with the match being restored under the same id.
//
// A match whose record is missing, corrupt, or written by a build that
// simulates differently is finished instead — the reaping this used to do
// unconditionally, now the fallback rather than the rule. A bad record must
// never keep the server from coming up, so only a database failure is returned.
func (s *Service) Restore(ctx context.Context) error {
	running, err := s.db.ListLobbies(ctx, db.LobbyRunning)
	if err != nil {
		return err
	}
	// ponytail: sequential, and each replay costs CPU proportional to how far
	// its match has run (worst case ~7s, see persist.go). Restore them
	// concurrently if a host ever carries enough long matches for startup to
	// outlast the deploy's patience.
	for _, l := range running {
		rerr := s.restore(ctx, l)
		if rerr == nil {
			continue
		}
		slog.Warn("could not restore a running match, finishing it", "lobby_id", l.ID, "err", rerr)
		if err := s.db.SetLobbyState(ctx, l.ID, db.LobbyFinished); err != nil {
			return err
		}
		if err := s.db.DeleteMatchLog(ctx, l.ID); err != nil {
			return err
		}
	}
	return nil
}

// restore replays one match and puts a tick driver back behind it.
func (s *Service) restore(ctx context.Context, lobby db.Lobby) error {
	rec, err := s.db.MatchLogByID(ctx, lobby.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("no replay record: the match predates match persistence")
		}
		return err
	}
	if rec.Fingerprint != fingerprint() {
		return fmt.Errorf("recorded by a build that simulates differently (%s, this build is %s)", rec.Fingerprint, fingerprint())
	}
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		return err
	}
	members, err := s.db.LobbyMembers(ctx, lobby.ID)
	if err != nil {
		return err
	}
	started := time.Now()
	match, err := replay(lobby, set, members, rec)
	if err != nil {
		return err
	}
	if err := s.reg.Start(match, s.matchEnded); err != nil {
		return err
	}
	slog.Info("match restored", "match_id", match.ID, "tick", rec.Tick,
		"commands", len(match.log), "replay_ms", time.Since(started).Milliseconds())
	return nil
}

// save writes a match's replay record. It runs on the tick driver's goroutine,
// with no lock held: Match.record copies under the match lock and this writes
// afterwards, so the simulation never waits on the disk.
//
// A failed save is logged, not propagated: the match keeps running, and the
// worst it costs is the rewind of a restart that never happens.
func (s *Service) save(m *Match) {
	tick, log, finished := m.record()
	if finished {
		return // forget removes the record instead
	}
	commands, err := json.Marshal(log)
	if err != nil {
		slog.Error("could not encode a match command log", "match_id", m.ID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.SaveMatchLog(ctx, db.MatchLog{
		LobbyID:     m.ID,
		Fingerprint: fingerprint(),
		Tick:        int64(tick),
		StartedAt:   m.Started,
		Commands:    string(commands),
	}); err != nil {
		slog.Error("could not save a match replay record", "match_id", m.ID, "err", err)
	}
}

// forget drops the replay record of a match that has ended.
func (s *Service) forget(m *Match) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.DeleteMatchLog(ctx, m.ID); err != nil {
		slog.Error("could not delete a finished match's replay record", "match_id", m.ID, "err", err)
	}
}

// statusError carries the HTTP status a domain failure maps to, so the handlers
// stay four lines each and every unmapped error is a 500.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

func errf(code int, format string, a ...any) error {
	return statusError{code: code, msg: fmt.Sprintf(format, a...)}
}

// Domain API. The HTTP handlers below are thin wrappers over these; the
// ownership checks live here, where they are testable without a session.

// LobbyView is a lobby plus its seats.
type LobbyView struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	OwnerID   int64       `json:"owner_id"`
	State     string      `json:"state"`
	Settings  Settings    `json:"settings"`
	Members   []db.Member `json:"members"`
	CreatedAt string      `json:"created_at"`
}

// Create opens a lobby owned by the caller, with the seed drawn server-side.
func (s *Service) Create(ctx context.Context, userID int64, name string, set Settings) (LobbyView, error) {
	if name == "" {
		return LobbyView{}, errf(http.StatusBadRequest, "name is required")
	}
	if len(name) > 64 {
		return LobbyView{}, errf(http.StatusBadRequest, "name must be at most 64 characters")
	}
	if err := set.Validate(); err != nil {
		return LobbyView{}, errf(http.StatusBadRequest, "%s", err)
	}
	seed, err := randomSeed()
	if err != nil {
		return LobbyView{}, err
	}
	set.Seed = seed // never the client's choice

	encoded, err := json.Marshal(set)
	if err != nil {
		return LobbyView{}, err
	}
	lobby, err := s.db.CreateLobby(ctx, userID, name, string(encoded))
	if err != nil {
		return LobbyView{}, err
	}
	return s.view(ctx, lobby)
}

// List returns the open lobbies.
func (s *Service) List(ctx context.Context) ([]LobbyView, error) {
	lobbies, err := s.db.ListLobbies(ctx, db.LobbyOpen)
	if err != nil {
		return nil, err
	}
	out := make([]LobbyView, 0, len(lobbies))
	for _, l := range lobbies {
		v, err := s.view(ctx, l)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one lobby.
func (s *Service) Get(ctx context.Context, id int64) (LobbyView, error) {
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return LobbyView{}, notFound(err, "lobby")
	}
	return s.view(ctx, lobby)
}

// Join seats the caller. Design §2.1: nobody may join after the start.
func (s *Service) Join(ctx context.Context, id, userID int64) (LobbyView, error) {
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return LobbyView{}, notFound(err, "lobby")
	}
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		return LobbyView{}, err
	}
	switch err := s.db.JoinLobby(ctx, id, userID, set.MaxPlayers); {
	case errors.Is(err, db.ErrLobbyFull):
		return LobbyView{}, errf(http.StatusConflict, "lobby is full")
	case errors.Is(err, db.ErrLobbyNotOpen):
		return LobbyView{}, errf(http.StatusConflict, "the match has already started")
	case errors.Is(err, sql.ErrNoRows):
		return LobbyView{}, errf(http.StatusNotFound, "lobby not found")
	case err != nil:
		return LobbyView{}, err
	}
	return s.Get(ctx, id)
}

// SetAI replaces the lobby's AI colonies (design §12 P2). It is the whole
// surface: adding, removing and reordering are all "here is the list I want",
// which is idempotent and cannot half-apply.
//
// Owner only, and only while the lobby is open — design §2.1's "nobody joins
// after the start" is about the colony list, and an AI colony is a colony.
func (s *Service) SetAI(ctx context.Context, id, userID int64, profiles []Profile) (LobbyView, error) {
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return LobbyView{}, notFound(err, "lobby")
	}
	if lobby.OwnerID != userID {
		return LobbyView{}, errf(http.StatusForbidden, "only the lobby owner may change the AI colonies")
	}
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		return LobbyView{}, err
	}
	set.AI = profiles
	if err := set.Validate(); err != nil {
		return LobbyView{}, errf(http.StatusBadRequest, "%s", err)
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		return LobbyView{}, err
	}
	if err := s.db.UpdateLobbySettings(ctx, id, userID, string(encoded)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LobbyView{}, errf(http.StatusConflict, "the match has already started")
		}
		return LobbyView{}, err
	}
	return s.Get(ctx, id)
}

// Leave frees the caller's seat. The owner cannot leave: the lobby would have
// nobody able to start or settle it.
func (s *Service) Leave(ctx context.Context, id, userID int64) error {
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return notFound(err, "lobby")
	}
	if lobby.OwnerID == userID {
		return errf(http.StatusConflict, "the owner cannot leave their own lobby")
	}
	if err := s.db.LeaveLobby(ctx, id, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf(http.StatusConflict, "you are not in this lobby, or it has already started")
		}
		return err
	}
	return nil
}

// Start generates the arena, registers the match and starts its tick driver.
// Only the owner may start, and only once: both rules are enforced by the
// UPDATE in db.StartLobby, not by a read-then-write here.
func (s *Service) Start(ctx context.Context, id, userID int64) (Info, error) {
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return Info{}, notFound(err, "lobby")
	}
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		return Info{}, err
	}
	if err := s.db.StartLobby(ctx, id, userID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return Info{}, err
		}
		if lobby.OwnerID != userID {
			return Info{}, errf(http.StatusForbidden, "only the lobby owner may start the match")
		}
		return Info{}, errf(http.StatusConflict, "the match has already started")
	}

	// Membership is read *after* the flip on purpose: joining and leaving are
	// both gated on state = 'open', so from here the seats cannot move, and
	// the colonies in the match are exactly the rows in lobby_members. Reading
	// first would let a join land in the gap and get a seat but no colony.
	members, err := s.db.LobbyMembers(ctx, id)
	if err == nil && len(members) == 0 {
		err = errf(http.StatusConflict, "the lobby is empty")
	}
	var match *Match
	if err == nil {
		match, err = newMatch(lobby, set, members)
	}
	if err == nil {
		err = s.reg.Start(match, s.matchEnded)
	}
	if err != nil {
		// The row says running but nothing is: put it back so the lobby is not
		// stranded.
		if rerr := s.db.SetLobbyState(ctx, id, db.LobbyOpen); rerr != nil {
			slog.Error("could not reopen a lobby whose start failed", "lobby_id", id, "err", rerr)
		}
		return Info{}, err
	}
	slog.Info("match started", "match_id", match.ID, "colonies", len(match.Colonies), "seed", set.Seed)
	return match.Info(), nil
}

// Match returns a running or finished match's metadata.
func (s *Service) Match(ctx context.Context, id int64) (Info, error) {
	if m, ok := s.reg.Get(id); ok {
		return m.Info(), nil
	}
	// Not in memory: either it never started, or it is over — Restore puts a
	// running match back at startup, and finishes the ones it cannot replay.
	lobby, err := s.db.LobbyByID(ctx, id)
	if err != nil {
		return Info{}, notFound(err, "match")
	}
	if lobby.State == db.LobbyOpen {
		return Info{}, errf(http.StatusNotFound, "the match has not started yet")
	}
	return Info{}, errf(http.StatusGone, "this match is over")
}

// matchEnded settles the lobby row when a match reaches its duration. It runs
// on the tick driver's goroutine, long after the starting request is gone, so
// it carries its own context.
func (s *Service) matchEnded(m *Match) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.SetLobbyState(ctx, m.ID, db.LobbyFinished); err != nil {
		slog.Error("could not mark a finished lobby", "lobby_id", m.ID, "err", err)
	}
}

func (s *Service) view(ctx context.Context, lobby db.Lobby) (LobbyView, error) {
	set, err := decodeSettings(lobby.SettingsJSON)
	if err != nil {
		return LobbyView{}, err
	}
	members, err := s.db.LobbyMembers(ctx, lobby.ID)
	if err != nil {
		return LobbyView{}, err
	}
	return LobbyView{
		ID:        lobby.ID,
		Name:      lobby.Name,
		OwnerID:   lobby.OwnerID,
		State:     lobby.State,
		Settings:  set,
		Members:   members,
		CreatedAt: lobby.CreatedAt.Format(time.RFC3339),
	}, nil
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errf(http.StatusNotFound, "%s not found", what)
	}
	return err
}

// HTTP surface. Every route below is behind RequireAuth; there is no second
// auth path in this package.

// Routes registers the lobby and match endpoints. requireAuth is
// auth.Handler.RequireAuth.
func (s *Service) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, requireAuth(h))
	}
	handle("GET /api/lobbies", s.handleList)
	handle("POST /api/lobbies", s.handleCreate)
	handle("GET /api/lobbies/{id}", s.handleGet)
	handle("POST /api/lobbies/{id}/join", s.handleJoin)
	handle("POST /api/lobbies/{id}/leave", s.handleLeave)
	handle("PUT /api/lobbies/{id}/ai", s.handleSetAI)
	handle("POST /api/lobbies/{id}/start", s.handleStart)
	handle("GET /api/matches/{id}", s.handleMatch)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	lobbies, err := s.List(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if lobbies == nil {
		lobbies = []LobbyView{}
	}
	// ai_profiles is the menu a lobby screen builds its "add an AI opponent"
	// control from, so the client never hard-codes the profile names.
	writeJSON(w, http.StatusOK, map[string]any{
		"lobbies": lobbies, "defaults": DefaultSettings(), "ai_profiles": Profiles(),
	})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string    `json:"name"`
		Settings *Settings `json:"settings"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	// Absent settings mean "the defaults"; present ones are validated in full.
	set := DefaultSettings()
	if body.Settings != nil {
		set = *body.Settings
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := s.Create(r.Context(), user.ID, body.Name, set)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	view, err := s.Get(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := s.Join(r.Context(), id, user.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleLeave(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	if err := s.Leave(r.Context(), id, user.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "left"})
}

func (s *Service) handleSetAI(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var body struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := s.SetAI(r.Context(), id, user.ID, body.Profiles)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	info, err := s.Start(r.Context(), id, user.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Service) handleMatch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	info, err := s.Match(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errf(http.StatusNotFound, "no such lobby")
	}
	return id, nil
}

// decodeBody reads a small JSON body. The limit is what stops an unbounded
// request from being read into memory before it is rejected.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errf(http.StatusBadRequest, "invalid request body: %s", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeErr maps a domain error to its status. Anything unmapped is a 500 whose
// detail is logged rather than returned.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var se statusError
	if errors.As(err, &se) {
		writeJSON(w, se.code, map[string]string{"error": se.msg})
		return
	}
	slog.Error("lobby request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
