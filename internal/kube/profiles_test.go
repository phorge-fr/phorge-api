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

func TestProfileCreateWritesTheSettingsItIsGivenAndOnlyThose(t *testing.T) {
	s, client := newProfileStore()
	if _, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "plain", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10)}); err != nil {
		t.Fatal(err)
	}
	obj, _ := client.Resource(profilesGVR).Namespace("t-demo").Get(t.Context(), "plain", metav1.GetOptions{})
	for _, field := range []string{"secureBoot", "sshPublicKeys"} {
		if _, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", field); ok {
			t.Errorf("%s was sent for a profile that does not say it: an absent value is not false nor empty", field)
		}
	}
	// A secure boot of false is a value the profile says: it is written, not dropped as a zero.
	got, err := s.Create(t.Context(), "demo", compute.CreateProfile{Name: "web", CPU: ptr(1), MemoryMiB: ptr(1024), DiskGiB: ptr(10),
		SecureBoot: ptr(false), SSHPublicKeys: []string{key}})
	if err != nil {
		t.Fatal(err)
	}
	if got.SecureBoot == nil || *got.SecureBoot || len(got.SSHPublicKeys) != 1 || got.SSHPublicKeys[0] != key {
		t.Errorf("the profile read back = %+v", got)
	}
	obj, _ = client.Resource(profilesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if v, ok, _ := unstructured.NestedBool(obj.Object, "spec", "secureBoot"); !ok || v {
		t.Errorf("spec.secureBoot = %v, found %v, want false", v, ok)
	}
	if v, ok, _ := unstructured.NestedStringSlice(obj.Object, "spec", "sshPublicKeys"); !ok || len(v) != 1 || v[0] != key {
		t.Errorf("spec.sshPublicKeys = %v, found %v", v, ok)
	}
}

// A profile says what its object says: the field being there is what counts, never its value.
func TestProfileReadsOnlyWhatTheObjectSays(t *testing.T) {
	s, client := newProfileStore()
	for name, spec := range map[string]map[string]any{
		"none":  {"cpu": int64(1), "memoryMiB": int64(1024), "diskGiB": int64(10)},
		"false": {"cpu": int64(1), "memoryMiB": int64(1024), "diskGiB": int64(10), "secureBoot": false},
		"true":  {"cpu": int64(1), "memoryMiB": int64(1024), "diskGiB": int64(10), "secureBoot": true, "sshPublicKeys": []any{key}},
		"empty": {"cpu": int64(1), "memoryMiB": int64(1024), "diskGiB": int64(10), "sshPublicKeys": []any{}},
	} {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": profilesGVR.GroupVersion().String(), "kind": "Profile",
			"metadata": map[string]any{"name": name, "namespace": "t-demo"}, "spec": spec,
		}}
		if _, err := client.Resource(profilesGVR).Namespace("t-demo").Create(t.Context(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]struct {
		secureBoot *bool
		keys       int
	}{"none": {nil, 0}, "false": {ptr(false), 0}, "true": {ptr(true), 1}, "empty": {nil, 0}}
	for name, w := range want {
		got, err := s.Get(t.Context(), "demo", name)
		if err != nil {
			t.Fatal(err)
		}
		if (got.SecureBoot == nil) != (w.secureBoot == nil) || (w.secureBoot != nil && *got.SecureBoot != *w.secureBoot) || len(got.SSHPublicKeys) != w.keys {
			t.Errorf("%s: secureBoot %v, %d keys, want %v and %d", name, got.SecureBoot, len(got.SSHPublicKeys), w.secureBoot, w.keys)
		}
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
