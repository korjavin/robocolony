package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/lobby"
)

// normalizeArgs undoes JSON's one lossy step for our purposes: every number
// comes back as a float64, and %d wants an integer again. The client does its
// own substitution and needs no such step; this is only so the round trip can
// be checked with fmt.Sprintf.
func normalizeArgs(args []any) []any {
	for i, v := range args {
		if f, ok := v.(float64); ok && f == math.Trunc(f) {
			args[i] = int(f)
		}
	}
	return args
}

// TestErrorBodiesCarryTheirKey pins the wire shape of the JSON errors a player
// can actually trigger: "error" is the English text it has always been, and the
// new "key" + "args" reformat back into exactly that string. That round trip is
// what stops the two drifting apart the next time a message is reworded — a new
// wording without a matching key would still print, but would no longer match.
func TestErrorBodiesCarryTheirKey(t *testing.T) {
	lib, database := newLibrary(t)
	user := newUser(t, database, "keys")
	raw := encode(t, lobby.DefaultProgram())
	if _, err := lib.SaveProgram(t.Context(), user.ID, 0, "dupe", raw, 0); err != nil {
		t.Fatalf("SaveProgram() = %v", err)
	}
	robots, match, owner, _ := twoColonies(t)

	// Two errors that need a request to exist at all.
	unknownField := func() error {
		r := httptest.NewRequest(http.MethodPost, "/api/programs", strings.NewReader(`{"nope":1}`))
		var dst struct{}
		return decodeBody(httptest.NewRecorder(), r, &dst)
	}
	badPathID := func() error {
		_, err := pathID(httptest.NewRequest(http.MethodGet, "/api/programs/x", nil))
		return err
	}

	errOf := func(_ any, err error) error { return err }

	for _, tt := range []struct {
		name     string
		err      error
		cmd      bool // the robot-command funnel rather than the library one
		wantCode int
		wantErr  string
		wantKey  string
		wantArgs []any
		wantMore []string // fields that must still be in the body
	}{
		{
			name:     "program not found",
			err:      errOf(lib.GetProgram(t.Context(), user.ID, 999999)),
			wantCode: http.StatusNotFound,
			wantErr:  "program not found",
			wantKey:  "%s not found",
			wantArgs: []any{"program"},
		},
		{
			name:     "blueprint not found",
			err:      errOf(lib.blueprint(t.Context(), user.ID, 999999)),
			wantCode: http.StatusNotFound,
			wantErr:  "blueprint not found",
			wantKey:  "%s not found",
			wantArgs: []any{"blueprint"},
		},
		{
			name:     "no args at all",
			err:      errOf(lib.SaveProgram(t.Context(), user.ID, 0, "  ", raw, 0)),
			wantCode: http.StatusBadRequest,
			wantErr:  "name is required",
			wantKey:  "name is required",
			wantArgs: []any{},
		},
		{
			name:     "a number arg",
			err:      errOf(lib.SaveProgram(t.Context(), user.ID, 0, strings.Repeat("x", maxNameLen+1), raw, 0)),
			wantCode: http.StatusBadRequest,
			wantErr:  "name must be at most 64 characters",
			wantKey:  "name must be at most %d characters",
			wantArgs: []any{maxNameLen},
		},
		{
			name:     "a player-authored name, quoted",
			err:      errOf(lib.SaveProgram(t.Context(), user.ID, 0, "dupe", raw, 0)),
			wantCode: http.StatusConflict,
			wantErr:  `you already have a program called "dupe"`,
			wantKey:  "you already have a program called %q",
			wantArgs: []any{"dupe"},
		},
		{
			name:     "an error as an arg",
			err:      unknownField(),
			wantCode: http.StatusBadRequest,
			wantErr:  `invalid request body: json: unknown field "nope"`,
			wantKey:  "invalid request body: %s",
			wantArgs: []any{`json: unknown field "nope"`},
		},
		{
			name:     "no such program",
			err:      badPathID(),
			wantCode: http.StatusNotFound,
			wantErr:  "no such program",
			wantKey:  "no such program",
			wantArgs: []any{},
		},
		{
			name:     "a refused save keeps its findings",
			err:      errOf(lib.SaveProgram(t.Context(), user.ID, 0, "broken", json.RawMessage(`{"v":1,"rules":`), 0)),
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "the program has errors and was not saved",
			wantKey:  "the program has errors and was not saved",
			wantArgs: []any{},
			wantMore: []string{"errors", "warnings", "notes"},
		},
		{
			name:     "anything unmapped",
			err:      errors.New("something the handlers never named"),
			wantCode: http.StatusInternalServerError,
			wantErr:  "internal error",
			wantKey:  "internal error",
			wantArgs: []any{},
		},
		{
			name:     "robot command, with a number arg",
			err:      errOf(robots.Recall(t.Context(), owner.ID, match.ID, 9999)),
			cmd:      true,
			wantCode: http.StatusNotFound,
			wantErr:  "no robot 9999 in this match",
			wantKey:  "no robot %d in this match",
			wantArgs: []any{9999},
		},
		{
			name:     "a hand-built command error still carries its key",
			err:      errOf(robots.ShadowTest(match.ID, 1, json.RawMessage(`{"v":1,"rules":`))),
			cmd:      true,
			wantCode: http.StatusBadRequest,
			wantErr:  "the draft program does not load",
			wantKey:  "the draft program does not load",
			wantArgs: []any{},
			wantMore: []string{"issues"},
		},
		{
			name:     "robot command, unmapped",
			err:      errors.New("boom"),
			cmd:      true,
			wantCode: http.StatusInternalServerError,
			wantErr:  "internal error",
			wantKey:  "internal error",
			wantArgs: []any{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("the call under test succeeded; it must fail for there to be a body")
			}
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/whatever", nil)
			if tt.cmd {
				writeCmdErr(rec, r, tt.err)
			} else {
				writeErr(rec, r, tt.err)
			}
			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
			}

			// The English message is unchanged, byte for byte.
			if got := body["error"]; got != tt.wantErr {
				t.Errorf("error = %q, want %q", got, tt.wantErr)
			}
			if got := body["key"]; got != tt.wantKey {
				t.Errorf("key = %q, want %q", got, tt.wantKey)
			}
			list, ok := body["args"].([]any)
			if !ok {
				t.Fatalf("args = %#v, want a list", body["args"])
			}
			if got := normalizeArgs(list); !reflect.DeepEqual(got, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", got, tt.wantArgs)
			}
			// The round trip: key + args are the message, not a paraphrase.
			if got := fmt.Sprintf(tt.wantKey, list...); got != tt.wantErr {
				t.Errorf("key and args reformat to %q, want %q", got, tt.wantErr)
			}
			for _, f := range tt.wantMore {
				if _, ok := body[f]; !ok {
					t.Errorf("body lost its %q field: %v", f, body)
				}
			}
		})
	}
}
