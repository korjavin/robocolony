package lobby

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
)

// normalizeWireArgs undoes JSON's one lossy step for our purposes: every number
// comes back as a float64, and %d wants an integer again. The client does its
// own substitution and needs no such step; this is only so the round trip can
// be checked with fmt.Sprintf.
func normalizeWireArgs(args []any) []any {
	for i, v := range args {
		if f, ok := v.(float64); ok && f == math.Trunc(f) {
			args[i] = int(f)
		}
	}
	return args
}

// TestErrorBodiesCarryTheirKey pins the wire shape of the lobby's JSON errors,
// the same way internal/server/errkey_test.go pins the library's: "error" is
// the English text it has always been, and the new "key" + "args" reformat back
// into exactly that string. That round trip is what stops the two drifting
// apart the next time a message is reworded — a new wording without a matching
// key would still print, but would no longer match.
func TestErrorBodiesCarryTheirKey(t *testing.T) {
	svc, database := newService(t)
	user := newUser(t, database, "keys")
	open, err := svc.Create(t.Context(), user.ID, "keyed lobby", DefaultSettings())
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	// Two errors that need a request to exist at all.
	unknownField := func() error {
		r := httptest.NewRequest(http.MethodPost, "/api/lobbies", strings.NewReader(`{"nope":1}`))
		var dst struct{}
		return decodeBody(httptest.NewRecorder(), r, &dst)
	}
	badPathID := func() error {
		_, err := pathID(httptest.NewRequest(http.MethodGet, "/api/lobbies/x", nil))
		return err
	}

	errOf := func(_ any, err error) error { return err }

	for _, tt := range []struct {
		name     string
		err      error
		wantCode int
		wantErr  string
		wantKey  string
		wantArgs []any
	}{
		{
			name:     "no args at all",
			err:      errOf(svc.Create(t.Context(), user.ID, "", DefaultSettings())),
			wantCode: http.StatusBadRequest,
			wantErr:  "name is required",
			wantKey:  "name is required",
			wantArgs: []any{},
		},
		{
			name:     "the vocabulary arg every not-found shares",
			err:      errOf(svc.Join(t.Context(), 999999, user.ID)),
			wantCode: http.StatusNotFound,
			wantErr:  "lobby not found",
			wantKey:  "%s not found",
			wantArgs: []any{"lobby"},
		},
		{
			name:     "two number args",
			err:      errOf(svc.SetLoadout(t.Context(), open.ID, user.ID, make([]Choice, maxLoadoutEntries+1))),
			wantCode: http.StatusBadRequest,
			wantErr:  "a colony may approve at most 18 blueprints, got 19",
			wantKey:  "a colony may approve at most %d blueprints, got %d",
			wantArgs: []any{maxLoadoutEntries, maxLoadoutEntries + 1},
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
			name:     "no such lobby",
			err:      badPathID(),
			wantCode: http.StatusNotFound,
			wantErr:  "no such lobby",
			wantKey:  "no such lobby",
			wantArgs: []any{},
		},
		{
			name:     "the history funnel, same shape",
			err:      errOf(svc.HistoryOf(t.Context(), 999999)),
			wantCode: http.StatusNotFound,
			wantErr:  "no finished match with this id",
			wantKey:  "no finished match with this id",
			wantArgs: []any{},
		},
		{
			name:     "anything unmapped",
			err:      errors.New("something the handlers never named"),
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
			writeErr(rec, httptest.NewRequest(http.MethodPost, "/api/whatever", nil), tt.err)
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
			if got := normalizeWireArgs(list); !reflect.DeepEqual(got, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", got, tt.wantArgs)
			}
			// The round trip: key + args are the message, not a paraphrase.
			if got := fmt.Sprintf(tt.wantKey, list...); got != tt.wantErr {
				t.Errorf("key and args reformat to %q, want %q", got, tt.wantErr)
			}
		})
	}
}
