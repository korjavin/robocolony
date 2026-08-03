// Command server is the robocolony game server: HTTP API, SSE world stream,
// and the static client, all from one binary.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/korjavin/robocolony/internal/config"
	"github.com/korjavin/robocolony/web"
)

// shutdownGrace is how long in-flight requests get to finish after a signal.
const shutdownGrace = 30 * time.Second

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

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cfg.Port),
		Handler:           routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: the world stream is long-lived SSE (design §4.4).
		IdleTimeout: 120 * time.Second,
		ErrorLog:    slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	log.Info("shutdown complete")
	return nil
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
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
