package config

import (
	"log/slog"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  nil,
			want: Config{Port: "8080", LogLevel: slog.LevelInfo, BaseURL: "http://localhost:8080", DBPath: "data/robocolony.db",
				GoogleRedirectURL: "http://localhost:8080/auth/callback"},
		},
		{
			name: "overrides",
			env: map[string]string{"PORT": "9999", "LOG_LEVEL": "debug", "BASE_URL": "https://rc.example.com", "DB_PATH": "/var/lib/rc.db",
				"GOOGLE_CLIENT_ID": "cid", "GOOGLE_CLIENT_SECRET": "secret"},
			want: Config{Port: "9999", LogLevel: slog.LevelDebug, BaseURL: "https://rc.example.com", DBPath: "/var/lib/rc.db",
				GoogleClientID: "cid", GoogleClientSecret: "secret",
				// Derived from BASE_URL: https means Secure cookies.
				GoogleRedirectURL: "https://rc.example.com/auth/callback", CookieSecure: true},
		},
		{
			name: "log level is case insensitive",
			env:  map[string]string{"LOG_LEVEL": "WARN"},
			want: Config{Port: "8080", LogLevel: slog.LevelWarn, BaseURL: "http://localhost:8080", DBPath: "data/robocolony.db",
				GoogleRedirectURL: "http://localhost:8080/auth/callback"},
		},
		{
			name: "explicit redirect URL wins, trailing slash does not double up",
			env:  map[string]string{"BASE_URL": "https://rc.example.com/", "GOOGLE_REDIRECT_URL": "https://other.example/cb"},
			want: Config{Port: "8080", LogLevel: slog.LevelInfo, BaseURL: "https://rc.example.com/", DBPath: "data/robocolony.db",
				GoogleRedirectURL: "https://other.example/cb", CookieSecure: true},
		},
		{
			name:    "bad log level fails loudly",
			env:     map[string]string{"LOG_LEVEL": "chatty"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
