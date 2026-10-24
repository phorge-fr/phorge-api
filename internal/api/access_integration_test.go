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

// TestMembershipsAndKeysOnTheRealCluster checks, on the real control cluster and OpenFGA store, that what a role or a scope
// grants is what the routes then allow: a member sees only what the role allows, a key authenticated against its real
// object works within its scopes, stops at its date and when it is deleted. It takes about five minutes and is skipped unless
// the cluster and the store are given, see TestTenantLifecycleOnTheRealCluster for how.
//
//	go test -run TestMembershipsAndKeys -timeout 30m ./internal/api
func TestMembershipsAndKeysOnTheRealCluster(t *testing.T) {
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
	admin := user("admin")
	admin.Groups = []string{adminGroup}
	users := tokens{"admin": admin, "owner": user("owner"), "bob": user("bob")}
	s := server.New(log)
	Register(s, Deps{
		// The keys are verified against their real objects, with their real hash: this is what a client would meet.
		Auth:        &auth.Authenticator{Users: users, Keys: auth.NewKeyVerifier(kube.NewKeys(conn)), Log: log},
		Authz:       authz.New(fga.New(url, storeID, key, nil), adminGroup),
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
	if code := call(t, e, "POST", "/v1/tenants", "admin", `{"name":"`+tenant+`","owner":"`+email("owner")+`"}`); code != http.StatusAccepted {
		t.Fatalf("create tenant: %d", code)
	}
	var tn tenantBody
	waitFor(t, 5*time.Minute, "the tenant to be Ready", func() bool {
		mustDecode(t, e.do(t, "GET", base, "admin", ""), &tn)
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
		{"POST", base + "/apikeys", `{"name":"k","scopes":["tenant:read"]}`, http.StatusForbidden},
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

	// ---- A key: its scopes, its date, its deletion ----
	rec = e.do(t, "POST", base+"/apikeys", "owner", `{"name":"ci","scopes":["tenant:read","instances:read"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create the key: %d %s", rec.Code, rec.Body.String())
	}
	var created keyBody
	mustDecode(t, rec, &created)
	ci := created.Key
	waitFor(t, 3*time.Minute, "the key to read the tenant", func() bool { return call(t, e, "GET", base, ci, "") == http.StatusOK })
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", base + "/instances", "", http.StatusOK},
		{"GET", base + "/images", "", http.StatusForbidden},
		{"POST", base + "/instances", `{"name":"web","image":"nope"}`, http.StatusForbidden},
		{"GET", base + "/memberships", "", http.StatusForbidden},
		{"POST", base + "/apikeys", `{"name":"k","scopes":["tenant:read"]}`, http.StatusForbidden},
		{"GET", "/v1/whoami", "", http.StatusOK},
	} {
		if code := call(t, e, c.method, c.path, ci, c.body); code != c.want {
			t.Errorf("the key ci: %s %s: %d, want %d", c.method, c.path, code, c.want)
		}
	}
	// A wrong secret is the same refusal as no key at all.
	wrong := ci[:len(ci)-4] + "0000"
	if code := call(t, e, "GET", base, wrong, ""); code != http.StatusUnauthorized {
		t.Errorf("a wrong secret: %d", code)
	}

	// Taking a scope away takes effect within seconds.
	if code := call(t, e, "PATCH", base+"/apikeys/ci", "owner", `{"scopes":["tenant:read"]}`); code != http.StatusOK {
		t.Fatalf("patch the scopes: %d", code)
	}
	waitFor(t, 2*time.Minute, "the scope to be taken away", func() bool {
		return call(t, e, "GET", base+"/instances", ci, "") == http.StatusForbidden
	})

	// A key that manages keys gives no more than it holds.
	rec = e.do(t, "POST", base+"/apikeys", "owner", `{"name":"mgr","scopes":["tenant:read","memberships:read","memberships:write"]}`)
	var mgr keyBody
	mustDecode(t, rec, &mgr)
	waitFor(t, 3*time.Minute, "the manager key to work", func() bool { return call(t, e, "GET", base+"/apikeys", mgr.Key, "") == http.StatusOK })
	if code := call(t, e, "POST", base+"/apikeys", mgr.Key, `{"name":"child","scopes":["tenant:read"]}`); code != http.StatusAccepted {
		t.Errorf("a key giving a subset of its scopes: %d", code)
	}
	if code := call(t, e, "POST", base+"/apikeys", mgr.Key, `{"name":"greedy","scopes":["instances:write"]}`); code != http.StatusForbidden {
		t.Errorf("a key giving a scope it does not hold: %d", code)
	}

	// A key stops at its date, to the second: the API checks the date, it does not wait for the composition.
	expires := time.Now().Add(45 * time.Second).UTC()
	rec = e.do(t, "POST", base+"/apikeys", "owner", `{"name":"short","scopes":["tenant:read"],"expiresAt":"`+expires.Format(time.RFC3339)+`"}`)
	var short keyBody
	mustDecode(t, rec, &short)
	waitFor(t, 40*time.Second, "the short key to work", func() bool { return call(t, e, "GET", base, short.Key, "") == http.StatusOK })
	time.Sleep(time.Until(expires.Add(time.Second)))
	if code := call(t, e, "GET", base, short.Key, ""); code != http.StatusUnauthorized {
		t.Errorf("a key one second after its date: %d, want 401", code)
	}

	// A key stops at once when it is deleted.
	if code := call(t, e, "DELETE", base+"/apikeys/ci", "owner", ""); code != http.StatusAccepted {
		t.Fatalf("delete the key: %d", code)
	}
	if code := call(t, e, "GET", base, ci, ""); code != http.StatusUnauthorized {
		t.Errorf("a key that is being deleted: %d, want 401 at once, not after its tuples are gone", code)
	}
	var listed struct{ Items []keyBody }
	mustDecode(t, e.do(t, "GET", base+"/apikeys", "owner", ""), &listed)
	for _, k := range listed.Items {
		if k.Key != "" {
			t.Errorf("the list shows the key of %s", k.Name)
		}
		if k.Name == "short" && !k.Expired {
			t.Errorf("the short key is not shown as expired: %+v", k)
		}
	}

	// And the tenant, with the keys and memberships in it.
	if code := call(t, e, "DELETE", base, "admin", ""); code != http.StatusAccepted {
		t.Fatalf("delete the tenant: %d", code)
	}
	waitFor(t, 8*time.Minute, "the tenant to be gone", func() bool {
		return call(t, e, "GET", base, "admin", "") == http.StatusNotFound
	})
}
