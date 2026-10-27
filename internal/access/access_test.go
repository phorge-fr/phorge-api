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
	const max = 30 * 24 * time.Hour
	later := now.Add(time.Hour)
	good := CreateKey{DisplayName: "CI", Scopes: []string{"tenants:read", "instances:write"}, ExpiresAt: &later}
	if err := good.Validate(now, max); err != nil {
		t.Fatal(err)
	}
	lastDay := now.Add(max)
	if err := (CreateKey{DisplayName: "a", Scopes: []string{"tenants:write"}, ExpiresAt: &lastDay}).Validate(now, max); err != nil {
		t.Errorf("the longest lifetime: %v", err)
	}

	past, tooFar := now.Add(-time.Second), now.Add(max+time.Second)
	for name, req := range map[string]CreateKey{
		"no display name":             {Scopes: []string{"tenants:read"}, ExpiresAt: &later},
		"a display name too long":     {DisplayName: strings.Repeat("a", 101), Scopes: []string{"tenants:read"}, ExpiresAt: &later},
		"no scope":                    {DisplayName: "ci", ExpiresAt: &later},
		"an empty list":               {DisplayName: "ci", Scopes: []string{}, ExpiresAt: &later},
		"a scope that does not exist": {DisplayName: "ci", Scopes: []string{"tenants:delete"}, ExpiresAt: &later},
		"the old scope name":          {DisplayName: "ci", Scopes: []string{"tenant:read"}, ExpiresAt: &later},
		"a scope twice":               {DisplayName: "ci", Scopes: []string{"tenants:read", "tenants:read"}, ExpiresAt: &later},
		"no expiry, one is required":  {DisplayName: "ci", Scopes: []string{"tenants:read"}},
		"an expiry that passed":       {DisplayName: "ci", Scopes: []string{"tenants:read"}, ExpiresAt: &past},
		"an expiry now":               {DisplayName: "ci", Scopes: []string{"tenants:read"}, ExpiresAt: &now},
		"an expiry too far":           {DisplayName: "ci", Scopes: []string{"tenants:read"}, ExpiresAt: &tooFar},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(now, max); !errors.As(err, &invalid) {
				t.Errorf("accepted: %v", err)
			}
		})
	}

	// With no maximum (0) a key may be permanent, and a date is only checked to be in the future.
	if err := (CreateKey{DisplayName: "perm", Scopes: []string{"tenants:read"}}).Validate(now, 0); err != nil {
		t.Errorf("no maximum, no expiry: %v", err)
	}
	far := now.Add(100 * 365 * 24 * time.Hour)
	if err := (CreateKey{DisplayName: "perm", Scopes: []string{"tenants:read"}, ExpiresAt: &far}).Validate(now, 0); err != nil {
		t.Errorf("no maximum, a far date: %v", err)
	}
	if err := (CreateKey{DisplayName: "perm", Scopes: []string{"tenants:read"}, ExpiresAt: &past}).Validate(now, 0); err == nil {
		t.Error("no maximum, a date that passed was accepted")
	}
}

func TestPatchKey(t *testing.T) {
	const max = 30 * 24 * time.Hour
	later, past, tooFar := now.Add(time.Hour), now.Add(-time.Hour), now.Add(max+time.Hour)
	scopes, none, bad := []string{"images:read"}, []string{}, []string{"nope"}
	for name, ok := range map[string]PatchKey{
		"scopes": {Scopes: &scopes}, "a date": {ExpiresAt: &later}, "a name": {DisplayName: ptr("CI")},
	} {
		if err := ok.Validate(now, max); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]PatchKey{
		"nothing": {}, "no scope left": {Scopes: &none}, "an unknown scope": {Scopes: &bad}, "a date that passed": {ExpiresAt: &past},
		"a date too far": {ExpiresAt: &tooFar}, "an empty name": {DisplayName: ptr("")},
	} {
		if err := req.Validate(now, max); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := (PatchKey{ExpiresAt: &tooFar}).Validate(now, 0); err != nil {
		t.Errorf("no maximum: a far date is fine: %v", err)
	}
}

func TestNewKeyIsParsedAndHashedByTheAuthentication(t *testing.T) {
	id, key, hash, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.ParseKey(key)
	if err != nil {
		t.Fatalf("the key does not parse: %v", err)
	}
	if p.ID != id || !ValidKeyName(id) || len(p.Secret) != 64 {
		t.Errorf("parsed %+v for the id %q: 8 bytes in hex and 32 bytes in hex are expected", p, id)
	}
	if hash != auth.HashSecret(p.Secret) {
		t.Error("the stored hash is not the one the authentication computes")
	}
	if strings.Contains(hash, p.Secret) {
		t.Error("the hash holds the secret")
	}
	_, other, _, _ := NewKey()
	if other == key {
		t.Error("two keys are the same")
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
