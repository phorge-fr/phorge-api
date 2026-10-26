package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// tenantRoutes are the routes of the tenants.
//
// Seeing a tenant takes a right on it (a role; for a key, the scope tenants:read too), and the platform administrators see
// all of them. Creating, resizing and deleting one is for the platform administrators only (for a key, with the scope
// tenants:write): the model has no relation for it, see authz.Authorizer.PlatformAdmin.
type tenantRoutes struct {
	base
	store tenants.Store
}

func (t *tenantRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants", authn(http.HandlerFunc(t.list)))
	s.Handle("POST /v1/tenants", authn(t.adminOnly(http.HandlerFunc(t.create))))
	s.Handle("GET /v1/tenants/{t}", authn(t.visible(http.HandlerFunc(t.get))))
	s.Handle("PATCH /v1/tenants/{t}", authn(t.visible(t.adminOnly(http.HandlerFunc(t.patch)))))
	s.Handle("DELETE /v1/tenants/{t}", authn(t.visible(t.adminOnly(http.HandlerFunc(t.delete)))))
}

// visible lets through who may read the tenant of the path, see base.guard.
func (t *tenantRoutes) visible(next http.Handler) http.Handler {
	return t.guard(authz.ReadTenant, next)
}

func (t *tenantRoutes) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		admin, err := t.authz.AdminMay(r.Context(), id, authz.WriteTenants)
		if err != nil {
			t.unavailable(w, "authorization", err)
			return
		}
		if !admin {
			t.log.Info("authorization refused", "subject", id.Subject, "key", id.Name, "route", r.Pattern)
			httperr.Write(w, http.StatusForbidden, "forbidden", "only the platform administrators may do this")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tenantBody is how a tenant is shown.
type tenantBody struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"displayName,omitempty"`
	Owner        string        `json:"owner"`
	Plan         tenants.Plan  `json:"plan"`
	Phase        tenants.Phase `json:"phase"`
	Problems     string        `json:"problems,omitempty"`
	Namespace    string        `json:"namespace,omitempty"`
	IncusProject string        `json:"incusProject,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
	CreatedBy    string        `json:"createdBy,omitempty"`
}

func (t *tenantRoutes) body(tn tenants.Tenant) tenantBody {
	return tenantBody{
		Name: tn.Name, DisplayName: tn.DisplayName, Owner: tn.Owner, Plan: tn.Plan, Phase: tn.PhaseAt(t.now()),
		Problems: tn.Problems, Namespace: tn.Namespace, IncusProject: tn.IncusProject, CreatedAt: tn.CreatedAt, CreatedBy: tn.CreatedBy,
	}
}

func (t *tenantRoutes) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())

	var visible []string
	all, err := t.authz.AdminMay(r.Context(), id, authz.ReadTenant)
	if err != nil {
		t.unavailable(w, "authorization", err)
		return
	}
	if !all {
		var err error
		if visible, err = t.authz.Tenants(r.Context(), id); err != nil {
			t.unavailable(w, "authorization", err)
			return
		}
	}
	found, err := t.store.List(r.Context())
	if err != nil {
		t.unavailable(w, "the cluster", err)
		return
	}

	items := []tenantBody{}
	for _, tn := range found {
		if all || slices.Contains(visible, tn.Name) {
			items = append(items, t.body(tn))
		}
	}
	slices.SortFunc(items, func(a, b tenantBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (t *tenantRoutes) get(w http.ResponseWriter, r *http.Request) {
	tn, err := t.store.Get(r.Context(), r.PathValue("t"))
	if err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	writeJSON(w, http.StatusOK, t.body(tn))
}

func (t *tenantRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req tenants.CreateRequest
	if !decode(w, r, &req) {
		return
	}
	id, _ := auth.FromContext(r.Context())
	req.CreatedBy = id.Subject
	if err := req.Validate(); err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	tn, err := t.store.Create(r.Context(), req)
	if err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	t.log.Info("tenant created", "tenant", tn.Name, "by", id.Subject)
	// Accepted, not created: Crossplane takes about a minute to compose it. The client polls the Location.
	w.Header().Set("Location", "/v1/tenants/"+tn.Name)
	writeJSON(w, http.StatusAccepted, t.body(tn))
}

func (t *tenantRoutes) patch(w http.ResponseWriter, r *http.Request) {
	var req tenants.PatchRequest
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	tn, err := t.store.Patch(r.Context(), r.PathValue("t"), req)
	if err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	id, _ := auth.FromContext(r.Context())
	t.log.Info("tenant changed", "tenant", tn.Name, "by", id.Subject)
	writeJSON(w, http.StatusOK, t.body(tn))
}

func (t *tenantRoutes) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("t")
	if err := t.store.Delete(r.Context(), name); err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	id, _ := auth.FromContext(r.Context())
	t.log.Info("tenant deleted", "tenant", name, "by", id.Subject)

	// The deletion is in the foreground: the tenant is still there, as Deleting, until everything it is made of is gone.
	// "Not found" is what tells a client that it is really done.
	tn, err := t.store.Get(r.Context(), name)
	if err != nil {
		tn = tenants.Tenant{Name: name, Deleting: true}
	}
	writeJSON(w, http.StatusAccepted, t.body(tn))
}
