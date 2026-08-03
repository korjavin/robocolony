package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/lobby"
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
	}

	// The zero Handler registers the routes and authenticates nobody, which is
	// all this wiring test needs; internal/auth covers the flow itself. The
	// lobby service never reaches its database on these paths.
	h := routes(&auth.Handler{}, lobby.New(nil), nil)
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
