package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/httperr"
	"github.com/phorge-fr/phorge-api/internal/resource"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// maxBody is the size of the biggest request body. Nothing the API takes is bigger than a few kilobytes of keys.
const maxBody = 64 << 10

// base is what the routes of every kind share.
type base struct {
	authz   *authz.Authorizer
	tenants tenants.Store
	log     *slog.Logger
	now     func() time.Time
}

// guard lets a request through when the caller may do action on the tenant of the path, and answers otherwise (see
// authz.Authorizer.Require). The platform administrators may do anything on a tenant, as they may delete it (a key of one,
// what its scopes say), but only on a tenant that exists. A name that is not a tenant slug is never looked at further.
func (b *base) guard(action authz.Action, next http.Handler) http.Handler {
	require := b.authz.Require(action, b.log, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant := r.PathValue("t")
		if !auth.ValidTenantName(tenant) {
			httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
			return
		}
		id, _ := auth.FromContext(r.Context())
		// A platform administrator acts on every tenant, within the scopes when it is a key.
		may, err := b.authz.AdminMay(r.Context(), id, action)
		if err != nil {
			b.unavailable(w, "authorization", err)
			return
		}
		if !may {
			// The key of an administrator that lacks the scope for this still reads every tenant: it is told what is missing
			// (403) rather than that the tenant does not exist.
			if id.Kind == auth.KindAPIKey {
				sees, err := b.authz.AdminMay(r.Context(), id, authz.ReadTenant)
				if err != nil {
					b.unavailable(w, "authorization", err)
					return
				}
				if sees {
					if _, err := b.tenants.Get(r.Context(), tenant); err != nil {
						b.storeError(w, err, "tenant")
						return
					}
					b.log.Info("authorization refused", "subject", id.Subject, "key", id.Name, "action", string(action), "tenant", tenant)
					httperr.Write(w, http.StatusForbidden, "forbidden", "this API key does not have the scope for this")
					return
				}
			}
			require.ServeHTTP(w, r)
			return
		}
		if _, err := b.tenants.Get(r.Context(), tenant); err != nil {
			b.storeError(w, err, "tenant")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// storeError answers for an error of a store. noun is what was looked for ("tenant", "image", "instance").
func (b *base) storeError(w http.ResponseWriter, err error, noun string) {
	var invalid *resource.InvalidError
	var inUse *resource.InUseError
	switch {
	case errors.Is(err, resource.ErrNotFound):
		httperr.Write(w, http.StatusNotFound, "not_found", "no such "+noun)
	case errors.Is(err, resource.ErrExists):
		httperr.Write(w, http.StatusConflict, "already_exists", "a "+noun+" of that name already exists")
	case errors.As(err, &inUse):
		msg := "the " + noun + " is still used by " + strings.Join(inUse.By, ", ")
		if len(inUse.By) == 0 {
			// Crossplane refuses, and for a few seconds after the last user is deleted it still does.
			msg = "the " + noun + " is still counted as used: if something that used it was just deleted, try again in a few seconds"
		}
		httperr.Write(w, http.StatusConflict, "in_use", msg)
	case errors.As(err, &invalid):
		httperr.Write(w, http.StatusUnprocessableEntity, "invalid", invalid.Error())
	default:
		b.unavailable(w, "the cluster", err)
	}
}

func (b *base) unavailable(w http.ResponseWriter, what string, err error) {
	b.log.Error("unavailable", "what", what, "error", err)
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
