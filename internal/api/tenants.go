package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
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

// maxBody is the size of the biggest request body. Nothing the API takes is bigger than a few hundred bytes.
const maxBody = 64 << 10

// tenantRoutes are the routes of the tenants.
//
// Seeing a tenant takes a right on it (a role, or a key with the scope tenant:read), and the platform administrators see
// all of them. Creating, resizing and deleting one is for the platform administrators only: the model has no relation
// for it, see authz.Authorizer.IsPlatformAdmin.
type tenantRoutes struct {
	store tenants.Store
	authz *authz.Authorizer
	log   *slog.Logger
	now   func() time.Time
}

func (t *tenantRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants", authn(http.HandlerFunc(t.list)))
	s.Handle("POST /v1/tenants", authn(t.adminOnly(http.HandlerFunc(t.create))))
	s.Handle("GET /v1/tenants/{t}", authn(t.visible(http.HandlerFunc(t.get))))
	s.Handle("PATCH /v1/tenants/{t}", authn(t.visible(t.adminOnly(http.HandlerFunc(t.patch)))))
	s.Handle("DELETE /v1/tenants/{t}", authn(t.visible(t.adminOnly(http.HandlerFunc(t.delete)))))
}

// visible lets through who may read the tenant of the path: the platform administrators, and whoever the model says.
// Anyone else is told there is no such tenant.
func (t *tenantRoutes) visible(next http.Handler) http.Handler {
	require := t.authz.Require(authz.ReadTenant, t.log, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := auth.FromContext(r.Context()); t.authz.IsPlatformAdmin(id) {
			next.ServeHTTP(w, r)
			return
		}
		require.ServeHTTP(w, r)
	})
}

func (t *tenantRoutes) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if !t.authz.IsPlatformAdmin(id) {
			t.log.Info("authorization refused", "subject", id.Subject, "route", r.Pattern)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (t *tenantRoutes) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())

	var visible []string
	all := t.authz.IsPlatformAdmin(id)
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
		// A key belongs to one tenant, whatever OpenFGA says.
		if all || (slices.Contains(visible, tn.Name) && (id.Kind != auth.KindAPIKey || id.Tenant == tn.Name)) {
			items = append(items, t.body(tn))
		}
	}
	slices.SortFunc(items, func(a, b tenantBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (t *tenantRoutes) get(w http.ResponseWriter, r *http.Request) {
	tn, err := t.store.Get(r.Context(), r.PathValue("t"))
	if err != nil {
		t.storeError(w, err)
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
		t.storeError(w, err)
		return
	}
	tn, err := t.store.Create(r.Context(), req)
	if err != nil {
		t.storeError(w, err)
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
		t.storeError(w, err)
		return
	}
	tn, err := t.store.Patch(r.Context(), r.PathValue("t"), req)
	if err != nil {
		t.storeError(w, err)
		return
	}
	id, _ := auth.FromContext(r.Context())
	t.log.Info("tenant changed", "tenant", tn.Name, "by", id.Subject)
	writeJSON(w, http.StatusOK, t.body(tn))
}

func (t *tenantRoutes) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("t")
	if err := t.store.Delete(r.Context(), name); err != nil {
		t.storeError(w, err)
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

func (t *tenantRoutes) storeError(w http.ResponseWriter, err error) {
	var invalid *tenants.InvalidError
	switch {
	case errors.Is(err, tenants.ErrNotFound):
		httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
	case errors.Is(err, tenants.ErrExists):
		httperr.Write(w, http.StatusConflict, "already_exists", "a tenant of that name already exists")
	case errors.As(err, &invalid):
		httperr.Write(w, http.StatusUnprocessableEntity, "invalid", invalid.Error())
	default:
		t.unavailable(w, "the cluster", err)
	}
}

func (t *tenantRoutes) unavailable(w http.ResponseWriter, what string, err error) {
	t.log.Error("unavailable", "what", what, "error", err)
	httperr.Write(w, http.StatusServiceUnavailable, "unavailable", what+" is temporarily unavailable")
}

// decode reads the JSON body of a request into v and answers by itself when it cannot: only JSON, a bounded size, no
// field the API does not know (an owner in a patch is refused rather than silently ignored), one document.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httperr.Write(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httperr.Write(w, http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
			return false
		}
		httperr.Write(w, http.StatusBadRequest, "bad_request", "the request body is not valid: "+err.Error())
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httperr.Write(w, http.StatusBadRequest, "bad_request", "the request body must hold one JSON document")
		return false
	}
	return true
}
