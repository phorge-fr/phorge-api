package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/status"
)

type membershipStore struct {
	data    map[string]map[string]access.Membership
	err     error
	deleted []string
}

func (s *membershipStore) List(_ context.Context, tenant string) ([]access.Membership, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []access.Membership
	for _, m := range s.data[tenant] {
		out = append(out, m)
	}
	return out, nil
}

func (s *membershipStore) Get(_ context.Context, tenant, name string) (access.Membership, error) {
	if s.err != nil {
		return access.Membership{}, s.err
	}
	m, ok := s.data[tenant][name]
	if !ok {
		return access.Membership{}, access.ErrNotFound
	}
	return m, nil
}

func (s *membershipStore) Create(_ context.Context, tenant string, r access.CreateMembership) (access.Membership, error) {
	if s.err != nil {
		return access.Membership{}, s.err
	}
	name := access.MembershipName(r.Subject)
	if _, ok := s.data[tenant][name]; ok {
		return access.Membership{}, access.ErrExists
	}
	m := access.Membership{Name: name, Subject: r.Subject, Role: r.Role, CreatedBy: r.CreatedBy, CreatedAt: now, Status: status.Object{NotReadySince: now}}
	s.put(tenant, m)
	return m, nil
}

func (s *membershipStore) Delete(_ context.Context, tenant, name string) error {
	m, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return err
	}
	m.Status.Deleting = true
	s.put(tenant, m)
	s.deleted = append(s.deleted, tenant+"/"+name)
	return nil
}

func (s *membershipStore) put(tenant string, m access.Membership) {
	if s.data[tenant] == nil {
		s.data[tenant] = map[string]access.Membership{}
	}
	s.data[tenant][m.Name] = m
}

type keyStore struct {
	data    map[string]map[string]access.ApiKey
	hashes  map[string]string // tenant/name -> the hash that was stored
	err     error
	deleted []string
}

func (s *keyStore) List(_ context.Context, tenant string) ([]access.ApiKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []access.ApiKey
	for _, k := range s.data[tenant] {
		out = append(out, k)
	}
	return out, nil
}

func (s *keyStore) Get(_ context.Context, tenant, name string) (access.ApiKey, error) {
	if s.err != nil {
		return access.ApiKey{}, s.err
	}
	k, ok := s.data[tenant][name]
	if !ok {
		return access.ApiKey{}, access.ErrNotFound
	}
	return k, nil
}

func (s *keyStore) Create(_ context.Context, tenant string, r access.KeyRecord) (access.ApiKey, error) {
	if s.err != nil {
		return access.ApiKey{}, s.err
	}
	if _, ok := s.data[tenant][r.Name]; ok {
		return access.ApiKey{}, access.ErrExists
	}
	k := access.ApiKey{Name: r.Name, DisplayName: r.DisplayName, Scopes: r.Scopes, ExpiresAt: r.ExpiresAt, CreatedBy: r.CreatedBy, CreatedAt: now,
		Status: status.Object{NotReadySince: now}}
	s.put(tenant, k)
	s.hashes[tenant+"/"+r.Name] = r.Hash
	return k, nil
}

func (s *keyStore) Patch(_ context.Context, tenant, name string, r access.PatchKey) (access.ApiKey, error) {
	k, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return k, err
	}
	if r.Scopes != nil {
		k.Scopes = *r.Scopes
	}
	if r.ExpiresAt != nil {
		k.ExpiresAt = r.ExpiresAt
	}
	if r.DisplayName != nil {
		k.DisplayName = *r.DisplayName
	}
	s.put(tenant, k)
	return k, nil
}

func (s *keyStore) Delete(_ context.Context, tenant, name string) error {
	k, err := s.Get(context.Background(), tenant, name)
	if err != nil {
		return err
	}
	k.Status.Deleting = true
	s.put(tenant, k)
	s.deleted = append(s.deleted, tenant+"/"+name)
	return nil
}

func (s *keyStore) put(tenant string, k access.ApiKey) {
	if s.data[tenant] == nil {
		s.data[tenant] = map[string]access.ApiKey{}
	}
	s.data[tenant][k.Name] = k
}

var bob = access.Subject{Kind: "User", Name: "bob@example.org"}

func TestMembershipsWhoMayDoWhat(t *testing.T) {
	const coll = "/v1/tenants/demo/memberships"
	item := coll + "/" + access.MembershipName(bob)
	create := `{"subject":{"kind":"User","name":"carol@example.org"},"role":"viewer"}`
	tests := []struct {
		who                       string
		list, get, create, delete int
	}{
		{"tadmin", 200, 200, 202, 202},
		{"admin", 200, 200, 202, 202},
		{"phk_demo_mgr", 200, 200, 202, 202},
		{"operator", 403, 403, 403, 403}, // an operator manages the instances, not who may
		{"user", 403, 403, 403, 403},
		{"viewer", 403, 403, 403, 403},
		{"phk_demo_ci", 403, 403, 403, 403},
		{"mallory", 404, 404, 404, 404},
		{"", 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.memberships.put("demo", access.Membership{Name: access.MembershipName(bob), Subject: bob, Role: access.Viewer})
			for _, c := range []struct {
				method, path, body string
				want               int
			}{
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, create, tt.create}, {"DELETE", item, "", tt.delete},
			} {
				if got := call(t, e, c.method, c.path, tt.who, c.body); got != c.want {
					t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
				}
			}
		})
	}
}

func TestCreateMembership(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/memberships", "tadmin", `{"subject":{"kind":"User","name":"Bob@Example.org"},"role":"operator"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got membershipBody
	mustDecode(t, rec, &got)
	if got.Subject.Name != "Bob@Example.org" || got.Role != access.Operator || got.CreatedBy != "user:tadmin@example.org" ||
		rec.Header().Get("Location") != "/v1/tenants/demo/memberships/"+got.Name {
		t.Errorf("body %+v, Location %q", got, rec.Header().Get("Location"))
	}

	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"the same user again, with another role": {`{"subject":{"kind":"User","name":"Bob@Example.org"},"role":"admin"}`, http.StatusConflict},
		"the owner of the tenant":                {`{"subject":{"kind":"User","name":"alice@example.org"},"role":"admin"}`, http.StatusConflict},
		"a group":                                {`{"subject":{"kind":"Group","name":"staff"},"role":"viewer"}`, http.StatusUnprocessableEntity},
		"a name that is not an email":            {`{"subject":{"kind":"User","name":"bob"},"role":"viewer"}`, http.StatusUnprocessableEntity},
		"a role that does not exist":             {`{"subject":{"kind":"User","name":"x@y"},"role":"root"}`, http.StatusUnprocessableEntity},
		"an unknown field":                       {`{"subject":{"kind":"User","name":"x@y"},"role":"viewer","tenant":"other"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/memberships", "tadmin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if rec := e.do(t, "POST", "/v1/tenants/demo/memberships", "tadmin", `{"subject":{"kind":"User","name":"alice@example.org"},"role":"viewer"}`); !strings.Contains(rec.Body.String(), "owner_is_admin") {
		t.Errorf("the owner: %s", rec.Body.String())
	}
	if len(e.memberships.data["demo"]) != 1 {
		t.Errorf("%d memberships, want only the first", len(e.memberships.data["demo"]))
	}
}

func TestMembershipOfAnotherTenantIsNotReachable(t *testing.T) {
	e := newEnv(t)
	e.memberships.put("other", access.Membership{Name: access.MembershipName(bob), Subject: bob, Role: access.Admin})
	if code := call(t, e, "GET", "/v1/tenants/demo/memberships/"+access.MembershipName(bob), "tadmin", ""); code != http.StatusNotFound {
		t.Errorf("a membership of another tenant, read through this one: %d", code)
	}
	if code := call(t, e, "GET", "/v1/tenants/demo/memberships/not-a-name", "tadmin", ""); code != http.StatusNotFound {
		t.Errorf("a name that is not one: %d", code)
	}
}

func TestDeleteMembership(t *testing.T) {
	e := newEnv(t)
	e.memberships.put("demo", access.Membership{Name: access.MembershipName(bob), Subject: bob, Role: access.Viewer})
	rec := e.do(t, "DELETE", "/v1/tenants/demo/memberships/"+access.MembershipName(bob), "tadmin", "")
	var got membershipBody
	mustDecode(t, rec, &got)
	if rec.Code != http.StatusAccepted || got.Phase != status.Deleting || len(e.memberships.deleted) != 1 {
		t.Errorf("status %d, %+v", rec.Code, got)
	}
	if code := call(t, e, "DELETE", "/v1/tenants/demo/memberships/"+access.MembershipName(access.Subject{Kind: "User", Name: "ghost@x"}), "tadmin", ""); code != http.StatusNotFound {
		t.Errorf("a membership that does not exist: %d", code)
	}
}

func TestApiKeysWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/tenants/demo/apikeys", "/v1/tenants/demo/apikeys/ci"
	create := `{"name":"new","scopes":["tenant:read"]}`
	tests := []struct {
		who                              string
		list, get, create, patch, delete int
	}{
		{"tadmin", 200, 200, 202, 200, 202},
		{"admin", 200, 200, 202, 200, 202},
		{"phk_demo_mgr", 200, 200, 202, 200, 202},
		{"operator", 403, 403, 403, 403, 403},
		{"viewer", 403, 403, 403, 403, 403},
		{"phk_demo_ci", 403, 403, 403, 403, 403},
		{"mallory", 404, 404, 404, 404, 404},
		{"", 401, 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.keys.put("demo", access.ApiKey{Name: "ci", Scopes: []string{"tenant:read"}})
			for _, c := range []struct {
				method, path, body string
				want               int
			}{
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, create, tt.create},
				{"PATCH", item, `{"displayName":"CI"}`, tt.patch}, {"DELETE", item, "", tt.delete},
			} {
				if got := call(t, e, c.method, c.path, tt.who, c.body); got != c.want {
					t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
				}
			}
		})
	}
}

func TestCreateApiKeyShowsTheKeyOnceAndStoresOnlyItsHash(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/apikeys", "tadmin", `{"name":"ci","displayName":"CI","scopes":["tenant:read","instances:write"],"expiresAt":"2026-12-31T00:00:00Z"}`)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Location") != "/v1/tenants/demo/apikeys/ci" {
		t.Fatalf("status %d, headers %v, %s", rec.Code, rec.Header(), rec.Body.String())
	}
	var created keyBody
	mustDecode(t, rec, &created)
	parsed, err := auth.ParseKey(created.Key)
	if err != nil || parsed.Tenant != "demo" || parsed.Name != "ci" {
		t.Fatalf("the key %q does not parse: %v", created.Key, err)
	}
	if e.keys.hashes["demo/ci"] != auth.HashSecret(parsed.Secret) {
		t.Error("the stored hash is not the hash of the secret of the key")
	}
	if created.CreatedBy != "user:tadmin@example.org" || created.ExpiresAt == nil || created.Expired {
		t.Errorf("body: %+v", created)
	}

	// Never again: not the key, not the hash, not the secret.
	for _, path := range []string{"/v1/tenants/demo/apikeys/ci", "/v1/tenants/demo/apikeys"} {
		body := e.do(t, "GET", path, "tadmin", "").Body.String()
		for _, leak := range []string{created.Key, parsed.Secret, e.keys.hashes["demo/ci"], `"key"`, "hash"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s shows %q: %s", path, leak, body)
			}
		}
	}
}

func TestAKeyNeverExceedsItsCreator(t *testing.T) {
	e := newEnv(t)
	n := 0
	post := func(who, scopes string) *httpResult {
		n++
		rec := e.do(t, "POST", "/v1/tenants/demo/apikeys", who, `{"name":"k`+strings.Repeat("x", n)+`","scopes":[`+scopes+`]}`)
		return &httpResult{rec.Code, rec.Body.String()}
	}
	// The key mgr holds tenant:read, instances:read and the memberships scopes. Nothing else.
	if r := post("phk_demo_mgr", `"instances:read","tenant:read"`); r.code != http.StatusAccepted {
		t.Errorf("a subset of its scopes: %d %s", r.code, r.body)
	}
	e.keys.data = map[string]map[string]access.ApiKey{}
	r := post("phk_demo_mgr", `"instances:write","images:write","tenant:read"`)
	if r.code != http.StatusForbidden || !strings.Contains(r.body, "images:write, instances:write") || strings.Contains(r.body, "tenant:read,") {
		t.Errorf("beyond its scopes: %d %s: the ones it lacks are named", r.code, r.body)
	}
	if len(e.keys.data["demo"]) != 0 {
		t.Error("a key was created that exceeds its creator")
	}
	// A member who may write memberships holds what an admin holds.
	if r := post("tadmin", strings.Join([]string{`"tenant:read"`, `"instances:read"`, `"instances:write"`, `"images:read"`, `"images:write"`, `"memberships:read"`, `"memberships:write"`}, ",")); r.code != http.StatusAccepted {
		t.Errorf("an admin: %d %s", r.code, r.body)
	}
	// A platform administrator holds everything on every tenant.
	e.keys.data = map[string]map[string]access.ApiKey{}
	if r := post("admin", `"instances:write","memberships:write"`); r.code != http.StatusAccepted {
		t.Errorf("a platform administrator: %d %s", r.code, r.body)
	}
}

type httpResult struct {
	code int
	body string
}

func TestApiKeyPatch(t *testing.T) {
	e := newEnv(t)
	e.keys.put("demo", access.ApiKey{Name: "ci", Scopes: []string{"tenant:read", "instances:read"}})

	// mgr may take scopes away, and may not add one it does not hold.
	if code := call(t, e, "PATCH", "/v1/tenants/demo/apikeys/ci", "phk_demo_mgr", `{"scopes":["tenant:read"]}`); code != http.StatusOK {
		t.Errorf("taking a scope away: %d", code)
	}
	if code := call(t, e, "PATCH", "/v1/tenants/demo/apikeys/ci", "phk_demo_mgr", `{"scopes":["tenant:read","images:write"]}`); code != http.StatusForbidden {
		t.Errorf("adding a scope it does not hold: %d", code)
	}
	if got := e.keys.data["demo"]["ci"].Scopes; !slices.Equal(got, []string{"tenant:read"}) {
		t.Errorf("scopes = %v", got)
	}
	if code := call(t, e, "PATCH", "/v1/tenants/demo/apikeys/ci", "tadmin", `{"scopes":["tenant:read","images:write"]}`); code != http.StatusOK {
		t.Errorf("an admin adding one: %d", code)
	}
	later := now.Add(time.Hour).Format(time.RFC3339)
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"nothing":            {`{}`, http.StatusUnprocessableEntity},
		"no scope left":      {`{"scopes":[]}`, http.StatusUnprocessableEntity},
		"a date that passed": {`{"expiresAt":"2020-01-01T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"the hash":           {`{"hash":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
		"the name":           {`{"name":"x"}`, http.StatusBadRequest},
		"a new date":         {`{"expiresAt":"` + later + `"}`, http.StatusOK},
	} {
		if code := call(t, e, "PATCH", "/v1/tenants/demo/apikeys/ci", "tadmin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
}

func TestCreateApiKeyRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	e.keys.put("demo", access.ApiKey{Name: "taken", Scopes: []string{"tenant:read"}})
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"a name that exists":          {`{"name":"taken","scopes":["tenant:read"]}`, http.StatusConflict},
		"an underscore in a name":     {`{"name":"ci_1","scopes":["tenant:read"]}`, http.StatusUnprocessableEntity},
		"no scope":                    {`{"name":"ci"}`, http.StatusUnprocessableEntity},
		"a scope that does not exist": {`{"name":"ci","scopes":["tenant:delete"]}`, http.StatusUnprocessableEntity},
		"a date that passed":          {`{"name":"ci","scopes":["tenant:read"],"expiresAt":"2020-01-01T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"a date that is not one":      {`{"name":"ci","scopes":["tenant:read"],"expiresAt":"tomorrow"}`, http.StatusBadRequest},
		"a hash given by the caller":  {`{"name":"ci","scopes":["tenant:read"],"hash":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/apikeys", "tadmin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
}

func TestAKeyPastItsDateIsShownExpiredBeforeTheCompositionCatchesUp(t *testing.T) {
	e := newEnv(t)
	past := now.Add(-time.Minute)
	e.keys.put("demo", access.ApiKey{Name: "old", Scopes: []string{"tenant:read"}, ExpiresAt: &past})
	var got keyBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/apikeys/old", "tadmin", ""), &got)
	if !got.Expired {
		t.Errorf("%+v: the date has passed, the composition has not noticed yet", got)
	}
}

func TestAccessRoutesWhenTheClusterIsDown(t *testing.T) {
	e := newEnv(t)
	e.memberships.err = errors.New("no cluster")
	e.keys.err = errors.New("no cluster")
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/tenants/demo/memberships", ""},
		{"POST", "/v1/tenants/demo/memberships", `{"subject":{"kind":"User","name":"x@y"},"role":"viewer"}`},
		{"GET", "/v1/tenants/demo/apikeys", ""},
		{"POST", "/v1/tenants/demo/apikeys", `{"name":"ci","scopes":["tenant:read"]}`},
		{"DELETE", "/v1/tenants/demo/apikeys/ci", ""},
	} {
		if code := call(t, e, c.method, c.path, "tadmin", c.body); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", c.method, c.path, code)
		}
	}
}
