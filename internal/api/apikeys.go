package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// keyRoutes are the routes of the API keys of a tenant, which only admins (or a key with the scopes memberships:read and
// memberships:write) manage. A key never exceeds its creator: nobody gives a key a scope they do not hold themselves.
type keyRoutes struct {
	base
	store access.KeyStore
}

func (k *keyRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/tenants/{t}/apikeys", "/v1/tenants/{t}/apikeys/{name}"
	s.Handle("GET "+collection, authn(k.guard(authz.ReadMemberships, http.HandlerFunc(k.list))))
	s.Handle("POST "+collection, authn(k.guard(authz.WriteMemberships, http.HandlerFunc(k.create))))
	s.Handle("GET "+item, authn(k.guard(authz.ReadMemberships, http.HandlerFunc(k.get))))
	s.Handle("PATCH "+item, authn(k.guard(authz.WriteMemberships, http.HandlerFunc(k.patch))))
	s.Handle("DELETE "+item, authn(k.guard(authz.WriteMemberships, http.HandlerFunc(k.delete))))
}

type keyBody struct {
	Name        string       `json:"name"`
	DisplayName string       `json:"displayName,omitempty"`
	Scopes      []string     `json:"scopes"`
	ExpiresAt   *time.Time   `json:"expiresAt,omitempty"`
	Expired     bool         `json:"expired"`
	Phase       status.Phase `json:"phase"`
	Problems    string       `json:"problems,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
	CreatedBy   string       `json:"createdBy,omitempty"`
	// Key is the key itself. It is in the answer to its creation and nowhere else: only its hash is stored.
	Key string `json:"key,omitempty"`
}

func (k *keyRoutes) body(key access.ApiKey) keyBody {
	scopes := key.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	// A key past its date is expired whatever the composition has caught up with.
	expired := key.Expired || (key.ExpiresAt != nil && !key.ExpiresAt.After(k.now()))
	return keyBody{
		Name: key.Name, DisplayName: key.DisplayName, Scopes: scopes, ExpiresAt: key.ExpiresAt, Expired: expired,
		Phase: key.Status.At(k.now()), Problems: key.Status.Problems, CreatedAt: key.CreatedAt, CreatedBy: key.CreatedBy,
	}
}

func (k *keyRoutes) list(w http.ResponseWriter, r *http.Request) {
	found, err := k.store.List(r.Context(), r.PathValue("t"))
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	items := []keyBody{}
	for _, key := range found {
		items = append(items, k.body(key))
	}
	slices.SortFunc(items, func(a, b keyBody) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (k *keyRoutes) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !access.ValidKeyName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such API key")
		return
	}
	key, err := k.store.Get(r.Context(), r.PathValue("t"), name)
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	writeJSON(w, http.StatusOK, k.body(key))
}

// mayGive answers whether the caller holds every scope in scopes on the tenant, and says which ones it does not. A platform
// administrator holds them all on every tenant that exists, as it may do anything on one.
func (k *keyRoutes) mayGive(w http.ResponseWriter, r *http.Request, tenant string, scopes []string) bool {
	id, _ := auth.FromContext(r.Context())
	if k.authz.IsPlatformAdmin(id) || len(scopes) == 0 {
		return true
	}
	missing, err := k.authz.Missing(r.Context(), id, tenant, scopes)
	if err != nil {
		k.unavailable(w, "authorization", err)
		return false
	}
	if len(missing) > 0 {
		k.log.Info("authorization refused", "subject", id.Subject, "route", r.Pattern, "missing", missing)
		httperr.Write(w, http.StatusForbidden, "forbidden",
			"a key cannot do more than the one who creates it: you do not hold "+strings.Join(missing, ", "))
		return false
	}
	return true
}

func (k *keyRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req access.CreateKey
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(k.now()); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	tenant := r.PathValue("t")
	if !k.mayGive(w, r, tenant, req.Scopes) {
		return
	}

	key, hash, err := access.NewKey(tenant, req.Name)
	if err != nil {
		k.unavailable(w, "the random source", err)
		return
	}
	id, _ := auth.FromContext(r.Context())
	created, err := k.store.Create(r.Context(), tenant, access.KeyRecord{CreateKey: req, Hash: hash, CreatedBy: id.Subject})
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	k.log.Info("API key created", "tenant", tenant, "key", req.Name, "scopes", req.Scopes, "by", id.Subject)

	// The only time the key is shown: it is not stored, and cannot be shown again.
	body := k.body(created)
	body.Key = key
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/tenants/"+tenant+"/apikeys/"+created.Name)
	writeJSON(w, http.StatusAccepted, body)
}

func (k *keyRoutes) patch(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !access.ValidKeyName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such API key")
		return
	}
	var req access.PatchKey
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(k.now()); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	current, err := k.store.Get(r.Context(), tenant, name)
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	// Taking a scope away is always allowed. Adding one is giving it: the caller has to hold it.
	if req.Scopes != nil {
		var added []string
		for _, s := range *req.Scopes {
			if !slices.Contains(current.Scopes, s) {
				added = append(added, s)
			}
		}
		if !k.mayGive(w, r, tenant, added) {
			return
		}
	}
	key, err := k.store.Patch(r.Context(), tenant, name, req)
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	id, _ := auth.FromContext(r.Context())
	k.log.Info("API key changed", "tenant", tenant, "key", name, "by", id.Subject)
	writeJSON(w, http.StatusOK, k.body(key))
}

func (k *keyRoutes) delete(w http.ResponseWriter, r *http.Request) {
	tenant, name := r.PathValue("t"), r.PathValue("name")
	if !access.ValidKeyName(name) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such API key")
		return
	}
	if err := k.store.Delete(r.Context(), tenant, name); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	id, _ := auth.FromContext(r.Context())
	k.log.Info("API key deleted", "tenant", tenant, "key", name, "by", id.Subject)

	key, err := k.store.Get(r.Context(), tenant, name)
	if err != nil {
		key = access.ApiKey{Name: name, Status: status.Object{Deleting: true}}
	}
	writeJSON(w, http.StatusAccepted, k.body(key))
}
