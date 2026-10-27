package kube

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func newClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		imagesGVR: "ImageList", instancesGVR: "InstanceList",
	})
}

func TestImageCreateWritesTheSpecOfTheXRD(t *testing.T) {
	client := newClient()
	s := NewImages(Static(client))

	got, err := s.Create(t.Context(), "demo", compute.CreateImage{
		Name: "debian", Source: compute.ImageSource{Name: "debian/13/cloud"}, Aliases: []string{"deb"}, CreatedBy: "user:a@b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "debian" || got.SourceName != "debian/13/cloud" || got.CreatedBy != "user:a@b" || len(got.Aliases) != 1 {
		t.Errorf("unexpected image: %+v", got)
	}

	obj, err := client.Resource(imagesGVR).Namespace("t-demo").Get(t.Context(), "debian", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the image must be in the namespace of the tenant: %v", err)
	}
	if obj.GetKind() != "Image" || obj.GetAPIVersion() != "phorge.fr/v1alpha1" {
		t.Errorf("wrong object %s %s", obj.GetAPIVersion(), obj.GetKind())
	}
	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "source", "remote"); found {
		t.Error("a remote was sent although none was given: the XRD defaults it")
	}
	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "source", "architecture"); found {
		t.Error("the architecture is the XRD's, it must not be set by the API")
	}
}

func TestImagesAreSeparatedByTenant(t *testing.T) {
	s := NewImages(Static(newClient()))
	for _, tenant := range []string{"demo", "other"} {
		if _, err := s.Create(t.Context(), tenant, compute.CreateImage{Name: "debian", Source: compute.ImageSource{Name: "debian/13/cloud"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(t.Context(), "demo", compute.CreateImage{Name: "debian", Source: compute.ImageSource{Name: "x"}}); !errors.Is(err, compute.ErrExists) {
		t.Errorf("twice in a tenant: %v", err)
	}
	if err := s.Delete(t.Context(), "demo", "debian"); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List(t.Context(), "other"); err != nil || len(list) != 1 {
		t.Errorf("another tenant lost its image: %v, %v", list, err)
	}
	if _, err := s.Get(t.Context(), "third", "debian"); !errors.Is(err, compute.ErrNotFound) {
		t.Errorf("get in a tenant that has none: %v", err)
	}
}

func TestImageDeleteIsInTheForegroundAndAnInUseRefusalIsAConflict(t *testing.T) {
	client := newClient()
	s := NewImages(Static(client))
	if _, err := s.Create(t.Context(), "demo", compute.CreateImage{Name: "debian", Source: compute.ImageSource{Name: "debian/13/cloud"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "demo", "debian"); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if del, ok := a.(ktesting.DeleteActionImpl); ok {
			if del.DeleteOptions.PropagationPolicy == nil || *del.DeleteOptions.PropagationPolicy != metav1.DeletePropagationForeground {
				t.Errorf("propagation policy = %v", del.DeleteOptions.PropagationPolicy)
			}
		}
	}

	client.PrependReactor("delete", "images", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(`admission webhook "nousages.protection.crossplane.io" denied the request: image is in-use by 1 usage(s)`)
	})
	var inUse *compute.InUseError
	if err := s.Delete(t.Context(), "demo", "debian"); !errors.As(err, &inUse) {
		t.Errorf("err = %v, want an InUseError: it is a conflict, not a failure of the cluster", err)
	}
}

func TestInstanceCreateSendsOnlyWhatWasAsked(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))

	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian"}); err != nil {
		t.Fatal(err)
	}
	obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := obj.Object["spec"].(map[string]any)
	if len(spec) != 1 {
		t.Errorf("spec = %v, only the image was asked for, the rest is the defaults of the XRD", spec)
	}
	if image, _, _ := unstructured.NestedString(obj.Object, "spec", "imageRef", "name"); image != "debian" {
		t.Errorf("imageRef = %q", image)
	}

	got, err := s.Create(t.Context(), "demo", compute.CreateInstance{
		Name: "big", Image: "debian", CPU: ptr(4), MemoryMiB: ptr(4096), DiskGiB: ptr(40),
		Running: ptr(false), Nesting: ptr(true), SecureBoot: ptr(false), SSHPublicKeys: []string{key},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU != 4 || got.MemoryMiB != 4096 || got.DiskGiB != 40 || got.Running || !got.Nesting || got.SecureBoot || len(got.SSHPublicKeys) != 1 {
		t.Errorf("unexpected instance: %+v", got)
	}
}

func TestInstancePatchChangesOnlyWhatWasGiven(t *testing.T) {
	s := NewInstances(Static(newClient()))
	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", CPU: ptr(2), MemoryMiB: ptr(2048), SSHPublicKeys: []string{key}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Patch(t.Context(), "demo", "web", compute.PatchInstance{Running: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Running || got.CPU != 2 || got.MemoryMiB != 2048 || got.Image != "debian" || len(got.SSHPublicKeys) != 1 {
		t.Errorf("stopping changed more than that: %+v", got)
	}

	none := []string{}
	got, err = s.Patch(t.Context(), "demo", "web", compute.PatchInstance{CPU: ptr(8), SSHPublicKeys: &none})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPU != 8 || len(got.SSHPublicKeys) != 0 || got.Running {
		t.Errorf("resize and clear the keys: %+v", got)
	}
	if _, err := s.Patch(t.Context(), "demo", "ghost", compute.PatchInstance{CPU: ptr(2)}); !errors.Is(err, compute.ErrNotFound) {
		t.Errorf("patch of a missing instance: %v", err)
	}
}

func TestInstanceOfReadsTheStatus(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web"},
		"spec":     map[string]any{"imageRef": map[string]any{"name": "debian"}, "cpu": float64(2), "memoryMiB": int64(1024), "running": true},
		"status": map[string]any{
			"state": "Running", "ipv4": "10.20.0.4", "problems": "x",
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
	got := instanceOf(obj)
	if got.State != "Running" || got.IPv4 != "10.20.0.4" || got.CPU != 2 || got.MemoryMiB != 1024 || !got.Running || !got.Status.Ready || got.Status.Problems != "x" {
		t.Errorf("unexpected instance: %+v", got)
	}
	if bare := instanceOf(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "x"}}}); bare.CPU != 0 || bare.Name != "x" {
		t.Errorf("an object with no spec must not panic nor invent anything: %+v", bare)
	}
}

func TestAStartIsRecordedOnTheObject(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	ctx := t.Context()
	if _, err := client.Resource(instancesGVR).Namespace("t-demo").Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "phorge.fr/v1alpha1", "kind": "Instance",
		"metadata": map[string]any{"name": "web", "namespace": "t-demo"},
		"spec":     map[string]any{"imageRef": map[string]any{"name": "debian"}, "running": false},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	got, err := s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &no})
	if err != nil || !got.StartedAt.IsZero() {
		t.Fatalf("a stop records no start: %+v, %v", got.StartedAt, err)
	}
	before := time.Now().Add(-time.Second)
	got, err = s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &yes, MarkStart: true})
	if err != nil || !got.Running || got.StartedAt.Before(before) || time.Since(got.StartedAt) > time.Minute {
		t.Errorf("a start must record its time: %+v, %v", got, err)
	}
	// A start that is not marked (the VM already ran) leaves the date as it was.
	prev := got.StartedAt
	got, err = s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &yes})
	if err != nil || !got.StartedAt.Equal(prev) {
		t.Errorf("an unmarked start changed the date: %v -> %v, %v", prev, got.StartedAt, err)
	}
}

func TestTheStartOfAnOperationIsRead(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web"},
		"spec":     map[string]any{"running": false},
		"status":   map[string]any{"state": "Running", "operationSince": "2026-10-27T00:00:53Z"},
	}}
	in := instanceOf(obj)
	if !in.OperationSince.Equal(time.Date(2026, 10, 27, 0, 0, 53, 0, time.UTC)) || in.Transition() != "stopping" {
		t.Errorf("%+v", in)
	}
	for _, bad := range []any{"", "tomorrow", int64(3), nil} {
		obj.Object["status"] = map[string]any{"operationSince": bad}
		if got := instanceOf(obj).OperationSince; !got.IsZero() {
			t.Errorf("%v was read as %v: a date that cannot be read is no date", bad, got)
		}
	}
}
