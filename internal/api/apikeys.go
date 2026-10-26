package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// keyRoutes are the routes of the API keys. A key is personal: it belongs to the user who created it, acts as them within
// its scopes, and only they see it. Only a person manages keys, signed in with Authentik: a key cannot create, read or delete
// keys. For now creating and changing one is for the platform administrators (the group of Authentik); reading and deleting
// your own stays open to anyone, so that a key can always be revoked.
type keyRoutes struct {
	base
	store access.KeyStore
}

func (k *keyRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/apikeys", "/v1/apikeys/{id}"
	s.Handle("GET "+collection, authn(k.person(http.HandlerFunc(k.list))))
	s.Handle("POST "+collection, authn(k.person(k.admin(http.HandlerFunc(k.create)))))
	s.Handle("GET "+item, authn(k.person(http.HandlerFunc(k.get))))
	s.Handle("PATCH "+item, authn(k.person(k.admin(http.HandlerFunc(k.patch)))))
	s.Handle("DELETE "+item, authn(k.person(http.HandlerFunc(k.delete))))
}

// person refuses what is not a user signed in with Authentik.
func (k *keyRoutes) person(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if id.Kind != auth.KindUser || id.Email == "" {
			k.log.Info("authorization refused", "subject", id.Subject, "key", id.Name, "route", r.Pattern)
			httperr.Write(w, http.StatusForbidden, "forbidden", "API keys are managed by a person signed in with Authentik, not by a key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// admin lets through the platform administrators.
func (k *keyRoutes) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		ok, err := k.authz.PlatformAdmin(r.Context(), id)
		if err != nil {
			k.unavailable(w, "authorization", err)
			return
		}
		if !ok {
			k.log.Info("authorization refused", "subject", id.Subject, "route", r.Pattern)
			httperr.Write(w, http.StatusForbidden, "forbidden", "for now only the platform administrators may create and change API keys")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type keyBody struct {
	ID          string       `json:"id"`
	Owner       string       `json:"owner"`
	DisplayName string       `json:"displayName"`
	Scopes      []string     `json:"scopes"`
	ExpiresAt   *time.Time   `json:"expiresAt,omitempty"`
	Expired     bool         `json:"expired"`
	Phase       status.Phase `json:"phase"`
	Problems    string       `json:"problems,omitempty"`
	CreatedAt   time.Time    `json:"createdAt"`
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
		ID: key.ID, Owner: key.Owner, DisplayName: key.DisplayName, Scopes: scopes, ExpiresAt: key.ExpiresAt, Expired: expired,
		Phase: key.Status.At(k.now()), Problems: key.Status.Problems, CreatedAt: key.CreatedAt,
	}
}

// mine reads the key of the path and answers 404 when it is not the caller's: someone else's key is not shown to exist.
func (k *keyRoutes) mine(w http.ResponseWriter, r *http.Request) (access.ApiKey, bool) {
	id, _ := auth.FromContext(r.Context())
	keyID := r.PathValue("id")
	if !access.ValidKeyName(keyID) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such API key")
		return access.ApiKey{}, false
	}
	key, err := k.store.Get(r.Context(), keyID)
	if err != nil {
		k.storeError(w, err, "API key")
		return access.ApiKey{}, false
	}
	if !strings.EqualFold(key.Owner, id.Email) {
		httperr.Write(w, http.StatusNotFound, "not_found", "no such API key")
		return access.ApiKey{}, false
	}
	return key, true
}

func (k *keyRoutes) list(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	found, err := k.store.List(r.Context(), id.Email)
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	items := []keyBody{}
	for _, key := range found {
		items = append(items, k.body(key))
	}
	slices.SortFunc(items, func(a, b keyBody) int {
		return strings.Compare(a.DisplayName+"\x00"+a.ID, b.DisplayName+"\x00"+b.ID)
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (k *keyRoutes) get(w http.ResponseWriter, r *http.Request) {
	if key, ok := k.mine(w, r); ok {
		writeJSON(w, http.StatusOK, k.body(key))
	}
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
	id, _ := auth.FromContext(r.Context())
	keyID, key, hash, err := access.NewKey()
	if err != nil {
		k.unavailable(w, "the random source", err)
		return
	}
	created, err := k.store.Create(r.Context(), access.KeyRecord{CreateKey: req, ID: keyID, Owner: id.Email, Hash: hash})
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	k.log.Info("API key created", "key", keyID, "scopes", req.Scopes, "by", id.Subject)

	// The only time the key is shown: it is not stored, and cannot be shown again.
	body := k.body(created)
	body.Key = key
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/apikeys/"+created.ID)
	writeJSON(w, http.StatusAccepted, body)
}

func (k *keyRoutes) patch(w http.ResponseWriter, r *http.Request) {
	var req access.PatchKey
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(k.now()); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	current, ok := k.mine(w, r)
	if !ok {
		return
	}
	key, err := k.store.Patch(r.Context(), current.ID, req)
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	id, _ := auth.FromContext(r.Context())
	k.log.Info("API key changed", "key", current.ID, "by", id.Subject)
	writeJSON(w, http.StatusOK, k.body(key))
}

func (k *keyRoutes) delete(w http.ResponseWriter, r *http.Request) {
	current, ok := k.mine(w, r)
	if !ok {
		return
	}
	if err := k.store.Delete(r.Context(), current.ID); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	id, _ := auth.FromContext(r.Context())
	k.log.Info("API key deleted", "key", current.ID, "by", id.Subject)

	key, err := k.store.Get(r.Context(), current.ID)
	if err != nil {
		key = current
		key.Status = status.Object{Deleting: true}
	}
	writeJSON(w, http.StatusAccepted, k.body(key))
}
