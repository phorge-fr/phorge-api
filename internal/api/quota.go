package api

import (
	"net/http"
	"strconv"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/quota"
	"github.com/phorge-fr/phorge-api/internal/ratelimit"
	"github.com/phorge-fr/phorge-api/internal/server"
)

// quotaRoutes tell a tenant what it is allowed to hold, the same for every tenant, and what it holds, so that a client draws what is
// left.
type quotaRoutes struct {
	base
	instances compute.InstanceStore
	networks  compute.NetworkStore
	profiles  compute.ProfileStore
}

func (q *quotaRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants/{t}/quota", authn(q.guard(authz.ReadInstances, http.HandlerFunc(q.get))))
}

type quotaBody struct {
	Limits quota.Limits `json:"limits"`
	Used   quota.Used   `json:"used"`
}

func (q *quotaRoutes) get(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("t")
	used, _, ok := holdings(w, r, &q.base, q.instances, q.networks, q.profiles, tenant)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, quotaBody{Limits: quota.PerTenant, Used: used})
}

// holdings reads what a tenant holds, and the profiles its users made (an instance counts what its profile comes to),
// or answers.
func holdings(w http.ResponseWriter, r *http.Request, b *base, instances compute.InstanceStore, networks compute.NetworkStore, profiles compute.ProfileStore, tenant string) (quota.Used, []compute.Profile, bool) {
	if _, err := b.tenants.Get(r.Context(), tenant); err != nil {
		b.storeError(w, err, "tenant")
		return quota.Used{}, nil, false
	}
	in, err := instances.List(r.Context(), tenant)
	if err != nil {
		b.storeError(w, err, "instance")
		return quota.Used{}, nil, false
	}
	nw, err := networks.List(r.Context(), tenant)
	if err != nil {
		b.storeError(w, err, "network")
		return quota.Used{}, nil, false
	}
	made, err := profiles.List(r.Context(), tenant)
	if err != nil {
		b.storeError(w, err, "profile")
		return quota.Used{}, nil, false
	}
	return quota.UsedBy(in, nw, made), made, true
}

// quotaExceeded answers a request that would pass a limit: 409, with a code a program can act on and a sentence that says which.
func quotaExceeded(w http.ResponseWriter, e *quota.Error) {
	httperr.Write(w, http.StatusConflict, "quota_exceeded", e.Message)
}

// limited slows the callers that write too much: the requests that create, change or delete, by caller. Reading is not limited.
// A caller is the subject of its credential, a person or a key.
func limited(l *ratelimit.Limiter, next http.Handler) http.Handler {
	if !l.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		id, ok := auth.FromContext(r.Context())
		if !ok || id.Subject == "" {
			next.ServeHTTP(w, r)
			return
		}
		if allowed, wait := l.Allow(id.Subject); !allowed {
			seconds := int((wait + 999_999_999) / 1_000_000_000) // up to the next second
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			httperr.Write(w, http.StatusTooManyRequests, "rate_limited", "too many requests: try again in "+strconv.Itoa(seconds)+" seconds")
			return
		}
		next.ServeHTTP(w, r)
	})
}
