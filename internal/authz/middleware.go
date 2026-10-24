package authz

import (
	"log/slog"
	"net/http"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/httperr"
)

// Require lets a request through when the caller may do action on the tenant named by the path value "t", and answers
// otherwise. It goes after auth.Authenticator.Middleware.
//
//   - 404 when the caller cannot even read the tenant: a tenant that is not yours is not shown to exist.
//   - 403 when it can read the tenant but not do this.
//   - 503 when OpenFGA cannot answer.
func (a *Authorizer) Require(action Action, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.FromContext(r.Context())
		if !ok {
			// A route that forgot the authentication: refuse rather than guess who is calling.
			log.Error("authorization reached without an identity", "path", r.URL.Path)
			httperr.Write(w, http.StatusUnauthorized, "unauthenticated", "a valid token or API key is required")
			return
		}
		tenant := r.PathValue("t")

		allowed, err := a.Allowed(r.Context(), id, action, tenant)
		if err != nil {
			unavailable(w, log, err)
			return
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}

		// Refused to read the tenant means it is not visible; otherwise ask whether it is.
		visible := false
		if action != ReadTenant {
			if visible, err = a.Allowed(r.Context(), id, ReadTenant, tenant); err != nil {
				unavailable(w, log, err)
				return
			}
		}
		log.Info("authorization refused", "subject", id.Subject, "action", string(action), "tenant", tenant, "visible", visible)
		if !visible {
			httperr.Write(w, http.StatusNotFound, "not_found", "no such tenant")
			return
		}
		httperr.Write(w, http.StatusForbidden, "forbidden", "you are not allowed to do this on this tenant")
	})
}

func unavailable(w http.ResponseWriter, log *slog.Logger, err error) {
	log.Error("authorization unavailable", "error", err)
	httperr.Write(w, http.StatusServiceUnavailable, "unavailable", "authorization is temporarily unavailable")
}
