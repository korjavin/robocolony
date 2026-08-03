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
			want: Config{Port: "8080", LogLevel: slog.LevelInfo, BaseURL: "http://localhost:8080", DBPath: "/app/data/robocolony.db"},
		},
		{
			name: "overrides",
			env:  map[string]string{"PORT": "9999", "LOG_LEVEL": "debug", "BASE_URL": "https://rc.example.com", "DB_PATH": "/var/lib/rc.db"},
			want: Config{Port: "9999", LogLevel: slog.LevelDebug, BaseURL: "https://rc.example.com", DBPath: "/var/lib/rc.db"},
		},
		{
			name: "log level is case insensitive",
			env:  map[string]string{"LOG_LEVEL": "WARN"},
			want: Config{Port: "8080", LogLevel: slog.LevelWarn, BaseURL: "http://localhost:8080", DBPath: "/app/data/robocolony.db"},
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
