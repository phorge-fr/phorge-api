package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/kube"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/tenants"
)

// TestTenantLifecycleOnTheRealCluster creates a tenant through the routes, on the real control cluster and the real
// OpenFGA store, waits for it, changes it, deletes it and checks that nothing is left. It takes about five minutes and
// is skipped unless the cluster and the store are given:
//
//	kubectl --context core -n openfga port-forward svc/openfga 18080:8080 &
//	export PHORGE_KUBE_CONTEXT=control
//	export PHORGE_FGA_URL=http://127.0.0.1:18080 PHORGE_FGA_STORE=01M50R0P12MY8JHNKP3ATYSJC2
//	export PHORGE_FGA_KEY=$(kubectl --context control -n phorge-api get secret phorge-api-openfga -o jsonpath='{.data.apiKey}' | base64 -d)
//	go test -run TestTenantLifecycle -timeout 20m ./internal/api
func TestTenantLifecycleOnTheRealCluster(t *testing.T) {
	kubeContext := os.Getenv("PHORGE_KUBE_CONTEXT")
	url, storeID, key := os.Getenv("PHORGE_FGA_URL"), os.Getenv("PHORGE_FGA_STORE"), os.Getenv("PHORGE_FGA_KEY")
	if kubeContext == "" || url == "" || storeID == "" || key == "" {
		t.Skip("PHORGE_KUBE_CONTEXT, PHORGE_FGA_URL, PHORGE_FGA_STORE and PHORGE_FGA_KEY are not set")
	}

	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	name := "apitest-api-" + hex.EncodeToString(suffix)
	ownerEmail := "owner-" + name + "@example.org"

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn := kube.NewConnection(kubeContext)
	store := kube.NewTenants(conn)
	users := tokens{
		"admin":    {Kind: auth.KindUser, Subject: "user:admin-" + name + "@example.org", Email: "admin-" + name + "@example.org", Groups: []string{adminGroup}},
		"owner":    {Kind: auth.KindUser, Subject: "user:" + ownerEmail, Email: ownerEmail},
		"stranger": {Kind: auth.KindUser, Subject: "user:stranger-" + name + "@example.org", Email: "stranger-" + name + "@example.org"},
	}
	fgaClient := fga.New(url, storeID, key, nil)
	s := server.New(log)
	Register(s, Deps{
		Auth:    &auth.Authenticator{Users: users, Keys: tokens{}, Log: log},
		Authz:   authz.New(fgaClient, adminGroup, nil),
		Tenants: store,
		Log:     log,
	})
	e := &env{srv: s}

	// Whatever happens, the tenant does not outlive the test.
	t.Cleanup(func() {
		// The context of the test is already cancelled here.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.Delete(ctx, name); err == nil {
			t.Errorf("the tenant %s was still there at the end of the test, it is being deleted: check that it is gone", name)
		}
	})

	t.Log("create", name)
	rec := e.do(t, http.MethodPost, "/v1/tenants", "admin", `{"name":"`+name+`","owner":"`+ownerEmail+`","displayName":"created by the API tests"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body.String())
	}

	started := time.Now()
	var got tenantBody
	waitFor(t, 5*time.Minute, "the tenant to become Ready", func() bool {
		rec := e.do(t, http.MethodGet, "/v1/tenants/"+name, "admin", "")
		mustDecode(t, rec, &got)
		if got.Phase == tenants.Failed {
			t.Fatalf("the tenant failed: %s", got.Problems)
		}
		return got.Phase == tenants.Ready
	})
	t.Logf("ready after %s: namespace %s, project %s", time.Since(started).Round(time.Second), got.Namespace, got.IncusProject)
	if got.Namespace != "t-"+name || got.IncusProject != "t-"+name || got.Plan != tenants.Small || got.Owner != ownerEmail {
		t.Errorf("unexpected tenant: %+v", got)
	}

	// The composition wrote the owner as admin of the tenant in the store of the API: the owner sees it, a stranger does not.
	waitFor(t, 2*time.Minute, "the owner to see the tenant", func() bool {
		return e.do(t, http.MethodGet, "/v1/tenants/"+name, "owner", "").Code == http.StatusOK
	})
	if code := e.do(t, http.MethodGet, "/v1/tenants/"+name, "stranger", "").Code; code != http.StatusNotFound {
		t.Errorf("a stranger gets %d, want 404", code)
	}
	if code := e.do(t, http.MethodPatch, "/v1/tenants/"+name, "owner", `{"plan":"large"}`).Code; code != http.StatusForbidden {
		t.Errorf("the owner resizing the tenant gets %d, want 403", code)
	}
	if code := e.do(t, http.MethodDelete, "/v1/tenants/"+name, "owner", "").Code; code != http.StatusForbidden {
		t.Errorf("the owner deleting the tenant gets %d, want 403", code)
	}
	if listed := names(t, e.do(t, http.MethodGet, "/v1/tenants", "owner", "")); !slices.Equal(listed, []string{name}) {
		t.Errorf("the owner sees %v, want only %s", listed, name)
	}

	// Resize.
	rec = e.do(t, http.MethodPatch, "/v1/tenants/"+name, "admin", `{"plan":"medium","displayName":"resized"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: status %d: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, 3*time.Minute, "the resized tenant to be Ready again", func() bool {
		mustDecode(t, e.do(t, http.MethodGet, "/v1/tenants/"+name, "admin", ""), &got)
		return got.Plan == tenants.Medium && got.DisplayName == "resized" && got.Phase == tenants.Ready
	})

	// Delete, in the foreground: it is Deleting until everything is gone, and then it is not found.
	rec = e.do(t, http.MethodDelete, "/v1/tenants/"+name, "admin", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete: status %d: %s", rec.Code, rec.Body.String())
	}
	deleting := time.Now()
	sawDeleting := false
	waitFor(t, 6*time.Minute, "the tenant to be gone", func() bool {
		rec := e.do(t, http.MethodGet, "/v1/tenants/"+name, "admin", "")
		if rec.Code == http.StatusOK {
			mustDecode(t, rec, &got)
			sawDeleting = sawDeleting || got.Phase == tenants.Deleting
		}
		return rec.Code == http.StatusNotFound
	})
	t.Logf("gone after %s (seen as Deleting: %v)", time.Since(deleting).Round(time.Second), sawDeleting)

	// Nothing is left in Kubernetes: the foreground deletion waited for the namespace, the network and the project.
	client, err := conn.Dynamic()
	if err != nil {
		t.Fatal(err)
	}
	namespaces := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	if _, err := client.Resource(namespaces).Get(t.Context(), "t-"+name, metav1.GetOptions{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("the namespace t-%s is still there (err %v)", name, err)
	}

	// The tuple of the owner goes within a minute of the deletion, asked of OpenFGA itself.
	waitFor(t, 3*time.Minute, "the tuple of the owner to be removed", func() bool {
		allowed, err := fgaClient.Check(t.Context(), "user:"+ownerEmail, "can_read_tenant", "tenant:"+name)
		return err == nil && !allowed
	})
}

func waitFor(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(5 * time.Second)
	}
}
