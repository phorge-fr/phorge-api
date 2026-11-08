package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/kube"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// TestMembershipsOnTheRealCluster checks, on the real control cluster and OpenFGA store, that what a role grants is what the
// routes then allow: a member sees only what the role allows, and nothing once the membership is deleted. The API keys are
// not here: creating one makes a service account in Authentik that nothing can delete afterwards (the identity of Crossplane
// cannot), so they are tested end to end by the opt-in group of Hangar (tests/e2e, E2E_APIKEYS=1). It takes about five
// minutes and is skipped unless the cluster and the store are given, see TestTenantLifecycleOnTheRealCluster for how.
//
//	go test -run TestMembershipsOnTheRealCluster -timeout 30m ./internal/api
func TestMembershipsOnTheRealCluster(t *testing.T) {
	kubeContext := os.Getenv("PHORGE_KUBE_CONTEXT")
	url, storeID, key := os.Getenv("PHORGE_FGA_URL"), os.Getenv("PHORGE_FGA_STORE"), os.Getenv("PHORGE_FGA_KEY")
	if kubeContext == "" || url == "" || storeID == "" || key == "" {
		t.Skip("PHORGE_KUBE_CONTEXT, PHORGE_FGA_URL, PHORGE_FGA_STORE and PHORGE_FGA_KEY are not set")
	}

	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	tenant := "apitest-acc-" + hex.EncodeToString(suffix)
	email := func(who string) string { return who + "-" + tenant + "@example.org" }
	user := func(who string) auth.Identity {
		return auth.Identity{Kind: auth.KindUser, Subject: "user:" + email(who), Email: email(who)}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn := kube.NewConnection(kubeContext)
	tenantStore := kube.NewTenants(conn)
	users := tokens{"owner": user("owner"), "bob": user("bob")}
	s := server.New(log)
	Register(s, Deps{
		Settings:    testSettings,
		Auth:        &auth.Authenticator{Users: users, Log: log},
		Authz:       authz.New(fga.New(url, storeID, key, nil)),
		Tenants:     tenantStore,
		Images:      kube.NewImages(conn),
		Instances:   kube.NewInstances(conn),
		Memberships: kube.NewMemberships(conn),
		Keys:        kube.NewApiKeys(conn),
		Log:         log,
	})
	e := &env{srv: s}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tenantStore.Delete(ctx, tenant); err == nil {
			t.Errorf("the tenant %s was still there at the end of the test, it is being deleted: check that it is gone", tenant)
		}
	})

	base := "/v1/tenants/" + tenant
	t.Log("tenant", tenant)
	if code := call(t, e, "POST", "/v1/tenants", "owner", `{"name":"`+tenant+`"}`); code != http.StatusAccepted {
		t.Fatalf("create tenant: %d", code)
	}
	var tn tenantBody
	waitFor(t, 5*time.Minute, "the tenant to be Ready", func() bool {
		mustDecode(t, e.do(t, "GET", base, "owner", ""), &tn)
		return tn.Phase == status.Ready
	})
	waitFor(t, 2*time.Minute, "the owner to be admin", func() bool {
		return call(t, e, "GET", base+"/memberships", "owner", "") == http.StatusOK
	})

	// ---- A member: what the role allows, nothing else ----
	bobBody := `{"subject":{"kind":"User","name":"` + email("bob") + `"},"role":"viewer"}`
	rec := e.do(t, "POST", base+"/memberships", "owner", bobBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create the membership: %d %s", rec.Code, rec.Body.String())
	}
	var ms membershipBody
	mustDecode(t, rec, &ms)
	if code := call(t, e, "GET", base, "bob", ""); code != http.StatusNotFound {
		// Normally still a 404: the tuple is written a few seconds after the object. Not an error if it was quick.
		t.Logf("bob already sees the tenant (%d)", code)
	}
	waitFor(t, 3*time.Minute, "bob to see the tenant", func() bool { return call(t, e, "GET", base, "bob", "") == http.StatusOK })
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", base + "/instances", "", http.StatusOK},
		{"GET", base + "/images", "", http.StatusOK},
		{"POST", base + "/instances", `{"name":"web","image":"nope"}`, http.StatusForbidden},
		{"POST", base + "/images", `{"name":"x","source":{"name":"alpine/3.22"}}`, http.StatusForbidden},
		{"GET", base + "/memberships", "", http.StatusForbidden},
		{"PATCH", base, `{"plan":"large"}`, http.StatusForbidden},
		{"DELETE", base, "", http.StatusForbidden},
	} {
		if code := call(t, e, c.method, c.path, "bob", c.body); code != c.want {
			t.Errorf("a viewer: %s %s: %d, want %d", c.method, c.path, code, c.want)
		}
	}
	if code := call(t, e, "POST", base+"/memberships", "owner", bobBody); code != http.StatusConflict {
		t.Errorf("the same user twice: %d", code)
	}
	if code := call(t, e, "POST", base+"/memberships", "owner", `{"subject":{"kind":"User","name":"`+email("owner")+`"},"role":"viewer"}`); code != http.StatusConflict {
		t.Errorf("a membership for the owner: %d", code)
	}

	// A role is changed by deleting the membership and creating another: as a user, bob may use instances (the image does
	// not exist, which is a 422, not a 403).
	if code := call(t, e, "DELETE", base+"/memberships/"+ms.Name, "owner", ""); code != http.StatusAccepted {
		t.Fatalf("delete the membership: %d", code)
	}
	waitFor(t, 3*time.Minute, "the membership to be gone", func() bool {
		return call(t, e, "GET", base+"/memberships/"+ms.Name, "owner", "") == http.StatusNotFound
	})
	if code := call(t, e, "POST", base+"/memberships", "owner", strings.Replace(bobBody, "viewer", "user", 1)); code != http.StatusAccepted {
		t.Fatalf("create the second membership: %d", code)
	}
	waitFor(t, 3*time.Minute, "bob to be allowed to write instances", func() bool {
		return call(t, e, "POST", base+"/instances", "bob", `{"name":"web","image":"nope"}`) == http.StatusUnprocessableEntity
	})
	if code := call(t, e, "GET", base+"/memberships", "bob", ""); code != http.StatusForbidden {
		t.Errorf("a user reading the memberships: %d", code)
	}
	mustDecode(t, e.do(t, "GET", base+"/memberships", "owner", ""), &struct{}{})

	// Bob is revoked: the tuples go within about a minute of the deletion.
	rec = e.do(t, "GET", base+"/memberships", "owner", "")
	var list struct{ Items []membershipBody }
	mustDecode(t, rec, &list)
	for _, m := range list.Items {
		if code := call(t, e, "DELETE", base+"/memberships/"+m.Name, "owner", ""); code != http.StatusAccepted {
			t.Errorf("delete %s: %d", m.Name, code)
		}
	}
	revoked := time.Now()
	waitFor(t, 4*time.Minute, "bob to lose the tenant", func() bool { return call(t, e, "GET", base, "bob", "") == http.StatusNotFound })
	t.Logf("bob revoked after %s", time.Since(revoked).Round(time.Second))

	// And the tenant, with the memberships in it.
	if code := call(t, e, "DELETE", base, "owner", ""); code != http.StatusAccepted {
		t.Fatalf("delete the tenant: %d", code)
	}
	waitFor(t, 8*time.Minute, "the tenant to be gone", func() bool {
		return call(t, e, "GET", base, "owner", "") == http.StatusNotFound
	})
}
