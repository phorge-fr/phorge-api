package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

func newServer(logs *strings.Builder) *Server {
	return New(slog.New(slog.NewJSONHandler(logs, nil)))
}

func TestHealth(t *testing.T) {
	h := newServer(&strings.Builder{}).Handler()
	for _, path := range []string{"/healthz", "/readyz"} {
		if got := get(t, h, path).Code; got != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, got)
		}
	}
}

func TestDrainFailsReadinessOnly(t *testing.T) {
	s := newServer(&strings.Builder{})
	s.Drain()
	if got := get(t, s.Handler(), "/readyz").Code; got != http.StatusServiceUnavailable {
		t.Errorf("/readyz while draining = %d, want 503", got)
	}
	if got := get(t, s.Handler(), "/healthz").Code; got != http.StatusOK {
		t.Errorf("/healthz while draining = %d, want 200: the pod must not be restarted", got)
	}
}

func TestMetricsUseTheRoutePatternNotThePath(t *testing.T) {
	s := newServer(&strings.Builder{})
	s.Handle("GET /v1/tenants/{t}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	h := s.Handler()

	get(t, h, "/v1/tenants/demo")
	get(t, h, "/v1/tenants/other")
	get(t, h, "/nowhere")

	body, _ := io.ReadAll(get(t, h, "/metrics").Body)
	metrics := string(body)
	for _, want := range []string{
		`phorge_api_http_requests_total{code="418",method="GET",route="GET /v1/tenants/{t}"} 2`,
		`phorge_api_http_requests_total{code="404",method="GET",route="unmatched"} 1`,
		"go_goroutines",
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("/metrics does not contain %q", want)
		}
	}
	if strings.Contains(metrics, "/v1/tenants/demo") || strings.Contains(metrics, "/v1/tenants/other") || strings.Contains(metrics, "/nowhere") {
		t.Error("a tenant name leaked into a metric label")
	}
}

func TestLogsRequestsButNotProbes(t *testing.T) {
	var logs strings.Builder
	s := newServer(&logs)
	s.Handle("GET /v1/ping", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h := s.Handler()

	get(t, h, "/healthz")
	get(t, h, "/readyz")
	get(t, h, "/metrics")
	if logs.Len() != 0 {
		t.Errorf("probes and scrapes should not be logged, got %s", logs.String())
	}
	get(t, h, "/v1/ping")
	if !strings.Contains(logs.String(), `"path":"/v1/ping"`) || !strings.Contains(logs.String(), `"status":200`) {
		t.Errorf("request not logged as JSON: %s", logs.String())
	}
}
