package authz_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"testing"
	"time"

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
	// A key is checked as itself, with the tuples that expire: one that is valid, one that expired, one with no tuple.
	keyOf := func(name string) auth.Identity {
		return auth.Identity{Kind: auth.KindServiceKey, Subject: "apikey:apikey-" + name, Name: "apikey-" + name}
	}
	valid, expired, nothing := keyOf("valid-"+tenant), keyOf("expired-"+tenant), keyOf("nothing-"+tenant)
	later, earlier := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)

	fgatest.Store{BaseURL: url, StoreID: store, Key: key}.Write(t,
		fgatest.Tuple{User: user("admin").Subject, Relation: "admin", Object: object},
		fgatest.Tuple{User: user("operator").Subject, Relation: "operator", Object: object},
		fgatest.Tuple{User: user("user").Subject, Relation: "user", Object: object},
		fgatest.Tuple{User: user("viewer").Subject, Relation: "viewer", Object: object},
		fgatest.Tuple{User: valid.Subject, Relation: "key_tenant_read", Object: object, ExpiresAt: later},
		fgatest.Tuple{User: valid.Subject, Relation: "key_instances_write", Object: object, ExpiresAt: later},
		fgatest.Tuple{User: expired.Subject, Relation: "key_tenant_read", Object: object, ExpiresAt: earlier},
		fgatest.Tuple{User: expired.Subject, Relation: "key_instances_write", Object: object, ExpiresAt: earlier},
	)

	client := fga.New(url, store, key, nil)
	a := authz.New(client)

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
		// A key has what its tuples say until their date, and nothing else: no role is derived for it.
		{"key with tenants:read and instances:write", valid, []authz.Action{authz.ReadTenant, authz.WriteInstances}},
		{"key past its date", expired, nil},
		{"key with no tuple", nothing, nil},
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

	t.Run("tenants a caller sees", func(t *testing.T) {
		seen, err := a.Tenants(t.Context(), user("viewer"))
		if err != nil || !slices.Equal(seen, []string{tenant}) {
			t.Errorf("viewer sees %v (err %v), want only %s", seen, err, tenant)
		}
		seen, err = a.Tenants(t.Context(), valid)
		if err != nil || !slices.Equal(seen, []string{tenant}) {
			t.Errorf("the key sees %v (err %v), want only %s", seen, err, tenant)
		}
		// Past its date a key sees nothing.
		seen, err = a.Tenants(t.Context(), expired)
		if err != nil || len(seen) != 0 {
			t.Errorf("a key past its date sees %v (err %v)", seen, err)
		}
		seen, err = a.Tenants(t.Context(), user("stranger"))
		if err != nil || len(seen) != 0 {
			t.Errorf("a stranger sees %v (err %v)", seen, err)
		}
	})
}
