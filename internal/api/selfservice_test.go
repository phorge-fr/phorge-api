package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// usersMay is the deployment where a person owns three tenants at most.
func usersMay() Settings {
	s := testSettings
	s.MaxTenantsPerUser = 3
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
		"a bigger plan":    {`{"name":"x2","plan":"large"}`, "small plan"},
		"a medium plan":    {`{"name":"x3","plan":"medium"}`, "small plan"},
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

func TestACreationAsksForNothingBeyondTheCaller(t *testing.T) {
	e := newEnvWith(t, usersMay())
	for name, body := range map[string]string{
		"for somebody else":    `{"name":"fresh","owner":"bob@example.org"}`,
		"another plan":         `{"name":"fresh","plan":"large"}`,
		"another plan, medium": `{"name":"fresh","plan":"medium"}`,
	} {
		if code := call(t, e, "POST", "/v1/tenants", "mallory", body); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, code)
		}
	}
	if len(e.store.created) != 0 {
		t.Errorf("something was created: %+v", e.store.created)
	}
	if code := call(t, e, "POST", "/v1/tenants", "mallory", `{"name":"fresh","owner":"MALLORY@example.org","plan":"small"}`); code != http.StatusAccepted {
		t.Errorf("itself, the smallest plan: status %d, want 202", code)
	}
}

func TestWhoCanCreateATenant(t *testing.T) {
	for who, want := range map[string]int{"mallory": http.StatusAccepted, "alice": http.StatusAccepted, "svc": http.StatusForbidden, "": http.StatusUnauthorized} {
		e := newEnvWith(t, usersMay())
		if code := call(t, e, "POST", "/v1/tenants", who, `{"name":"fresh"}`); code != want {
			t.Errorf("%q: status %d, want %d", who, code, want)
		}
	}
}

func TestWhoamiSaysWhetherTheCallerMayCreateATenant(t *testing.T) {
	e := newEnvWith(t, usersMay())
	for who, want := range map[string]string{"mallory": `"canCreateTenants":true`, "admin": `"canCreateTenants":true`, "svc": `"canCreateTenants":false`} {
		body := e.do(t, "GET", "/v1/whoami", who, "").Body.String()
		if !strings.Contains(body, want) {
			t.Errorf("%s: %s, want %s", who, body, want)
		}
		if strings.Contains(body, "platformAdmin") {
			t.Errorf("%s: %s: there is no platform administrator any more", who, body)
		}
	}
	// a person the token gives no email for has nothing to own a tenant by
	if canCreateOwnTenant(auth.Identity{Kind: auth.KindUser, Subject: "user:x"}) {
		t.Error("a person without an email creates tenants")
	}
}

func TestTheAdminsOfATenantChangeAndDeleteIt(t *testing.T) {
	e := newEnvWith(t, usersMay())
	e.store.tenants["demo"] = tenants.Tenant{Name: "demo", Owner: "alice@example.org", Plan: tenants.Small, Ready: true, NotReadySince: now}
	for who, want := range map[string]int{"alice": 200, "tadmin": 200, "operator": 403, "user": 403, "viewer": 403, "mallory": 404, "svc": 403, "": 401} {
		if code := call(t, e, "PATCH", "/v1/tenants/demo", who, `{"displayName":"renamed"}`); code != want {
			t.Errorf("rename by %q: status %d, want %d", who, code, want)
		}
	}
	// the plan is changed where the platform is run, on request: not through the API, by nobody
	for who, want := range map[string]int{"alice": 403, "tadmin": 403, "admin": 403} {
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
