package lobby

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// TickRate is the fixed simulation rate (AGENTS.md locked decision).
const TickRate = 10

const tickInterval = time.Second / TickRate

// Colony is one seat in a running match. The index into Match.Colonies is the
// sim.ColonyID: human seats first, in join order, then one per AI profile.
type Colony struct {
	ID          sim.ColonyID `json:"id"`
	UserID      int64        `json:"user_id"`
	DisplayName string       `json:"display_name"`
	// AI names the profile driving this colony, and is empty for a human seat.
	// UserID is 0 alongside it: user ids start at 1, so no authenticated caller
	// can ever match an AI colony and command its robots.
	AI Profile `json:"ai,omitempty"`
}

// Match is one running simulation, and the only thing in this package that is
// touched by more than one goroutine.
//
// The contract: internal/sim is single-threaded and holds no locks of its own,
// so every read and every write of world (and of the runtime that drives it)
// happens under mu. The tick driver takes mu ten times a second; anything that
// holds it for longer than a tick makes the match stutter for everyone, which
// is why Read's documentation forbids doing I/O inside it.
//
// Nothing about a match's lifetime is tied to an HTTP connection: design §2.2
// keeps a colony running while its player is away.
type Match struct {
	ID       int64    // the lobby id: one match per lobby
	Name     string   // the lobby name
	Settings Settings // seed included, so a finished match is reproducible
	Colonies []Colony
	Started  time.Time

	mu       sync.Mutex
	world    *sim.World
	runtime  *prog.Runtime
	finished bool

	// log is every player command applied so far, in order. With the seed and
	// the settings it is the whole match (see persist.go): it is what a restart
	// replays, and it is why the world itself is never serialised.
	log []Command

	// replayed is how many of log have been re-applied while rebuilding this
	// match from a stored record (persist.go). It stays zero on a live match,
	// which appends to log rather than replaying it.
	replayed int

	// history is the sampled score/robots/collected series behind the match
	// view's graph (history.go). Observation, not state: a replay rebuilds it
	// by re-stepping, and nothing in it is persisted.
	history History

	// events is the match-wide event feed (events.go), bounded by eventCap.
	// Observation on the same terms as history, and re-derived the same way.
	events []sim.Event
}

// newMatch generates the arena and puts every colony in it: one per member,
// then one per AI profile in the settings (design §12 P2).
func newMatch(lobby db.Lobby, s Settings, members []db.Member) (*Match, error) {
	if len(members) == 0 {
		return nil, errors.New("lobby: cannot start a match with no members")
	}
	// Colony kits are resolved before anything is generated: an unknown profile
	// in a stored settings row, or a loadout this build cannot rebuild, must
	// fail the start rather than leave a base standing with nothing approved.
	kits := make([]kit, 0, s.Colonies(len(members)))
	for _, m := range members {
		k, err := memberKit(m, s.budget())
		if err != nil {
			return nil, err
		}
		kits = append(kits, k)
	}
	for _, p := range s.AI {
		k, ok := p.kit()
		if !ok {
			return nil, fmt.Errorf("lobby: unknown AI profile %q", p)
		}
		kits = append(kits, k)
	}

	w := sim.Generate(s.Seed, s.GenOpts(len(kits)))
	if len(w.Bases) != len(kits) {
		// Generation caps colonies by arena area. maxPlayers is far below that
		// cap, so this is a programming error, not a player-reachable one.
		return nil, fmt.Errorf("lobby: generated %d bases for %d colonies", len(w.Bases), len(kits))
	}

	rt := prog.NewRuntime()
	// Forget is the pairing prog.Runtime documents: without it the runtime keeps
	// an evaluator per robot ever built for the length of the match. Nothing in
	// the runtime is simulation state, so this changes no world and no state
	// hash — but the aggressive profile makes wrecks common for the first time,
	// which makes the leak worth closing.
	w.Control, w.OnDestroy = rt.Control, rt.Forget

	m := &Match{
		ID:       lobby.ID,
		Name:     lobby.Name,
		Settings: s,
		Colonies: make([]Colony, len(kits)),
		Started:  time.Now().UTC(),
		world:    w,
		runtime:  rt,
	}
	for i, k := range kits {
		base := w.Bases[i]
		for _, np := range k.programs {
			rt.Install(np.id, np.p)
		}
		equipColony(w, base, k)
		if i < len(members) {
			m.Colonies[i] = Colony{ID: base.Colony, UserID: members[i].UserID, DisplayName: members[i].DisplayName}
			continue
		}
		p := s.AI[i-len(members)]
		m.Colonies[i] = Colony{ID: base.Colony, DisplayName: p.DisplayName(), AI: p}
	}

	// The graph starts where the match does: tick 0, every colony at its
	// starting kit. Nothing else has touched m yet, so this needs no lock.
	m.history = History{Interval: historyEvery, Colonies: make([]ColonyHistory, len(m.Colonies))}
	for i, c := range m.Colonies {
		m.history.Colonies[i].Colony = c.ID
	}
	m.sample()
	return m, nil
}

// ErrMatchOver is Apply's refusal: the match ended before the command reached
// the lock. internal/server maps it to the same 409 its own pre-check gives.
var ErrMatchOver = errors.New("lobby: the match is over")

// Read runs f with exclusive access to the live world and its program runtime.
//
// f must be short and must never write to the network or the database: it runs
// on the same mutex the tick driver needs ten times a second. Build a snapshot
// inside f, send it outside.
func (m *Match) Read(f func(*sim.World, *prog.Runtime)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(m.world, m.runtime)
}

// Apply is Read for a player command: it runs f under the same lock and, if f
// succeeds, records cmd in the match's input log so a restart can replay it.
//
// Every mutation of a live world that does not come from a tick must go
// through here. One that goes through Read instead is invisible to the log,
// and a replay would silently rebuild a world in which it never happened.
//
// cmd.Tick is filled in from the world, not by the caller: the tick a command
// applied at is only knowable under the lock.
//
// A finished match refuses. Callers check Finished() first for a clean answer,
// but that check cannot hold — the flag is set under this same lock — and this
// is where it has to be enforced: the finishing save (E9) is the last write of
// the record, so a command accepted after it is lost from the log for good, and
// the stored match would replay into a world the players did not see.
func (m *Match) Apply(cmd Command, f func(*sim.World, *prog.Runtime) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return ErrMatchOver
	}
	if err := f(m.world, m.runtime); err != nil {
		return err
	}
	cmd.Tick = m.world.Tick
	m.log = append(m.log, cmd)
	return nil
}

// record copies what a restart needs, and reports whether the match is over.
//
// It exists so that saving never holds the match lock across disk I/O: this
// takes the lock, copies, and returns; the caller writes afterwards. A save is
// therefore always a consistent (tick, commands) pair, and a slow disk costs a
// match nothing but the microseconds spent here.
func (m *Match) record() (tick uint64, log []Command, finished bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.world.Tick, slices.Clone(m.log), m.finished
}

// Info is the match metadata endpoint's payload. The live world stream is E4.1;
// this is deliberately only what a lobby screen needs.
type Info struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Tick     uint64   `json:"tick"`
	EndTick  uint64   `json:"end_tick"`
	TickRate int      `json:"tick_rate"`
	Seed     int64    `json:"seed"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Started  string   `json:"started_at"`
	Colonies []Status `json:"colonies"`
}

// Status is one colony's headline numbers. Score is the design §9 score, which
// is what makes this a standing rather than a list of seats — the history page
// (E9) shows nothing else.
type Status struct {
	Colony
	Robots    int `json:"robots"`
	Inventory int `json:"inventory"`
	Score     int `json:"score"`
}

// Info reports the match metadata under the lock.
func (m *Match) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := db.LobbyRunning
	if m.finished {
		state = db.LobbyFinished
	}
	info := Info{
		ID:       m.ID,
		Name:     m.Name,
		State:    state,
		Tick:     m.world.Tick,
		EndTick:  m.Settings.durationTicks(),
		TickRate: TickRate,
		Seed:     m.Settings.Seed,
		Width:    m.world.Width,
		Height:   m.world.Height,
		Started:  m.Started.Format(time.RFC3339),
		Colonies: make([]Status, len(m.Colonies)),
	}
	for i, c := range m.Colonies {
		st := Status{Colony: c, Score: m.world.Score(c.ID)}
		for _, r := range m.world.Robots {
			if r.Colony == c.ID {
				st.Robots++
			}
		}
		for _, b := range m.world.Bases {
			if b.Colony != c.ID {
				continue
			}
			for _, e := range b.SortedInventory() {
				st.Inventory += e.Count
			}
		}
		info.Colonies[i] = st
	}
	return info
}

// step advances the simulation by one tick and reports whether the match is
// still running.
func (m *Match) step() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finished {
		return false
	}
	m.world.Step()
	m.spawnResources()
	// Both before the end check, so a match's last tick keeps its events and a
	// last tick that is a sampling tick keeps its point. Each reads the world
	// and writes nothing in it (events.go, history.go).
	m.collectEvents()
	m.sample()
	if m.world.Tick >= m.Settings.durationTicks() {
		m.finished = true
		return false
	}
	return true
}

// spawnResources implements the design §2.3 resource spawn rate: the slow
// appearance of new components during a match. Caller holds mu.
//
// It draws from the world's rng on a tick-count schedule, so it stays as
// deterministic as the simulation it feeds. A draw that lands somewhere
// unusable is dropped rather than retried — the same rule Generate uses to keep
// rng consumption independent of luck.
func (m *Match) spawnResources() {
	every := m.Settings.spawnEvery()
	if every == 0 || m.world.Tick%every != 0 {
		return
	}
	w := m.world
	c := sim.Coord{X: w.Rand().Intn(w.Width), Y: w.Rand().Intn(w.Height)}
	cat := sim.Catalogue()
	v := cat[w.Rand().Intn(len(cat))].Variant
	// Any cell some locomotion can enter is a legal spawn. AntiGrav is the
	// locomotion that enters everything except a hard barrier (internal/sim
	// terrainSpecs), so this is the test "not a hard barrier" without naming a
	// terrain class: a rubble massif is a legs colony's supply, and filtering on
	// one locomotion left whole regions barren for the match.
	//
	// Reachability needs no test here: Generate ends with repairPockets, which
	// carves until every non-Barrier cell is AntiGrav-reachable from base 0, and
	// terrain never changes after generation. A drop that passes is reachable.
	if !w.Passable(c, sim.AntiGrav) {
		return
	}
	for _, b := range w.Bases {
		if b.Coord == c {
			return
		}
	}
	w.Loose = append(w.Loose, &sim.LooseComponent{ID: w.NextID(), Coord: c, Variant: v})
}

// Finished reports whether the match has ended.
func (m *Match) Finished() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finished
}

// Registry holds the live matches. The world itself stays in memory; what
// survives a restart is the replay record each driver saves (persist.go).
type Registry struct {
	mu      sync.Mutex
	matches map[int64]*Match
	wg      sync.WaitGroup
	done    chan struct{} // closed by Shutdown; every driver watches it
	closed  bool

	// save persists a match's replay record. Called from the tick driver, never
	// under the match lock. Nil disables persistence, which is what a test that
	// only wants a ticking world asks for.
	save func(*Match)
}

// NewRegistry returns an empty registry. save may be nil; see the Registry
// field of the same name.
func NewRegistry(save func(*Match)) *Registry {
	return &Registry{matches: map[int64]*Match{}, done: make(chan struct{}), save: save}
}

// ErrShuttingDown is returned by Start once Shutdown has begun.
var ErrShuttingDown = errors.New("lobby: server is shutting down")

// Start registers a match and puts a tick driver behind it. onEnd runs on the
// driver goroutine when the match reaches its duration — not when the server
// shuts down, which leaves the match unfinished on purpose.
func (r *Registry) Start(m *Match, onEnd func(*Match)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrShuttingDown
	}
	if _, dup := r.matches[m.ID]; dup {
		return fmt.Errorf("lobby: match %d is already running", m.ID)
	}
	r.matches[m.ID] = m
	r.wg.Add(1)
	go r.drive(m, onEnd)
	return nil
}

// Get returns a running or finished match by id.
func (r *Registry) Get(id int64) (*Match, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.matches[id]
	return m, ok
}

// drive is the per-match tick loop. A step that overruns its 100ms budget drops
// the tick rather than queueing it: the simulation falls behind wall time under
// load instead of spiralling into a backlog it can never work off.
// ponytail: no catch-up. Add one only if a real match is observed lagging.
func (r *Registry) drive(m *Match, onEnd func(*Match)) {
	defer r.wg.Done()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	// Immediately, not at the first interval: without this a match killed
	// ungracefully in its first ten seconds has no record at all, and Restore
	// would finish a lobby it could have replayed from tick 0.
	r.persist(m)

	unsaved := 0
	for {
		select {
		case <-r.done:
			// The last save of the match, and the one that makes a graceful
			// deploy cost the players nothing: Shutdown waits for this
			// goroutine, so the record is on disk before the process exits.
			r.persist(m)
			slog.Info("match suspended by shutdown", "match_id", m.ID, "tick", m.Info().Tick)
			return
		case <-ticker.C:
			if m.step() {
				// Saving on the driver goroutine, between ticks: it is one
				// small write every saveEvery ticks, it holds no match lock,
				// and a disk that stalls delays this match rather than
				// stealing a worker from every other one. A kill -9 costs the
				// match the ticks since the last save and nothing more.
				if unsaved++; unsaved >= saveEvery {
					unsaved = 0
					r.persist(m)
				}
				continue
			}
			slog.Info("match finished", "match_id", m.ID, "ticks", m.Settings.durationTicks())
			// The finishing save (E9). It is what turns the replay record into
			// history: the periodic saves land up to saveEvery ticks short of
			// the end, and this one is at the exact final tick and carries the
			// standing with it.
			//
			// Before onEnd, which settles the lobby row: a process killed in
			// between then leaves a record marked finished under a lobby that
			// still says running, and Restore settles that (lobby.go). The other
			// order would leave a finished lobby whose record stops short of the
			// end, with nothing left to notice.
			r.persist(m)
			if onEnd != nil {
				onEnd(m)
			}
			return
		}
	}
}

// saveEvery is how many ticks pass between two replay-record saves — ten
// seconds' worth. It is the bound on how far a match rewinds if the process
// dies without running its shutdown path; a graceful stop rewinds nothing.
const saveEvery = 10 * TickRate

func (r *Registry) persist(m *Match) {
	if r.save != nil {
		r.save(m)
	}
}

// Shutdown stops every tick driver and waits for them, so no goroutine outlives
// the process and no half-applied tick is left behind. It is idempotent.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.done)
	}
	r.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("lobby: tick drivers did not stop: %w", ctx.Err())
	}
}
