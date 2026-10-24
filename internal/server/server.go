// Package server builds the HTTP handler of the API: health, metrics and, later, the /v1 routes.
package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server is the HTTP handler and the readiness switch.
type Server struct {
	log      *slog.Logger
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	draining atomic.Bool
	mux      *http.ServeMux
	routes   []string
}

// New returns a Server with /healthz, /readyz and /metrics.
func New(log *slog.Logger) *Server {
	s := &Server{
		log:      log,
		registry: prometheus.NewRegistry(),
		mux:      http.NewServeMux(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "phorge_api_http_requests_total",
			Help: "HTTP requests by method, route pattern and status code.",
		}, []string{"method", "route", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "phorge_api_http_request_duration_seconds",
			Help:    "HTTP request duration by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	s.registry.MustRegister(
		s.requests, s.duration,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	s.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	s.Handle("GET /readyz", http.HandlerFunc(s.readyz))
	s.Handle("GET /metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	return s
}

// Handle registers a route (a Go 1.22 pattern such as "GET /v1/tenants/{t}"). Every route is logged and measured.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.routes = append(s.routes, pattern)
	s.mux.Handle(pattern, h)
}

// Routes lists the registered patterns, so that a test can compare them with the OpenAPI document.
func (s *Server) Routes() []string { return append([]string(nil), s.routes...) }

// Registry lets the other packages register their own metrics.
func (s *Server) Registry() prometheus.Registerer { return s.registry }

// Drain makes /readyz fail, so that the Service stops sending traffic before the server shuts down.
func (s *Server) Drain() { s.draining.Store(true) }

// Handler is the root handler, with access logs and metrics.
func (s *Server) Handler() http.Handler { return s.instrument(s.mux) }

// readyz will check the dependencies (OpenFGA, the Kubernetes API) as they are wired in.
func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)

		// The pattern, not the path: a path holds tenant names, which would make the cardinality unbounded.
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		s.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.code)).Inc()
		s.duration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())

		// Probes and scrapes every few seconds would drown the logs.
		if route != "GET /healthz" && route != "GET /readyz" && route != "GET /metrics" {
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.code, "duration_ms", elapsed.Milliseconds())
		}
	})
}
