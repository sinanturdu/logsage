package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		wantStatus int
		wantBody   string
	}{
		{name: "get ok", method: http.MethodGet, wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "head ok", method: http.MethodHead, wantStatus: http.StatusOK},
		{name: "post not allowed", method: http.MethodPost, wantStatus: http.StatusMethodNotAllowed},
	}

	mux := http.NewServeMux()
	Register(mux)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, "/healthz", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

type checkerFunc func(context.Context) error

func (f checkerFunc) Ready(ctx context.Context) error { return f(ctx) }

func TestReadyz(t *testing.T) {
	ok := checkerFunc(func(context.Context) error { return nil })
	down := checkerFunc(func(context.Context) error { return errors.New("redis: connection refused") })

	tests := []struct {
		name       string
		method     string
		checkers   []ReadinessChecker
		wantStatus int
		wantBody   string
	}{
		{name: "no checkers is ready", method: http.MethodGet, wantStatus: http.StatusOK, wantBody: `{"status":"ready"}`},
		{name: "all checkers ok", method: http.MethodGet, checkers: []ReadinessChecker{ok, ok}, wantStatus: http.StatusOK, wantBody: `{"status":"ready"}`},
		{name: "one checker fails", method: http.MethodGet, checkers: []ReadinessChecker{ok, down}, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"unavailable"}`},
		{name: "head ok", method: http.MethodHead, checkers: []ReadinessChecker{ok}, wantStatus: http.StatusOK},
		{name: "post not allowed", method: http.MethodPost, wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			mux := http.NewServeMux()
			RegisterReady(mux, slog.New(slog.NewTextHandler(&logs, nil)), tt.checkers...)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tt.method, "/readyz", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), "connection refused") {
				t.Errorf("body leaks checker error: %s", rec.Body.String())
			}
			if tt.wantStatus == http.StatusServiceUnavailable && !strings.Contains(logs.String(), "connection refused") {
				t.Errorf("failure not logged: %q", logs.String())
			}
		})
	}
}
