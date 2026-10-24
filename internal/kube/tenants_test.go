package kube

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/phorge-fr/phorge-api/internal/tenants"
)

func newTenantStore() (*Tenants, *fake.FakeDynamicClient) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{tenantsGVR: "TenantList"})
	return NewTenants(Static(client)), client
}

func TestCreateWritesTheObject(t *testing.T) {
	s, client := newTenantStore()

	got, err := s.Create(t.Context(), tenants.CreateRequest{
		Name: "demo", Owner: "Alice@Example.org", Plan: tenants.Medium, DisplayName: "Demo", CreatedBy: "user:admin@example.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.Owner != "Alice@Example.org" || got.Plan != tenants.Medium || got.CreatedBy != "user:admin@example.org" {
		t.Errorf("unexpected tenant: %+v", got)
	}

	obj, err := client.Resource(tenantsGVR).Namespace(TenantNamespace).Get(t.Context(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAPIVersion() != "phorge.fr/v1alpha1" || obj.GetKind() != "Tenant" || obj.GetNamespace() != "phorge-system" {
		t.Errorf("wrong object: %s %s in %s", obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace())
	}
	owner, _, _ := unstructured.NestedString(obj.Object, "spec", "owner")
	plan, _, _ := unstructured.NestedString(obj.Object, "spec", "plan")
	if owner != "Alice@Example.org" || plan != "medium" {
		t.Errorf("spec: owner %q plan %q", owner, plan)
	}
}

func TestCreateLeavesThePlanToTheDefaultOfTheXRD(t *testing.T) {
	s, client := newTenantStore()
	if _, err := s.Create(t.Context(), tenants.CreateRequest{Name: "demo", Owner: "a@b"}); err != nil {
		t.Fatal(err)
	}
	obj, _ := client.Resource(tenantsGVR).Namespace(TenantNamespace).Get(t.Context(), "demo", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "plan"); found {
		t.Error("a plan was sent although none was asked for: the XRD has a default")
	}
	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "displayName"); found {
		t.Error("an empty display name was sent")
	}
}

func TestCreateTwiceIsAConflict(t *testing.T) {
	s, _ := newTenantStore()
	req := tenants.CreateRequest{Name: "demo", Owner: "a@b"}
	if _, err := s.Create(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), req); !errors.Is(err, tenants.ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestGetAndListAndNotFound(t *testing.T) {
	s, _ := newTenantStore()
	for _, n := range []string{"b-tenant", "a-tenant"} {
		if _, err := s.Create(t.Context(), tenants.CreateRequest{Name: n, Owner: "a@b"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List(t.Context())
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v, %v", all, err)
	}
	if _, err := s.Get(t.Context(), "a-tenant"); err != nil {
		t.Error(err)
	}
	if _, err := s.Get(t.Context(), "ghost"); !errors.Is(err, tenants.ErrNotFound) {
		t.Errorf("get: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(t.Context(), "ghost"); !errors.Is(err, tenants.ErrNotFound) {
		t.Errorf("delete: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Patch(t.Context(), "ghost", tenants.PatchRequest{DisplayName: ptr("x")}); !errors.Is(err, tenants.ErrNotFound) {
		t.Errorf("patch: err = %v, want ErrNotFound", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestPatchChangesOnlyWhatWasGiven(t *testing.T) {
	s, client := newTenantStore()
	if _, err := s.Create(t.Context(), tenants.CreateRequest{Name: "demo", Owner: "a@b", Plan: tenants.Small, DisplayName: "Old"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Patch(t.Context(), "demo", tenants.PatchRequest{Plan: ptr(tenants.Large)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan != tenants.Large || got.DisplayName != "Old" || got.Owner != "a@b" {
		t.Errorf("after the plan: %+v", got)
	}

	if _, err := s.Patch(t.Context(), "demo", tenants.PatchRequest{DisplayName: ptr("")}); err != nil {
		t.Fatal(err)
	}
	obj, _ := client.Resource(tenantsGVR).Namespace(TenantNamespace).Get(t.Context(), "demo", metav1.GetOptions{})
	if name, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName"); name != "" {
		t.Errorf("display name = %q, want it cleared", name)
	}
	if plan, _, _ := unstructured.NestedString(obj.Object, "spec", "plan"); plan != "large" {
		t.Errorf("plan = %q, a patch of the display name changed it", plan)
	}
}

func TestDeleteIsInTheForeground(t *testing.T) {
	s, client := newTenantStore()
	if _, err := s.Create(t.Context(), tenants.CreateRequest{Name: "demo", Owner: "a@b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, a := range client.Actions() {
		del, ok := a.(ktesting.DeleteActionImpl)
		if !ok {
			continue
		}
		found = true
		if del.DeleteOptions.PropagationPolicy == nil || *del.DeleteOptions.PropagationPolicy != metav1.DeletePropagationForeground {
			t.Errorf("propagation policy = %v: in the background the tenant is gone after a second while its resources last a minute", del.DeleteOptions.PropagationPolicy)
		}
	}
	if !found {
		t.Error("no delete was sent")
	}
}

func TestTenantOfReadsTheStatus(t *testing.T) {
	created := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	changed := created.Add(2 * time.Minute)
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "phorge.fr/v1alpha1", "kind": "Tenant",
		"metadata": map[string]any{
			"name": "demo", "namespace": "phorge-system", "creationTimestamp": created.Format(time.RFC3339),
			"annotations": map[string]any{CreatedByAnnotation: "user:admin@example.org"},
		},
		"spec": map[string]any{"owner": "a@b", "plan": "small", "displayName": "Demo"},
		"status": map[string]any{
			"namespace": "t-demo", "incusProject": "t-demo", "problems": "Alias 'x' doesn't exist",
			"conditions": []any{
				map[string]any{"type": "Synced", "status": "True", "lastTransitionTime": created.Format(time.RFC3339)},
				map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": changed.Format(time.RFC3339)},
			},
		},
	}}
	got := tenantOf(obj)
	if got.Ready || !got.NotReadySince.Equal(changed) {
		t.Errorf("Ready=%v NotReadySince=%v, want the time of the Ready condition, not of Synced", got.Ready, got.NotReadySince)
	}
	if got.Namespace != "t-demo" || got.IncusProject != "t-demo" || got.Problems == "" || got.CreatedBy != "user:admin@example.org" || !got.CreatedAt.Equal(created) {
		t.Errorf("unexpected tenant: %+v", got)
	}

	// A tenant that has no condition yet is not ready, since its creation.
	bare := tenantOf(&unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "x", "creationTimestamp": created.Format(time.RFC3339)},
	}})
	if bare.Ready || !bare.NotReadySince.Equal(created) {
		t.Errorf("bare: %+v", bare)
	}

	ready := tenantOf(&unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "x"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}})
	if !ready.Ready {
		t.Error("a Ready=True condition was not read")
	}
}

func TestAnUnreachableClusterIsNotNotFound(t *testing.T) {
	boom := errors.New("no kubeconfig")
	s := NewTenants(&Connection{connect: func() (dynamic.Interface, error) { return nil, boom }})
	for name, call := range map[string]func() error{
		"get":    func() error { _, err := s.Get(t.Context(), "demo"); return err },
		"list":   func() error { _, err := s.List(t.Context()); return err },
		"delete": func() error { return s.Delete(t.Context(), "demo") },
	} {
		if err := call(); !errors.Is(err, boom) || errors.Is(err, tenants.ErrNotFound) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
