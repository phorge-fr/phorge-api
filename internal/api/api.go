// Package api registers the routes of the API.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/openapi"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// Deps is what the routes need.
type Deps struct {
	Auth    *auth.Authenticator
	Authz   *authz.Authorizer
	Tenants tenants.Store
	// Images and Instances are the stores of the tenant routes.
	Images      compute.ImageStore
	Instances   compute.InstanceStore
	Memberships access.MembershipStore
	Keys        access.KeyStore
	Log         *slog.Logger
	// Now is the clock, time.Now when nil.
	Now func() time.Time
}

// Register adds the routes. A route under /v1 must go through d.Auth.Middleware and be described in
// internal/openapi/openapi.yaml: the tests check both.
func Register(s *server.Server, d Deps) {
	s.Handle("GET /v1/whoami", d.Auth.Middleware(whoami(d.Authz, d.Log)))

	now := d.Now
	if now == nil {
		now = time.Now
	}
	b := base{authz: d.Authz, tenants: d.Tenants, log: d.Log, now: now}
	(&tenantRoutes{base: b, store: d.Tenants}).register(s, d.Auth.Middleware)
	(&imageRoutes{base: b, store: d.Images, instances: d.Instances}).register(s, d.Auth.Middleware)
	(&instanceRoutes{base: b, store: d.Instances, images: d.Images}).register(s, d.Auth.Middleware)
	(&membershipRoutes{base: b, store: d.Memberships}).register(s, d.Auth.Middleware)
	(&keyRoutes{base: b, store: d.Keys}).register(s, d.Auth.Middleware)

	s.Handle("GET /openapi.yaml", openapi.SpecHandler())
	s.Handle("GET /docs", openapi.UIHandler())
	s.Handle("GET /docs/init.js", openapi.InitScriptHandler())
}

type identityBody struct {
	Kind    auth.Kind `json:"kind"`
	Subject string    `json:"subject"`
	Email   string    `json:"email,omitempty"`
	// Name is the id of the key.
	Name   string   `json:"name,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// PlatformAdmin: the caller may create, resize and delete tenants.
	PlatformAdmin bool `json:"platformAdmin"`
}

func whoami(a *authz.Authorizer, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		admin, err := a.AdminMay(r.Context(), id, authz.WriteTenants)
		if err != nil {
			// Who you are does not depend on Authentik being up: the answer is "not an administrator" rather than a failure.
			log.Error("platform administrator status unknown", "error", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identityBody{
			Kind: id.Kind, Subject: id.Subject, Email: id.Email, Name: id.Name, Scopes: id.Scopes, PlatformAdmin: admin,
		})
	}
}
