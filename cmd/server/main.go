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
	// Before the listener, and design §2.2's whole point: a match left running
	// by the previous process is replayed back to where it was, and one that
	// cannot be replayed is finished rather than left as a ghost.
	if err := lobbies.Restore(ctx); err != nil {
		return err
	}

	// Closed by RegisterOnShutdown below, i.e. the moment Shutdown starts. Only
	// the world streams watch it: an ordinary request finishes on its own and
	// the drain waits for it, but an SSE stream never finishes, so without a
	// signal of its own the drain can only time out.
	stopping := make(chan struct{})

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           routes(authHandler, lobbies, database, server.NewLibrary(database), stopping),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: the world stream is long-lived SSE (design §4.4).
		IdleTimeout: 120 * time.Second,
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.RegisterOnShutdown(func() { close(stopping) })

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
	// Not returned yet: a request that refuses to drain must not skip the tick
	// drivers below, whose replay records are what the next process resumes
	// from. It is still reported at the end — with the streams closing on their
	// own, a timeout here now means a genuinely stuck request, not a spectator.
	drainErr := srv.Shutdown(shutdownCtx)
	if drainErr != nil {
		log.Error("request drain did not finish", "err", drainErr, "grace", shutdownGrace.String())
	}
	// After the listener, so a request cannot start a match into a registry
	// that is already draining. Its own deadline, because a slow request drain
	// must not leave the tick drivers unstopped. Each driver saves its match's
	// replay record on the way out, so a match suspended here resumes where it
	// stopped when the next process calls Restore.
	matchCtx, cancelMatches := context.WithTimeout(context.Background(), matchStopGrace)
	defer cancelMatches()
	if err := lobbies.Shutdown(matchCtx); err != nil {
		return err
	}
	if drainErr != nil {
		return drainErr
	}
	log.Info("shutdown complete")
	return nil
}

func routes(a *auth.Handler, lobbies *lobby.Service, database *db.DB, library *server.Library, stopping <-chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	a.Routes(mux)
	mux.HandleFunc("GET "+auth.LoginPath, loginPage)
	mux.HandleFunc("GET /lobby", lobbyPage)
	mux.HandleFunc("GET /match", matchPage)
	mux.HandleFunc("GET /blueprints", blueprintsPage)
	mux.HandleFunc("GET /editor", editorPage)
	mux.HandleFunc("GET /help", helpPage)
	// Everything under /api needs a session; the static shell does not.
	mux.Handle("GET /api/me", a.RequireAuth(http.HandlerFunc(me)))
	lobbies.Routes(mux, a.RequireAuth)
	library.Routes(mux, a.RequireAuth)
	server.NewDryRunner(library).Routes(mux, a.RequireAuth)
	mux.Handle("GET /api/matches/{id}/stream", a.RequireAuth(server.Stream(lobbies.Registry(), stopping)))
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

// blueprintsPage is the blueprint configurator: assemble a robot and see what
// the parts cost it. It used to be a <details> block in the editor's sidebar,
// where the numbers fit but their consequences did not.
func blueprintsPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "blueprints.html")
}

// editorPage gives the program editor a route of its own. It was previously
// reachable only as the static file /editor.html, which no page linked to — so
// the editor existed and could not be found.
func editorPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "editor.html")
}

// helpPage is the rule-language guide the editor links to: how a tick is
// decided, what the sensors really cover, and what the validation badges mean.
// Static, and deliberately reachable without a session — reading how the
// language works is not a privileged operation.
func helpPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, web.FS, "help.html")
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
