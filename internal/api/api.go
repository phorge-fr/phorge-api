// Package api registers the routes of the API.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/openapi"
	"github.com/phorge-fr/phorge-api/internal/server"
)

// Deps is what the routes need.
type Deps struct {
	Auth *auth.Authenticator
}

// Register adds the routes. A route under /v1 must go through d.Auth.Middleware and be described in
// internal/openapi/openapi.yaml: the tests check both.
func Register(s *server.Server, d Deps) {
	s.Handle("GET /v1/whoami", d.Auth.Middleware(http.HandlerFunc(whoami)))

	s.Handle("GET /openapi.yaml", openapi.SpecHandler())
	s.Handle("GET /docs", openapi.UIHandler())
	s.Handle("GET /docs/init.js", openapi.InitScriptHandler())
}

type identityBody struct {
	Kind    auth.Kind `json:"kind"`
	Subject string    `json:"subject"`
	Email   string    `json:"email,omitempty"`
	Tenant  string    `json:"tenant,omitempty"`
	Name    string    `json:"name,omitempty"`
	Scopes  []string  `json:"scopes,omitempty"`
}

func whoami(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(identityBody{
		Kind: id.Kind, Subject: id.Subject, Email: id.Email, Tenant: id.Tenant, Name: id.Name, Scopes: id.Scopes,
	})
}
