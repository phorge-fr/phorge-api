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

var (
	alice = auth.Identity{Kind: auth.KindUser, Subject: "user:alice@example.org", Email: "alice@example.org"}
	ci    = auth.Identity{Kind: auth.KindAPIKey, Subject: "apikey:demo.ci", Tenant: "demo", Name: "ci"}
)

func TestEveryActionAndScopeIsInTheTables(t *testing.T) {
	actions := []Action{ReadTenant, ReadInstances, WriteInstances, ReadImages, WriteImages, ReadMemberships, WriteMemberships}
	for _, a := range actions {
		if !strings.HasPrefix(relations[a], "can_") {
			t.Errorf("%s has no can_* relation: %q", a, relations[a])
		}
	}
	if len(relations) != len(actions) {
		t.Errorf("%d relations for %d actions", len(relations), len(actions))
	}
	// One scope per action, and no two scopes for the same one: a scope is a relation of its own in the model.
	seen := map[Action]string{}
	for scope, action := range scopes {
		if prev, dup := seen[action]; dup {
			t.Errorf("scopes %s and %s give the same action %s", prev, scope, action)
		}
		seen[action] = scope
	}
	if len(scopes) != len(actions) {
		t.Errorf("%d scopes for %d actions", len(scopes), len(actions))
	}
	want := "images:read,images:write,instances:read,instances:write,memberships:read,memberships:write,tenant:read"
	if got := strings.Join(Scopes(), ","); got != want {
		t.Errorf("Scopes() = %s, want the ones of the ApiKey XRD: %s", got, want)
	}
}

func TestAllowedAsksTheRightQuestion(t *testing.T) {
	f := &fake{grants: grants("user:alice@example.org can_write_instances tenant:demo")}
	a := New(f, nil)

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

func TestAKeyIsAskedAsAnApiKey(t *testing.T) {
	f := &fake{grants: grants("apikey:demo.ci can_read_instances tenant:demo")}
	if ok, err := New(f, nil).Allowed(t.Context(), ci, ReadInstances, "demo"); err != nil || !ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestAKeyNeverActsOnAnotherTenant(t *testing.T) {
	// Even if OpenFGA were to say yes: the key belongs to one tenant.
	f := &fake{grants: grants("apikey:demo.ci can_read_instances tenant:other")}
	if ok, err := New(f, nil).Allowed(t.Context(), ci, ReadInstances, "other"); err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
	if len(f.asked) != 0 {
		t.Errorf("OpenFGA was asked %q, there is nothing to ask", f.asked)
	}
}

func TestNothingThatIsNotATenantReachesOpenFGA(t *testing.T) {
	f := &fake{}
	a := New(f, nil)
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
	if ok, _ := New(f, nil).Allowed(t.Context(), auth.Identity{}, ReadTenant, "demo"); ok {
		t.Error("an empty identity was allowed")
	}
	if ok, err := New(f, nil).Allowed(t.Context(), alice, Action("fly"), "demo"); ok || err == nil {
		t.Errorf("an unknown action: ok=%v err=%v", ok, err)
	}
}

func TestADecisionThatCannotBeTakenIsAnErrorAndARefusal(t *testing.T) {
	boom := errors.New("openfga is down")
	f := &fake{grants: grants("user:alice@example.org can_read_tenant tenant:demo"), err: boom}
	ok, err := New(f, nil).Allowed(t.Context(), alice, ReadTenant, "demo")
	if ok || !errors.Is(err, boom) {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestTenants(t *testing.T) {
	f := &fake{objects: []string{"tenant:zeta", "tenant:demo", "tenant:Bad", "group:x", "tenant:a#b", "tenant:"}}
	got, err := New(f, nil).Tenants(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"demo", "zeta"}) {
		t.Errorf("Tenants = %v: sorted, and only what is a tenant slug", got)
	}
	if want := "list user:alice@example.org can_read_tenant tenant"; f.asked[0] != want {
		t.Errorf("asked %q, want %q", f.asked[0], want)
	}

	if got, _ := New(f, nil).Tenants(t.Context(), auth.Identity{}); got != nil {
		t.Errorf("an empty identity sees %v", got)
	}
	boom := errors.New("down")
	if _, err := New(&fake{err: boom}, nil).Tenants(t.Context(), alice); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestPlatformAdmins(t *testing.T) {
	a := New(&fake{}, []string{"Alice@Example.org"})
	if !a.IsPlatformAdmin(auth.Identity{Kind: auth.KindUser, Email: "Alice@Example.org"}) {
		t.Error("an administrator was not recognised")
	}
	for name, id := range map[string]auth.Identity{
		"another case": {Kind: auth.KindUser, Email: "alice@example.org"},
		"nobody":       {},
		"a key":        {Kind: auth.KindAPIKey, Email: "Alice@Example.org", Subject: "apikey:demo.ci"},
		"empty email":  {Kind: auth.KindUser},
	} {
		if a.IsPlatformAdmin(id) {
			t.Errorf("%s was taken for an administrator", name)
		}
	}
	if New(&fake{}, nil).IsPlatformAdmin(alice) {
		t.Error("with no administrator configured, nobody is one")
	}
}

func TestMissingKeepsAKeyWithinItsCreator(t *testing.T) {
	// alice may read the tenant and write instances, nothing else.
	f := &fake{grants: grants(
		"user:alice@example.org can_read_tenant tenant:demo",
		"user:alice@example.org can_write_instances tenant:demo",
	)}
	a := New(f, nil)

	missing, err := a.Missing(t.Context(), alice, "demo", []string{"tenant:read", "instances:write"})
	if err != nil || len(missing) != 0 {
		t.Errorf("within her rights: missing=%v err=%v", missing, err)
	}

	missing, err = a.Missing(t.Context(), alice, "demo", []string{"memberships:write", "tenant:read", "images:write", "memberships:write"})
	if err != nil || !slices.Equal(missing, []string{"images:write", "memberships:write"}) {
		t.Errorf("beyond her rights: missing=%v err=%v, want the two scopes she lacks, sorted, once", missing, err)
	}
}

func TestAKeyCanOnlyGiveASubsetOfItsOwnScopes(t *testing.T) {
	f := &fake{grants: grants(
		"apikey:demo.ci can_write_memberships tenant:demo",
		"apikey:demo.ci can_read_instances tenant:demo",
	)}
	a := New(f, nil)
	missing, err := a.Missing(t.Context(), ci, "demo", []string{"instances:read", "instances:write"})
	if err != nil || !slices.Equal(missing, []string{"instances:write"}) {
		t.Errorf("missing=%v err=%v", missing, err)
	}
	// And never for another tenant.
	missing, err = a.Missing(t.Context(), ci, "other", []string{"instances:read"})
	if err != nil || !slices.Equal(missing, []string{"instances:read"}) {
		t.Errorf("another tenant: missing=%v err=%v", missing, err)
	}
}

func TestMissingRefusesAnUnknownScopeAndPropagatesErrors(t *testing.T) {
	a := New(&fake{}, nil)
	_, err := a.Missing(t.Context(), alice, "demo", []string{"tenant:read", "tenant:delete"})
	var unknown *UnknownScopeError
	if !errors.As(err, &unknown) || unknown.Scope != "tenant:delete" {
		t.Errorf("err = %v", err)
	}

	boom := errors.New("down")
	if _, err := New(&fake{err: boom}, nil).Missing(t.Context(), alice, "demo", []string{"tenant:read"}); !errors.Is(err, boom) {
		t.Errorf("err = %v: if the rights of the creator cannot be read, the key must not be created", err)
	}
}
