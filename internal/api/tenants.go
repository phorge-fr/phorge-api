package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/quota"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// tenantRoutes are the routes of the tenants.
//
// Seeing a tenant takes a right on it (a role; for a key, the scope tenants:read too). Any person creates a tenant for themselves:
// they own it, and the number of tenants a person owns is limited (MAX_TENANTS_PER_USER). Changing the
// name of a tenant and deleting it is for its admins, the owner among them. Every tenant has the same limits (internal/quota),
// whatever its plan, which is deprecated and kept as it is read until the console stops using it. A key never creates, changes or deletes a tenant.
type tenantRoutes struct {
	base
	store    tenants.Store
	settings Settings
}

func (t *tenantRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants", authn(http.HandlerFunc(t.list)))
	s.Handle("POST /v1/tenants", authn(t.mayCreate(http.HandlerFunc(t.create))))
	s.Handle("GET /v1/tenants/{t}", authn(t.visible(http.HandlerFunc(t.get))))
	s.Handle("PATCH /v1/tenants/{t}", authn(t.visible(t.adminOfTheTenant(http.HandlerFunc(t.patch)))))
	s.Handle("DELETE /v1/tenants/{t}", authn(t.visible(t.adminOfTheTenant(http.HandlerFunc(t.delete)))))
}

// visible lets through who may read the tenant of the path, see base.guard.
func (t *tenantRoutes) visible(next http.Handler) http.Handler {
	return t.guard(authz.ReadTenant, next)
}

// canCreateOwnTenant reports whether this caller creates a tenant for themselves: any person, with an email, which is what the tenant
// is owned by. A key never does.
func canCreateOwnTenant(id auth.Identity) bool {
	return id.Kind == auth.KindUser && id.Email != ""
}

// mayCreate lets through a person, for themselves.
func (t *tenantRoutes) mayCreate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if canCreateOwnTenant(id) {
			next.ServeHTTP(w, r)
			return
		}
		t.log.Info("authorization refused", "subject", id.Subject, "key", id.Name, "route", r.Pattern)
		httperr.Write(w, http.StatusForbidden, "forbidden", "a tenant is created by a person signed in with Authentik, not by a key")
	})
}

// adminOfTheTenant lets through the admins of the tenant of the path, the owner among them. The tenant is already known to exist and to
// be visible to the caller, see visible.
func (t *tenantRoutes) adminOfTheTenant(next http.Handler) http.Handler {
	return t.authz.Require(authz.WriteTenant, t.log, next)
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
		Problems: tn.MessageAt(t.now()), Namespace: tn.Namespace, IncusProject: tn.IncusProject, CreatedAt: tn.CreatedAt, CreatedBy: tn.CreatedBy,
	}
}

func (t *tenantRoutes) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())

	visible, err := t.authz.Tenants(r.Context(), id)
	if err != nil {
		t.unavailable(w, "authorization", err)
		return
	}
	found, err := t.store.List(r.Context())
	if err != nil {
		t.unavailable(w, "the cluster", err)
		return
	}

	items := []tenantBody{}
	for _, tn := range found {
		if slices.Contains(visible, tn.Name) {
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
	// A person creates a tenant for themselves: the owner is the caller, and the plan the default one, which limits nothing. A request that says otherwise
	// is refused, not changed in silence.
	if req.Owner != "" && !strings.EqualFold(req.Owner, id.Email) {
		httperr.Write(w, http.StatusForbidden, "forbidden", "you can only create a tenant for yourself")
		return
	}
	if req.Plan != "" && req.Plan != tenants.Small {
		httperr.Write(w, http.StatusForbidden, "forbidden", "there is no choice of plan any more: every tenant has the same limits, leave plan out")
		return
	}
	req.Owner, req.Plan = id.Email, tenants.Small
	if err := req.Validate(); err != nil {
		t.storeError(w, err, "tenant")
		return
	}
	if e := t.checkOwnedTenants(r, id.Email); e != nil {
		quotaExceeded(w, e)
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

// checkOwnedTenants refuses a person who already owns as many tenants as they may. The tenants are counted by their owner, whoever
// created them: a tenant made for someone by an administrator counts for them.
func (t *tenantRoutes) checkOwnedTenants(r *http.Request, email string) *quota.Error {
	max := t.settings.MaxTenantsPerUser
	if max == 0 {
		return nil
	}
	all, err := t.store.List(r.Context())
	if err != nil {
		// The count could not be made: the limit is the point of this check, so a tenant is not created blind.
		return &quota.Error{Limit: "tenants", Message: "the tenants you own could not be counted, try again"}
	}
	owned := 0
	for _, tn := range all {
		if strings.EqualFold(tn.Owner, email) {
			owned++
		}
	}
	return quota.CheckCount("tenants", owned, max, "you own too many tenants")
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
	// Every tenant has the same limits: the plan is deprecated, kept until the console stops reading it, and it changes nothing. Its
	// admins rename a tenant, and a request that gives a plan is still refused, as it always was.
	if req.Plan != nil {
		httperr.Write(w, http.StatusForbidden, "forbidden", "there is no choice of plan any more: every tenant has the same limits")
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
