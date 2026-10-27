package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/openapi"
	"github.com/phorge-fr/phorge-api/internal/server"
)

type verifier struct {
	id  auth.Identity
	err error
}

func (v verifier) Verify(context.Context, string) (auth.Identity, error) { return v.id, v.err }

func newServer(users, keys auth.Verifier) *server.Server { return newServerWith(users, keys, nil) }

// newServerWith is newServer with an Authentik that knows the groups of some users, for the keys.
func newServerWith(users, keys auth.Verifier, dir authz.Directory) *server.Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := server.New(log)
	Register(s, Deps{
		Settings: testSettings,
		Auth:     &auth.Authenticator{Users: users, Keys: keys, Log: log},
		Authz:    authz.New(nil, "phorge-platform-admins", dir), // nothing here asks OpenFGA
		Log:      log,
	})
	return s
}

func get(h http.Handler, path, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(openapi.Spec())
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatalf("the OpenAPI document is not valid: %v", err)
	}
	return doc
}

// TestOpenAPIMatchesTheRoutes is what keeps the document honest: every registered route is described and every
// description is a route.
func TestOpenAPIMatchesTheRoutes(t *testing.T) {
	doc := loadSpec(t)

	var described []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			described = append(described, method+" "+path)
		}
	}

	var registered []string
	for _, pattern := range newServer(verifier{}, verifier{}).Routes() {
		// The page of Swagger UI and its script are not part of the API.
		if pattern == "GET /docs" || pattern == "GET /docs/init.js" {
			continue
		}
		registered = append(registered, pattern)
	}

	slices.Sort(described)
	slices.Sort(registered)
	if !slices.Equal(described, registered) {
		t.Errorf("openapi.yaml and the registered routes differ\n described: %v\nregistered: %v", described, registered)
	}
}

func TestEveryV1OperationRequiresACredential(t *testing.T) {
	doc := loadSpec(t)
	for path, item := range doc.Paths.Map() {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		for method, op := range item.Operations() {
			if op.Security == nil || len(*op.Security) == 0 {
				t.Errorf("%s %s has no security requirement", method, path)
			}
			if op.OperationID == "" || op.Summary == "" {
				t.Errorf("%s %s needs an operationId and a summary", method, path)
			}
			if op.Responses.Value("401") == nil {
				t.Errorf("%s %s does not document its 401", method, path)
			}
		}
	}
}

func TestEveryV1RouteRefusesAnonymousCallers(t *testing.T) {
	s := newServer(verifier{err: auth.ErrUnauthenticated}, verifier{err: auth.ErrUnauthenticated})
	for _, pattern := range s.Routes() {
		method, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		req := httptest.NewRequestWithContext(context.Background(), method, strings.NewReplacer("{", "", "}", "").Replace(path), nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a credential, want 401", pattern, rec.Code)
		}
	}
}

func TestWhoami(t *testing.T) {
	s := newServer(
		verifier{id: auth.Identity{Kind: auth.KindUser, Subject: "user:alice@example.org", Email: "alice@example.org", Groups: []string{"staff", "phorge-platform-admins"}}},
		// A key does not carry groups, and what it claims for itself means nothing: without Authentik it is nobody's administrator.
		verifier{id: auth.Identity{Kind: auth.KindAPIKey, Subject: "user:alice@example.org", Email: "alice@example.org", Name: "0123456789abcdef", Scopes: []string{"tenants:read"}, Groups: []string{"phorge-platform-admins"}}},
	)

	rec := get(s.Handler(), "/v1/whoami", "Bearer a.jwt.token")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"kind":"user","subject":"user:alice@example.org","email":"alice@example.org","platformAdmin":true}`; got != want {
		t.Errorf("user: %s", got)
	}

	rec = get(s.Handler(), "/v1/whoami", "Bearer phk_0123456789abcdef_"+strings.Repeat("a", 64))
	if got, want := strings.TrimSpace(rec.Body.String()), `{"kind":"apikey","subject":"user:alice@example.org","email":"alice@example.org","name":"0123456789abcdef","scopes":["tenants:read"],"platformAdmin":false}`; got != want {
		t.Errorf("key: %s", got)
	}
}

type fixedDirectory map[string][]string

func (d fixedDirectory) Member(_ context.Context, email, group string) (bool, error) {
	return slices.Contains(d[group], email), nil
}

func TestWhoamiForTheKeyOfAnAdministrator(t *testing.T) {
	key := func(scopes ...string) verifier {
		return verifier{id: auth.Identity{Kind: auth.KindAPIKey, Subject: "user:alice@example.org", Email: "alice@example.org", Name: "0123456789abcdef", Scopes: scopes}}
	}
	dir := fixedDirectory{"phorge-platform-admins": {"alice@example.org"}}
	for name, tt := range map[string]struct {
		key  verifier
		want bool
	}{
		"with tenants:write": {key("tenants:read", "tenants:write"), true},
		"without it":         {key("tenants:read"), false},
	} {
		s := newServerWith(verifier{}, tt.key, dir)
		var got identityBody
		mustDecode(t, get(s.Handler(), "/v1/whoami", "Bearer phk_0123456789abcdef_"+strings.Repeat("a", 64)), &got)
		if got.PlatformAdmin != tt.want {
			t.Errorf("%s: platformAdmin = %v, want %v", name, got.PlatformAdmin, tt.want)
		}
	}
}

func TestWhoamiDoesNotShowTheGroups(t *testing.T) {
	s := newServer(verifier{id: auth.Identity{Kind: auth.KindUser, Subject: "user:alice@example.org", Email: "alice@example.org", Groups: []string{"secret-group"}}}, verifier{})
	if body := get(s.Handler(), "/v1/whoami", "Bearer a.jwt.token").Body.String(); strings.Contains(body, "secret-group") {
		t.Errorf("the groups are not part of the answer: %s", body)
	}
}

func TestSwaggerUI(t *testing.T) {
	h := newServer(verifier{}, verifier{}).Handler()

	rec := get(h, "/openapi.yaml", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "yaml") || !strings.HasPrefix(rec.Body.String(), "openapi: 3.") {
		t.Errorf("/openapi.yaml: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = get(h, "/docs", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/docs: status %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if strings.Count(body, `integrity="sha384-`) != 2 {
		t.Error("the script and the stylesheet loaded from the CDN must both carry an integrity hash")
	}
	if strings.Contains(body, "<script>") {
		t.Error("no inline script: the CSP forbids it")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "'unsafe-eval'") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}

	rec = get(h, "/docs/init.js", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SwaggerUIBundle") {
		t.Errorf("/docs/init.js: status %d", rec.Code)
	}
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
}
