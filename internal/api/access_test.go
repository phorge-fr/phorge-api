package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/access"
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

// keyStore is the cluster of the keys: the ApiKey objects, and the Secret that Crossplane fills with the key of each (secrets,
// by id). Creating a key makes its Secret at once, unless noKey says Crossplane never does.
type keyStore struct {
	data    map[string]access.ApiKey
	secrets map[string]string
	noKey   bool
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
	k := access.ApiKey{ID: r.ID, Owner: r.Owner, DisplayName: r.DisplayName, Grants: r.Grants, ExpiresAt: r.ExpiresAt, CreatedAt: now,
		Identity: access.AccountPrefix + r.ID, Status: status.Object{NotReadySince: now}}
	s.data[r.ID] = k
	if !s.noKey {
		s.secrets[r.ID] = "key-of-" + r.ID
	}
	return k, nil
}

func (s *keyStore) Patch(_ context.Context, id string, r access.PatchKey) (access.ApiKey, error) {
	k, err := s.Get(context.Background(), id)
	if err != nil {
		return k, err
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
	delete(s.secrets, id)
	return nil
}

func (s *keyStore) TakeKey(_ context.Context, id string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	key, ok := s.secrets[id]
	if !ok {
		return "", access.ErrKeyNotIssued
	}
	delete(s.secrets, id)
	return key, nil
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
		{"svc_mgr", 200, 200, 202, 202},
		{"operator", 403, 403, 403, 403}, // an operator manages the instances, not who may
		{"user", 403, 403, 403, 403},
		{"viewer", 403, 403, 403, 403},
		{"svc_images", 403, 403, 403, 403},
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
	later := now.Add(time.Hour)
	return access.ApiKey{ID: id, Owner: "admin@example.org", DisplayName: "CI", Grants: []access.Grant{{Tenant: "demo", Scopes: []string{"tenants:read"}}}, ExpiresAt: &later}
}

const newKey = `{"displayName":"new","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":["tenants:read"]}]}`

func TestApiKeysWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/apikeys", "/v1/apikeys/" + keyID
	tests := []struct {
		who                              string
		list, get, create, patch, delete int
	}{
		// The owner of the key.
		{"admin", 200, 200, 201, 200, 202},
		// Someone else's key is not shown to exist. Anyone creates keys within what they hold themselves: admin2 holds nothing on demo,
		// alice reads it.
		{"admin2", 200, 404, 403, 404, 404},
		{"alice", 200, 404, 201, 404, 404},
		{"exadmin", 200, 404, 403, 404, 404},
		// A key never manages keys, whoever it belongs to and whatever it holds.
		{"svc_mgr", 403, 403, 403, 403, 403},
		{"svc", 403, 403, 403, 403, 403},
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
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, newKey, tt.create},
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
	e.keys.data["0000000000000001"] = access.ApiKey{ID: "0000000000000001", Owner: "admin@example.org", DisplayName: "b"}
	e.keys.data["0000000000000002"] = access.ApiKey{ID: "0000000000000002", Owner: "Admin@Example.org", DisplayName: "a"}
	e.keys.data["0000000000000003"] = access.ApiKey{ID: "0000000000000003", Owner: "admin2@example.org", DisplayName: "c"}

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

func TestCreateApiKeyHandsOverTheKeyOnce(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/apikeys", "admin", `{"displayName":"CI","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":["tenants:read","instances:write"]}]}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, headers %v, %s", rec.Code, rec.Header(), rec.Body.String())
	}
	var created keyBody
	mustDecode(t, rec, &created)
	if !access.ValidKeyName(created.ID) || created.Key != "key-of-"+created.ID {
		t.Errorf("id %q, key %q: the key is the one that was in the Secret", created.ID, created.Key)
	}
	if created.Identity != "apikey-"+created.ID {
		t.Errorf("identity = %q: it is what the key is exchanged with", created.Identity)
	}
	if rec.Header().Get("Location") != "/v1/apikeys/"+created.ID {
		t.Errorf("Location = %q", rec.Header().Get("Location"))
	}
	if len(created.Grants) != 1 || created.Grants[0].Tenant != "demo" || len(created.Grants[0].Scopes) != 2 {
		t.Errorf("grants = %+v", created.Grants)
	}
	stored := e.keys.data[created.ID]
	if stored.Owner != "admin@example.org" || stored.DisplayName != "CI" || stored.ExpiresAt == nil {
		t.Errorf("stored key: %+v: the owner is the caller", stored)
	}
	if len(e.keys.secrets) != 0 {
		t.Errorf("the Secret is still in the cluster: %v", e.keys.secrets)
	}
	// Never again: not by id, not in the list.
	for _, path := range []string{"/v1/apikeys/" + created.ID, "/v1/apikeys"} {
		body := e.do(t, "GET", path, "admin", "").Body.String()
		for _, leak := range []string{created.Key, `"key"`} {
			if strings.Contains(body, leak) {
				t.Errorf("GET %s shows %q", path, leak)
			}
		}
	}
}

func TestAKeyNeverGetsWhatItsCreatorDoesNotHold(t *testing.T) {
	e := newEnv(t)
	grant := func(tenant string, scopes ...string) string {
		list, _ := json.Marshal(scopes)
		return `{"displayName":"mine","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"` + tenant + `","scopes":` + string(list) + `}]}`
	}
	for name, tt := range map[string]struct {
		token, body string
		want        int
	}{
		"alice reads demo, and holds that":                       {"alice", grant("demo", "tenants:read"), http.StatusCreated},
		"alice writes instances, which she does not hold":        {"alice", grant("demo", "instances:write"), http.StatusForbidden},
		"alice on a tenant she does not see":                     {"alice", grant("other", "tenants:read"), http.StatusForbidden},
		"alice on a tenant that does not exist (same answer)":    {"alice", grant("ghost", "tenants:read"), http.StatusForbidden},
		"the operator writes instances and images, and holds it": {"operator", grant("demo", "instances:write", "images:write"), http.StatusCreated},
		"the operator manages memberships, which is not hers":    {"operator", grant("demo", "memberships:write"), http.StatusForbidden},
		"admin holds memberships on the other tenant too":        {"admin", grant("other", "memberships:write"), http.StatusCreated},
		"admin, on a tenant that does not exist":                 {"admin", grant("ghost", "tenants:read"), http.StatusForbidden},
		// The scopes of the networks and of the tenant: the same rule, the role of the creator decides.
		"the operator writes networks, as she may":           {"operator", grant("demo", "networks:read", "networks:write"), http.StatusCreated},
		"alice reads networks, as any role does":             {"alice", grant("demo", "networks:read"), http.StatusCreated},
		"alice writes networks, which she does not hold":     {"alice", grant("demo", "networks:write"), http.StatusForbidden},
		"the owner of a tenant deletes it, so a key may":     {"tadmin", grant("demo", "tenants:write"), http.StatusCreated},
		"the operator deletes the tenant, which is not hers": {"operator", grant("demo", "tenants:write"), http.StatusForbidden},
	} {
		if code := call(t, e, "POST", "/v1/apikeys", tt.token, tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if len(e.keys.data) != 6 {
		t.Errorf("%d keys exist, want only the 6 that were allowed", len(e.keys.data))
	}
}

func TestCreateApiKeyRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	long := now.Add(400 * 24 * time.Hour).Format(time.RFC3339)
	const g = `"grants":[{"tenant":"demo","scopes":["tenants:read"]}]`
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"no display name":               {`{"expiresAt":"2026-12-31T00:00:00Z",` + g + `}`, http.StatusUnprocessableEntity},
		"no grant":                      {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z"}`, http.StatusUnprocessableEntity},
		"no scope":                      {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":[]}]}`, http.StatusUnprocessableEntity},
		"a scope that does not exist":   {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":["tenants:delete"]}]}`, http.StatusUnprocessableEntity},
		"tenants:create, no key has it": {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"demo","scopes":["tenants:create"]}]}`, http.StatusUnprocessableEntity},
		"a tenant that is not a name":   {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z","grants":[{"tenant":"Demo#admin","scopes":["tenants:read"]}]}`, http.StatusUnprocessableEntity},
		"no expiry, a key expires":      {`{"displayName":"ci",` + g + `}`, http.StatusUnprocessableEntity},
		"a date that passed":            {`{"displayName":"ci","expiresAt":"2020-01-01T00:00:00Z",` + g + `}`, http.StatusUnprocessableEntity},
		"a date too far":                {`{"displayName":"ci","expiresAt":"` + long + `",` + g + `}`, http.StatusUnprocessableEntity},
		"a date that is not one":        {`{"displayName":"ci","expiresAt":"tomorrow",` + g + `}`, http.StatusBadRequest},
		"the former scopes field":       {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z","scopes":["tenants:read"],` + g + `}`, http.StatusBadRequest},
		"a hash given by the caller":    {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z",` + g + `,"hash":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest},
		"an owner given by the caller":  {`{"displayName":"ci","expiresAt":"2026-12-31T00:00:00Z",` + g + `,"owner":"x@y"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "POST", "/v1/apikeys", "admin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	if len(e.keys.data) != 0 {
		t.Errorf("%d keys were created by refused requests", len(e.keys.data))
	}
}

func TestAKeyThatCannotBeIssuedLeavesNothing(t *testing.T) {
	quick := testSettings
	quick.KeyIssueTimeout, quick.KeyPollInterval = 40*time.Millisecond, 5*time.Millisecond
	e := newEnvWith(t, quick)
	e.keys.noKey = true // Crossplane never makes the account
	rec := e.do(t, "POST", "/v1/apikeys", "admin", newKey)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(e.keys.deleted) != 1 || !e.keys.data[e.keys.deleted[0]].Status.Deleting {
		t.Errorf("deleted = %v: a key that cannot be handed over is removed, it could not be shown later", e.keys.deleted)
	}
	if strings.Contains(rec.Body.String(), "key-of-") {
		t.Errorf("the answer shows a key: %s", rec.Body.String())
	}
}

func TestApiKeyPatch(t *testing.T) {
	e := newEnv(t)
	e.keys.data[keyID] = adminsKey(keyID)
	for name, tt := range map[string]struct {
		body string
		want int
	}{
		"a name":         {`{"displayName":"renamed"}`, http.StatusOK},
		"nothing":        {`{}`, http.StatusUnprocessableEntity},
		"an empty name":  {`{"displayName":""}`, http.StatusUnprocessableEntity},
		"the grants":     {`{"grants":[{"tenant":"other","scopes":["tenants:read"]}]}`, http.StatusBadRequest},
		"the old scopes": {`{"scopes":["tenants:read"]}`, http.StatusBadRequest},
		"the date":       {`{"expiresAt":"2026-12-30T00:00:00Z"}`, http.StatusBadRequest},
		"the owner":      {`{"owner":"x@y"}`, http.StatusBadRequest},
	} {
		if code := call(t, e, "PATCH", "/v1/apikeys/"+keyID, "admin", tt.body); code != tt.want {
			t.Errorf("%s: status %d, want %d", name, code, tt.want)
		}
	}
	got := e.keys.data[keyID]
	if got.DisplayName != "renamed" || got.Owner != "admin@example.org" || len(got.Grants) != 1 || got.Grants[0].Tenant != "demo" {
		t.Errorf("key after the patches: %+v: only the name changes", got)
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
	e.keys.data["00000000000000e1"] = access.ApiKey{ID: "00000000000000e1", Owner: "exadmin@example.org", DisplayName: "old"}
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
		{"POST", "/v1/apikeys", newKey},
		{"DELETE", "/v1/apikeys/" + keyID, ""},
	} {
		if code := call(t, e, c.method, c.path, "admin", c.body); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status %d, want 503", c.method, c.path, code)
		}
	}
}

func TestAnyPersonCreatesKeysAndNoKeyDoes(t *testing.T) {
	e := newEnv(t)
	for _, who := range []string{"alice", "mallory", "viewer"} {
		want := http.StatusCreated
		body := newKey
		if who == "mallory" {
			// mallory holds nothing on demo: the rights of the creator decide, not a group.
			want = http.StatusForbidden
		}
		if code := call(t, e, "POST", "/v1/apikeys", who, body); code != want {
			t.Errorf("%s: %d, want %d", who, code, want)
		}
	}
	// And a key still cannot create one.
	if code := call(t, e, "POST", "/v1/apikeys", "svc", newKey); code != http.StatusForbidden {
		t.Errorf("a key: %d, want 403", code)
	}
}

func TestTheKeyLifetimeIsADeploymentSetting(t *testing.T) {
	far := now.Add(5 * 365 * 24 * time.Hour).Format(time.RFC3339)
	body := `{"displayName":"long","expiresAt":"` + far + `","grants":[{"tenant":"demo","scopes":["tenants:read"]}]}`
	if code := call(t, newEnv(t), "POST", "/v1/apikeys", "admin", body); code != http.StatusUnprocessableEntity {
		t.Errorf("five years with a maximum of %s: %d, want 422", testSettings.KeyMaxLifetime, code)
	}
	none := testSettings
	none.KeyMaxLifetime = 0
	e := newEnvWith(t, none)
	if code := call(t, e, "POST", "/v1/apikeys", "admin", body); code != http.StatusCreated {
		t.Errorf("five years with no maximum: %d, want 201", code)
	}
	// A key always expires: OpenFGA refuses it from that date, whatever the deployment says.
	if code := call(t, e, "POST", "/v1/apikeys", "admin", `{"displayName":"forever","grants":[{"tenant":"demo","scopes":["tenants:read"]}]}`); code != http.StatusUnprocessableEntity {
		t.Errorf("no expiry with no maximum: %d, want 422", code)
	}
}

func TestTheScopesOfAKeyAreListed(t *testing.T) {
	e := newEnv(t)
	var body struct {
		Items []scopeBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/apikeys/scopes", "alice", ""), &body)
	var got []string
	for _, s := range body.Items {
		got = append(got, s.Scope)
		if s.Description == "" || !strings.Contains(s.Scope, ":") {
			t.Errorf("%+v: a scope is <resource>:<verb> and says what it does", s)
		}
	}
	want := "images:read images:write instances:read instances:write memberships:read memberships:write networks:read networks:write profiles:read profiles:write tenants:read tenants:write"
	if strings.Join(got, " ") != want {
		t.Errorf("scopes = %v, want %s", got, want)
	}
	// It is part of managing keys: for a person, never for a key, and never without a credential.
	for who, code := range map[string]int{"svc": http.StatusForbidden, "": http.StatusUnauthorized} {
		if got := call(t, e, "GET", "/v1/apikeys/scopes", who, ""); got != code {
			t.Errorf("%q: status %d, want %d", who, got, code)
		}
	}
	// and the path of a key called "scopes" is not this list
	if got := call(t, e, "GET", "/v1/apikeys/scopes", "alice", ""); got != http.StatusOK {
		t.Errorf("status %d", got)
	}
}
