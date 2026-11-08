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

func newProfileStore() (*Profiles, *fake.FakeDynamicClient) {
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{profilesGVR: "ProfileList"})
	return NewProfiles(Static(client)), client
}

func TestProfileCreateWritesTheThreeValues(t *testing.T) {
	s, client := newProfileStore()
	got, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "web", Description: "web servers", CPU: ptr(2), MemoryMiB: ptr(2048), DiskGiB: ptr(20), CreatedBy: "user:a@b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web" || got.CPU != 2 || got.MemoryMiB != 2048 || got.DiskGiB != 20 || got.Description != "web servers" || got.CreatedBy != "user:a@b" {
		t.Errorf("unexpected profile: %+v", got)
	}
	obj, err := client.Resource(profilesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetAPIVersion() != "phorge.fr/v1alpha1" || obj.GetKind() != "Profile" || obj.GetNamespace() != "t-demo" {
		t.Errorf("wrong object: %s %s in %s", obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace())
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec["cpu"] != int64(2) || spec["memoryMiB"] != int64(2048) || spec["diskGiB"] != int64(20) || spec["description"] != "web servers" {
		t.Errorf("spec = %v", spec)
	}
}

func TestProfileCreateWithoutADescriptionSendsNone(t *testing.T) {
	s, client := newProfileStore()
	if _, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "web", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); err != nil {
		t.Fatal(err)
	}
	obj, _ := client.Resource(profilesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if _, ok, _ := unstructured.NestedString(obj.Object, "spec", "description"); ok {
		t.Error("an empty description was sent: the composition says what the profile comes to")
	}
}

func TestProfileStoreListsGetsAndMapsErrors(t *testing.T) {
	s, _ := newProfileStore()
	for _, n := range []string{"b", "a"} {
		if _, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: n, CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(t.Context(), "other", compute.CreateProfile{Name: "x", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); err != nil {
		t.Fatal(err)
	}
	all, err := s.List(t.Context(), "demo")
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v, %v: the profiles of another tenant must not be listed", all, err)
	}
	if _, err := s.Get(t.Context(), "demo", "ghost"); !errors.Is(err, compute.ErrNotFound) {
		t.Errorf("get: err = %v", err)
	}
	if _, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "a", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); !errors.Is(err, compute.ErrExists) {
		t.Errorf("create twice: err = %v", err)
	}
}

func TestProfileDeleteIsInTheForegroundAndInUseIsAConflict(t *testing.T) {
	s, client := newProfileStore()
	if _, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "web", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "demo", "web"); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if del, ok := a.(ktesting.DeleteActionImpl); ok {
			if del.DeleteOptions.PropagationPolicy == nil || *del.DeleteOptions.PropagationPolicy != metav1.DeletePropagationForeground {
				t.Errorf("propagation policy = %v", del.DeleteOptions.PropagationPolicy)
			}
		}
	}
	client.PrependReactor("delete", "profiles", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook refused: in-use by 1 usage(s)")
	})
	var inUse *compute.InUseError
	if err := s.Delete(t.Context(), "demo", "web"); !errors.As(err, &inUse) {
		t.Errorf("err = %v, want an InUseError", err)
	}
}
