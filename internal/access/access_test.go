package access

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func TestCreateMembership(t *testing.T) {
	for _, role := range []Role{Admin, Operator, User, Viewer} {
		if err := (CreateMembership{Subject: Subject{Kind: "User", Name: "Bob@Example.org"}, Role: role}).Validate(); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
	for name, req := range map[string]CreateMembership{
		"a group":                    {Subject: Subject{Kind: "Group", Name: "staff"}, Role: Viewer},
		"no kind":                    {Subject: Subject{Name: "bob@example.org"}, Role: Viewer},
		"no name":                    {Subject: Subject{Kind: "User"}, Role: Viewer},
		"a name that is not email":   {Subject: Subject{Kind: "User", Name: "bob"}, Role: Viewer},
		"a wildcard":                 {Subject: Subject{Kind: "User", Name: "*"}, Role: Viewer},
		"a relation in the name":     {Subject: Subject{Kind: "User", Name: "bob@example.org#admin"}, Role: Viewer},
		"a type in the name":         {Subject: Subject{Kind: "User", Name: "user:bob@example.org"}, Role: Viewer},
		"no role":                    {Subject: Subject{Kind: "User", Name: "bob@example.org"}},
		"a role that does not exist": {Subject: Subject{Kind: "User", Name: "bob@example.org"}, Role: "root"},
		"owner":                      {Subject: Subject{Kind: "User", Name: "bob@example.org"}, Role: "owner"},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}
}

func TestMembershipNameIsOnePerUser(t *testing.T) {
	bob := Subject{Kind: "User", Name: "bob@example.org"}
	name := MembershipName(bob)
	if !ValidMembershipName(name) {
		t.Errorf("%q is not a valid name", name)
	}
	if name != MembershipName(bob) {
		t.Error("the name is not stable: a second membership of the same user would not be refused")
	}
	if name == MembershipName(Subject{Kind: "User", Name: "Bob@example.org"}) {
		t.Error("the email keeps its case, like every tuple")
	}
	if name == MembershipName(Subject{Kind: "User", Name: "alice@example.org"}) {
		t.Error("two users share a name")
	}
	for _, bad := range []string{"", "m-xyz", "m-" + strings.Repeat("a", 23), "m-" + strings.Repeat("A", 24), "bob", "../" + name} {
		if ValidMembershipName(bad) {
			t.Errorf("%q was accepted as a name", bad)
		}
	}
}

func TestCreateKey(t *testing.T) {
	const max = 30 * 24 * time.Hour
	later := now.Add(time.Hour)
	one := []Grant{{Tenant: "demo", Scopes: []string{"tenants:read", "instances:write"}}}
	good := CreateKey{DisplayName: "CI", Grants: one, ExpiresAt: &later}
	if err := good.Validate(now, max); err != nil {
		t.Fatal(err)
	}
	lastDay := now.Add(max)
	if err := (CreateKey{DisplayName: "a", Grants: one, ExpiresAt: &lastDay}).Validate(now, max); err != nil {
		t.Errorf("the longest lifetime: %v", err)
	}
	two := []Grant{{Tenant: "demo", Scopes: []string{"images:read"}}, {Tenant: "other", Scopes: []string{"memberships:write"}}}
	if err := (CreateKey{DisplayName: "two", Grants: two, ExpiresAt: &later}).Validate(now, max); err != nil {
		t.Errorf("two tenants: %v", err)
	}

	past, tooFar := now.Add(-time.Second), now.Add(max+time.Second)
	scopes := func(s ...string) []Grant { return []Grant{{Tenant: "demo", Scopes: s}} }
	for name, req := range map[string]CreateKey{
		"no display name":                      {Grants: one, ExpiresAt: &later},
		"a display name too long":              {DisplayName: strings.Repeat("a", 101), Grants: one, ExpiresAt: &later},
		"no grant":                             {DisplayName: "ci", ExpiresAt: &later},
		"an empty list":                        {DisplayName: "ci", Grants: []Grant{}, ExpiresAt: &later},
		"no scope":                             {DisplayName: "ci", Grants: scopes(), ExpiresAt: &later},
		"a scope that does not exist":          {DisplayName: "ci", Grants: scopes("tenants:delete"), ExpiresAt: &later},
		"tenants:write, which a key never has": {DisplayName: "ci", Grants: scopes("tenants:write"), ExpiresAt: &later},
		"the old scope name":                   {DisplayName: "ci", Grants: scopes("tenant:read"), ExpiresAt: &later},
		"a scope twice":                        {DisplayName: "ci", Grants: scopes("tenants:read", "tenants:read"), ExpiresAt: &later},
		"a tenant that is not a name":          {DisplayName: "ci", Grants: []Grant{{Tenant: "Demo#admin", Scopes: []string{"tenants:read"}}}, ExpiresAt: &later},
		"a tenant twice":                       {DisplayName: "ci", Grants: []Grant{two[0], two[0]}, ExpiresAt: &later},
		"too many tenants":                     {DisplayName: "ci", Grants: tooMany(), ExpiresAt: &later},
		"no expiry: a key expires":             {DisplayName: "ci", Grants: one},
		"an expiry that passed":                {DisplayName: "ci", Grants: one, ExpiresAt: &past},
		"an expiry now":                        {DisplayName: "ci", Grants: one, ExpiresAt: &now},
		"an expiry too far":                    {DisplayName: "ci", Grants: one, ExpiresAt: &tooFar},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(now, max); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}

	// With no maximum (0) a date is only checked to be in the future, and one is always required.
	far := now.Add(100 * 365 * 24 * time.Hour)
	if err := (CreateKey{DisplayName: "far", Grants: one, ExpiresAt: &far}).Validate(now, 0); err != nil {
		t.Errorf("no maximum, a far date: %v", err)
	}
	if err := (CreateKey{DisplayName: "perm", Grants: one}).Validate(now, 0); err == nil {
		t.Error("no maximum, no expiry was accepted: a key always expires")
	}
	if err := (CreateKey{DisplayName: "late", Grants: one, ExpiresAt: &past}).Validate(now, 0); err == nil {
		t.Error("no maximum, a date that passed was accepted")
	}
}

func tooMany() []Grant {
	grants := make([]Grant, MaxGrants+1)
	for i := range grants {
		grants[i] = Grant{Tenant: fmt.Sprintf("tenant-%02d", i), Scopes: []string{"tenants:read"}}
	}
	return grants
}

func TestPatchKey(t *testing.T) {
	if err := (PatchKey{DisplayName: ptr("CI")}).Validate(); err != nil {
		t.Errorf("a name: %v", err)
	}
	for name, req := range map[string]PatchKey{
		"nothing": {}, "an empty name": {DisplayName: ptr("")}, "a name too long": {DisplayName: ptr(strings.Repeat("a", 101))},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestNewKeyID(t *testing.T) {
	id, err := NewKeyID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidKeyName(id) {
		t.Errorf("%q is not an id of a key: 8 random bytes in hex are expected", id)
	}
	if name := AccountPrefix + id; len(name) > 63 {
		t.Errorf("the user name %q is too long for a key name", name)
	}
	if other, _ := NewKeyID(); other == id {
		t.Error("two keys have the same id")
	}
}

func TestOwnerLabel(t *testing.T) {
	label := OwnerLabel("alice@example.org")
	if len(label) != 32 || strings.ContainsAny(label, "@.") {
		t.Errorf("%q is not usable as a label value", label)
	}
	if label != OwnerLabel(" Alice@Example.org ") {
		t.Error("the label depends on the case: the keys of one user would be split")
	}
	if label == OwnerLabel("bob@example.org") {
		t.Error("two users share a label")
	}
}
