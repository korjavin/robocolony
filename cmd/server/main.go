// Command server is the robocolony game server: HTTP API, SSE world stream,
// and the static client, all from one binary.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/korjavin/robocolony/internal/auth"
	"github.com/korjavin/robocolony/internal/config"
	"github.com/korjavin/robocolony/internal/db"
	"github.com/korjavin/robocolony/internal/lobby"
	"github.com/korjavin/robocolony/internal/server"
	"github.com/korjavin/robocolony/web"
)

// shutdownGrace is how long in-flight requests get to finish after a signal.
const shutdownGrace = 30 * time.Second

// matchStopGrace is how long the tick drivers get to notice the shutdown. They
// only have to finish the tick they are in, so this is generous.
const matchStopGrace = 5 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadEnv()
	if err != nil {
		// The logger is not configured yet, so use the default one.
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Before the listener: an unwritable DB_PATH must stop the deploy, not
	// surface as a 500 on the first login.
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	if cfg.GoogleClientID == "" || cfg.GoogleClientSecret == "" {
		return errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set: every route but /health needs a session, so there is nothing useful to serve without them")
	}
	// Also before the listener: OIDC discovery is a network call, and a
	// half-configured login should fail the deploy, not the first player.
	authHandler, err := auth.New(ctx, database, cfg)
	if err != nil {
		return err
	}

	lobbies := lobby.New(database)
	// Live match state is in-memory (AGENTS.md): anything still marked running
	// belongs to a process that is gone.
	if err := lobbies.ReapStaleLobbies(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           routes(authHandler, lobbies, database, server.NewLibrary(database)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: the world stream is long-lived SSE (design §4.4).
		IdleTimeout: 120 * time.Second,
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("server listening", "addr", srv.Addr, "base_url", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutdown signal received, draining", "grace", shutdownGrace.String())
	stop() // a second signal now kills the process instead of being swallowed

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	// After the listener, so a request cannot start a match into a registry
	// that is already draining. Its own deadline, because a slow request drain
	// must not leave the tick drivers unstopped. Running matches die here: the
	// POC keeps live match state in memory only (AGENTS.md); E7.6 owns
	// persisting it.
	matchCtx, cancelMatches := context.WithTimeout(context.Background(), matchStopGrace)
	defer cancelMatches()
	if err := lobbies.Shutdown(matchCtx); err != nil {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

func routes(a *auth.Handler, lobbies *lobby.Service, database *db.DB, library *server.Library) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	a.Routes(mux)
	mux.HandleFunc("GET "+auth.LoginPath, loginPage)
	mux.HandleFunc("GET /lobby", lobbyPage)
	mux.HandleFunc("GET /match", matchPage)
	// Everything under /api needs a session; the static shell does not.
	mux.Handle("GET /api/me", a.RequireAuth(http.HandlerFunc(me)))
	lobbies.Routes(mux, a.RequireAuth)
	library.Routes(mux, a.RequireAuth)
	mux.Handle("GET /api/matches/{id}/stream", a.RequireAuth(server.Stream(lobbies.Registry())))
	server.NewRobots(lobbies.Registry(), database).Routes(mux, a.RequireAuth)
	// FileServerFS serves index.html for "/" and 404s everything unknown.
	mux.Handle("GET /", http.FileServerFS(web.FS))
	return mux
}

// health is the container healthcheck: no auth, no database, no allocation of
// anything that could make it lie about the process being up.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}

func loginPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "login.html")
}

// lobbyPage is the static shell; it fetches everything it shows from /api,
// which is where the session is actually required.
func lobbyPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "lobby.html")
}

// matchPage is the observer shell for /match?id=N. Like the lobby it is static:
// the session is required by the world stream it subscribes to.
func matchPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "match.html")
}

// me is the smallest authenticated endpoint: it tells the client who it is,
// and proves the session middleware end to end.
func me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFrom(r.Context())
	if !ok { // unreachable behind RequireAuth
		http.Error(w, "no user in context", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":           user.ID,
		"email":        user.Email,
		"display_name": user.DisplayName,
	})
}
