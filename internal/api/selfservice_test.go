package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/config"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// usersMay is the deployment that lets any person create a tenant for themselves, three at most.
func usersMay() Settings {
	s := testSettings
	s.TenantCreation, s.MaxTenantsPerUser = config.TenantCreationUsers, 3
	return s
}

func TestAPersonCreatesATenantForThemselves(t *testing.T) {
	e := newEnvWith(t, usersMay())
	rec := e.do(t, "POST", "/v1/tenants", "mallory", `{"name":"mine","displayName":"Mine"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body tenantBody
	mustDecode(t, rec, &body)
	if body.Owner != "mallory@example.org" || body.Plan != tenants.Small || body.CreatedBy != "user:mallory@example.org" {
		t.Errorf("the owner is the caller and the plan the smallest: %+v", body)
	}
	if got := e.store.tenants["mine"]; got.Owner != "mallory@example.org" || got.Plan != tenants.Small {
		t.Errorf("the store received %+v", got)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/tenants/mine" {
		t.Errorf("Location = %q", loc)
	}
}

func TestAPersonCannotCreateATenantForSomeoneElseOrWithABiggerPlan(t *testing.T) {
	e := newEnvWith(t, usersMay())
	for name, tt := range map[string]struct{ body, want string }{
		"for someone else": {`{"name":"x1","owner":"bob@example.org"}`, "only create a tenant for yourself"},
		"a bigger plan":    {`{"name":"x2","plan":"large"}`, "plan other than small"},
		"a medium plan":    {`{"name":"x3","plan":"medium"}`, "plan other than small"},
	} {
		rec := e.do(t, "POST", "/v1/tenants", "mallory", tt.body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), tt.want) {
			t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body)
		}
	}
	for _, name := range []string{"x1", "x2", "x3"} {
		if _, ok := e.store.tenants[name]; ok {
			t.Errorf("%s was created", name)
		}
	}
	// their own address, in another case, is theirs; the small plan is what they get anyway
	if code := call(t, e, "POST", "/v1/tenants", "mallory", `{"name":"x4","owner":"Mallory@Example.org","plan":"small"}`); code != http.StatusAccepted {
		t.Errorf("their own address: status %d", code)
	}
}

func TestAPersonOwnsAtMostSomeTenants(t *testing.T) {
	e := newEnvWith(t, usersMay())
	for _, n := range []string{"a1", "a2", "a3"} {
		if code := call(t, e, "POST", "/v1/tenants", "mallory", `{"name":"`+n+`"}`); code != http.StatusAccepted {
			t.Fatalf("%s: status %d", n, code)
		}
	}
	rec := e.do(t, "POST", "/v1/tenants", "mallory", `{"name":"a4"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"quota_exceeded"`) || !strings.Contains(rec.Body.String(), "3 out of 3") {
		t.Errorf("the 4th: status %d: %s", rec.Code, rec.Body)
	}
	if _, ok := e.store.tenants["a4"]; ok {
		t.Error("the 4th was created")
	}
	// another person is not counted with them, and a tenant made for them by an administrator counts for them
	if code := call(t, e, "POST", "/v1/tenants", "viewer", `{"name":"v1"}`); code != http.StatusAccepted {
		t.Errorf("another person: status %d", code)
	}
	e.store.tenants["gift"] = tenants.Tenant{Name: "gift", Owner: "MALLORY@example.org", Plan: tenants.Small}
	e.store.tenants["a3"] = tenants.Tenant{Name: "a3-gone"}
	delete(e.store.tenants, "a3")
	if code := call(t, e, "POST", "/v1/tenants", "mallory", `{"name":"a5"}`); code != http.StatusConflict {
		t.Errorf("a tenant given by an administrator counts, whatever the case of the address: status %d, want 409", code)
	}
	// 0 is no limit
	s := usersMay()
	s.MaxTenantsPerUser = 0
	e = newEnvWith(t, s)
	for i := 0; i < 10; i++ {
		if code := call(t, e, "POST", "/v1/tenants", "mallory", `{"name":"ub`+strings.Repeat("a", i)+`"}`); code != http.StatusAccepted {
			t.Fatalf("no limit: status %d at %d", code, i)
		}
	}
}

func TestPlatformAdministratorsAreNotBoundByTheCap(t *testing.T) {
	s := usersMay()
	s.MaxTenantsPerUser = 1
	e := newEnvWith(t, s)
	for i, n := range []string{"p1", "p2", "p3"} {
		body := `{"name":"` + n + `","owner":"bob@example.org","plan":"large"}`
		if code := call(t, e, "POST", "/v1/tenants", "admin", body); code != http.StatusAccepted {
			t.Fatalf("%d: status %d", i, code)
		}
	}
	if got := e.store.tenants["p1"]; got.Owner != "bob@example.org" || got.Plan != tenants.Large {
		t.Errorf("an administrator chooses the owner and the plan: %+v", got)
	}
}

func TestWhoCanCreateATenant(t *testing.T) {
	for _, tt := range []struct {
		name     string
		settings Settings
		who      string
		want     int
	}{
		{"admins only: a person", testSettings, "mallory", http.StatusForbidden},
		{"admins only: an administrator", testSettings, "admin", http.StatusAccepted},
		{"users: a person", usersMay(), "mallory", http.StatusAccepted},
		{"users: a key", usersMay(), "svc", http.StatusForbidden},
		{"users: nobody", usersMay(), "", http.StatusUnauthorized},
	} {
		e := newEnvWith(t, tt.settings)
		body := `{"name":"fresh","owner":"x@example.org"}`
		if tt.who == "mallory" {
			body = `{"name":"fresh"}`
		}
		if code := call(t, e, "POST", "/v1/tenants", tt.who, body); code != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, code, tt.want)
		}
	}
}

func TestWhoamiSaysWhetherTheCallerMayCreateATenant(t *testing.T) {
	for _, tt := range []struct {
		settings Settings
		who      string
		want     string
	}{
		{testSettings, "mallory", `"canCreateTenants":false`},
		{usersMay(), "mallory", `"canCreateTenants":true`},
		{usersMay(), "svc", `"canCreateTenants":false`},
		{testSettings, "admin", `"canCreateTenants":true`},
	} {
		e := newEnvWith(t, tt.settings)
		if body := e.do(t, "GET", "/v1/whoami", tt.who, "").Body.String(); !strings.Contains(body, tt.want) {
			t.Errorf("%s: %s, want %s", tt.who, body, tt.want)
		}
	}
	// a person the token gives no email for has nothing to own a tenant by
	e := newEnvWith(t, usersMay())
	if canCreateOwnTenant(usersMay(), auth.Identity{Kind: auth.KindUser, Subject: "user:x"}) {
		t.Error("a person without an email creates tenants")
	}
	_ = e
}

func TestTheAdminsOfATenantChangeAndDeleteIt(t *testing.T) {
	e := newEnvWith(t, usersMay())
	e.store.tenants["demo"] = tenants.Tenant{Name: "demo", Owner: "alice@example.org", Plan: tenants.Small, Ready: true, NotReadySince: now}
	for who, want := range map[string]int{"alice": 200, "tadmin": 200, "operator": 403, "user": 403, "viewer": 403, "mallory": 404, "svc": 403, "": 401} {
		if code := call(t, e, "PATCH", "/v1/tenants/demo", who, `{"displayName":"renamed"}`); code != want {
			t.Errorf("rename by %q: status %d, want %d", who, code, want)
		}
	}
	// the plan is a platform administrator's
	for who, want := range map[string]int{"alice": 403, "tadmin": 403, "admin": 200} {
		if code := call(t, e, "PATCH", "/v1/tenants/demo", who, `{"plan":"large"}`); code != want {
			t.Errorf("plan by %q: status %d, want %d", who, code, want)
		}
	}
	for who, want := range map[string]int{"operator": 403, "mallory": 404, "svc": 403, "alice": 202} {
		if code := call(t, e, "DELETE", "/v1/tenants/demo", who, ""); code != want {
			t.Errorf("delete by %q: status %d, want %d", who, code, want)
		}
	}
}
