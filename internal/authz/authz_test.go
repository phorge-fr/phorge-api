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
	// ci is a key of alice: it is alice for OpenFGA, within the scopes it was given.
	ci = auth.Identity{Kind: auth.KindAPIKey, Subject: "user:alice@example.org", Email: "alice@example.org", Name: "0123456789abcdef",
		Scopes: []string{"instances:read", "tenants:read"}}
)

// groupsOf is a Directory that answers from a map.
type groupsOf struct {
	groups map[string][]string
	err    error
	asked  []string
}

func (g *groupsOf) Groups(_ context.Context, email string) ([]string, error) {
	g.asked = append(g.asked, email)
	return g.groups[email], g.err
}

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
	actions = append(actions, WriteTenants)
	// One scope per action, and no two scopes for the same one.
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
	want := "images:read,images:write,instances:read,instances:write,memberships:read,memberships:write,tenants:read,tenants:write"
	if got := strings.Join(Scopes(), ","); got != want {
		t.Errorf("Scopes() = %s, want the ones of the ApiKey XRD: %s", got, want)
	}
}

func TestAllowedAsksTheRightQuestion(t *testing.T) {
	f := &fake{grants: grants("user:alice@example.org can_write_instances tenant:demo")}
	a := New(f, "", nil)

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

func TestAKeyIsAskedAsItsOwner(t *testing.T) {
	f := &fake{grants: grants("user:alice@example.org can_read_instances tenant:demo")}
	if ok, err := New(f, "", nil).Allowed(t.Context(), ci, ReadInstances, "demo"); err != nil || !ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
	if want := []string{"user:alice@example.org can_read_instances tenant:demo"}; !slices.Equal(f.asked, want) {
		t.Errorf("asked %q, want %q", f.asked, want)
	}
}

func TestAKeyNeverDoesWhatItsScopesDoNotName(t *testing.T) {
	// Alice may do it, the key was not given the scope: refused, and OpenFGA is not even asked.
	f := &fake{grants: grants("user:alice@example.org can_write_instances tenant:demo")}
	if ok, err := New(f, "", nil).Allowed(t.Context(), ci, WriteInstances, "demo"); err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
	if len(f.asked) != 0 {
		t.Errorf("OpenFGA was asked %q, there is nothing to ask", f.asked)
	}
}

func TestAKeyNeverDoesWhatItsOwnerMayNot(t *testing.T) {
	// The scope is there, the owner has no right on that tenant.
	f := &fake{grants: grants("user:alice@example.org can_read_instances tenant:demo")}
	if ok, err := New(f, "", nil).Allowed(t.Context(), ci, ReadInstances, "other"); err != nil || ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestScopeAllows(t *testing.T) {
	if !ScopeAllows(alice, WriteTenants) || !ScopeAllows(alice, WriteInstances) {
		t.Error("a user has no scopes to hold")
	}
	read := auth.Identity{Kind: auth.KindAPIKey, Scopes: []string{"tenants:read"}}
	write := auth.Identity{Kind: auth.KindAPIKey, Scopes: []string{"tenants:write"}}
	none := auth.Identity{Kind: auth.KindAPIKey}
	for _, c := range []struct {
		id     auth.Identity
		action Action
		want   bool
	}{
		{read, ReadTenant, true}, {read, WriteTenants, false}, {read, ReadInstances, false},
		{write, WriteTenants, true}, {write, ReadTenant, true}, {write, WriteInstances, false},
		{none, ReadTenant, false}, {none, WriteTenants, false},
	} {
		if got := ScopeAllows(c.id, c.action); got != c.want {
			t.Errorf("%v on %s = %v, want %v", c.id.Scopes, c.action, got, c.want)
		}
	}
}

func TestNothingThatIsNotATenantReachesOpenFGA(t *testing.T) {
	f := &fake{}
	a := New(f, "", nil)
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
	if ok, _ := New(f, "", nil).Allowed(t.Context(), auth.Identity{}, ReadTenant, "demo"); ok {
		t.Error("an empty identity was allowed")
	}
	if ok, err := New(f, "", nil).Allowed(t.Context(), alice, Action("fly"), "demo"); ok || err == nil {
		t.Errorf("an unknown action: ok=%v err=%v", ok, err)
	}
}

func TestADecisionThatCannotBeTakenIsAnErrorAndARefusal(t *testing.T) {
	boom := errors.New("openfga is down")
	f := &fake{grants: grants("user:alice@example.org can_read_tenant tenant:demo"), err: boom}
	ok, err := New(f, "", nil).Allowed(t.Context(), alice, ReadTenant, "demo")
	if ok || !errors.Is(err, boom) {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestTenants(t *testing.T) {
	f := &fake{objects: []string{"tenant:zeta", "tenant:demo", "tenant:Bad", "group:x", "tenant:a#b", "tenant:"}}
	got, err := New(f, "", nil).Tenants(t.Context(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"demo", "zeta"}) {
		t.Errorf("Tenants = %v: sorted, and only what is a tenant slug", got)
	}
	if want := "list user:alice@example.org can_read_tenant tenant"; f.asked[0] != want {
		t.Errorf("asked %q, want %q", f.asked[0], want)
	}

	if got, _ := New(f, "", nil).Tenants(t.Context(), auth.Identity{}); got != nil {
		t.Errorf("an empty identity sees %v", got)
	}
	boom := errors.New("down")
	if _, err := New(&fake{err: boom}, "", nil).Tenants(t.Context(), alice); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestPlatformAdminForAUser(t *testing.T) {
	a := New(&fake{}, "phorge-platform-admins", nil)
	member := auth.Identity{Kind: auth.KindUser, Email: "alice@example.org", Groups: []string{"staff", "phorge-platform-admins"}}
	if ok, err := a.PlatformAdmin(t.Context(), member); err != nil || !ok {
		t.Errorf("a member of the group was not recognised: ok=%v err=%v", ok, err)
	}
	for name, id := range map[string]auth.Identity{
		"no group":      {Kind: auth.KindUser, Email: "alice@example.org"},
		"another group": {Kind: auth.KindUser, Groups: []string{"authentik Admins"}},
		"another case":  {Kind: auth.KindUser, Groups: []string{"Phorge-Platform-Admins"}},
		"a longer name": {Kind: auth.KindUser, Groups: []string{"phorge-platform-admins-ro"}},
		"nobody":        {},
	} {
		if ok, _ := a.PlatformAdmin(t.Context(), id); ok {
			t.Errorf("%s was taken for an administrator", name)
		}
	}
	if ok, _ := New(&fake{}, "", nil).PlatformAdmin(t.Context(), auth.Identity{Kind: auth.KindUser, Groups: []string{""}}); ok {
		t.Error("with no group configured, nobody is an administrator, not even a user whose group has an empty name")
	}
}

func TestPlatformAdminForAKeyIsAskedToAuthentik(t *testing.T) {
	dir := &groupsOf{groups: map[string][]string{"alice@example.org": {"phorge-platform-admins"}, "bob@example.org": {"staff"}}}
	a := New(&fake{}, "phorge-platform-admins", dir)

	if ok, err := a.PlatformAdmin(t.Context(), ci); err != nil || !ok {
		t.Errorf("a key of an administrator: ok=%v err=%v", ok, err)
	}
	if !slices.Equal(dir.asked, []string{"alice@example.org"}) {
		t.Errorf("Authentik was asked about %q", dir.asked)
	}
	bobs := auth.Identity{Kind: auth.KindAPIKey, Email: "bob@example.org", Scopes: []string{"tenants:write"}}
	if ok, err := a.PlatformAdmin(t.Context(), bobs); err != nil || ok {
		t.Errorf("a key of someone who left the group: ok=%v err=%v", ok, err)
	}
	// Groups a key claims for itself mean nothing.
	forged := auth.Identity{Kind: auth.KindAPIKey, Email: "bob@example.org", Groups: []string{"phorge-platform-admins"}}
	if ok, _ := a.PlatformAdmin(t.Context(), forged); ok {
		t.Error("the groups carried by a key were believed")
	}
	if ok, _ := New(&fake{}, "phorge-platform-admins", nil).PlatformAdmin(t.Context(), ci); ok {
		t.Error("without a directory, a key is never an administrator")
	}
	if ok, _ := a.PlatformAdmin(t.Context(), auth.Identity{Kind: auth.KindAPIKey}); ok {
		t.Error("a key without an owner is not an administrator")
	}
}

func TestPlatformAdminThatCannotBeAskedIsAnError(t *testing.T) {
	boom := errors.New("authentik is down")
	a := New(&fake{}, "phorge-platform-admins", &groupsOf{err: boom})
	if ok, err := a.PlatformAdmin(t.Context(), ci); ok || !errors.Is(err, boom) {
		t.Errorf("ok=%v err=%v: it must be a refusal that says the service is unavailable", ok, err)
	}
	// A user's groups are in the token: Authentik is not needed.
	member := auth.Identity{Kind: auth.KindUser, Groups: []string{"phorge-platform-admins"}}
	if ok, err := a.PlatformAdmin(t.Context(), member); err != nil || !ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestAdminMayRespectsTheScopesOfAKey(t *testing.T) {
	dir := &groupsOf{groups: map[string][]string{"alice@example.org": {"phorge-platform-admins"}}}
	a := New(&fake{}, "phorge-platform-admins", dir)

	// An administrator's key with tenants:read sees every tenant, and cannot delete one.
	if ok, err := a.AdminMay(t.Context(), ci, ReadTenant); err != nil || !ok {
		t.Errorf("read: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AdminMay(t.Context(), ci, WriteTenants); err != nil || ok {
		t.Errorf("write: ok=%v err=%v", ok, err)
	}
	// Without the scope Authentik is not even asked.
	dir.asked = nil
	if ok, _ := a.AdminMay(t.Context(), ci, WriteInstances); ok || len(dir.asked) != 0 {
		t.Errorf("ok=%v, Authentik asked %q", ok, dir.asked)
	}
	admin := auth.Identity{Kind: auth.KindUser, Groups: []string{"phorge-platform-admins"}}
	if ok, _ := a.AdminMay(t.Context(), admin, WriteTenants); !ok {
		t.Error("a user in the group may write tenants")
	}
}
