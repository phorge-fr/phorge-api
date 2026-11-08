package kube

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

func newNetworkStore() (*Networks, *fake.FakeDynamicClient) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{networksGVR: "NetworkList"})
	return NewNetworks(Static(client)), client
}

func TestNetworkCreateWritesTheObject(t *testing.T) {
	s, client := newNetworkStore()
	got, err := s.Create(t.Context(), "demo", compute.CreateNetwork{Name: "lan", Description: "office", CreatedBy: "user:a@b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lan" || got.Description != "office" || got.CreatedBy != "user:a@b" {
		t.Errorf("unexpected network: %+v", got)
	}
	obj, err := client.Resource(networksGVR).Namespace("t-demo").Get(t.Context(), "lan", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAPIVersion() != "phorge.fr/v1alpha1" || obj.GetKind() != "Network" || obj.GetNamespace() != "t-demo" {
		t.Errorf("wrong object: %s %s in %s", obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace())
	}
	// The kind has no field for the addresses or the NAT: nothing but the description may be written.
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if len(spec) != 1 || spec["description"] != "office" {
		t.Errorf("spec = %v, want the description and nothing else", spec)
	}
}

func TestNetworkCreateWithoutADescriptionWritesAnEmptySpec(t *testing.T) {
	s, client := newNetworkStore()
	if _, err := s.Create(t.Context(), "demo", compute.CreateNetwork{Name: "lan"}); err != nil {
		t.Fatal(err)
	}
	obj, _ := client.Resource(networksGVR).Namespace("t-demo").Get(t.Context(), "lan", metav1.GetOptions{})
	if spec, _, _ := unstructured.NestedMap(obj.Object, "spec"); len(spec) != 0 {
		t.Errorf("spec = %v: an empty description was sent", spec)
	}
}

func TestNetworkStoreListsGetsAndMapsNotFound(t *testing.T) {
	s, _ := newNetworkStore()
	for _, n := range []string{"b", "a"} {
		if _, err := s.Create(t.Context(), "demo", compute.CreateNetwork{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(t.Context(), "other", compute.CreateNetwork{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(t.Context(), "demo")
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v, %v: the networks of another tenant must not be listed", all, err)
	}
	if _, err := s.Get(t.Context(), "demo", "a"); err != nil {
		t.Error(err)
	}
	if _, err := s.Get(t.Context(), "demo", "ghost"); !errors.Is(err, compute.ErrNotFound) {
		t.Errorf("get: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Create(t.Context(), "demo", compute.CreateNetwork{Name: "a"}); !errors.Is(err, compute.ErrExists) {
		t.Errorf("create twice: err = %v, want ErrExists", err)
	}
}

func TestNetworkDeleteIsInTheForegroundAndInUseIsAConflict(t *testing.T) {
	s, client := newNetworkStore()
	if _, err := s.Create(t.Context(), "demo", compute.CreateNetwork{Name: "lan"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "demo", "lan"); err != nil {
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
			t.Errorf("propagation policy = %v", del.DeleteOptions.PropagationPolicy)
		}
	}
	if !found {
		t.Error("no delete was sent")
	}

	client.PrependReactor("delete", "networks", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook refused: in-use by 1 usage(s)")
	})
	var inUse *compute.InUseError
	if err := s.Delete(t.Context(), "demo", "lan"); !errors.As(err, &inUse) {
		t.Errorf("err = %v, want an InUseError: it is a conflict, not a failure of the cluster", err)
	}
}

func TestNetworkOfReadsTheStatus(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "phorge.fr/v1alpha1", "kind": "Network",
		"metadata": map[string]any{"name": "lan", "namespace": "t-demo", "annotations": map[string]any{CreatedByAnnotation: "user:a@b"}},
		"spec":     map[string]any{"description": "office"},
		"status": map[string]any{
			"problems": "network Synced: x",
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "lastTransitionTime": "2026-11-08T10:00:00Z"},
			},
		},
	}}
	n := networkOf(obj)
	if n.Name != "lan" || n.Description != "office" || n.CreatedBy != "user:a@b" || n.Status.Ready || n.Status.Problems != "network Synced: x" {
		t.Errorf("networkOf = %+v", n)
	}
}
