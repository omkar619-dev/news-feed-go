// Package metrics defines Prometheus metrics and the HTTP instrumentation middleware.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// httpRequestsTotal: a COUNTER (only goes up). One series per
	// (method, route, status) combination — request volume + error rates.
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests, by method, route, and status code.",
		},
		[]string{"method", "route", "status"},
	)

	// httpRequestDuration: a HISTOGRAM (buckets of latencies) so Prometheus can
	// compute p50/p95/p99. DefBuckets are sensible default latency boundaries.
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, by method and route.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "route"},
	)
)

// Middleware times every request and records the two metrics above.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap the writer so we can read back the status code the handler wrote
		// (the plain ResponseWriter doesn't expose it).
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		// Use the ROUTE PATTERN ("/posts/{id}"), NOT the literal path
		// ("/posts/42") — otherwise each id becomes its own metric series and
		// cardinality explodes. This is THE classic Prometheus mistake.
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}

		httpRequestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
		httpRequestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}
