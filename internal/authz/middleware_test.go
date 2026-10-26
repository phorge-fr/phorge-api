package authz

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

func serve(t *testing.T, a *Authorizer, action Action, id *auth.Identity, tenant string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	mux := http.NewServeMux()
	mux.Handle("GET /v1/tenants/{t}/things", a.Require(action, slog.New(slog.NewTextHandler(io.Discard, nil)),
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/tenants/"+tenant+"/things", nil)
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), *id))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec, reached
}

func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct{ Error struct{ Code string } }
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatalf("not the shared error format: %v", err)
	}
	return b.Error.Code
}

func TestRequire(t *testing.T) {
	f := &fake{grants: grants(
		"user:alice@example.org can_read_tenant tenant:demo",
		"user:alice@example.org can_read_instances tenant:demo",
		"user:bob@example.org can_read_tenant tenant:demo",
		"user:bob@example.org can_write_instances tenant:demo",
	)}
	a := New(f, "", nil)
	bob := auth.Identity{Kind: auth.KindUser, Subject: "user:bob@example.org", Email: "bob@example.org"}
	mallory := auth.Identity{Kind: auth.KindUser, Subject: "user:mallory@example.org", Email: "mallory@example.org"}

	tests := []struct {
		name       string
		action     Action
		id         *auth.Identity
		tenant     string
		wantStatus int
		wantCode   string
	}{
		{"allowed", ReadInstances, &alice, "demo", http.StatusOK, ""},
		{"can read the tenant but not write", WriteInstances, &alice, "demo", http.StatusForbidden, "forbidden"},
		{"another can write", WriteInstances, &bob, "demo", http.StatusOK, ""},
		{"a stranger does not see the tenant", ReadInstances, &mallory, "demo", http.StatusNotFound, "not_found"},
		{"a stranger asking to read the tenant", ReadTenant, &mallory, "demo", http.StatusNotFound, "not_found"},
		{"a tenant that does not exist looks the same as one that is not yours", ReadInstances, &alice, "ghost", http.StatusNotFound, "not_found"},
		{"a name that is not a tenant", ReadInstances, &alice, "Demo%23admin", http.StatusNotFound, "not_found"},
		{"no identity", ReadInstances, nil, "demo", http.StatusUnauthorized, "unauthenticated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, reached := serve(t, a, tt.action, tt.id, tt.tenant)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if reached != (tt.wantStatus == http.StatusOK) {
				t.Errorf("handler reached = %v", reached)
			}
			if tt.wantCode != "" && code(t, rec) != tt.wantCode {
				t.Errorf("code %q, want %q", code(t, rec), tt.wantCode)
			}
		})
	}
}

func TestANotFoundSaysNothingAboutTheTenant(t *testing.T) {
	// Nobody granted anything: whatever the tenant, the answer is the same "no such tenant" and does not repeat the name.
	rec, _ := serve(t, New(&fake{}, "", nil), WriteImages, &alice, "demo")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "demo") {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRequireIs503WhenOpenFGAIsDown(t *testing.T) {
	a := New(&fake{err: errors.New("openfga is down")}, "", nil)
	for _, action := range []Action{ReadTenant, ReadInstances, WriteInstances} {
		rec, reached := serve(t, a, action, &alice, "demo")
		if rec.Code != http.StatusServiceUnavailable || reached || code(t, rec) != "unavailable" {
			t.Errorf("%s: status %d, reached %v", action, rec.Code, reached)
		}
	}
}
