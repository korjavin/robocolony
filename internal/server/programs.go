package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/prog"
	"github.com/korjavin/robocolony/internal/sim"
)

// Library is the player's program and blueprint library: the CRUD behind
// web/editor.html, plus the catalogue the editor builds its dropdowns from.
//
// Validation here is authoritative. A program is untrusted input that ends up
// driving the simulation, so nothing is stored that prog.Validate rejects
// against the blueprint it is meant to run on, and the client's opinion of its
// own legality is never consulted.
//
// Ownership is not checked here either: every query is scoped by (user_id, id)
// in internal/db, so an id belonging to somebody else reads as "not found".
type Library struct{ db *db.DB }

// NewLibrary wires the library to the database.
func NewLibrary(database *db.DB) *Library { return &Library{db: database} }

// maxNameLen bounds a library entry's name, matching the lobby name limit.
const maxNameLen = 64

// ProgramView is a library entry on the wire. Program is passed through
// verbatim: it was validated and re-encoded on the way in, so re-marshalling it
// here would only risk drifting from what is stored.
type ProgramView struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Program   json.RawMessage `json:"program"`
	UpdatedAt string          `json:"updated_at"`
}

// BlueprintView is a saved physical configuration on the wire.
type BlueprintView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Components []int  `json:"components"`
	BlueprintStats
}

// BlueprintStats is what a parts list costs and buys: the design §6.3 verdict
// plus the consequences a player is trading off — mass and the §6.4 speed it
// taxes, the §6.1 armor tier's durability, and the component value that feeds
// the §9 fleet score.
//
// Every number is derived by internal/sim, never recomputed here or in the
// browser: E7.3 retunes the game by editing sim's tables, and nothing outside
// that package may hold a second copy of them.
type BlueprintStats struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"` // the §6.3 violation, when OK is false
	Mass   int    `json:"mass"`
	Value  int    `json:"value"`
	Health int    `json:"health"`
	Speed  int    `json:"speed"`

	// What the configurator's two meters and its silhouette are drawn from.
	// TicksPerCell is Speed in the unit a player thinks in; Budget is the
	// default starting budget the Value meter fills against and Fleet how many
	// robots it opens with; Sight and Radar are the wedge and the ring.
	TicksPerCell int `json:"ticks_per_cell"`
	Budget       int `json:"budget"`
	Fleet        int `json:"fleet"`
	Sight        int `json:"sight"`
	Radar        int `json:"radar"`

	// Consequences is what those numbers mean, in sentences (blueprint.go).
	// Empty for an illegal parts list.
	Consequences []string `json:"consequences,omitempty"`
}

func blueprintStats(bp sim.Blueprint) BlueprintStats {
	budget := lobby.DefaultStartingBudget()
	s := BlueprintStats{
		OK: true, Mass: bp.Mass(), Value: bp.Value(),
		Health: sim.StartingHealth(bp), Speed: sim.EffectiveSpeed(bp),
		TicksPerCell: sim.TicksPerCell(bp, sim.Open),
		Budget:       budget,
		Sight:        sim.VisionRange,
		Radar:        sim.BlueprintRadarRange(bp),
		Consequences: consequences(bp),
	}
	if err := bp.Validate(); err != nil {
		s.OK, s.Error = false, err.Error()
		return s
	}
	// Only meaningful once §6.3 is satisfied: startingRoster reads the first
	// approved blueprint, and an illegal one is never approved.
	s.Fleet = lobby.StartingFleet(bp, budget)
	return s
}

// Language is the whole editable language in one static payload: the predicate
// and action catalogue, the component catalogue, the structural limits and the
// starter templates.
//
// It exists so the editor's dropdowns are generated from the server's own
// catalogue rather than a second copy in JavaScript, which could only drift.
type Language struct {
	Catalogue prog.Catalogue `json:"catalogue"`
	// SchemaVersion is prog.SchemaVersion, so the editor stamps and checks the
	// wire format's "v" from the server's own constant rather than a second copy
	// of it in JavaScript. Import needs it to refuse a document from a future
	// build with a readable message.
	SchemaVersion int         `json:"schema_version"`
	Components    []Component `json:"components"`
	MemPoints     int         `json:"mem_points"`
	Limits        Limits      `json:"limits"`
	Templates     []Template  `json:"templates"`
}

// Limits mirrors the caps prog enforces, so the editor can stop the player
// before a save is refused.
type Limits struct {
	MaxRules          int `json:"max_rules"`
	MaxActionsPerRule int `json:"max_actions_per_rule"`
	MaxCondDepth      int `json:"max_cond_depth"`
	MaxNameLen        int `json:"max_name_len"`
}

// Template is one of the design §10.7–§10.9 worked programs, paired with the
// starter blueprint it validates clean against — the scavenger needs a parts
// radar, the responder needs a weapon.
type Template struct {
	Name      string          `json:"name"`
	Section   string          `json:"section"`
	Blueprint string          `json:"blueprint"`
	Program   json.RawMessage `json:"program"`
}

// Starter blueprints. Every player's library is seeded with these on first
// read, so the templates below always have hardware that fits and nobody meets
// an empty blueprint picker.
const (
	starterScavenger = "scavenger"
	starterDefender  = "defender"
)

// starter is one seeded blueprint: a name and the parts it is built from.
type starter struct {
	name       string
	components []sim.Variant
}

func starterBlueprints() []starter {
	return []starter{
		{starterScavenger, lobby.DefaultBlueprint().Components},
		{starterDefender, []sim.Variant{sim.Tracks, sim.HeavyArmor, sim.Laser, sim.PartsRadar}},
	}
}

// scoutProgram is design §10.8, the memory-assisted scout, rule for rule.
func scoutProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "memory-assisted scout", Rules: []prog.Rule{
		{When: prog.And(prog.Pred(prog.SeesComponent), prog.Pred(prog.CarryingComponent)),
			Then: []prog.Action{prog.DoArg(prog.SaveVisibleTarget, 1)}},
		{When: prog.And(prog.Pred(prog.AtOwnBase), prog.Pred(prog.CarryingComponent)),
			Then: []prog.Action{prog.Do(prog.DepositComponentAtBase)}},
		{When: prog.Pred(prog.CarryingComponent),
			Then: []prog.Action{prog.Do(prog.MoveToOwnBase)}},
		{When: prog.And(prog.PredArg(prog.AtPoint, 1), prog.Pred(prog.ComponentInReach), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.Do(prog.PickUpComponent)}},
		{When: prog.And(prog.PredArg(prog.AtPoint, 1), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.DoArg(prog.ClearPoint, 1)}},
		{When: prog.And(prog.PredArg(prog.PointIsSet, 1), prog.Pred(prog.CarryingNothing)),
			Then: []prog.Action{prog.DoArg(prog.MoveToPoint, 1)}},
	}}
}

// responderProgram is design §10.9, the defensive responder, rule for rule.
func responderProgram() prog.Program {
	return prog.Program{V: prog.SchemaVersion, Name: "defensive responder", Rules: []prog.Rule{
		{When: prog.And(prog.PredArg(prog.HealthBelow, 25), prog.Pred(prog.SeesEnemyRobot)),
			Then: []prog.Action{prog.Do(prog.MoveAwayFromTarget)}},
		{When: prog.Pred(prog.ReceivedComeHere),
			Then: []prog.Action{prog.DoArg(prog.SaveSignalPosition, 2)}},
		{When: prog.And(prog.Pred(prog.SeesEnemyRobot), prog.Pred(prog.VisibleTargetInWpnRange)),
			Then: []prog.Action{prog.Do(prog.AttackVisibleTarget)}},
		{When: prog.PredArg(prog.AtPoint, 2),
			Then: []prog.Action{prog.DoArg(prog.ClearPoint, 2)}},
		{When: prog.PredArg(prog.PointIsSet, 2),
			Then: []prog.Action{prog.DoArg(prog.MoveToPoint, 2)}},
		{When: prog.Pred(prog.RadarDetectsTarget),
			Then: []prog.Action{prog.Do(prog.MoveToRadarTarget)}},
		{When: prog.Pred(prog.SeesObstacle),
			Then: []prog.Action{prog.Do(prog.TurnRandom)}},
	}}
}

// starterProgram is one of the design's worked programs: the name it is filed
// under, the section it comes from, and the starter blueprint it validates
// clean against.
//
// One list, two uses: the editor's "start from a template" dropdown, and the
// rows a new player's library is seeded with (ListPrograms). They must be the
// same set — a template the library does not hold is a program the player can
// see and not install.
type starterProgram struct {
	name, section, blueprint string
	p                        prog.Program
}

func starterPrograms() []starterProgram {
	return []starterProgram{
		{"component scavenger", "§10.7", starterScavenger, lobby.DefaultProgram()},
		{"memory-assisted scout", "§10.8", starterScavenger, scoutProgram()},
		{"defensive responder", "§10.9", starterDefender, responderProgram()},
	}
}

// LanguageDoc builds the static editor payload.
func LanguageDoc() Language {
	cat := sim.Catalogue()
	comps := make([]Component, 0, len(cat))
	for _, c := range cat {
		comps = append(comps, Component{
			Variant: int(c.Variant), Name: c.Name, Kind: c.Kind.String(),
			Mass: c.Mass, Value: c.Value,
		})
	}
	tmpl := starterPrograms()
	templates := make([]Template, 0, len(tmpl))
	for _, t := range tmpl {
		// Encode cannot fail on a value built from prog's own types.
		raw, _ := t.p.Encode()
		templates = append(templates, Template{
			Name: t.name, Section: t.section, Blueprint: t.blueprint, Program: raw,
		})
	}
	return Language{
		Catalogue:     prog.Language(),
		SchemaVersion: prog.SchemaVersion,
		Components:    comps,
		MemPoints:     sim.MemPoints,
		Limits: Limits{
			MaxRules:          prog.MaxRules,
			MaxActionsPerRule: prog.MaxActionsPerRule,
			MaxCondDepth:      prog.MaxCondDepth,
			MaxNameLen:        maxNameLen,
		},
		Templates: templates,
	}
}

// Domain API. The HTTP handlers further down are thin wrappers; everything
// worth asserting about ownership and validation is testable from here without
// a session.

// ListPrograms returns the caller's library, seeding the design's three worked
// programs the first time.
//
// Without this a player who has never opened the editor has an empty library,
// and design §4.2's reprogram command — recall a robot, install a program — has
// nothing to offer them (rc-tad.8). The templates were only ever offered by
// /api/language to the *editor*; InstallProgram resolves a library row.
//
// ponytail: seeded on read for the same reason ListBlueprints seeds blueprints
// — there is no first-login hook to hang it on — and idempotent for the same
// reason: the (user_id, name) unique index turns a concurrent double-seed into
// a duplicate name, which is skipped rather than doubled.
//
// "The first time" is therefore really "whenever it is empty": a player who
// deletes one starter keeps it deleted, but one who deletes all of them gets
// them back on the next read. That is deliberate — the worked programs are the
// design's documentation, not the player's data, and it means nobody can strand
// a robot at its base with nothing to install. Remembering that a library was
// once seeded would take a migration and a flag to protect a state nobody
// asked for. Add it if the starters ever become editable in place.
//
// These rows go in through db.CreateProgram rather than SaveProgram: they are
// built from prog's own types in this binary, not untrusted input, and the
// blueprint they validate against (see starterProgram.blueprint) has an id only
// once ListBlueprints has run. What must hold — that each one is installable on
// the blueprint it is offered for — is asserted in TestStarterTemplatesValidate.
func (l *Library) ListPrograms(ctx context.Context, userID int64) ([]ProgramView, error) {
	rows, err := l.db.ListPrograms(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		for _, s := range starterPrograms() {
			encoded, err := s.p.Encode()
			if err != nil {
				return nil, err
			}
			if _, err := l.db.CreateProgram(ctx, userID, s.name, string(encoded)); err != nil {
				if db.IsDuplicateName(err) {
					continue // somebody else seeded it first
				}
				return nil, err
			}
		}
		if rows, err = l.db.ListPrograms(ctx, userID); err != nil {
			return nil, err
		}
	}
	out := make([]ProgramView, 0, len(rows))
	for _, p := range rows {
		out = append(out, programView(p))
	}
	return out, nil
}

// GetProgram returns one of the caller's own programs.
func (l *Library) GetProgram(ctx context.Context, userID, id int64) (ProgramView, error) {
	p, err := l.db.ProgramByID(ctx, userID, id)
	if err != nil {
		return ProgramView{}, notFound(err, "program")
	}
	return programView(p), nil
}

// SaveProgram validates a program against a blueprint and stores it. id == 0
// creates, anything else rewrites the caller's own row of that id.
//
// Errors block the save, warnings never do (design §10.10): a caller that gets
// a view back can still have warnings waiting for it on the validate endpoint.
func (l *Library) SaveProgram(ctx context.Context, userID, id int64, name string, raw json.RawMessage, blueprintID int64) (ProgramView, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return ProgramView{}, libErrf(http.StatusBadRequest, "name is required")
	case len(name) > maxNameLen:
		return ProgramView{}, libErrf(http.StatusBadRequest, "name must be at most %d characters", maxNameLen)
	}
	p, res, err := l.check(ctx, userID, raw, blueprintID)
	if err != nil {
		return ProgramView{}, err
	}
	if !res.OK() {
		return ProgramView{}, libValidationError(res)
	}
	p.Name = name
	// A nil slice encodes as "rules":null, which every consumer would then have
	// to guard. An empty program is legal — Validate only warns about it — so
	// normalise here, once, rather than in each reader.
	if p.Rules == nil {
		p.Rules = []prog.Rule{}
	}
	encoded, err := p.Encode()
	if err != nil {
		return ProgramView{}, err
	}

	var row db.Program
	if id == 0 {
		row, err = l.db.CreateProgram(ctx, userID, name, string(encoded))
	} else {
		row, err = l.db.UpdateProgram(ctx, userID, id, name, string(encoded))
	}
	switch {
	case db.IsDuplicateName(err):
		return ProgramView{}, libErrf(http.StatusConflict, "you already have a program called %q", name)
	case errors.Is(err, sql.ErrNoRows):
		return ProgramView{}, libErrf(http.StatusNotFound, "program not found")
	case err != nil:
		return ProgramView{}, err
	}
	return programView(row), nil
}

// DeleteProgram removes one of the caller's own programs.
func (l *Library) DeleteProgram(ctx context.Context, userID, id int64) error {
	if err := l.db.DeleteProgram(ctx, userID, id); err != nil {
		return notFound(err, "program")
	}
	return nil
}

// ValidateProgram checks a program against a blueprint without storing it. This
// is the same call SaveProgram makes, so the editor's feedback and the save
// gate can never disagree.
func (l *Library) ValidateProgram(ctx context.Context, userID int64, raw json.RawMessage, blueprintID int64) (prog.Result, error) {
	_, res, err := l.check(ctx, userID, raw, blueprintID)
	return res, err
}

// check parses and validates. A program that will not parse comes back as a
// Result with errors rather than a Go error, because the editor renders it the
// same way as any other finding.
func (l *Library) check(ctx context.Context, userID int64, raw json.RawMessage, blueprintID int64) (prog.Program, prog.Result, error) {
	bp, err := l.blueprint(ctx, userID, blueprintID)
	if err != nil {
		return prog.Program{}, prog.Result{}, err
	}
	p, res, ok := parseProgram(raw)
	if !ok {
		return prog.Program{}, res, nil
	}
	return p, prog.Validate(p, bp), nil
}

// parseProgram unmarshals leniently so that prog.Validate — not the JSON
// decoder — reports the rule-indexed issues the editor highlights. Only what
// json cannot represent at all is turned into a program-level error here.
func parseProgram(raw json.RawMessage) (prog.Program, prog.Result, bool) {
	fail := func(format string, a ...any) (prog.Program, prog.Result, bool) {
		return prog.Program{}, prog.Result{Errors: []prog.Issue{{
			Severity: prog.SevError, Code: "malformed", Rule: -1,
			Message: fmt.Sprintf(format, a...),
		}}}, false
	}
	if len(raw) == 0 {
		return fail("no program in the request")
	}
	var ptr *prog.Program
	if err := json.Unmarshal(raw, &ptr); err != nil {
		return fail("program is not valid JSON: %s", err)
	}
	if ptr == nil {
		return fail("no program in the request")
	}
	p := *ptr
	switch p.V {
	case 0:
		p.V = prog.SchemaVersion // "v" omitted: assume the only version there is
	case prog.SchemaVersion:
	default:
		return fail("unsupported schema version %d, want %d", p.V, prog.SchemaVersion)
	}
	return p, prog.Result{}, true
}

// blueprint resolves the blueprint a program is checked against. Zero means the
// starter kit, which is what a robot runs before anyone edits its design.
func (l *Library) blueprint(ctx context.Context, userID, id int64) (sim.Blueprint, error) {
	if id == 0 {
		return lobby.DefaultBlueprint(), nil
	}
	row, err := l.db.BlueprintByID(ctx, userID, id)
	if err != nil {
		return sim.Blueprint{}, notFound(err, "blueprint")
	}
	return decodeBlueprint(row)
}

// ListBlueprints returns the caller's blueprints, seeding the starter kit the
// first time so the picker is never empty and the templates always have
// hardware that fits.
//
// ponytail: seeding on read, because there is no first-login hook to hang it
// on. The unique (user_id, name) index makes a concurrent double-seed harmless.
func (l *Library) ListBlueprints(ctx context.Context, userID int64) ([]BlueprintView, error) {
	rows, err := l.db.ListBlueprints(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		for _, s := range starterBlueprints() {
			if _, err := l.SaveBlueprint(ctx, userID, 0, s.name, variants(s.components)); err != nil {
				var se statusError
				if errors.As(err, &se) && se.code == http.StatusConflict {
					continue // somebody else seeded it first
				}
				return nil, err
			}
		}
		if rows, err = l.db.ListBlueprints(ctx, userID); err != nil {
			return nil, err
		}
	}
	out := make([]BlueprintView, 0, len(rows))
	for _, r := range rows {
		bp, err := decodeBlueprint(r)
		if err != nil {
			return nil, err
		}
		out = append(out, blueprintView(r, bp))
	}
	return out, nil
}

// SaveBlueprint stores a physical configuration, enforcing the design §6.3
// constraints server-side. id == 0 creates, anything else rewrites the caller's
// own row of that id — the same shape SaveProgram has.
//
// Editing in place is safe for the same reason deleting is: an approval keeps a
// frozen snapshot, so nothing that already fielded this design reads the row
// again. It can leave one of the player's programs no longer installable on it
// (drop the radar a scavenger needs), which surfaces at the next save or
// install through the message prog.Validate already writes.
func (l *Library) SaveBlueprint(ctx context.Context, userID, id int64, name string, components []int) (BlueprintView, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return BlueprintView{}, libErrf(http.StatusBadRequest, "name is required")
	case len(name) > maxNameLen:
		return BlueprintView{}, libErrf(http.StatusBadRequest, "name must be at most %d characters", maxNameLen)
	case len(components) > maxComponents:
		return BlueprintView{}, libErrf(http.StatusBadRequest, "too many components")
	}
	bp := sim.Blueprint{Name: name, Components: toVariants(components)}
	if err := bp.Validate(); err != nil {
		return BlueprintView{}, libErrf(http.StatusBadRequest, "%s", err)
	}
	encoded, err := json.Marshal(storedBlueprint{Components: components})
	if err != nil {
		return BlueprintView{}, err
	}
	var row db.Blueprint
	if id == 0 {
		row, err = l.db.CreateBlueprint(ctx, userID, name, string(encoded))
	} else {
		row, err = l.db.UpdateBlueprint(ctx, userID, id, name, string(encoded))
	}
	switch {
	case db.IsDuplicateName(err):
		return BlueprintView{}, libErrf(http.StatusConflict, "you already have a blueprint called %q", name)
	case errors.Is(err, sql.ErrNoRows):
		return BlueprintView{}, libErrf(http.StatusNotFound, "blueprint not found")
	case err != nil:
		return BlueprintView{}, err
	}
	return blueprintView(row, bp), nil
}

// DeleteBlueprint removes one of the caller's own blueprints.
//
// Nothing points at the row. A lobby loadout stores a frozen snapshot of the
// parts list rather than a library id (internal/lobby/loadout.go), so a design
// already approved in an open lobby, or fielded in a running match, keeps
// working after its library row is gone; and no table has a foreign key to
// blueprints.id. Deleting every blueprint is fine too — ListBlueprints re-seeds
// the starter kit whenever the list comes back empty.
func (l *Library) DeleteBlueprint(ctx context.Context, userID, id int64) error {
	if err := l.db.DeleteBlueprint(ctx, userID, id); err != nil {
		return notFound(err, "blueprint")
	}
	return nil
}

// PreviewBlueprint answers "what would this robot be?" for a parts list that
// has not been saved. It exists so the blueprint editor can show the §6.3
// verdict and the derived numbers live as components are added and removed,
// without a second copy of either the constraints or the balance tables in
// JavaScript.
//
// An illegal parts list is a 200 carrying OK false and the reason: it is a
// design the player is still building, not a failed request.
func (l *Library) PreviewBlueprint(components []int) (BlueprintStats, error) {
	if len(components) > maxComponents {
		return BlueprintStats{}, libErrf(http.StatusBadRequest, "too many components")
	}
	return blueprintStats(sim.Blueprint{Components: toVariants(components)}), nil
}

// maxComponents is the most parts a blueprint could legally need: one of every
// catalogue row, counting the second weapon slot.
var maxComponents = len(sim.Catalogue()) + sim.MaxWeapons

// storedBlueprint is the blueprints.json column. The row already carries the
// name and the default program, so only the parts list lives in here.
type storedBlueprint struct {
	Components []int `json:"components"`
}

func decodeBlueprint(row db.Blueprint) (sim.Blueprint, error) {
	var stored storedBlueprint
	if err := json.Unmarshal([]byte(row.JSON), &stored); err != nil {
		return sim.Blueprint{}, fmt.Errorf("server: blueprint %d: %w", row.ID, err)
	}
	bp := sim.Blueprint{
		ID:         strconv.FormatInt(row.ID, 10),
		Name:       row.Name,
		Components: toVariants(stored.Components),
	}
	if row.DefaultProgramID.Valid {
		bp.ProgramID = strconv.FormatInt(row.DefaultProgramID.Int64, 10)
	}
	return bp, nil
}

func blueprintView(row db.Blueprint, bp sim.Blueprint) BlueprintView {
	return BlueprintView{
		ID: row.ID, Name: row.Name, Components: variants(bp.Components),
		BlueprintStats: blueprintStats(bp),
	}
}

func programView(p db.Program) ProgramView {
	return ProgramView{
		ID: p.ID, Name: p.Name, Program: json.RawMessage(p.JSON),
		UpdatedAt: p.UpdatedAt.Format(time.RFC3339),
	}
}

// toVariants converts wire component numbers. An out-of-range number is mapped
// to VariantNone, which sim.Blueprint.Validate then rejects as unknown.
func toVariants(in []int) []sim.Variant {
	out := make([]sim.Variant, 0, len(in))
	for _, v := range in {
		if v < 0 || v > 255 {
			v = int(sim.VariantNone)
		}
		out = append(out, sim.Variant(v))
	}
	return out
}

// Errors. statusError carries the HTTP status a domain failure maps to, so the
// handlers stay short and anything unmapped is a 500.

type statusError struct {
	code   int
	msg    string
	result *prog.Result // validation findings, when the failure is a refused save
}

func (e statusError) Error() string { return e.msg }

func libErrf(code int, format string, a ...any) error {
	return statusError{code: code, msg: fmt.Sprintf(format, a...)}
}

// libValidationError refuses a save and carries the findings back, errors and
// warnings still in their own lists.
func libValidationError(res prog.Result) error {
	return statusError{
		code:   http.StatusUnprocessableEntity,
		msg:    "the program has errors and was not saved",
		result: &res,
	}
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return libErrf(http.StatusNotFound, "%s not found", what)
	}
	return err
}

// HTTP surface. Every route is behind RequireAuth; there is no second auth path
// in this package, and no handler trusts an id without a user next to it.

// Routes registers the library endpoints. requireAuth is
// auth.Handler.RequireAuth.
func (l *Library) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, requireAuth(h))
	}
	handle("GET /api/language", handleLanguage)
	handle("GET /api/programs", l.handleListPrograms)
	handle("POST /api/programs", l.handleCreateProgram)
	handle("POST /api/programs/validate", l.handleValidate)
	handle("GET /api/programs/{id}", l.handleGetProgram)
	handle("PUT /api/programs/{id}", l.handleUpdateProgram)
	handle("DELETE /api/programs/{id}", l.handleDeleteProgram)
	handle("GET /api/blueprints", l.handleListBlueprints)
	handle("POST /api/blueprints", l.handleCreateBlueprint)
	handle("POST /api/blueprints/preview", l.handlePreviewBlueprint)
	handle("PUT /api/blueprints/{id}", l.handleUpdateBlueprint)
	handle("DELETE /api/blueprints/{id}", l.handleDeleteBlueprint)
}

// programBody is the shape every write endpoint takes.
type programBody struct {
	Name        string          `json:"name"`
	Program     json.RawMessage `json:"program"`
	BlueprintID int64           `json:"blueprint_id"`
}

func handleLanguage(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, LanguageDoc())
}

func (l *Library) handleListPrograms(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	list, err := l.ListPrograms(r.Context(), user.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"programs": list})
}

func (l *Library) handleGetProgram(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := l.GetProgram(r.Context(), user.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (l *Library) handleCreateProgram(w http.ResponseWriter, r *http.Request) {
	l.save(w, r, 0)
}

func (l *Library) handleUpdateProgram(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	l.save(w, r, id)
}

func (l *Library) save(w http.ResponseWriter, r *http.Request, id int64) {
	var body programBody
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := l.SaveProgram(r.Context(), user.ID, id, body.Name, body.Program, body.BlueprintID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	code := http.StatusOK
	if id == 0 {
		code = http.StatusCreated
	}
	writeJSON(w, code, view)
}

func (l *Library) handleDeleteProgram(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	if err := l.DeleteProgram(r.Context(), user.ID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (l *Library) handleValidate(w http.ResponseWriter, r *http.Request) {
	var body programBody
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	res, err := l.ValidateProgram(r.Context(), user.ID, body.Program, body.BlueprintID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeResult(w, http.StatusOK, res)
}

func (l *Library) handleListBlueprints(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	list, err := l.ListBlueprints(r.Context(), user.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blueprints": list})
}

func (l *Library) handleCreateBlueprint(w http.ResponseWriter, r *http.Request) {
	l.saveBlueprint(w, r, 0)
}

func (l *Library) handleUpdateBlueprint(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	l.saveBlueprint(w, r, id)
}

func (l *Library) saveBlueprint(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Name       string `json:"name"`
		Components []int  `json:"components"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := l.SaveBlueprint(r.Context(), user.ID, id, body.Name, body.Components)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	code := http.StatusOK
	if id == 0 {
		code = http.StatusCreated
	}
	writeJSON(w, code, view)
}

func (l *Library) handleDeleteBlueprint(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	if err := l.DeleteBlueprint(r.Context(), user.ID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (l *Library) handlePreviewBlueprint(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Components []int `json:"components"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	stats, err := l.PreviewBlueprint(body.Components)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Which of the player's programs this hardware can actually run is part of
	// the same question: a design is not finished until something can be
	// installed on it. Only asked once the parts list is legal — prog.Validate
	// against a blueprint §6.3 rejects would report the missing locomotion as
	// a program fault.
	out := BlueprintPreview{BlueprintStats: stats}
	if stats.OK {
		user, _ := auth.UserFrom(r.Context())
		fits, err := l.programFit(r.Context(), user.ID, sim.Blueprint{Components: toVariants(body.Components)})
		if err != nil {
			writeErr(w, r, err)
			return
		}
		out.Programs = fits
	}
	writeJSON(w, http.StatusOK, out)
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, libErrf(http.StatusNotFound, "no such program")
	}
	return id, nil
}

// maxBodyBytes bounds a program upload. prog.MaxRules × prog.MaxCondNodes of
// hand-authored JSON fits comfortably; anything larger is refused before it is
// read into memory.
const maxBodyBytes = 512 << 10

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return libErrf(http.StatusBadRequest, "invalid request body: %s", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeResult sends validation findings with the buckets kept apart. Errors and
// warnings are never null, so the editor iterates both unconditionally; notes
// are omitempty in prog and simply absent when there are none.
func writeResult(w http.ResponseWriter, code int, res prog.Result) {
	if res.Errors == nil {
		res.Errors = []prog.Issue{}
	}
	if res.Warnings == nil {
		res.Warnings = []prog.Issue{}
	}
	writeJSON(w, code, res)
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var se statusError
	if errors.As(err, &se) {
		if se.result != nil {
			res := *se.result
			if res.Errors == nil {
				res.Errors = []prog.Issue{}
			}
			if res.Warnings == nil {
				res.Warnings = []prog.Issue{}
			}
			writeJSON(w, se.code, map[string]any{
				"error": se.msg, "errors": res.Errors,
				"warnings": res.Warnings, "notes": res.Notes,
			})
			return
		}
		writeJSON(w, se.code, map[string]string{"error": se.msg})
		return
	}
	slog.Error("library request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
