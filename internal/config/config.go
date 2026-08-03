// Package config reads process configuration from the environment once at
// startup. The resulting Config is passed down explicitly; there is no global.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Config holds every setting the server needs. Extend it as epics land.
type Config struct {
	Port     string     // TCP port to listen on
	LogLevel slog.Level // minimum level for the slog handler
	BaseURL  string     // public origin, used for OIDC redirects
	DBPath   string     // SQLite file; its directory must be writable by the process uid

	GoogleClientID     string
	GoogleClientSecret string
	// GoogleRedirectURL defaults to BaseURL + /auth/callback so there is only
	// one URL to keep in sync with the Google console. It must match the
	// console entry byte for byte.
	GoogleRedirectURL string
	// CookieSecure follows the BaseURL scheme: on behind Traefik's TLS, off
	// for plain http://localhost, which would otherwise drop every cookie.
	CookieSecure bool
}

// Load reads the environment and applies defaults. It fails only on values
// that are present but unparseable, so a typo is loud instead of silent.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:     or(getenv("PORT"), "8080"),
		LogLevel: slog.LevelInfo,
		BaseURL:  or(getenv("BASE_URL"), "http://localhost:8080"),
		// Relative on purpose: the container's WORKDIR is /app, so this default
		// resolves to /app/data/robocolony.db there while a local `go run`
		// still works without setting anything.
		DBPath: or(getenv("DB_PATH"), "data/robocolony.db"),

		GoogleClientID:     getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: getenv("GOOGLE_CLIENT_SECRET"),
	}
	if s := getenv("LOG_LEVEL"); s != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(s)); err != nil {
			return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
		}
	}
	cfg.GoogleRedirectURL = or(getenv("GOOGLE_REDIRECT_URL"), strings.TrimSuffix(cfg.BaseURL, "/")+"/auth/callback")
	cfg.CookieSecure = strings.HasPrefix(cfg.BaseURL, "https://")
	return cfg, nil
}

// LoadEnv is Load against the real process environment.
func LoadEnv() (Config, error) { return Load(os.Getenv) }

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
