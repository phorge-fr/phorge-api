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
	"github.com/phorge-fr/phorge-api/internal/ratelimit"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/tenants"
	"github.com/phorge-fr/phorge-api/internal/terminal"
)

// Settings are the values that decide how the API behaves. They come from the environment (internal/config): none has a value
// here.
type Settings struct {
	// InstanceBootGrace is how long after a creation or a start a stop is refused, 0 never refuses.
	InstanceBootGrace time.Duration
	// InstanceStuckAfter is how long a transition lasts before it is reported in problems.
	InstanceStuckAfter time.Duration
	// KeyMaxLifetime caps the life of an API key, 0 leaves it free (a key always has an expiry).
	KeyMaxLifetime time.Duration
	// KeyIssueTimeout is how long creating a key waits for Crossplane to make its service account, and KeyPollInterval how often
	// it looks. Zero takes the defaults (a minute, a second): the values are for the tests.
	KeyIssueTimeout, KeyPollInterval time.Duration
	// MaxTenantsPerUser is how many tenants a person may own, 0 is no limit.
	MaxTenantsPerUser int
	// RateLimitPerMinute is how many requests that create, change or delete a caller may send a minute, in bursts of RateLimitBurst;
	// 0 limits nothing.
	RateLimitPerMinute, RateLimitBurst int
	// MaxImagesPerTenant is how many images a tenant may hold, and MaxProfilesPerTenant how many profiles its users may make; 0 is
	// no limit.
	MaxImagesPerTenant, MaxProfilesPerTenant int
}

// Deps is what the routes need.
type Deps struct {
	Auth    *auth.Authenticator
	Authz   *authz.Authorizer
	Tenants tenants.Store
	// Images, Networks and Instances are the stores of the tenant routes.
	Images      compute.ImageStore
	Networks    compute.NetworkStore
	Profiles    compute.ProfileStore
	Instances   compute.InstanceStore
	Memberships access.MembershipStore
	Keys        access.KeyStore
	// Terminal opens a terminal on an instance. Nil: the route is not registered.
	Terminal *Terminal
	Log      *slog.Logger
	Settings Settings
	// Now is the clock, time.Now when nil.
	Now func() time.Time
}

// Register adds the routes. A route under /v1 must go through d.Auth.Middleware and be described in
// internal/openapi/openapi.yaml: the tests check both.
func Register(s *server.Server, d Deps) {
	s.Handle("GET /v1/whoami", d.Auth.Middleware(whoami(d.Authz, d.Settings)))
	s.Handle("GET /v1/limits", d.Auth.Middleware(limits(d.Settings)))

	now := d.Now
	if now == nil {
		now = time.Now
	}
	b := base{authz: d.Authz, tenants: d.Tenants, log: d.Log, now: now}
	// Who writes too much is slowed down, after the credential is known and before anything is read or written.
	limiter := ratelimit.New(d.Settings.RateLimitPerMinute, d.Settings.RateLimitBurst, now)
	authn := func(h http.Handler) http.Handler { return d.Auth.Middleware(limited(limiter, h)) }
	(&tenantRoutes{base: b, store: d.Tenants, settings: d.Settings}).register(s, authn)
	(&imageRoutes{base: b, store: d.Images, instances: d.Instances, settings: d.Settings}).register(s, authn)
	(&profileRoutes{base: b, store: d.Profiles, instances: d.Instances, settings: d.Settings}).register(s, authn)
	(&quotaRoutes{base: b, instances: d.Instances, networks: d.Networks, profiles: d.Profiles}).register(s, authn)
	(&networkRoutes{base: b, store: d.Networks, instances: d.Instances, profiles: d.Profiles}).register(s, authn)
	(&instanceRoutes{base: b, store: d.Instances, images: d.Images, networks: d.Networks, profiles: d.Profiles, settings: d.Settings}).register(s, authn)
	if d.Terminal != nil {
		(&terminalRoutes{
			base: b, instances: d.Instances, terminal: *d.Terminal, registry: terminal.NewRegistry(d.Terminal.Settings),
			opens: ratelimit.New(d.Settings.RateLimitPerMinute, d.Settings.RateLimitBurst, now),
		}).register(s, authn)
	}
	(&membershipRoutes{base: b, store: d.Memberships}).register(s, authn)
	(&keyRoutes{base: b, store: d.Keys, settings: d.Settings}).register(s, authn)

	s.Handle("GET /openapi.yaml", openapi.SpecHandler())
	s.Handle("GET /docs", openapi.UIHandler())
	s.Handle("GET /docs/init.js", openapi.InitScriptHandler())
}

type identityBody struct {
	Kind    auth.Kind `json:"kind"`
	Subject string    `json:"subject"`
	Email   string    `json:"email,omitempty"`
	// Name is the id of the key.
	Name string `json:"name,omitempty"`
	// CanCreateTenants: the caller may create a tenant for themselves: a person with an email, never a key.
	CanCreateTenants bool `json:"canCreateTenants"`
}

func whoami(a *authz.Authorizer, settings Settings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identityBody{
			Kind: id.Kind, Subject: id.Subject, Email: id.Email, Name: id.Name,
			CanCreateTenants: canCreateOwnTenant(id),
		})
	}
}
