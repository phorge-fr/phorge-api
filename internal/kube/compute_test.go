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

// An instance that does not say its SSH keys takes its profile's; one that says an empty list has none. The object must carry the
// difference: absent is left out of the spec, an empty list is written as such, and the answer is read from the spec first so an empty
// list is "no key" and does not fall through to the status.
func TestInstanceCreateTellsAbsentKeysFromNone(t *testing.T) {
	for name, tt := range map[string]struct {
		keys    []string
		written bool
		want    int
	}{
		"absent":     {nil, false, 0},
		"empty list": {[]string{}, true, 0},
		"keys":       {[]string{key}, true, 1},
	} {
		client := newClient()
		s := NewInstances(Static(client))
		got, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", SSHPublicKeys: tt.keys})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.SSHPublicKeys) != tt.want {
			t.Errorf("%s: the answer has %v, want %d keys", name, got.SSHPublicKeys, tt.want)
		}
		obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		v, found, err := unstructured.NestedStringSlice(obj.Object, "spec", "sshPublicKeys")
		if err != nil || found != tt.written || len(v) != tt.want {
			t.Errorf("%s: spec.sshPublicKeys = %v, found %v, err %v; want written %v with %d keys", name, v, found, err, tt.written, tt.want)
		}
	}
}

// What the platform says it has in force does not replace an empty list the instance wrote.
func TestInstanceCreatedWithNoKeyKeepsNoKeyWhenTheStatusHasSome(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", SSHPublicKeys: []string{}}); err != nil {
		t.Fatal(err)
	}
	obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringSlice(obj.Object, []string{key}, "status", "sshPublicKeys"); err != nil {
		t.Fatal(err)
	}
	if got := instanceOf(obj); len(got.SSHPublicKeys) != 0 {
		t.Errorf("keys = %v, the spec says none", got.SSHPublicKeys)
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

// The XRD of the Instance used to default nesting, secureBoot and sshPublicKeys into every object and no longer does (an absent
// field means "the instance did not say"), the status says the value in force. The API reads both shapes of object.
func TestInstanceOfReadsTheSettingsInForce(t *testing.T) {
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPPP"
	cases := []struct {
		name       string
		spec       map[string]any
		status     map[string]any
		nesting    bool
		secureBoot bool
		keys       []string
	}{
		{
			name:       "defaults stored in the spec, no status",
			spec:       map[string]any{"nesting": false, "secureBoot": true, "sshPublicKeys": []any{}},
			nesting:    false,
			secureBoot: true,
		},
		{
			name:       "defaults stored in the spec, status says the same",
			spec:       map[string]any{"nesting": false, "secureBoot": true, "sshPublicKeys": []any{}},
			status:     map[string]any{"nesting": false, "secureBoot": true, "sshPublicKeys": []any{}},
			nesting:    false,
			secureBoot: true,
		},
		{
			name:       "stored values the instance said",
			spec:       map[string]any{"nesting": true, "secureBoot": false, "sshPublicKeys": []any{key}},
			status:     map[string]any{"nesting": true, "secureBoot": false, "sshPublicKeys": []any{key}},
			nesting:    true,
			secureBoot: false,
			keys:       []string{key},
		},
		{
			name:       "absent fields and a status that says them",
			spec:       map[string]any{},
			status:     map[string]any{"nesting": true, "secureBoot": false, "sshPublicKeys": []any{key}},
			nesting:    true,
			secureBoot: false,
			keys:       []string{key},
		},
		{
			name:       "absent fields and a status that says the defaults",
			spec:       map[string]any{},
			status:     map[string]any{"nesting": false, "secureBoot": true, "sshPublicKeys": []any{}},
			nesting:    false,
			secureBoot: true,
		},
		{
			name:       "absent fields, no status yet: the defaults, secure boot is on",
			spec:       map[string]any{},
			nesting:    false,
			secureBoot: true,
		},
		{
			name:       "absent fields and a status with none of the settings yet",
			spec:       map[string]any{},
			status:     map[string]any{"state": "Running"},
			nesting:    false,
			secureBoot: true,
		},
		{
			name:       "a false in the spec is said, it is not absent",
			spec:       map[string]any{"secureBoot": false},
			nesting:    false,
			secureBoot: false,
		},
		{
			name:       "the spec just changed by the API wins over the status that did not follow yet",
			spec:       map[string]any{"sshPublicKeys": []any{other}},
			status:     map[string]any{"sshPublicKeys": []any{key}},
			nesting:    false,
			secureBoot: true,
			keys:       []string{other},
		},
		{
			name:       "keys cleared in the spec while the status still has them",
			spec:       map[string]any{"sshPublicKeys": []any{}},
			status:     map[string]any{"sshPublicKeys": []any{key}},
			nesting:    false,
			secureBoot: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			object := map[string]any{"metadata": map[string]any{"name": "web"}, "spec": c.spec}
			if c.status != nil {
				object["status"] = c.status
			}
			got := instanceOf(&unstructured.Unstructured{Object: object})
			if got.Nesting != c.nesting || got.SecureBoot != c.secureBoot {
				t.Errorf("nesting = %v, secureBoot = %v, want %v and %v", got.Nesting, got.SecureBoot, c.nesting, c.secureBoot)
			}
			if len(got.SSHPublicKeys) != len(c.keys) {
				t.Fatalf("keys = %v, want %v", got.SSHPublicKeys, c.keys)
			}
			for i := range c.keys {
				if got.SSHPublicKeys[i] != c.keys[i] {
					t.Errorf("keys = %v, want %v", got.SSHPublicKeys, c.keys)
				}
			}
		})
	}
	if bare := instanceOf(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "x"}}}); !bare.SecureBoot || bare.Nesting {
		t.Errorf("an object with no spec has the defaults: %+v", bare)
	}
}

func TestAChangeOfRunningIsRecordedOnTheObject(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	ctx := t.Context()
	if _, err := client.Resource(instancesGVR).Namespace("t-demo").Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "phorge.fr/v1alpha1", "kind": "Instance",
		"metadata": map[string]any{"name": "web", "namespace": "t-demo"},
		"spec":     map[string]any{"imageRef": map[string]any{"name": "debian"}, "running": true},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	got, err := s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &no})
	if err != nil || !got.RunningChangedAt.IsZero() {
		t.Fatalf("a patch that is not marked records nothing: %v, %v", got.RunningChangedAt, err)
	}
	before := time.Now().Add(-time.Second)
	got, err = s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &yes, MarkChange: true})
	if err != nil || got.RunningChangedAt.Before(before) || time.Since(got.RunningChangedAt) > time.Minute {
		t.Errorf("a marked change must record its time: %v, %v", got.RunningChangedAt, err)
	}
	prev := got.RunningChangedAt
	got, err = s.Patch(ctx, "demo", "web", compute.PatchInstance{Running: &yes})
	if err != nil || !got.RunningChangedAt.Equal(prev) {
		t.Errorf("an unmarked patch changed the date: %v -> %v, %v", prev, got.RunningChangedAt, err)
	}
	for _, bad := range []string{"", "tomorrow"} {
		obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "web", "annotations": map[string]any{RunningChangedAtAnnotation: bad}}}}
		if at := instanceOf(obj).RunningChangedAt; !at.IsZero() {
			t.Errorf("%q was read as %v: a date that cannot be read is no date", bad, at)
		}
	}
}

// A size is a profile or the three values: the XRD refuses a spec that has both, so the store must never write both.
func TestInstanceCreateWithAProfileSendsNoSize(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", Network: "net", Profile: "m"}); err != nil {
		t.Fatal(err)
	}
	obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec["profile"] != "m" || spec["network"] != "net" {
		t.Errorf("spec = %v", spec)
	}
	for _, k := range []string{"cpu", "memoryMiB", "diskGiB"} {
		if _, ok := spec[k]; ok {
			t.Errorf("%s was sent next to a profile", k)
		}
	}
	got := instanceOf(obj)
	if got.Profile != "m" || len(got.Networks) != 1 || got.Networks[0] != "net" {
		t.Errorf("instanceOf = %+v", got)
	}
}

func TestInstancePatchMovesBetweenAProfileAndItsOwnSize(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", Network: "net", CPU: ptr(2), MemoryMiB: ptr(2048), DiskGiB: ptr(20)}); err != nil {
		t.Fatal(err)
	}
	spec := func() map[string]any {
		obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		m, _, _ := unstructured.NestedMap(obj.Object, "spec")
		return m
	}

	profile := "s"
	if _, err := s.Patch(t.Context(), "demo", "web", compute.PatchInstance{Profile: &profile}); err != nil {
		t.Fatal(err)
	}
	got := spec()
	if got["profile"] != "s" {
		t.Errorf("spec = %v: the profile was not set", got)
	}
	for _, k := range []string{"cpu", "memoryMiB", "diskGiB"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s was left next to the profile: the XRD refuses both", k)
		}
	}

	if _, err := s.Patch(t.Context(), "demo", "web", compute.PatchInstance{DropProfile: true, CPU: ptr(4), MemoryMiB: ptr(4096), DiskGiB: ptr(40)}); err != nil {
		t.Fatal(err)
	}
	got = spec()
	if _, ok := got["profile"]; ok {
		t.Errorf("spec = %v: the profile was left next to the values", got)
	}
	if got["cpu"] != int64(4) || got["memoryMiB"] != int64(4096) || got["diskGiB"] != int64(40) {
		t.Errorf("spec = %v: the three values were not set", got)
	}
	if got["network"] != "net" || got["imageRef"] == nil {
		t.Errorf("spec = %v: a change of size touched something else", got)
	}
}

// The XRD takes networks or the deprecated network, one of the two: the store writes the list, and a patch of the list removes the
// single network of an instance that was made with it.
func TestInstanceNetworksAreAListAndReplaceTheSingleNetwork(t *testing.T) {
	client := newClient()
	s := NewInstances(Static(client))
	if _, err := s.Create(t.Context(), "demo", compute.CreateInstance{Name: "web", Image: "debian", Networks: []string{"net", "lan"}, Profile: "s"}); err != nil {
		t.Fatal(err)
	}
	get := func() *unstructured.Unstructured {
		obj, err := client.Resource(instancesGVR).Namespace("t-demo").Get(t.Context(), "web", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return obj
	}
	spec, _, _ := unstructured.NestedMap(get().Object, "spec")
	if _, ok := spec["network"]; ok {
		t.Errorf("the single network was sent next to the list: %v", spec)
	}
	if got := instanceOf(get()).Networks; len(got) != 2 || got[0] != "net" || got[1] != "lan" {
		t.Errorf("networks = %v, want the list in order", got)
	}

	// An instance made with the deprecated field reads as a list of one, and a patch of the list drops the field.
	old := get()
	_ = unstructured.SetNestedField(old.Object, "net", "spec", "network")
	unstructured.RemoveNestedField(old.Object, "spec", "networks")
	if got := instanceOf(old).Networks; len(got) != 1 || got[0] != "net" {
		t.Errorf("an instance with the single network reads %v, want [net]", got)
	}
	if _, err := client.Resource(instancesGVR).Namespace("t-demo").Update(t.Context(), old, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	both := []string{"net", "lan"}
	if _, err := s.Patch(t.Context(), "demo", "web", compute.PatchInstance{Networks: &both}); err != nil {
		t.Fatal(err)
	}
	spec, _, _ = unstructured.NestedMap(get().Object, "spec")
	if _, ok := spec["network"]; ok {
		t.Errorf("the single network stayed next to the list: %v", spec)
	}
	if got := instanceOf(get()).Networks; len(got) != 2 {
		t.Errorf("networks after the patch = %v", got)
	}
}

func TestInstanceOfReadsTheAddressOnEachNetwork(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web"},
		"spec":     map[string]any{"networks": []any{"net", "lan"}},
		"status": map[string]any{"networks": []any{
			map[string]any{"network": "net", "ipv4": "10.0.0.2", "ipv6": "fd00::2"},
			map[string]any{"network": "lan", "ipv4": "10.1.0.2", "ipv6": ""},
		}},
	}}
	got := instanceOf(obj).Addresses
	if len(got) != 2 || got[0] != (compute.InstanceNetwork{Network: "net", IPv4: "10.0.0.2", IPv6: "fd00::2"}) || got[1].IPv4 != "10.1.0.2" {
		t.Errorf("addresses = %+v", got)
	}
}
