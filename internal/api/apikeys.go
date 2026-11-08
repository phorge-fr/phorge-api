package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// keyRoutes are the routes of the API keys. A key is a service account of Authentik with scopes on tenants, and it belongs to
// the user who created it: only they see it, and it never gets more than they hold. Only a person manages keys, signed in with
// Authentik: a key cannot create, read or delete keys. For now creating one is for the platform administrators (the group of
// Authentik); reading and deleting your own stays open to anyone, so that a key can always be revoked.
type keyRoutes struct {
	base
	store    access.KeyStore
	settings Settings
}

func (k *keyRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	const collection, item = "/v1/apikeys", "/v1/apikeys/{id}"
	s.Handle("GET "+collection, authn(k.person(http.HandlerFunc(k.list))))
	s.Handle("POST "+collection, authn(k.person(k.admin(http.HandlerFunc(k.create)))))
	s.Handle("GET "+item, authn(k.person(http.HandlerFunc(k.get))))
	s.Handle("PATCH "+item, authn(k.person(http.HandlerFunc(k.patch))))
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

// admin lets through who may create keys: the platform administrators, or any person when the deployment says so
// (API_KEY_CREATION). A key never exceeds what its creator holds, so opening this only decides who gets one.
func (k *keyRoutes) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k.settings.KeyCreation == config.KeyCreationUsers {
			next.ServeHTTP(w, r)
			return
		}
		id, _ := auth.FromContext(r.Context())
		if !k.authz.PlatformAdmin(r.Context(), id) {
			k.log.Info("authorization refused", "subject", id.Subject, "route", r.Pattern)
			httperr.Write(w, http.StatusForbidden, "forbidden", "only the platform administrators may create API keys on this platform")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type keyBody struct {
	ID          string         `json:"id"`
	Owner       string         `json:"owner"`
	DisplayName string         `json:"displayName"`
	Grants      []access.Grant `json:"grants"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	Expired     bool           `json:"expired"`
	// Identity is the user name of the service account: the key is exchanged with it for a token of Authentik.
	Identity  string       `json:"identity"`
	Phase     status.Phase `json:"phase"`
	Problems  string       `json:"problems,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	// Key is the key itself. It is in the answer to its creation and nowhere else: it is read from the cluster once.
	Key string `json:"key,omitempty"`
}

func (k *keyRoutes) body(key access.ApiKey) keyBody {
	grants := key.Grants
	if grants == nil {
		grants = []access.Grant{}
	}
	identity := key.Identity
	if identity == "" {
		identity = access.AccountPrefix + key.ID
	}
	// A key past its date is expired whatever the composition has caught up with: OpenFGA refuses it at the date.
	expired := key.Expired || (key.ExpiresAt != nil && !key.ExpiresAt.After(k.now()))
	return keyBody{
		ID: key.ID, Owner: key.Owner, DisplayName: key.DisplayName, Grants: grants, ExpiresAt: key.ExpiresAt, Expired: expired,
		Identity: identity, Phase: key.Status.At(k.now()), Problems: key.Status.Message(key.Status.At(k.now())), CreatedAt: key.CreatedAt,
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

// holds reports whether the creator holds what a grant gives, on a tenant that exists. A key never exceeds the person who
// makes it, and a tenant they cannot see is not told apart from one that does not exist.
func (k *keyRoutes) holds(w http.ResponseWriter, r *http.Request, id auth.Identity, grants []access.Grant) bool {
	admin := k.authz.PlatformAdmin(r.Context(), id)
	for _, g := range grants {
		if admin {
			if _, err := k.tenants.Get(r.Context(), g.Tenant); err != nil {
				if errors.Is(err, tenants.ErrNotFound) {
					httperr.Write(w, http.StatusForbidden, "forbidden", "a key cannot be given what you do not hold on tenant "+g.Tenant)
				} else {
					k.storeError(w, err, "tenant")
				}
				return false
			}
			continue
		}
		for _, scope := range g.Scopes {
			action, _ := authz.ScopeAction(scope)
			ok, err := k.authz.Allowed(r.Context(), id, action, g.Tenant)
			if err != nil {
				k.unavailable(w, "authorization", err)
				return false
			}
			if !ok {
				k.log.Info("key refused: more than its creator holds", "subject", id.Subject, "tenant", g.Tenant, "scope", scope)
				httperr.Write(w, http.StatusForbidden, "forbidden", "a key cannot be given what you do not hold on tenant "+g.Tenant)
				return false
			}
		}
	}
	return true
}

// issue waits for Crossplane to make the service account of a key and returns its key, read once from the cluster.
func (k *keyRoutes) issue(ctx context.Context, id string) (string, error) {
	timeout, every := k.settings.KeyIssueTimeout, k.settings.KeyPollInterval
	if timeout <= 0 {
		timeout = time.Minute
	}
	if every <= 0 {
		every = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		key, err := k.store.TakeKey(ctx, id)
		if !errors.Is(err, access.ErrKeyNotIssued) {
			return key, err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("the key of %s was not issued within %s", id, timeout)
		case <-time.After(every):
		}
	}
}

func (k *keyRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req access.CreateKey
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(k.now(), k.settings.KeyMaxLifetime); err != nil {
		k.storeError(w, err, "API key")
		return
	}
	id, _ := auth.FromContext(r.Context())
	if !k.holds(w, r, id, req.Grants) {
		return
	}
	keyID, err := access.NewKeyID()
	if err != nil {
		k.unavailable(w, "the random source", err)
		return
	}
	created, err := k.store.Create(r.Context(), access.KeyRecord{CreateKey: req, ID: keyID, Owner: id.Email})
	if err != nil {
		k.storeError(w, err, "API key")
		return
	}
	key, err := k.issue(r.Context(), keyID)
	if err != nil {
		// A key that cannot be handed over is of no use and could not be shown later: nothing is left behind.
		rollback, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		if derr := k.store.Delete(rollback, keyID); derr != nil {
			k.log.Error("API key not removed after failing to issue it", "key", keyID, "error", derr)
		}
		k.unavailable(w, "the creation of the key", err)
		return
	}
	k.log.Info("API key created", "key", keyID, "tenants", len(req.Grants), "by", id.Subject)

	// The only time the key is shown: it was removed from the cluster as it was read.
	body := k.body(created)
	body.Key = key
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/v1/apikeys/"+created.ID)
	writeJSON(w, http.StatusCreated, body)
}

func (k *keyRoutes) patch(w http.ResponseWriter, r *http.Request) {
	var req access.PatchKey
	if !decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
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
