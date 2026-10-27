package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/auth"
	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/fga"
	"github.com/phorge-fr/phorge-api/internal/kube"
	"github.com/phorge-fr/phorge-api/internal/server"
	"github.com/phorge-fr/phorge-api/internal/status"
)

// TestImageAndInstanceOnTheRealCluster pulls a real image and boots a real VM through the routes, in a tenant of its own, on
// the real control cluster, Incus and OpenFGA store. It takes ten to twenty minutes (the image pull and the boot) and is
// skipped unless the cluster and the store are given, see TestTenantLifecycleOnTheRealCluster for how.
//
//	go test -run TestImageAndInstance -timeout 40m ./internal/api
func TestImageAndInstanceOnTheRealCluster(t *testing.T) {
	kubeContext := os.Getenv("PHORGE_KUBE_CONTEXT")
	url, storeID, key := os.Getenv("PHORGE_FGA_URL"), os.Getenv("PHORGE_FGA_STORE"), os.Getenv("PHORGE_FGA_KEY")
	if kubeContext == "" || url == "" || storeID == "" || key == "" {
		t.Skip("PHORGE_KUBE_CONTEXT, PHORGE_FGA_URL, PHORGE_FGA_STORE and PHORGE_FGA_KEY are not set")
	}

	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	tenant := "apitest-vm-" + hex.EncodeToString(suffix)
	ownerEmail := "owner-" + tenant + "@example.org"

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn := kube.NewConnection(kubeContext)
	tenantStore, imageStore, instanceStore := kube.NewTenants(conn), kube.NewImages(conn), kube.NewInstances(conn)
	users := tokens{
		"admin":    {Kind: auth.KindUser, Subject: "user:admin-" + tenant + "@example.org", Email: "admin-" + tenant + "@example.org", Groups: []string{adminGroup}},
		"owner":    {Kind: auth.KindUser, Subject: "user:" + ownerEmail, Email: ownerEmail},
		"stranger": {Kind: auth.KindUser, Subject: "user:stranger-" + tenant + "@example.org", Email: "stranger-" + tenant + "@example.org"},
	}
	s := server.New(log)
	Register(s, Deps{
		Settings:  testSettings,
		Auth:      &auth.Authenticator{Users: users, Keys: tokens{}, Log: log},
		Authz:     authz.New(fga.New(url, storeID, key, nil), adminGroup, nil),
		Tenants:   tenantStore,
		Images:    imageStore,
		Instances: instanceStore,
		Log:       log,
	})
	e := &env{srv: s}

	// Whatever happens the tenant does not outlive the test; deleting it deletes what is in it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := tenantStore.Delete(ctx, tenant); err == nil {
			t.Errorf("the tenant %s was still there at the end of the test, it is being deleted: check that it is gone", tenant)
		}
	})

	base := "/v1/tenants/" + tenant
	t.Log("tenant", tenant)
	if code := call(t, e, "POST", "/v1/tenants", "admin", `{"name":"`+tenant+`","owner":"`+ownerEmail+`"}`); code != http.StatusAccepted {
		t.Fatalf("create tenant: %d", code)
	}
	var tn tenantBody
	waitFor(t, 5*time.Minute, "the tenant to be Ready", func() bool {
		mustDecode(t, e.do(t, "GET", base, "admin", ""), &tn)
		return tn.Phase == "Ready"
	})
	waitFor(t, 2*time.Minute, "the owner to be admin of the tenant", func() bool {
		return call(t, e, "GET", base+"/images", "owner", "") == http.StatusOK
	})
	if code := call(t, e, "GET", base+"/images", "stranger", ""); code != http.StatusNotFound {
		t.Errorf("a stranger on the images: %d", code)
	}

	// An image: pulled for a few minutes, during which no instance can start from it.
	pulled := time.Now()
	rec := e.do(t, "POST", base+"/images", "owner", `{"name":"debian","source":{"name":"debian/13/cloud"},"aliases":["debian-13"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create image: %d %s", rec.Code, rec.Body.String())
	}
	sawNotReady := false
	var img imageBody
	waitFor(t, 15*time.Minute, "the image to be Ready", func() bool {
		mustDecode(t, e.do(t, "GET", base+"/images/debian", "owner", ""), &img)
		if img.Phase == status.Failed {
			t.Fatalf("the image failed: %s", img.Problems)
		}
		if img.Phase != status.Ready && !sawNotReady {
			sawNotReady = true
			if code := call(t, e, "POST", base+"/instances", "owner", `{"name":"early","image":"debian"}`); code != http.StatusConflict {
				t.Errorf("an instance from an image that is being pulled: %d, want 409", code)
			}
		}
		return img.Phase == status.Ready
	})
	t.Logf("image ready after %s, fingerprint %.12s, seen not ready: %v", time.Since(pulled).Round(time.Second), img.Fingerprint, sawNotReady)
	if len(img.Fingerprint) != 64 {
		t.Errorf("fingerprint = %q", img.Fingerprint)
	}

	// An instance from it, with a key.
	const sshKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl apitest@example.org"
	booted := time.Now()
	rec = e.do(t, "POST", base+"/instances", "owner", `{"name":"web","image":"debian","cpu":1,"memoryMiB":1024,"diskGiB":10,"sshPublicKeys":["`+sshKey+`"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create instance: %d %s", rec.Code, rec.Body.String())
	}
	var vm instanceBody
	waitFor(t, 10*time.Minute, "the instance to be Ready and Running", func() bool {
		mustDecode(t, e.do(t, "GET", base+"/instances/web", "owner", ""), &vm)
		if vm.Phase == status.Failed {
			t.Fatalf("the instance failed: %s", vm.Problems)
		}
		return vm.Phase == status.Ready && vm.State == "Running"
	})
	t.Logf("instance running after %s, ipv4 %q", time.Since(booted).Round(time.Second), vm.IPv4)
	if vm.Image != "debian" || vm.CPU != 1 || vm.MemoryMiB != 1024 || vm.DiskGiB != 10 || !vm.Running || len(vm.SSHPublicKeys) != 1 {
		t.Errorf("unexpected instance: %+v", vm)
	}

	// Stop and start: the same VM.
	for _, step := range []struct {
		running bool
		state   string
	}{{false, "Stopped"}, {true, "Running"}} {
		body := `{"running":false}`
		if step.running {
			body = `{"running":true}`
		}
		if code := call(t, e, "PATCH", base+"/instances/web", "owner", body); code != http.StatusOK {
			t.Fatalf("patch %s: %d", body, code)
		}
		waitFor(t, 5*time.Minute, "the instance to be "+step.state, func() bool {
			mustDecode(t, e.do(t, "GET", base+"/instances/web", "owner", ""), &vm)
			return vm.State == step.state && vm.Phase == status.Ready
		})
	}

	// The plan small allows one VM: Incus refuses the second, and says so in problems.
	if code := call(t, e, "POST", base+"/instances", "owner", `{"name":"second","image":"debian"}`); code != http.StatusAccepted {
		t.Fatalf("create the second instance: %d", code)
	}
	var second instanceBody
	waitFor(t, 8*time.Minute, "the second instance to fail on the quota", func() bool {
		mustDecode(t, e.do(t, "GET", base+"/instances/second", "owner", ""), &second)
		return second.Phase == status.Failed
	})
	t.Logf("the quota refusal, as it comes: %s", second.Problems)
	if !strings.Contains(second.Problems, "maximum") {
		t.Errorf("problems = %q, want the refusal of Incus", second.Problems)
	}
	if code := call(t, e, "DELETE", base+"/instances/second", "owner", ""); code != http.StatusAccepted {
		t.Errorf("delete the second instance: %d", code)
	}
	waitFor(t, 5*time.Minute, "the second instance to be gone", func() bool {
		return call(t, e, "GET", base+"/instances/second", "owner", "") == http.StatusNotFound
	})

	// An image that an instance uses cannot be deleted: through the routes it is a 409 that names the instance, and what
	// Crossplane itself answers is logged, since the status of its webhook is not documented.
	rec = e.do(t, "DELETE", base+"/images/debian", "owner", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "web") {
		t.Errorf("delete an image in use: %d %s", rec.Code, rec.Body.String())
	}
	err := imageStore.Delete(t.Context(), tenant, "debian")
	var inUse *compute.InUseError
	t.Logf("what the cluster answers to the deletion of an image in use: %v (an InUseError: %v)", err, errors.As(err, &inUse))
	if err == nil {
		t.Log("the deletion was accepted: the image goes by itself once the instance is gone")
	}

	// Delete the instance in the foreground, then the image.
	if code := call(t, e, "DELETE", base+"/instances/web", "owner", ""); code != http.StatusAccepted {
		t.Fatalf("delete the instance: %d", code)
	}
	waitFor(t, 6*time.Minute, "the instance to be gone", func() bool {
		return call(t, e, "GET", base+"/instances/web", "owner", "") == http.StatusNotFound
	})
	// Crossplane keeps refusing for a few seconds after the last instance is gone (a 409 that names nobody): try again.
	// The refusal that was logged above is remembered, so the image may also go by itself, which is a 404.
	waitFor(t, 3*time.Minute, "the deletion of the image to be accepted", func() bool {
		code := call(t, e, "DELETE", base+"/images/debian", "owner", "")
		if code != http.StatusAccepted && code != http.StatusNotFound && code != http.StatusConflict {
			t.Fatalf("delete the image: %d", code)
		}
		return code != http.StatusConflict
	})
	waitFor(t, 5*time.Minute, "the image to be gone", func() bool {
		return call(t, e, "GET", base+"/images/debian", "owner", "") == http.StatusNotFound
	})

	// And the tenant, which must leave nothing in Incus (its project cannot be deleted while it holds something).
	if code := call(t, e, "DELETE", base, "admin", ""); code != http.StatusAccepted {
		t.Fatalf("delete the tenant: %d", code)
	}
	waitFor(t, 8*time.Minute, "the tenant to be gone", func() bool {
		return call(t, e, "GET", base, "admin", "") == http.StatusNotFound
	})
}
