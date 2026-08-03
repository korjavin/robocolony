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
	Mass       int    `json:"mass"`
	Value      int    `json:"value"`
}

// Language is the whole editable language in one static payload: the predicate
// and action catalogue, the component catalogue, the structural limits and the
// starter templates.
//
// It exists so the editor's dropdowns are generated from the server's own
// catalogue rather than a second copy in JavaScript, which could only drift.
type Language struct {
	Catalogue  prog.Catalogue `json:"catalogue"`
	Components []Component    `json:"components"`
	MemPoints  int            `json:"mem_points"`
	Limits     Limits         `json:"limits"`
	Templates  []Template     `json:"templates"`
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
	tmpl := []struct {
		name, section, blueprint string
		p                        prog.Program
	}{
		{"component scavenger", "§10.7", starterScavenger, lobby.DefaultProgram()},
		{"memory-assisted scout", "§10.8", starterScavenger, scoutProgram()},
		{"defensive responder", "§10.9", starterDefender, responderProgram()},
	}
	templates := make([]Template, 0, len(tmpl))
	for _, t := range tmpl {
		// Encode cannot fail on a value built from prog's own types.
		raw, _ := t.p.Encode()
		templates = append(templates, Template{
			Name: t.name, Section: t.section, Blueprint: t.blueprint, Program: raw,
		})
	}
	return Language{
		Catalogue:  prog.Language(),
		Components: comps,
		MemPoints:  sim.MemPoints,
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

// ListPrograms returns the caller's library.
func (l *Library) ListPrograms(ctx context.Context, userID int64) ([]ProgramView, error) {
	rows, err := l.db.ListPrograms(ctx, userID)
	if err != nil {
		return nil, err
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
			if _, err := l.CreateBlueprint(ctx, userID, s.name, variants(s.components)); err != nil {
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

// CreateBlueprint saves a physical configuration, enforcing the design §6.3
// constraints server-side.
func (l *Library) CreateBlueprint(ctx context.Context, userID int64, name string, components []int) (BlueprintView, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return BlueprintView{}, libErrf(http.StatusBadRequest, "name is required")
	case len(name) > maxNameLen:
		return BlueprintView{}, libErrf(http.StatusBadRequest, "name must be at most %d characters", maxNameLen)
	// A blueprint cannot legally need more parts than the catalogue has rows,
	// counting the two weapon slots.
	case len(components) > len(sim.Catalogue())+sim.MaxWeapons:
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
	row, err := l.db.CreateBlueprint(ctx, userID, name, string(encoded))
	switch {
	case db.IsDuplicateName(err):
		return BlueprintView{}, libErrf(http.StatusConflict, "you already have a blueprint called %q", name)
	case err != nil:
		return BlueprintView{}, err
	}
	return blueprintView(row, bp), nil
}

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
		Mass: bp.Mass(), Value: bp.Value(),
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
	var body struct {
		Name       string `json:"name"`
		Components []int  `json:"components"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	user, _ := auth.UserFrom(r.Context())
	view, err := l.CreateBlueprint(r.Context(), user.ID, body.Name, body.Components)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
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

// writeResult sends validation findings with the two lists kept apart, and
// never null: the editor iterates both unconditionally.
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
				"error": se.msg, "errors": res.Errors, "warnings": res.Warnings,
			})
			return
		}
		writeJSON(w, se.code, map[string]string{"error": se.msg})
		return
	}
	slog.Error("library request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}
