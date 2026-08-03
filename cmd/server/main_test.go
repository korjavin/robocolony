package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutes(t *testing.T) {
	tests := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/health", http.StatusOK, `{"status":"ok"}`},
		{"/", http.StatusOK, "robocolony"},
		{"/nope", http.StatusNotFound, ""},
	}

	h := routes()
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("GET %s = %d, want %d", tt.path, rec.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("GET %s body = %q, want it to contain %q", tt.path, rec.Body.String(), tt.wantBody)
			}
		})
	}
}
