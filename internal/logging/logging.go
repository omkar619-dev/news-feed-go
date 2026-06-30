// Package logging configures structured (JSON) logging via slog.
package logging

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Setup makes slog's DEFAULT logger emit JSON to stdout. Call it once at
// startup; afterwards any slog.Info/Warn/Error(...) anywhere in the program
// produces a structured JSON line.
func Setup() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
}

// RequestLogger logs ONE structured line per HTTP request. It replaces chi's
// plain-text middleware.Logger so request logs are JSON too.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap the writer so we can read the status code + bytes the handler wrote.
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		// Route TEMPLATE, not the literal path (same reasoning as the metrics
		// middleware — keeps the "route" field tidy and groupable).
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		slog.Info("http request",
			"method", r.Method,
			"route", route,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
