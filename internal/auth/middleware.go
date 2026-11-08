package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/phorge-fr/phorge-api/internal/httperr"
)

// Authenticator authenticates the requests of the /v1 routes.
type Authenticator struct {
	// Users checks the tokens of Authentik, of a person or of the service account of a key.
	Users Verifier
	Log   *slog.Logger
}

// Middleware refuses a request that carries no valid credential and gives the others their Identity.
//
// The caller is told the same thing whatever was wrong with the credential; the reason is only logged, never the
// credential.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearer(r)
		if !ok {
			unauthenticated(w)
			return
		}

		id, err := a.Users.Verify(r.Context(), token)
		switch {
		case errors.Is(err, ErrUnavailable):
			a.Log.Error("authentication unavailable", "error", err)
			httperr.Write(w, http.StatusServiceUnavailable, "unavailable", "authentication is temporarily unavailable")
			return
		case err != nil:
			a.Log.Info("authentication refused", "reason", err.Error())
			unauthenticated(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithToken(WithIdentity(r.Context(), id), token)))
	})
}

func bearer(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	return token, found && strings.EqualFold(scheme, "Bearer") && token != ""
}

func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="phorge"`)
	httperr.Write(w, http.StatusUnauthorized, "unauthenticated", "a valid token or API key is required")
}
