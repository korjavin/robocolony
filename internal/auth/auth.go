// Package auth implements Google OIDC login, cookie sessions and the
// RequireAuth middleware.
//
// The authorization-code flow is standard: /auth/login redirects to Google
// with a random state and a PKCE challenge, /auth/callback exchanges the code
// and verifies the ID token, /auth/logout revokes the session. ID token
// signature verification is delegated to github.com/coreos/go-oidc — rolling
// our own JWKS handling is exactly the kind of cryptographic code that should
// not be hand-written.
//
// Nothing here logs a token, an authorization code or a cookie value.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/korjavin/robocolony/internal/config"
	"github.com/korjavin/robocolony/internal/db"
)

const (
	// sessionCookie carries a random 32-byte token. Only its SHA-256 reaches
	// the database, so a database leak hands out no live sessions.
	sessionCookie = "rc_session"
	// The login cookies are single-use and short-lived: they exist only
	// between the redirect to Google and the callback.
	stateCookie    = "rc_oauth_state"
	verifierCookie = "rc_oauth_verifier"

	googleIssuer = "https://accounts.google.com"

	// LoginPath is where an unauthenticated browser navigation is sent.
	LoginPath = "/login"

	sessionTTL = 30 * 24 * time.Hour
	loginTTL   = 10 * time.Minute
)

// Claims is the subset of a verified Google ID token that robocolony uses.
type Claims struct {
	Subject string // Google's stable `sub`; the account identity
	Email   string
	Name    string
}

// VerifyFunc verifies a raw ID token and returns its claims. Tests inject a
// fake so the suite needs no network and no real OAuth client.
type VerifyFunc func(ctx context.Context, rawIDToken string) (Claims, error)

// Handler owns the auth endpoints and the session middleware. The zero value
// registers routes and denies every request, which is what cmd/server's tests
// want; use New for a working one.
type Handler struct {
	db     *db.DB
	oauth  *oauth2.Config
	verify VerifyFunc
	secure bool // Secure cookie flag: off for local http://localhost
}

// New discovers Google's OIDC endpoints — one network call at startup, which
// also fails the deploy early if the issuer is unreachable — and returns a
// handler wired to them.
func New(ctx context.Context, database *db.DB, cfg config.Config) (*Handler, error) {
	provider, err := oidc.NewProvider(ctx, googleIssuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discover %s: %w", googleIssuer, err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.GoogleClientID})

	return newHandler(database, cfg, provider.Endpoint(), func(ctx context.Context, raw string) (Claims, error) {
		tok, err := verifier.Verify(ctx, raw)
		if err != nil {
			return Claims{}, err
		}
		var c struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := tok.Claims(&c); err != nil {
			return Claims{}, err
		}
		return Claims{Subject: tok.Subject, Email: c.Email, Name: c.Name}, nil
	}), nil
}

func newHandler(database *db.DB, cfg config.Config, endpoint oauth2.Endpoint, verify VerifyFunc) *Handler {
	return &Handler{
		db: database,
		oauth: &oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			RedirectURL:  cfg.GoogleRedirectURL,
			Endpoint:     endpoint,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verify: verify,
		secure: cfg.CookieSecure,
	}
}

// Routes registers the auth endpoints. They are deliberately not behind
// RequireAuth.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", h.login)
	mux.HandleFunc("GET /auth/callback", h.callback)
	mux.HandleFunc("POST /auth/logout", h.logout)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken()
	if err != nil {
		slog.Error("generate state", "err", err)
		http.Error(w, "sign-in unavailable", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	h.setCookie(w, stateCookie, state, loginTTL)
	h.setCookie(w, verifierCookie, verifier, loginTTL)
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	// Whatever happens, the login cookies are spent.
	h.clearCookie(w, stateCookie)
	h.clearCookie(w, verifierCookie)

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		slog.Warn("google refused the sign-in", "error", e) // an error code, not a secret
		http.Error(w, "sign-in failed", http.StatusBadRequest)
		return
	}

	// CSRF: the state in the callback must match the one we minted, and a
	// missing cookie is a mismatch, not a pass.
	state, err := r.Cookie(stateCookie)
	if err != nil || !sameToken(state.Value, q.Get("state")) {
		slog.Warn("rejected callback with a bad state")
		http.Error(w, "invalid sign-in state", http.StatusBadRequest)
		return
	}
	verifier, err := r.Cookie(verifierCookie)
	if err != nil || verifier.Value == "" {
		http.Error(w, "invalid sign-in state", http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	if code == "" {
		http.Error(w, "invalid sign-in state", http.StatusBadRequest)
		return
	}

	tok, err := h.oauth.Exchange(r.Context(), code, oauth2.VerifierOption(verifier.Value))
	if err != nil {
		slog.Warn("token exchange failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		slog.Warn("token response carried no id_token")
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	claims, err := h.verify(r.Context(), rawIDToken)
	if err != nil {
		slog.Warn("id token verification failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}
	if claims.Subject == "" {
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}

	name := claims.Name
	if name == "" {
		name = claims.Email
	}
	user, err := h.db.UpsertUser(r.Context(), claims.Subject, claims.Email, name)
	if err != nil {
		slog.Error("upsert user", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	if err := h.startSession(w, r, user.ID); err != nil {
		slog.Error("create session", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}
	slog.Info("login", "user_id", user.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// startSession mints a fresh token, so a session id chosen by an attacker
// before login is never the one that ends up authenticated.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	if err := h.db.CreateSession(r.Context(), hashToken(token), userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	h.setCookie(w, sessionCookie, token, sessionTTL)
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := h.db.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			slog.Error("delete session", "err", err)
		}
	}
	h.clearCookie(w, sessionCookie)
	http.Redirect(w, r, LoginPath, http.StatusSeeOther)
}

// RequireAuth rejects anyone without a live session and puts the user in the
// request context for handlers downstream.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := h.session(w, r)
		if !ok {
			deny(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) (db.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return db.User{}, false
	}
	hash := hashToken(c.Value)
	// SessionUser already treats an expired session as absent.
	user, err := h.db.SessionUser(r.Context(), hash)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("session lookup", "err", err)
		}
		return db.User{}, false
	}

	// Sliding expiry, at most one write per half life rather than one per
	// request. The cookie follows the row so the two do not drift apart.
	refreshed, err := h.db.RefreshSession(r.Context(), hash, sessionTTL)
	if err != nil {
		slog.Error("refresh session", "err", err)
	} else if refreshed {
		h.setCookie(w, sessionCookie, c.Value, sessionTTL)
	}
	return user, true
}

// deny answers identically for every path, so an unauthenticated caller cannot
// tell an existing resource from a missing one.
func deny(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, LoginPath, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthenticated"}` + "\n"))
}

type userKey struct{}

// UserFrom returns the user a RequireAuth-protected handler is serving.
func UserFrom(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(userKey{}).(db.User)
	return u, ok
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode, // the callback arrives as a top-level GET
	})
}

func (h *Handler) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// randomToken returns 32 bytes of cryptographic randomness, URL-safe.
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sameToken(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
