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
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/server"
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
	data    map[string]access.ApiKey
	hashes  map[string]string // id -> the hash that was stored
	err     error
	deleted []string
}

func (s *keyStore) List(_ context.Context, owner string) ([]access.ApiKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []access.ApiKey
	for _, k := range s.data {
		if strings.EqualFold(k.Owner, owner) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *keyStore) Get(_ context.Context, id string) (access.ApiKey, error) {
	if s.err != nil {
		return access.ApiKey{}, s.err
	}
	k, ok := s.data[id]
	if !ok {
		return access.ApiKey{}, access.ErrNotFound
	}
	return k, nil
}

func (s *keyStore) Create(_ context.Context, r access.KeyRecord) (access.ApiKey, error) {
	if s.err != nil {
		return access.ApiKey{}, s.err
	}
	if _, ok := s.data[r.ID]; ok {
		return access.ApiKey{}, access.ErrExists
	}
	k := access.ApiKey{ID: r.ID, Owner: r.Owner, DisplayName: r.DisplayName, Scopes: r.Scopes, ExpiresAt: r.ExpiresAt, CreatedAt: now,
		Status: status.Object{NotReadySince: now}}
	s.data[r.ID] = k
	s.hashes[r.ID] = r.Hash
	return k, nil
}

func (s *keyStore) Patch(_ context.Context, id string, r access.PatchKey) (access.ApiKey, error) {
	k, err := s.Get(context.Background(), id)
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
	s.data[id] = k
	return k, nil
}

func (s *keyStore) Delete(_ context.Context, id string) error {
	k, err := s.Get(context.Background(), id)
	if err != nil {
		return err
	}
	k.Status.Deleting = true
	s.data[id] = k
	s.deleted = append(s.deleted, id)
	return nil
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
		{"phk_mgr", 200, 200, 202, 202},
		{"operator", 403, 403, 403, 403}, // an operator manages the instances, not who may
		{"user", 403, 403, 403, 403},
		{"viewer", 403, 403, 403, 403},
		{"phk_ci", 403, 403, 403, 403},
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

const keyID = "0123456789abcdef"

// adminsKey is a key of admin@example.org, who is a platform administrator.
func adminsKey(id string) access.ApiKey {
	return access.ApiKey{ID: id, Owner: "admin@example.org", DisplayName: "CI", Scopes: []string{"tenants:read"}}
}

func TestApiKeysWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/apikeys", "/v1/apikeys/" + keyID
	create := `{"displayName":"new","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z"}`
	tests := []struct {
		who                              string
		list, get, create, patch, delete int
	}{
		// The owner, a platform administrator.
		{"admin", 200, 200, 202, 200, 202},
		// Another administrator: someone else's key is not shown to exist.
		{"admin2", 200, 404, 202, 404, 404},
		// Anyone may look at their own keys and revoke them, so a key can always be revoked; creating and changing is for the
		// administrators for now.
		{"alice", 200, 404, 403, 403, 404},
		{"exadmin", 200, 404, 403, 403, 404},
		// A key never manages keys, whoever it belongs to and whatever it is worth.
		{"phk_admin_write", 403, 403, 403, 403, 403},
		{"phk_mgr", 403, 403, 403, 403, 403},
		{"", 401, 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.keys.data[keyID] = adminsKey(keyID)
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

func TestAUserSeesOnlyTheirOwnKeys(t *testing.T) {
	e := newEnv(t)
	e.keys.data["0000000000000001"] = access.ApiKey{ID: "0000000000000001", Owner: "admin@example.org", DisplayName: "b", Scopes: []string{"tenants:read"}}
	e.keys.data["0000000000000002"] = access.ApiKey{ID: "0000000000000002", Owner: "Admin@Example.org", DisplayName: "a", Scopes: []string{"tenants:read"}}
	e.keys.data["0000000000000003"] = access.ApiKey{ID: "0000000000000003", Owner: "admin2@example.org", DisplayName: "c", Scopes: []string{"tenants:read"}}

	var got struct {
		Items []keyBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/apikeys", "admin", ""), &got)
	var ids []string
	for _, k := range got.Items {
		ids = append(ids, k.ID)
	}
	if !slices.Equal(ids, []string{"0000000000000002", "0000000000000001"}) {
		t.Errorf("admin sees %v: its two keys, sorted by name, and not admin2's", ids)
	}
	if rec := e.do(t, "GET", "/v1/apikeys", "alice", ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("no key is [], not null: %s", rec.Body.String())
	}
}

func TestCreateApiKeyShowsTheKeyOnceAndStoresOnlyItsHash(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/apikeys", "admin", `{"displayName":"CI","scopes":["tenants:read","tenants:write","instances:write"],"expiresAt":"2026-12-31T00:00:00Z"}`)
	if rec.Code != http.StatusAccepted || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, headers %v, %s", rec.Code, rec.Header(), rec.Body.String())
	}
	var created keyBody
	mustDecode(t, rec, &created)
	parsed, err := auth.ParseKey(created.Key)
	if err != nil || parsed.ID != created.ID {
		t.Fatalf("the key %q does not parse, or not to its id %q: %v", created.Key, created.ID, err)
	}
	if rec.Header().Get("Location") != "/v1/apikeys/"+created.ID {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	if e.keys.hashes[created.ID] != auth.HashSecret(parsed.Secret) {
		t.Error("the stored hash is not the hash of the secret of the key")
	}
	if created.Owner != "admin@example.org" || created.ExpiresAt == nil || created.Expired || created.DisplayName != "CI" {
		t.Errorf("body: %+v", created)
	}

	// Never again: not the key, not the hash, not the secret.
	for _, path := range []string{"/v1/apikeys/" + created.ID, "/v1/apikeys"} {
		body := e.do(t, "GET", path, "admin", "").Body.String()
		for _, leak := range []string{created.Key, parsed.Secret, e.keys.hashes[created.ID], `"key"`, "hash"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s shows %q: %s", path, leak, body)
			}
		}
	}
}

// keyReader lets the real verifier read the keys of the fake store.
type keyReader struct{ s *keyStore }

func (r keyReader) GetKey(ctx context.Context, id string) (auth.KeyRecord, error) {
	k, err := r.s.Get(ctx, id)
	if errors.Is(err, access.ErrNotFound) {
		return auth.KeyRecord{}, auth.ErrKeyNotFound
	}
	if err != nil {
		return auth.KeyRecord{}, err
	}
	return auth.KeyRecord{Hash: r.s.hashes[id], Owner: k.Owner, Scopes: k.Scopes, Deleting: k.Status.Deleting}, nil
}

func TestACreatedKeyActsAsItsOwnerWithinItsScopes(t *testing.T) {
	// The whole chain with the real verifier: create a key, present it, see what it can do. The owner is a platform
	// administrator (Authentik says so for admin@example.org, the key carries no group).
	e := newEnv(t)
	var created keyBody
	mustDecode(t, e.do(t, "POST", "/v1/apikeys", "admin", `{"displayName":"CI","scopes":["tenants:read","tenants:write"],"expiresAt":"2026-12-31T00:00:00Z"}`), &created)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := server.New(log)
	Register(s, Deps{
		Settings: testSettings,
		Auth:     &auth.Authenticator{Users: tokens{}, Keys: auth.NewKeyVerifier(keyReader{e.keys}), Log: log},
		Authz:    authz.New(e.fga, adminGroup, directory{adminGroup: {"admin@example.org"}}),
		Tenants:  e.store, Images: e.images, Instances: e.instances, Memberships: e.memberships, Keys: e.keys,
		Log: log, Now: func() time.Time { return now },
	})
	do := func(method, path, token, body string) int {
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"it sees every tenant", "GET", "/v1/tenants", "", http.StatusOK},
		{"it creates one", "POST", "/v1/tenants", `{"name":"fresh","owner":"x@y.fr","plan":"small"}`, http.StatusAccepted},
		{"it reads one it has no role on", "GET", "/v1/tenants/other", "", http.StatusOK},
		// tenants:read and tenants:write say nothing about what is inside a tenant.
		{"it does not list the images", "GET", "/v1/tenants/other/images", "", http.StatusForbidden},
		// A key never manages keys, not even an administrator's.
		{"it does not list keys", "GET", "/v1/apikeys", "", http.StatusForbidden},
		{"it does not create keys", "POST", "/v1/apikeys", `{"displayName":"x","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusForbidden},
	} {
		if got := do(c.method, c.path, created.Key, c.body); got != c.want {
			t.Errorf("%s: %s %s gave %d, want %d", c.name, c.method, c.path, got, c.want)
		}
	}

	// Deleted, it stops at once.
	if code := call(t, e, "DELETE", "/v1/apikeys/"+created.ID, "admin", ""); code != http.StatusAccepted {
		t.Fatalf("delete: %d", code)
	}
	if got := do("GET", "/v1/tenants", created.Key, ""); got != http.StatusUnauthorized {
		t.Errorf("a deleted key gave %d, want 401", got)
	}
	// And a wrong secret never gets in.
	if got := do("GET", "/v1/tenants", auth.FormatKey(created.ID, strings.Repeat("a", 64)), ""); got != http.StatusUnauthorized {
		t.Errorf("a wrong secret gave %d, want 401", got)
	}
}

func TestCreateApiKeyRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	long := now.Add(400 * 24 * time.Hour).Format(time.RFC3339)
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"no display name":              {`{"scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"no scope":                     {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"a scope that does not exist":  {`{"displayName":"ci","scopes":["tenants:delete"],"expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"the former scope name":        {`{"displayName":"ci","scopes":["tenant:read"],"expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"no expiry":                    {`{"displayName":"ci","scopes":["tenants:read"]}`, http.StatusUnprocessableEntity},
		"a date that passed":           {`{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"2020-01-01T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"a date too far":               {`{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"` + long + `"}`, http.StatusUnprocessableEntity},
		"a date that is not one":       {`{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"tomorrow"}`, http.StatusBadRequest},
		"a hash given by the caller":   {`{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z","hash":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
		"an owner given by the caller": {`{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z","owner":"x@y"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/apikeys", "admin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if len(e.keys.data) != 0 {
		t.Errorf("%d keys were created by refused requests", len(e.keys.data))
	}
}

func TestApiKeyPatch(t *testing.T) {
	e := newEnv(t)
	e.keys.data[keyID] = adminsKey(keyID)
	later := now.Add(time.Hour).Format(time.RFC3339)
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"scopes":             {`{"scopes":["tenants:read","images:write"]}`, http.StatusOK},
		"a new date":         {`{"expiresAt":"` + later + `"}`, http.StatusOK},
		"a name":             {`{"displayName":"renamed"}`, http.StatusOK},
		"nothing":            {`{}`, http.StatusUnprocessableEntity},
		"no scope left":      {`{"scopes":[]}`, http.StatusUnprocessableEntity},
		"a date that passed": {`{"expiresAt":"2020-01-01T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"the hash":           {`{"hash":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
		"the owner":          {`{"owner":"x@y"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "PATCH", "/v1/apikeys/"+keyID, "admin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if got := e.keys.data[keyID]; !slices.Equal(got.Scopes, []string{"tenants:read", "images:write"}) || got.DisplayName != "renamed" || got.Owner != "admin@example.org" {
		t.Errorf("key after the patches: %+v", got)
	}
	if code := call(t, e, "PATCH", "/v1/apikeys/not-an-id", "admin", `{"displayName":"x"}`); code != http.StatusNotFound {
		t.Errorf("an id that is not one: %d", code)
	}
}

func TestDeleteApiKey(t *testing.T) {
	e := newEnv(t)
	e.keys.data[keyID] = adminsKey(keyID)
	rec := e.do(t, "DELETE", "/v1/apikeys/"+keyID, "admin", "")
	var got keyBody
	mustDecode(t, rec, &got)
	if rec.Code != http.StatusAccepted || got.Phase != status.Deleting || !slices.Equal(e.keys.deleted, []string{keyID}) {
		t.Errorf("status %d, %+v, deleted %v", rec.Code, got, e.keys.deleted)
	}
	if code := call(t, e, "DELETE", "/v1/apikeys/ffffffffffffffff", "admin", ""); code != http.StatusNotFound {
		t.Errorf("a key that does not exist: %d", code)
	}
	// Someone who is no longer an administrator still revokes their own key.
	e.keys.data["00000000000000e1"] = access.ApiKey{ID: "00000000000000e1", Owner: "exadmin@example.org", DisplayName: "old", Scopes: []string{"tenants:read"}}
	if code := call(t, e, "DELETE", "/v1/apikeys/00000000000000e1", "exadmin", ""); code != http.StatusAccepted {
		t.Errorf("revoking one's own key after leaving the group: %d", code)
	}
}

func TestAKeyPastItsDateIsShownExpiredBeforeTheCompositionCatchesUp(t *testing.T) {
	e := newEnv(t)
	past := now.Add(-time.Minute)
	k := adminsKey(keyID)
	k.ExpiresAt = &past
	e.keys.data[keyID] = k
	var got keyBody
	mustDecode(t, e.do(t, "GET", "/v1/apikeys/"+keyID, "admin", ""), &got)
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
		{"GET", "/v1/apikeys", ""},
		{"POST", "/v1/apikeys", `{"displayName":"ci","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z"}`},
		{"DELETE", "/v1/apikeys/" + keyID, ""},
	} {
		if code := call(t, e, c.method, c.path, "admin", c.body); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", c.method, c.path, code)
		}
	}
}

func TestWhoMayCreateKeysIsADeploymentSetting(t *testing.T) {
	body := `{"displayName":"mine","scopes":["tenants:read"],"expiresAt":"2026-12-31T00:00:00Z"}`
	// As deployed for these tests: the platform administrators only.
	e := newEnv(t)
	if code := call(t, e, "POST", "/v1/apikeys", "alice", body); code != http.StatusForbidden {
		t.Errorf("a person who is not an administrator: %d, want 403", code)
	}
	// Opened by the deployment: any person signed in creates theirs. A key never exceeds its owner, so what it can do is unchanged.
	open := testSettings
	open.KeyCreation = config.KeyCreationUsers
	e = newEnvWith(t, open)
	if code := call(t, e, "POST", "/v1/apikeys", "alice", body); code != http.StatusAccepted {
		t.Errorf("a person, with the creation opened: %d, want 202", code)
	}
	// And a key still cannot create one.
	if code := call(t, e, "POST", "/v1/apikeys", "phk_ci", body); code != http.StatusForbidden {
		t.Errorf("a key, with the creation opened: %d, want 403", code)
	}
}

func TestTheKeyLifetimeIsADeploymentSetting(t *testing.T) {
	far := now.Add(5 * 365 * 24 * time.Hour).Format(time.RFC3339)
	body := `{"displayName":"long","scopes":["tenants:read"],"expiresAt":"` + far + `"}`
	if code := call(t, newEnv(t), "POST", "/v1/apikeys", "admin", body); code != http.StatusUnprocessableEntity {
		t.Errorf("five years with a maximum of %s: %d, want 422", testSettings.KeyMaxLifetime, code)
	}
	none := testSettings
	none.KeyMaxLifetime = 0
	e := newEnvWith(t, none)
	if code := call(t, e, "POST", "/v1/apikeys", "admin", body); code != http.StatusAccepted {
		t.Errorf("five years with no maximum: %d, want 202", code)
	}
	if code := call(t, e, "POST", "/v1/apikeys", "admin", `{"displayName":"forever","scopes":["tenants:read"]}`); code != http.StatusAccepted {
		t.Errorf("no expiry with no maximum: %d, want 202", code)
	}
}
