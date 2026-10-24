package access

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
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
	later := now.Add(time.Hour)
	good := CreateKey{Name: "ci", DisplayName: "CI", Scopes: []string{"tenant:read", "instances:write"}, ExpiresAt: &later}
	if err := good.Validate(now); err != nil {
		t.Fatal(err)
	}
	if err := (CreateKey{Name: "a", Scopes: []string{"images:read"}}).Validate(now); err != nil {
		t.Errorf("no expiry and no display name: %v", err)
	}

	past := now.Add(-time.Second)
	for name, req := range map[string]CreateKey{
		"no name":                     {Scopes: []string{"tenant:read"}},
		"an underscore in a name":     {Name: "ci_1", Scopes: []string{"tenant:read"}},
		"an upper case name":          {Name: "CI", Scopes: []string{"tenant:read"}},
		"a name of 64 characters":     {Name: strings.Repeat("a", 64), Scopes: []string{"tenant:read"}},
		"no scope":                    {Name: "ci"},
		"an empty list":               {Name: "ci", Scopes: []string{}},
		"a scope that does not exist": {Name: "ci", Scopes: []string{"tenant:delete"}},
		"a scope twice":               {Name: "ci", Scopes: []string{"tenant:read", "tenant:read"}},
		"an expiry that passed":       {Name: "ci", Scopes: []string{"tenant:read"}, ExpiresAt: &past},
		"an expiry now":               {Name: "ci", Scopes: []string{"tenant:read"}, ExpiresAt: &now},
		"a display name too long":     {Name: "ci", Scopes: []string{"tenant:read"}, DisplayName: strings.Repeat("a", 101)},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(now); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}
}

func TestPatchKey(t *testing.T) {
	later, past := now.Add(time.Hour), now.Add(-time.Hour)
	scopes, none, bad := []string{"images:read"}, []string{}, []string{"nope"}
	for name, ok := range map[string]PatchKey{
		"scopes": {Scopes: &scopes}, "a date": {ExpiresAt: &later}, "a name": {DisplayName: ptr("CI")},
	} {
		if err := ok.Validate(now); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]PatchKey{
		"nothing": {}, "no scope left": {Scopes: &none}, "an unknown scope": {Scopes: &bad}, "a date that passed": {ExpiresAt: &past},
	} {
		if err := req.Validate(now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestNewKeyIsParsedAndHashedByTheAuthentication(t *testing.T) {
	key, hash, err := NewKey("demo", "ci")
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.ParseKey(key)
	if err != nil {
		t.Fatalf("the key does not parse: %v", err)
	}
	if p.Tenant != "demo" || p.Name != "ci" || len(p.Secret) != 64 {
		t.Errorf("parsed %+v: 32 bytes in hex are expected", p)
	}
	if hash != auth.HashSecret(p.Secret) {
		t.Error("the stored hash is not the one the authentication computes")
	}
	if strings.Contains(hash, p.Secret) {
		t.Error("the hash holds the secret")
	}
	other, _, _ := NewKey("demo", "ci")
	if other == key {
		t.Error("two keys are the same")
	}
}
