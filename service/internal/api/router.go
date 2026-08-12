// Package api builds the HTTP surface shared by the Lambda and local
// entrypoints. Routes live under /api/app/* (the path CloudFront routes to this
// service). Health is public; everything else requires a verified bearer token.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/521studios/encounter-builder-api/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Config injects the service's dependencies into the router.
type Config struct {
	Auth *auth.Verifier
	Env  string
}

// NewRouter wires the routes. The same *chi.Mux is served by cmd/lambda (via the
// API-Gateway-v2 proxy adapter) and cmd/local (via net/http).
func NewRouter(cfg Config) *chi.Mux {
	if cfg.Auth == nil {
		// Fail fast at construction rather than nil-panic deep in the middleware
		// on the first request — every protected route depends on it.
		panic("api: NewRouter requires a non-nil Auth verifier")
	}
	h := &handler{cfg: cfg}

	r := chi.NewRouter()
	r.Use(requestLogger)

	// Public: liveness/readiness, no auth (also CloudFront/ALB health if wired).
	r.Get("/api/app/healthz", h.health)

	// Everything below requires a valid lets-roll OIDC bearer token.
	r.Group(func(r chi.Router) {
		r.Use(cfg.Auth.Middleware)
		r.Get("/api/app/me", h.me)
	})

	return r
}

// requestLogger logs one line per request with method, path, status, and
// duration. Hand-rolled to avoid a middleware dependency (matches the sibling
// pfsrd2-data-api pattern).
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
