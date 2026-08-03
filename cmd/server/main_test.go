package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/server"
)

func TestRoutes(t *testing.T) {
	tests := []struct {
		path     string
		accept   string
		wantCode int
		wantBody string
	}{
		// The healthcheck must stay reachable without a session and without
		// touching the database.
		{path: "/health", wantCode: http.StatusOK, wantBody: `{"status":"ok"}`},
		{path: "/", wantCode: http.StatusOK, wantBody: "robocolony"},
		{path: "/login", wantCode: http.StatusOK, wantBody: "Sign in with Google"},
		{path: "/nope", wantCode: http.StatusNotFound},
		// Protected: no session cookie, so the API 401s and a browser is sent
		// to the login page.
		{path: "/api/me", accept: "application/json", wantCode: http.StatusUnauthorized},
		{path: "/api/me", accept: "text/html", wantCode: http.StatusFound},
		// The lobby API is behind the same middleware; its static shell is not,
		// because the page fetches everything it shows from /api.
		{path: "/api/lobbies", accept: "application/json", wantCode: http.StatusUnauthorized},
		{path: "/api/matches/1", accept: "application/json", wantCode: http.StatusUnauthorized},
		{path: "/lobby", wantCode: http.StatusOK, wantBody: "Open lobbies"},
		// Same for the program editor: its API needs a session, its shell does
		// not. The library service never reaches its database on this path.
		{path: "/api/programs", accept: "application/json", wantCode: http.StatusUnauthorized},
		{path: "/editor.html", wantCode: http.StatusOK, wantBody: "Program editor"},
		// The editor has a route of its own; /editor.html kept working so old
		// links do not break, but nothing should depend on the file extension.
		{path: "/editor", wantCode: http.StatusOK, wantBody: "Program editor"},
		{path: "/match", wantCode: http.StatusOK, wantBody: "Selected robot"},
		// The renderer is a module the shell loads by URL: if the embed
		// directive stops matching web/js, the page 404s in the browser and
		// nothing else in the build notices.
		{path: "/js/match.js", wantCode: http.StatusOK, wantBody: "EventSource"},
		{path: "/api/matches/1/stream", accept: "application/json", wantCode: http.StatusUnauthorized},
	}

	// The zero Handler registers the routes and authenticates nobody, which is
	// all this wiring test needs; internal/auth covers the flow itself. The
	// lobby service never reaches its database on these paths.
	h := routes(&auth.Handler{}, lobby.New(nil), nil, server.NewLibrary(nil), nil)
	for _, tt := range tests {
		t.Run(tt.path+" "+tt.accept, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			h.ServeHTTP(rec, r)
			if rec.Code != tt.wantCode {
				t.Fatalf("GET %s = %d, want %d", tt.path, rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("GET %s body = %q, want it to contain %q", tt.path, rec.Body.String(), tt.wantBody)
			}
		})
	}
}
