package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

// fake answers Check from a set of "user relation object" lines and records what it was asked.
type fake struct {
	grants  map[string]bool
	objects []string
	err     error
	asked   []string
}

func (f *fake) Check(_ context.Context, user, relation, object string) (bool, error) {
	f.asked = append(f.asked, user+" "+relation+" "+object)
	if f.err != nil {
		return false, f.err
	}
	return f.grants[user+" "+relation+" "+object], nil
}

func (f *fake) ListObjects(_ context.Context, user, relation, objectType string) ([]string, error) {
	f.asked = append(f.asked, "list "+user+" "+relation+" "+objectType)
	return f.objects, f.err
}

func grants(lines ...string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lines {
		m[l] = true
	}
	return m
}

var alice = auth.Identity{Kind: auth.KindUser, Subject: "user:alice@example.org", Email: "alice@example.org"}

func TestEveryActionAndScopeIsInTheTables(t *testing.T) {
	// The actions on a tenant are relations of the model. Creating, resizing and deleting a tenant is not: it is for the
	// platform administrators.
	actions := []Action{ReadTenant, ReadInstances, WriteInstances, ReadImages, WriteImages, ReadMemberships, WriteMemberships}
	for _, a := range actions {
		if !strings.HasPrefix(relations[a], "can_") {
			t.Errorf("%s has no can_* relation: %q", a, relations[a])
		}
	}
	if len(relations) != len(actions) {
		t.Errorf("%d relations for %d actions", len(relations), len(actions))
	}
	// One scope per action, and no two scopes for the same one. A key cannot write tenants: that is not a relation.
	seen := map[Action]string{}
	for scope, action := range keyScopes {
		if prev, dup := seen[action]; dup {
			t.Errorf("scopes %s and %s give the same action %s", prev, scope, action)
		}
		seen[action] = scope
		if _, ok := relations[action]; !ok {
			t.Errorf("scope %s gives %s, which is not a relation of the model", scope, action)
		}
	}
	if len(keyScopes) != len(actions) {
		t.Errorf("%d scopes for %d actions", len(keyScopes), len(actions))
	}
	want := "images:read,images:write,instances:read,instances:write,memberships:read,memberships:write,tenants:read"
	if got := strings.Join(Scopes(), ","); got != want {
		t.Errorf("Scopes() = %s, want the ones of the ApiKey XRD: %s", got, want)
	}
	if a, ok := ScopeAction("instances:write"); !ok || a != WriteInstances {
		t.Errorf("ScopeAction(instances:write) = %v, %v", a, ok)
	}
	if _, ok := ScopeAction("tenants:write"); ok {
		t.Error("tenants:write is a scope of a key: it must not exist")
	}
}

func TestAllowedAsksTheRightQuestion(t *testing.T) {
	f := &fake{grants: grants("user:alice@example.org can_write_instances tenant:demo")}
	a := New(f, "")

	ok, err := a.Allowed(t.Context(), alice, WriteInstances, "demo")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ok, _ := a.Allowed(t.Context(), alice, WriteImages, "demo"); ok {
		t.Error("an action that was not granted was allowed")
	}
	if ok, _ := a.Allowed(t.Context(), alice, WriteInstances, "other"); ok {
		t.Error("a tenant that was not granted was allowed")
	}
	want := []string{
		"user:alice@example.org can_write_instances tenant:demo",
		"user:alice@example.org can_write_images tenant:demo",
		"user:alice@example.org can_write_instances tenant:other",
	}
	if !slices.Equal(f.asked, want) {
		t.Errorf("asked %q, want %q", f.asked, want)
	}
}

func TestNothingThatIsNotATenantReachesOpenFGA(t *testing.T) {
	f := &fake{}
	a := New(f, "")
	for _, tenant := range []string{"", "x", "Demo", "demo#admin", "demo:x", "../demo", "*", "demo ", strings.Repeat("a", 40)} {
		if ok, err := a.Allowed(t.Context(), alice, ReadTenant, tenant); err != nil || ok {
			t.Errorf("%q: ok=%v err=%v", tenant, ok, err)
		}
	}
	if len(f.asked) != 0 {
		t.Errorf("OpenFGA was asked %q", f.asked)
	}
}

func TestNoIdentityNoAnswer(t *testing.T) {
	f := &fake{grants: grants(" can_read_tenant tenant:demo")}
	if ok, _ := New(f, "").Allowed(t.Context(), auth.Identity{}, ReadTenant, "demo"); ok {
		t.Error("an empty identity was allowed")
	}
	if ok, err := New(f, "").Allowed(t.Context(), alice, Action("fly"), "demo"); ok || err == nil {
		t.Errorf("an unknown action: ok=%v err=%v", ok, err)
	}
}

func TestADecisionThatCannotBeTakenIsAnErrorAndARefusal(t *testing.T) {
	boom := errors.New("openfga is down")
	f := &fake{grants: grants("user:alice@example.org can_read_tenant tenant:demo"), err: boom}
	ok, err := New(f, "").Allowed(t.Context(), alice, ReadTenant, "demo")
	if ok || !errors.Is(err, boom) {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestTenants(t *testing.T) {
	f := &fake{objects: []string{"tenant:zeta", "tenant:demo", "tenant:Bad", "group:x", "tenant:a#b", "tenant:"}}
	got, err := New(f, "").Tenants(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"demo", "zeta"}) {
		t.Errorf("Tenants = %v: sorted, and only what is a tenant slug", got)
	}
	if want := "list user:alice@example.org can_read_tenant tenant"; f.asked[0] != want {
		t.Errorf("asked %q, want %q", f.asked[0], want)
	}

	if got, _ := New(f, "").Tenants(t.Context(), auth.Identity{}); got != nil {
		t.Errorf("an empty identity sees %v", got)
	}
	boom := errors.New("down")
	if _, err := New(&fake{err: boom}, "").Tenants(t.Context(), alice); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestPlatformAdminForAUser(t *testing.T) {
	a := New(&fake{}, "phorge-platform-admins")
	member := auth.Identity{Kind: auth.KindUser, Email: "alice@example.org", Groups: []string{"staff", "phorge-platform-admins"}}
	if !a.PlatformAdmin(t.Context(), member) {
		t.Error("a member of the group was not recognised")
	}
	for name, id := range map[string]auth.Identity{
		"no group":      {Kind: auth.KindUser, Email: "alice@example.org"},
		"another group": {Kind: auth.KindUser, Groups: []string{"authentik Admins"}},
		"another case":  {Kind: auth.KindUser, Groups: []string{"Phorge-Platform-Admins"}},
		"a longer name": {Kind: auth.KindUser, Groups: []string{"phorge-platform-admins-ro"}},
		"nobody":        {},
	} {
		if a.PlatformAdmin(t.Context(), id) {
			t.Errorf("%s was taken for an administrator", name)
		}
	}
	if New(&fake{}, "").PlatformAdmin(t.Context(), auth.Identity{Kind: auth.KindUser, Groups: []string{""}}) {
		t.Error("with no group configured, nobody is an administrator, not even a user whose group has an empty name")
	}
}

func TestAKeyHasOnlyWhatOpenFGAHoldsForIt(t *testing.T) {
	svc := auth.Identity{Kind: auth.KindServiceKey, Subject: "apikey:apikey-0123456789abcdef", Name: "apikey-0123456789abcdef"}
	f := &fake{grants: grants(
		"apikey:apikey-0123456789abcdef can_read_tenant tenant:demo",
		"apikey:apikey-0123456789abcdef can_write_instances tenant:demo",
	)}
	a := New(f, "phorge-platform-admins")
	for _, tt := range []struct {
		action Action
		tenant string
		want   bool
	}{
		{ReadTenant, "demo", true},
		{WriteInstances, "demo", true},
		// No tuple, no right: there is no table to widen it.
		{ReadInstances, "demo", false},
		{WriteImages, "demo", false},
		{ReadTenant, "other", false},
		{ReadTenant, "Demo", false},
	} {
		got, err := a.Allowed(t.Context(), svc, tt.action, tt.tenant)
		if err != nil || got != tt.want {
			t.Errorf("%s on %s: %v, %v, want %v", tt.action, tt.tenant, got, err, tt.want)
		}
	}
	// It is never a platform administrator, even with the group in its claims: it has no groups to believe.
	withGroup := svc
	withGroup.Groups = []string{"phorge-platform-admins"}
	if a.PlatformAdmin(t.Context(), svc) || a.PlatformAdmin(t.Context(), withGroup) {
		t.Error("a key is not an administrator")
	}
}
