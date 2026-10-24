package authz_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/fga/fgatest"
)

// This test runs against the real phorge-api store, with the real model, on tuples of a tenant of its own that it
// removes at the end. It is skipped unless the store is reachable:
//
//	kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
//	export PHORGE_FGA_URL=http://127.0.0.1:18080
//	export PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
//	export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
func TestAgainstTheRealModel(t *testing.T) {
	url, store, key := os.Getenv("PHORGE_FGA_URL"), os.Getenv("PHORGE_FGA_STORE"), os.Getenv("PHORGE_FGA_KEY")
	if url == "" || store == "" || key == "" {
		t.Skip("PHORGE_FGA_URL, PHORGE_FGA_STORE and PHORGE_FGA_KEY are not set")
	}

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tenant := "apitest-authz-" + hex.EncodeToString(suffix)
	object := "tenant:" + tenant
	user := func(role string) auth.Identity {
		email := role + "-" + tenant + "@example.org"
		return auth.Identity{Kind: auth.KindUser, Subject: "user:" + email, Email: email}
	}
	key1 := auth.Identity{Kind: auth.KindAPIKey, Subject: "apikey:" + tenant + ".ci", Tenant: tenant, Name: "ci"}

	fgatest.Store{BaseURL: url, StoreID: store, Key: key}.Write(t,
		fgatest.Tuple{User: user("admin").Subject, Relation: "admin", Object: object},
		fgatest.Tuple{User: user("operator").Subject, Relation: "operator", Object: object},
		fgatest.Tuple{User: user("user").Subject, Relation: "user", Object: object},
		fgatest.Tuple{User: user("viewer").Subject, Relation: "viewer", Object: object},
		// A key with two scopes: they are relations of their own, instances:write does not give instances:read.
		fgatest.Tuple{User: key1.Subject, Relation: "key_tenant_read", Object: object},
		fgatest.Tuple{User: key1.Subject, Relation: "key_instances_write", Object: object},
	)

	client := fga.New(url, store, key, nil)
	a := authz.New(client, "")

	all := []authz.Action{authz.ReadTenant, authz.ReadInstances, authz.WriteInstances, authz.ReadImages, authz.WriteImages, authz.ReadMemberships, authz.WriteMemberships}
	tests := []struct {
		who   string
		id    auth.Identity
		allow []authz.Action
	}{
		{"admin", user("admin"), all},
		{"operator", user("operator"), []authz.Action{authz.ReadTenant, authz.ReadInstances, authz.WriteInstances, authz.ReadImages, authz.WriteImages}},
		{"user", user("user"), []authz.Action{authz.ReadTenant, authz.ReadInstances, authz.WriteInstances, authz.ReadImages}},
		{"viewer", user("viewer"), []authz.Action{authz.ReadTenant, authz.ReadInstances, authz.ReadImages}},
		{"key tenant:read + instances:write", key1, []authz.Action{authz.ReadTenant, authz.WriteInstances}},
		{"stranger", user("stranger"), nil},
	}
	for _, tt := range tests {
		for _, action := range all {
			t.Run(tt.who+"/"+string(action), func(t *testing.T) {
				got, err := a.Allowed(t.Context(), tt.id, action, tenant)
				if err != nil {
					t.Fatal(err)
				}
				if want := slices.Contains(tt.allow, action); got != want {
					t.Errorf("allowed = %v, want %v", got, want)
				}
			})
		}
	}

	t.Run("a key does not act on another tenant", func(t *testing.T) {
		other := key1
		other.Tenant = "apitest-authz-other"
		if ok, err := a.Allowed(t.Context(), other, authz.ReadTenant, "apitest-authz-other"); err != nil || ok {
			t.Errorf("ok=%v err=%v", ok, err)
		}
	})

	t.Run("tenants a caller sees", func(t *testing.T) {
		seen, err := a.Tenants(t.Context(), user("viewer"))
		if err != nil || !slices.Equal(seen, []string{tenant}) {
			t.Errorf("viewer sees %v (err %v), want only %s", seen, err, tenant)
		}
		seen, err = a.Tenants(t.Context(), key1)
		if err != nil || !slices.Equal(seen, []string{tenant}) {
			t.Errorf("the key sees %v (err %v), want only %s", seen, err, tenant)
		}
		seen, err = a.Tenants(t.Context(), user("stranger"))
		if err != nil || len(seen) != 0 {
			t.Errorf("a stranger sees %v (err %v)", seen, err)
		}
	})

	t.Run("a key never exceeds its creator", func(t *testing.T) {
		// An operator may give what an operator holds, not the memberships.
		missing, err := a.Missing(t.Context(), user("operator"), tenant, authz.Scopes())
		if err != nil || !slices.Equal(missing, []string{"memberships:read", "memberships:write"}) {
			t.Errorf("operator: missing=%v err=%v", missing, err)
		}
		missing, err = a.Missing(t.Context(), user("admin"), tenant, authz.Scopes())
		if err != nil || len(missing) != 0 {
			t.Errorf("admin: missing=%v err=%v", missing, err)
		}
		// A key holds only its scopes, so it can only give those.
		missing, err = a.Missing(t.Context(), key1, tenant, []string{"tenant:read", "instances:read", "instances:write"})
		if err != nil || !slices.Equal(missing, []string{"instances:read"}) {
			t.Errorf("key: missing=%v err=%v", missing, err)
		}
	})
}
