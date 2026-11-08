package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// store is an in-memory tenants.Store that, like the foreground deletion, keeps a deleted tenant as Deleting.
type store struct {
	tenants map[string]tenants.Tenant
	created []tenants.CreateRequest
	patched []string
	deleted []string
	err     error
}

func (s *store) List(context.Context) ([]tenants.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []tenants.Tenant
	for _, t := range s.tenants {
		out = append(out, t)
	}
	return out, nil
}

func (s *store) Get(_ context.Context, name string) (tenants.Tenant, error) {
	if s.err != nil {
		return tenants.Tenant{}, s.err
	}
	t, ok := s.tenants[name]
	if !ok {
		return tenants.Tenant{}, tenants.ErrNotFound
	}
	return t, nil
}

func (s *store) Create(_ context.Context, r tenants.CreateRequest) (tenants.Tenant, error) {
	if s.err != nil {
		return tenants.Tenant{}, s.err
	}
	if _, ok := s.tenants[r.Name]; ok {
		return tenants.Tenant{}, tenants.ErrExists
	}
	s.created = append(s.created, r)
	plan := r.Plan
	if plan == "" {
		plan = tenants.Small
	}
	t := tenants.Tenant{Name: r.Name, Owner: r.Owner, Plan: plan, DisplayName: r.DisplayName, CreatedBy: r.CreatedBy, CreatedAt: now, NotReadySince: now}
	s.tenants[r.Name] = t
	return t, nil
}

func (s *store) Patch(_ context.Context, name string, r tenants.PatchRequest) (tenants.Tenant, error) {
	t, err := s.Get(context.Background(), name)
	if err != nil {
		return t, err
	}
	if r.Plan != nil {
		t.Plan = *r.Plan
	}
	if r.DisplayName != nil {
		t.DisplayName = *r.DisplayName
	}
	s.tenants[name] = t
	s.patched = append(s.patched, name)
	return t, nil
}

func (s *store) Delete(_ context.Context, name string) error {
	t, err := s.Get(context.Background(), name)
	if err != nil {
		return err
	}
	t.Deleting = true
	s.tenants[name] = t
	s.deleted = append(s.deleted, name)
	return nil
}

// checker is the OpenFGA of these tests: a set of "user relation object" lines.
type checker struct {
	grants  map[string]bool
	objects map[string][]string // ListObjects by user
	err     error
}

func (c *checker) Check(_ context.Context, user, relation, object string) (bool, error) {
	return c.grants[user+" "+relation+" "+object], c.err
}

func (c *checker) ListObjects(_ context.Context, user, _, _ string) ([]string, error) {
	return c.objects[user], c.err
}

type tokens map[string]auth.Identity

func (t tokens) Verify(_ context.Context, token string) (auth.Identity, error) {
	if id, ok := t[token]; ok {
		return id, nil
	}
	return auth.Identity{}, auth.ErrUnauthenticated
}

const adminGroup = "phorge-platform-admins"

// testSettings are the values these tests run with, as a deployment would write them: the API itself has none.
var testSettings = Settings{
	InstanceBootGrace:  3 * time.Minute,
	InstanceStuckAfter: 10 * time.Minute,
	KeyMaxLifetime:     366 * 24 * time.Hour,
	KeyCreation:        config.KeyCreationPlatformAdmins,
}

type env struct {
	srv         *server.Server
	store       *store
	fga         *checker
	images      *imageStore
	networks    *networkStore
	instances   *instanceStore
	memberships *membershipStore
	keys        *keyStore
}

func newEnv(t *testing.T) *env { return newEnvWith(t, testSettings) }

// newEnvWith is newEnv with the settings a deployment would give.
func newEnvWith(t *testing.T, settings Settings) *env {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := &store{tenants: map[string]tenants.Tenant{
		"demo":  {Name: "demo", Owner: "alice@example.org", Plan: tenants.Large, CreatedAt: now, Ready: true, NotReadySince: now, Namespace: "t-demo", IncusProject: "t-demo"},
		"other": {Name: "other", Owner: "bob@example.org", Plan: tenants.Large, CreatedAt: now, NotReadySince: now.Add(-time.Hour), Problems: "project Synced: Reached maximum"},
	}}
	fga := &checker{
		grants: map[string]bool{
			"user:alice@example.org can_read_tenant tenant:demo": true,
			// The tuples of a service key: it reads the tenant and writes the instances, nothing else.
			"apikey:apikey-0123456789abcdef can_read_tenant tenant:demo":     true,
			"apikey:apikey-0123456789abcdef can_write_instances tenant:demo": true,
			// A key that reads the images, and one that manages the memberships and reads the instances: no more than that.
			"apikey:apikey-00000000000000a1 can_read_tenant tenant:demo":       true,
			"apikey:apikey-00000000000000a1 can_read_images tenant:demo":       true,
			"apikey:apikey-00000000000000a2 can_read_tenant tenant:demo":       true,
			"apikey:apikey-00000000000000a2 can_read_instances tenant:demo":    true,
			"apikey:apikey-00000000000000a2 can_read_memberships tenant:demo":  true,
			"apikey:apikey-00000000000000a2 can_write_memberships tenant:demo": true,
			"user:viewer@example.org can_read_tenant tenant:demo":              true,

			// What the roles of the model give, written out (the model itself is tested against OpenFGA in authz).
			"user:viewer@example.org can_read_images tenant:demo":    true,
			"user:viewer@example.org can_read_networks tenant:demo":  true,
			"user:viewer@example.org can_read_instances tenant:demo": true,

			"user:user@example.org can_read_tenant tenant:demo":     true,
			"user:user@example.org can_read_images tenant:demo":     true,
			"user:user@example.org can_read_networks tenant:demo":   true,
			"user:user@example.org can_read_instances tenant:demo":  true,
			"user:user@example.org can_write_instances tenant:demo": true,

			"user:operator@example.org can_read_tenant tenant:demo":     true,
			"user:operator@example.org can_read_images tenant:demo":     true,
			"user:operator@example.org can_write_images tenant:demo":    true,
			"user:operator@example.org can_read_networks tenant:demo":   true,
			"user:operator@example.org can_write_networks tenant:demo":  true,
			"user:operator@example.org can_read_instances tenant:demo":  true,
			"user:operator@example.org can_write_instances tenant:demo": true,

			// An admin of the tenant holds everything the model gives.
			"user:tadmin@example.org can_read_tenant tenant:demo":  true,
			"user:tadmin@example.org can_write_tenant tenant:demo": true,
			// alice owns demo: she is its admin.
			"user:alice@example.org can_write_tenant tenant:demo":       true,
			"user:tadmin@example.org can_read_images tenant:demo":       true,
			"user:tadmin@example.org can_write_images tenant:demo":      true,
			"user:tadmin@example.org can_read_networks tenant:demo":     true,
			"user:tadmin@example.org can_write_networks tenant:demo":    true,
			"user:tadmin@example.org can_read_instances tenant:demo":    true,
			"user:tadmin@example.org can_write_instances tenant:demo":   true,
			"user:tadmin@example.org can_read_memberships tenant:demo":  true,
			"user:tadmin@example.org can_write_memberships tenant:demo": true,
		},
		objects: map[string][]string{
			"user:alice@example.org":         {"tenant:demo"},
			"user:viewer@example.org":        {"tenant:demo"},
			"user:operator@example.org":      {"tenant:demo"},
			"user:tadmin@example.org":        {"tenant:demo"},
			"apikey:apikey-0123456789abcdef": {"tenant:demo"},
			"apikey:apikey-00000000000000a1": {"tenant:demo"},
			"apikey:apikey-00000000000000a2": {"tenant:demo"},
		},
	}
	users := tokens{
		"admin":    {Kind: auth.KindUser, Subject: "user:admin@example.org", Email: "admin@example.org", Groups: []string{adminGroup}},
		"alice":    {Kind: auth.KindUser, Subject: "user:alice@example.org", Email: "alice@example.org"},
		"viewer":   {Kind: auth.KindUser, Subject: "user:viewer@example.org", Email: "viewer@example.org"},
		"user":     {Kind: auth.KindUser, Subject: "user:user@example.org", Email: "user@example.org"},
		"operator": {Kind: auth.KindUser, Subject: "user:operator@example.org", Email: "operator@example.org"},
		"tadmin":   {Kind: auth.KindUser, Subject: "user:tadmin@example.org", Email: "tadmin@example.org"},
		"mallory":  {Kind: auth.KindUser, Subject: "user:mallory@example.org", Email: "mallory@example.org", Groups: []string{"staff"}},
		"almostit": {Kind: auth.KindUser, Subject: "user:almost@example.org", Email: "almost@example.org", Groups: []string{adminGroup + "-ro"}},
		// A second administrator, and one who left the group.
		"admin2":  {Kind: auth.KindUser, Subject: "user:admin2@example.org", Email: "admin2@example.org", Groups: []string{adminGroup}},
		"exadmin": {Kind: auth.KindUser, Subject: "user:exadmin@example.org", Email: "exadmin@example.org"},
		// Service keys: service accounts of Authentik, with only the tuples OpenFGA holds for "apikey:<name>".
		"svc":        {Kind: auth.KindServiceKey, Subject: "apikey:apikey-0123456789abcdef", Name: "apikey-0123456789abcdef"},
		"svc_images": {Kind: auth.KindServiceKey, Subject: "apikey:apikey-00000000000000a1", Name: "apikey-00000000000000a1"},
		"svc_mgr":    {Kind: auth.KindServiceKey, Subject: "apikey:apikey-00000000000000a2", Name: "apikey-00000000000000a2"},
		// A key whose tuples expired, or that has none: OpenFGA says no to everything.
		"svc_none": {Kind: auth.KindServiceKey, Subject: "apikey:apikey-00000000000000a3", Name: "apikey-00000000000000a3"},
	}
	images := &imageStore{data: map[string]map[string]compute.Image{}}
	networks := &networkStore{data: map[string]map[string]compute.Network{}}
	// The one network of the tenant, which an instance is attached to when its request names none.
	networks.put("demo", compute.Network{Name: "net", CreatedAt: now, Status: status.Object{Ready: true, NotReadySince: now}})
	instances := &instanceStore{data: map[string]map[string]compute.Instance{}}
	memberships := &membershipStore{data: map[string]map[string]access.Membership{}}
	apikeys := &keyStore{data: map[string]access.ApiKey{}, secrets: map[string]string{}}
	s := server.New(log)
	Register(s, Deps{
		Settings:    settings,
		Auth:        &auth.Authenticator{Users: users, Log: log},
		Authz:       authz.New(fga, adminGroup),
		Tenants:     st,
		Images:      images,
		Networks:    networks,
		Instances:   instances,
		Memberships: memberships,
		Keys:        apikeys,
		Log:         log,
		Now:         func() time.Time { return now },
	})
	return &env{srv: s, store: st, fga: fga, images: images, networks: networks, instances: instances, memberships: memberships, keys: apikeys}
}

func (e *env) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func names(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out struct {
		Items []struct{ Name string } `json:"items"`
	}
	mustDecode(t, rec, &out)
	got := []string{}
	for _, i := range out.Items {
		got = append(got, i.Name)
	}
	return got
}

func TestListShowsWhatTheCallerMaySee(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		token string
		want  []string
	}{
		{"admin", []string{"demo", "other"}},
		{"alice", []string{"demo"}},
		{"viewer", []string{"demo"}},
		{"mallory", []string{}},
		{"almostit", []string{}},
		{"svc", []string{"demo"}},
		{"svc_none", []string{}}, // a key whose tuples expired sees nothing
	}
	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			rec := e.do(t, http.MethodGet, "/v1/tenants", tt.token, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if got := names(t, rec); !slices.Equal(got, tt.want) {
				t.Errorf("tenants = %v, want %v", got, tt.want)
			}
		})
	}
	if rec := e.do(t, http.MethodGet, "/v1/tenants", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a credential: %d", rec.Code)
	}
}

func TestListIsSortedAndAnEmptyListIsAnArray(t *testing.T) {
	e := newEnv(t)
	e.store.tenants["alpha"] = tenants.Tenant{Name: "alpha", Owner: "a@b", Plan: tenants.Small}
	if got := names(t, e.do(t, http.MethodGet, "/v1/tenants", "admin", "")); !slices.Equal(got, []string{"alpha", "demo", "other"}) {
		t.Errorf("order = %v", got)
	}
	if body := e.do(t, http.MethodGet, "/v1/tenants", "mallory", "").Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("an empty list must be [], not null: %s", body)
	}
}

func TestGet(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name, token, path string
		want              int
	}{
		{"a member", "alice", "/v1/tenants/demo", http.StatusOK},
		{"a viewer", "viewer", "/v1/tenants/demo", http.StatusOK},
		{"a key with a tuple on the tenant", "svc", "/v1/tenants/demo", http.StatusOK},
		{"the same key on a tenant it has no tuple on", "svc", "/v1/tenants/other", http.StatusNotFound},
		{"a key with no tuple left", "svc_none", "/v1/tenants/demo", http.StatusNotFound},
		{"a platform administrator, with no role on it", "admin", "/v1/tenants/other", http.StatusOK},
		{"a stranger", "mallory", "/v1/tenants/demo", http.StatusNotFound},
		{"a group that looks like the administrators", "almostit", "/v1/tenants/demo", http.StatusNotFound},
		{"an administrator asks for a tenant that does not exist", "admin", "/v1/tenants/ghost", http.StatusNotFound},
		{"a name that is not a tenant", "admin", "/v1/tenants/Demo", http.StatusNotFound},
		{"nobody", "", "/v1/tenants/demo", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(t, http.MethodGet, tt.path, tt.token, "")
			if rec.Code != tt.want {
				t.Errorf("status %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestGetShowsThePhase(t *testing.T) {
	e := newEnv(t)
	var ready, failed tenantBody
	mustDecode(t, e.do(t, http.MethodGet, "/v1/tenants/demo", "admin", ""), &ready)
	mustDecode(t, e.do(t, http.MethodGet, "/v1/tenants/other", "admin", ""), &failed)
	if ready.Phase != tenants.Ready || ready.Namespace != "t-demo" || ready.Owner != "alice@example.org" || ready.Plan != tenants.Large {
		t.Errorf("ready: %+v", ready)
	}
	if failed.Phase != tenants.Failed || failed.Problems != "This tenant has reached a limit of its plan." {
		t.Errorf("failed: %+v, the problem is told in words once it has lasted", failed)
	}
}

// What the console showed for a tenant whose project did not exist: the conditions of Crossplane's objects, with the name of an
// Incus project. A client is never told that.
func TestACrossplaneConditionNeverReachesTheClient(t *testing.T) {
	e := newEnv(t)
	e.store.tenants["broken"] = tenants.Tenant{Name: "broken", Owner: "alice@example.org", Plan: tenants.Small, CreatedAt: now, NotReadySince: now.Add(-time.Hour),
		Problems: "network Ready: not ready | network Synced: create failed: apply failed: Failed to create network \"default\": Failed to load project \"t-broken\": Project not found | " +
			"owner Ready: not ready | profile LastAsyncOperation: apply failed: Failed to create profile \"phorge\": Project not found | project Ready: not ready"}
	var got tenantBody
	mustDecode(t, e.do(t, http.MethodGet, "/v1/tenants/broken", "admin", ""), &got)
	if got.Phase != tenants.Failed {
		t.Errorf("phase = %s", got.Phase)
	}
	for _, leak := range []string{"Ready", "Synced", "LastAsyncOperation", "apply failed", "Project not found", "t-broken", "network", "profile", "owner"} {
		if strings.Contains(got.Problems, leak) {
			t.Errorf("%q reaches the client: %q", leak, got.Problems)
		}
	}
	if got.Problems == "" {
		t.Error("a failed tenant says nothing")
	}
}

func TestCreate(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPost, "/v1/tenants", "admin", `{"name":"newone","owner":"Carol@Example.org","plan":"medium","displayName":"New one"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "/v1/tenants/newone" {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	var got tenantBody
	mustDecode(t, rec, &got)
	if got.Name != "newone" || got.Owner != "Carol@Example.org" || got.Plan != tenants.Medium || got.Phase != tenants.Provisioning {
		t.Errorf("body: %+v", got)
	}
	if len(e.store.created) != 1 || e.store.created[0].CreatedBy != "user:admin@example.org" {
		t.Errorf("created: %+v: the creator is recorded", e.store.created)
	}
}

func TestCreateIsForPlatformAdministratorsOnly(t *testing.T) {
	e := newEnv(t)
	const body = `{"name":"newone","owner":"a@b"}`
	for _, token := range []string{"alice", "viewer", "mallory", "almostit", "svc", "svc_none"} {
		rec := e.do(t, http.MethodPost, "/v1/tenants", token, body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", token, rec.Code)
		}
	}
	if rec := e.do(t, http.MethodPost, "/v1/tenants", "", body); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if len(e.store.created) != 0 {
		t.Errorf("a tenant was created by somebody who may not: %+v", e.store.created)
	}
}

func TestCreateRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	post := func(contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/tenants", strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Authorization", "Bearer admin")
		rec := httptest.NewRecorder()
		e.srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"a name that is not a slug", "application/json", `{"name":"Demo!","owner":"a@b"}`, http.StatusUnprocessableEntity},
		{"no owner", "application/json", `{"name":"newone"}`, http.StatusUnprocessableEntity},
		{"an owner that is a wildcard", "application/json", `{"name":"newone","owner":"*"}`, http.StatusUnprocessableEntity},
		{"an unknown plan", "application/json", `{"name":"newone","owner":"a@b","plan":"huge"}`, http.StatusUnprocessableEntity},
		{"a name that exists", "application/json", `{"name":"demo","owner":"a@b"}`, http.StatusConflict},
		{"not JSON", "application/json", `name=newone`, http.StatusBadRequest},
		{"a field that does not exist", "application/json", `{"name":"newone","owner":"a@b","admin":true}`, http.StatusBadRequest},
		{"a wrong type", "application/json", `{"name":"newone","owner":42}`, http.StatusBadRequest},
		{"two documents", "application/json", `{"name":"newone","owner":"a@b"}{"name":"two","owner":"a@b"}`, http.StatusBadRequest},
		{"an empty body", "application/json", ``, http.StatusBadRequest},
		{"a form", "application/x-www-form-urlencoded", `name=newone&owner=a@b`, http.StatusUnsupportedMediaType},
		{"no content type", "", `{"name":"newone","owner":"a@b"}`, http.StatusUnsupportedMediaType},
		{"a body that is too big", "application/json", `{"name":"newone","owner":"a@b","displayName":"` + strings.Repeat("a", maxBody) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := post(tt.contentType, tt.body); rec.Code != tt.want {
				t.Errorf("status %d, want %d (%.200s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
	if len(e.store.created) != 0 {
		t.Errorf("a bad request created %+v", e.store.created)
	}
}

func TestPatch(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodPatch, "/v1/tenants/demo", "admin", `{"plan":"large","displayName":"Production"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got tenantBody
	mustDecode(t, rec, &got)
	if got.Plan != tenants.Large || got.DisplayName != "Production" || got.Owner != "alice@example.org" {
		t.Errorf("body: %+v", got)
	}

	for name, tt := range map[string]struct {
		token, path, body string
		want              int
	}{
		"the owner cannot be changed":            {"admin", "/v1/tenants/demo", `{"owner":"mallory@example.org"}`, http.StatusBadRequest},
		"nor the name":                           {"admin", "/v1/tenants/demo", `{"name":"x"}`, http.StatusBadRequest},
		"an empty patch":                         {"admin", "/v1/tenants/demo", `{}`, http.StatusUnprocessableEntity},
		"an unknown plan":                        {"admin", "/v1/tenants/demo", `{"plan":"huge"}`, http.StatusUnprocessableEntity},
		"a tenant that does not exist":           {"admin", "/v1/tenants/ghost", `{"plan":"large"}`, http.StatusNotFound},
		"a member who sees the tenant":           {"alice", "/v1/tenants/demo", `{"plan":"large"}`, http.StatusForbidden},
		"a stranger does not see it":             {"mallory", "/v1/tenants/demo", `{"plan":"large"}`, http.StatusNotFound},
		"a key, which is never an administrator": {"svc", "/v1/tenants/demo", `{"plan":"large"}`, http.StatusForbidden},
		"nobody":                                 {"", "/v1/tenants/demo", `{"plan":"large"}`, http.StatusUnauthorized},
	} {
		before := len(e.store.patched)
		if rec := e.do(t, http.MethodPatch, tt.path, tt.token, tt.body); rec.Code != tt.want {
			t.Errorf("%s: status %d, want %d (%.200s)", name, rec.Code, tt.want, rec.Body.String())
		}
		if tt.want != http.StatusOK && len(e.store.patched) != before {
			t.Errorf("%s: the tenant was patched", name)
		}
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, http.MethodDelete, "/v1/tenants/demo", "admin", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got tenantBody
	mustDecode(t, rec, &got)
	if got.Name != "demo" || got.Phase != tenants.Deleting {
		t.Errorf("body: %+v, a tenant being deleted is shown as Deleting", got)
	}
	if !slices.Equal(e.store.deleted, []string{"demo"}) {
		t.Errorf("deleted = %v", e.store.deleted)
	}
}

// Deleting a tenant destroys its virtual machines: it is for the admins of the tenant, the owner among them (alice), and the
// platform administrators. Everyone else, a key included, is refused.
func TestDeleteIsForTheAdminsOfTheTenantOnly(t *testing.T) {
	e := newEnv(t)
	for token, want := range map[string]int{
		"viewer":   http.StatusForbidden,
		"operator": http.StatusForbidden,
		"svc":      http.StatusForbidden, // it reads the tenant, it does not delete it
		"svc_none": http.StatusNotFound,
		"mallory":  http.StatusNotFound,
		"almostit": http.StatusNotFound,
		"":         http.StatusUnauthorized,
	} {
		if rec := e.do(t, http.MethodDelete, "/v1/tenants/demo", token, ""); rec.Code != want {
			t.Errorf("%q: status %d, want %d", token, rec.Code, want)
		}
	}
	if rec := e.do(t, http.MethodDelete, "/v1/tenants/ghost", "admin", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a tenant that does not exist: %d", rec.Code)
	}
	if len(e.store.deleted) != 0 {
		t.Errorf("deleted by somebody who may not: %v", e.store.deleted)
	}
	if rec := e.do(t, http.MethodDelete, "/v1/tenants/demo", "alice", ""); rec.Code != http.StatusAccepted {
		t.Errorf("the owner: status %d, want 202", rec.Code)
	}
}

func TestWhenTheBackendsAreDownItIs503(t *testing.T) {
	e := newEnv(t)
	e.store.err = errors.New("the cluster does not answer")
	for _, tt := range []struct{ method, path, token, body string }{
		{http.MethodGet, "/v1/tenants", "admin", ""},
		{http.MethodGet, "/v1/tenants", "alice", ""},
		{http.MethodGet, "/v1/tenants/demo", "admin", ""},
		{http.MethodPost, "/v1/tenants", "admin", `{"name":"newone","owner":"a@b"}`},
		{http.MethodDelete, "/v1/tenants/demo", "admin", ""},
	} {
		if rec := e.do(t, tt.method, tt.path, tt.token, tt.body); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s as %s: status %d, want 503", tt.method, tt.path, tt.token, rec.Code)
		}
	}

	e = newEnv(t)
	e.fga.err = errors.New("openfga is down")
	// The platform administrators do not need OpenFGA to see tenants; everybody else does.
	if rec := e.do(t, http.MethodGet, "/v1/tenants", "alice", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list without OpenFGA: %d", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/v1/tenants/demo", "alice", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("get without OpenFGA: %d", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/v1/tenants", "admin", ""); rec.Code != http.StatusOK {
		t.Errorf("an administrator, without OpenFGA: %d", rec.Code)
	}
}

func TestAServiceKeyDoesWhatOpenFGAHoldsForIt(t *testing.T) {
	e := newEnv(t)
	for _, tt := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"reads the tenant it has a tuple on", http.MethodGet, "/v1/tenants/demo", "", http.StatusOK},
		{"does not see another tenant", http.MethodGet, "/v1/tenants/other", "", http.StatusNotFound},
		{"is refused what it has no tuple for", http.MethodGet, "/v1/tenants/demo/images", "", http.StatusForbidden},
		{"lists the tenants it can read", http.MethodGet, "/v1/tenants", "", http.StatusOK},
		{"cannot create a tenant", http.MethodPost, "/v1/tenants", `{"name":"newone","plan":"small"}`, http.StatusForbidden},
		{"cannot change a tenant", http.MethodPatch, "/v1/tenants/demo", `{"plan":"large"}`, http.StatusForbidden},
		{"cannot create a key", http.MethodPost, "/v1/apikeys", `{"displayName":"x","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":["tenants:read"]}]}`, http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if rec := e.do(t, tt.method, tt.path, "svc", tt.body); rec.Code != tt.want {
				t.Errorf("status %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}

	rec := e.do(t, http.MethodGet, "/v1/whoami", "svc", "")
	if got, want := strings.TrimSpace(rec.Body.String()), `{"kind":"servicekey","subject":"apikey:apikey-0123456789abcdef","name":"apikey-0123456789abcdef","platformAdmin":false,"canCreateTenants":false}`; got != want {
		t.Errorf("whoami: %s", got)
	}
}
