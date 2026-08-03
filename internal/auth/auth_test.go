package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/korjavin/robocolony/internal/config"
	"github.com/korjavin/robocolony/internal/db"
)

// The real Google OAuth client does not exist yet, so the tests stand in for
// both remote sides: an httptest token endpoint (the oauth2 client code is
// real) and a fake ID token verifier.
const fakeIDToken = "fake.id.token"

func newTestHandler(t *testing.T) *Handler {
	t.Helper()

	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open() = %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		// PKCE must survive the round trip.
		if r.FormValue("code_verifier") == "" {
			http.Error(w, "missing code_verifier", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","id_token":"`+fakeIDToken+`"}`)
	}))
	t.Cleanup(token.Close)

	cfg := config.Config{
		GoogleClientID:     "client-id",
		GoogleClientSecret: "client-secret",
		GoogleRedirectURL:  "http://localhost:8080/auth/callback",
	}
	return newHandler(database, cfg,
		oauth2.Endpoint{AuthURL: "https://accounts.example/authorize", TokenURL: token.URL},
		func(_ context.Context, raw string) (Claims, error) {
			if raw != fakeIDToken {
				return Claims{}, errors.New("bad id token")
			}
			return Claims{Subject: "sub-1", Email: "ada@example.com", Name: "Ada"}, nil
		})
}

func cookies(rec *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range (&http.Response{Header: rec.Header()}).Cookies() {
		out[c.Name] = c
	}
	return out
}

// startLogin runs GET /auth/login and returns the redirect target plus the
// cookies the browser would now hold.
func startLogin(t *testing.T, h *Handler) (*url.URL, map[string]*http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.login(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /auth/login = %d, want %d", rec.Code, http.StatusFound)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	return loc, cookies(rec)
}

func TestLoginRedirect(t *testing.T) {
	h := newTestHandler(t)
	loc, set := startLogin(t, h)

	state := set[stateCookie]
	if state == nil || state.Value == "" {
		t.Fatal("no state cookie was set")
	}
	if !state.HttpOnly || state.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie = %+v, want HttpOnly and SameSite=Lax", state)
	}
	if set[verifierCookie] == nil || set[verifierCookie].Value == "" {
		t.Fatal("no PKCE verifier cookie was set")
	}

	q := loc.Query()
	if q.Get("state") != state.Value {
		t.Errorf("redirect state = %q, want the cookie value", q.Get("state"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Errorf("redirect = %q, want a S256 PKCE challenge", loc)
	}
	if q.Get("client_id") != "client-id" || q.Get("redirect_uri") != "http://localhost:8080/auth/callback" {
		t.Errorf("redirect = %q, want the configured client and redirect URI", loc)
	}
}

// callback replays the browser's return from Google.
func callback(h *Handler, query string, jar map[string]*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/auth/callback?"+query, nil)
	for _, c := range jar {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.callback(rec, r)
	return rec
}

func TestLoginRoundTrip(t *testing.T) {
	h := newTestHandler(t)
	loc, jar := startLogin(t, h)

	rec := callback(h, url.Values{"state": {loc.Query().Get("state")}, "code": {"auth-code"}}.Encode(), jar)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d (%s), want %d", rec.Code, rec.Body.String(), http.StatusSeeOther)
	}
	session := cookies(rec)[sessionCookie]
	if session == nil || session.Value == "" {
		t.Fatal("callback set no session cookie")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie = %+v, want HttpOnly and SameSite=Lax", session)
	}
	// The single-use login cookies are cleared.
	if c := cookies(rec)[stateCookie]; c == nil || c.MaxAge >= 0 {
		t.Errorf("state cookie = %+v, want it expired", c)
	}

	// The raw token is never what is stored.
	var stored string
	if err := h.db.QueryRowContext(t.Context(), `SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatalf("read session row: %v", err)
	}
	if stored == session.Value {
		t.Fatal("the raw session token was stored in the database")
	}
	if stored != hashToken(session.Value) {
		t.Fatal("the stored hash does not match the cookie token")
	}

	// And the session works: a second visit stays logged in.
	user, ok := authenticated(t, h, session.Value)
	if !ok {
		t.Fatal("the fresh session did not authenticate")
	}
	if user.GoogleSub != "sub-1" || user.Email != "ada@example.com" || user.DisplayName != "Ada" {
		t.Errorf("user = %+v, want the ID token claims", user)
	}
}

func TestCallbackRejected(t *testing.T) {
	h := newTestHandler(t)

	tests := []struct {
		name  string
		query func(state string) string
		drop  string // cookie the browser does not send
	}{
		{name: "state mismatch", query: func(string) string { return "state=attacker&code=c" }},
		{name: "state missing from the query", query: func(string) string { return "code=c" }},
		{name: "no state cookie", query: func(s string) string { return "state=" + s + "&code=c" }, drop: stateCookie},
		{name: "no verifier cookie", query: func(s string) string { return "state=" + s + "&code=c" }, drop: verifierCookie},
		{name: "no code", query: func(s string) string { return "state=" + s }},
		{name: "google reported an error", query: func(s string) string { return "state=" + s + "&error=access_denied" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc, jar := startLogin(t, h)
			delete(jar, tt.drop)

			rec := callback(h, tt.query(loc.Query().Get("state")), jar)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("callback = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if c := cookies(rec)[sessionCookie]; c != nil {
				t.Errorf("a rejected callback set a session cookie: %+v", c)
			}
			var n int
			if err := h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sessions`).Scan(&n); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if n != 0 {
				t.Errorf("a rejected callback created %d sessions", n)
			}
		})
	}
}

// authenticated runs a request with the given session token through
// RequireAuth and reports the user the protected handler saw.
func authenticated(t *testing.T, h *Handler, token string) (db.User, bool) {
	t.Helper()
	var (
		got  db.User
		seen bool
	)
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, seen = UserFrom(r.Context())
	})
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	h.RequireAuth(next).ServeHTTP(httptest.NewRecorder(), r)
	return got, seen
}

func TestRequireAuth(t *testing.T) {
	h := newTestHandler(t)
	ctx := t.Context()

	user, err := h.db.UpsertUser(ctx, "sub-1", "ada@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	if err := h.db.CreateSession(ctx, hashToken("live"), user.ID, time.Now().Add(sessionTTL)); err != nil {
		t.Fatalf("CreateSession() = %v", err)
	}
	if err := h.db.CreateSession(ctx, hashToken("stale"), user.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateSession(expired) = %v", err)
	}

	tests := []struct {
		name     string
		token    string // "" means no cookie at all
		accept   string
		wantCode int
		wantUser bool
	}{
		{name: "no cookie, API", accept: "application/json", wantCode: http.StatusUnauthorized},
		{name: "no cookie, browser navigation", accept: "text/html,application/xhtml+xml", wantCode: http.StatusFound},
		{name: "unknown token", token: "nonsense", accept: "application/json", wantCode: http.StatusUnauthorized},
		{name: "expired session", token: "stale", accept: "application/json", wantCode: http.StatusUnauthorized},
		{name: "expired session, browser", token: "stale", accept: "text/html", wantCode: http.StatusFound},
		{name: "live session", token: "live", accept: "application/json", wantCode: http.StatusOK, wantUser: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, ok := UserFrom(r.Context())
				if !ok || got.ID != user.ID {
					t.Errorf("handler context user = %+v/%v, want %d", got, ok, user.ID)
				}
				seen = true
			})

			r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			r.Header.Set("Accept", tt.accept)
			if tt.token != "" {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tt.token})
			}
			rec := httptest.NewRecorder()
			h.RequireAuth(next).ServeHTTP(rec, r)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if seen != tt.wantUser {
				t.Errorf("protected handler ran = %v, want %v", seen, tt.wantUser)
			}
			if tt.wantCode == http.StatusFound && rec.Header().Get("Location") != LoginPath {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), LoginPath)
			}
			if tt.wantCode == http.StatusUnauthorized && !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("body = %q, want a JSON error", rec.Body.String())
			}
		})
	}
}

func TestSessionSlidingExpiry(t *testing.T) {
	h := newTestHandler(t)
	ctx := t.Context()

	user, err := h.db.UpsertUser(ctx, "sub-1", "ada@example.com", "Ada")
	if err != nil {
		t.Fatalf("UpsertUser() = %v", err)
	}
	// Well past its half life, so the next use must extend it.
	if err := h.db.CreateSession(ctx, hashToken("old"), user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession() = %v", err)
	}
	if _, ok := authenticated(t, h, "old"); !ok {
		t.Fatal("the session did not authenticate")
	}

	var expires int64
	if err := h.db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE token_hash = ?`, hashToken("old")).Scan(&expires); err != nil {
		t.Fatalf("read expires_at: %v", err)
	}
	if want := time.Now().Add(sessionTTL / 2).Unix(); expires < want {
		t.Errorf("expires_at = %d, want it slid past %d", expires, want)
	}

	// A freshly created session is not rewritten on every request.
	if err := h.db.CreateSession(ctx, hashToken("fresh"), user.ID, time.Now().Add(sessionTTL)); err != nil {
		t.Fatalf("CreateSession() = %v", err)
	}
	refreshed, err := h.db.RefreshSession(ctx, hashToken("fresh"), sessionTTL)
	if err != nil {
		t.Fatalf("RefreshSession() = %v", err)
	}
	if refreshed {
		t.Error("a fresh session was rewritten; the half-life guard is not working")
	}
}

func TestLogout(t *testing.T) {
	h := newTestHandler(t)
	loc, jar := startLogin(t, h)
	rec := callback(h, url.Values{"state": {loc.Query().Get("state")}, "code": {"auth-code"}}.Encode(), jar)
	token := cookies(rec)[sessionCookie].Value

	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	out := httptest.NewRecorder()
	h.logout(out, r)

	if out.Code != http.StatusSeeOther {
		t.Errorf("logout = %d, want %d", out.Code, http.StatusSeeOther)
	}
	if c := cookies(out)[sessionCookie]; c == nil || c.MaxAge >= 0 {
		t.Errorf("session cookie = %+v, want it expired", c)
	}
	if _, ok := authenticated(t, h, token); ok {
		t.Error("the session still authenticates after logout")
	}
}

func TestCookieSecureFollowsConfig(t *testing.T) {
	h := newTestHandler(t)
	h.secure = true
	rec := httptest.NewRecorder()
	h.setCookie(rec, sessionCookie, "v", time.Hour)
	if c := cookies(rec)[sessionCookie]; !c.Secure {
		t.Error("Secure was not set with CookieSecure on")
	}
}
