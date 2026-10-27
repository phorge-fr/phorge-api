package kube

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

var (
	imagesGVR    = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "images"}
	instancesGVR = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "instances"}
)

// Images reads and writes the Image objects of a tenant, in its namespace t-<tenant>. It implements compute.ImageStore.
type Images struct{ conn *Connection }

// NewImages returns a store that works through conn.
func NewImages(conn *Connection) *Images { return &Images{conn: conn} }

var _ compute.ImageStore = (*Images)(nil)

// Instances reads and writes the Instance objects of a tenant. It implements compute.InstanceStore.
type Instances struct{ conn *Connection }

// NewInstances returns a store that works through conn.
func NewInstances(conn *Connection) *Instances { return &Instances{conn: conn} }

var _ compute.InstanceStore = (*Instances)(nil)

func namespaced(conn *Connection, gvr schema.GroupVersionResource, tenant string) (dynamic.ResourceInterface, error) {
	client, err := conn.Dynamic()
	if err != nil {
		return nil, err
	}
	return client.Resource(gvr).Namespace(NamespaceOf(tenant)), nil
}

func objectMeta(name, namespace, createdBy string) map[string]any {
	meta := map[string]any{"name": name, "namespace": namespace}
	if createdBy != "" {
		meta["annotations"] = map[string]any{CreatedByAnnotation: createdBy}
	}
	return meta
}

// ---- images ----

func (s *Images) List(ctx context.Context, tenant string) ([]compute.Image, error) {
	r, err := namespaced(s.conn, imagesGVR, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]compute.Image, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, imageOf(&list.Items[i]))
	}
	return out, nil
}

func (s *Images) Get(ctx context.Context, tenant, name string) (compute.Image, error) {
	r, err := namespaced(s.conn, imagesGVR, tenant)
	if err != nil {
		return compute.Image{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return compute.Image{}, mapError(err)
	}
	return imageOf(obj), nil
}

func (s *Images) Create(ctx context.Context, tenant string, req compute.CreateImage) (compute.Image, error) {
	r, err := namespaced(s.conn, imagesGVR, tenant)
	if err != nil {
		return compute.Image{}, err
	}
	source := map[string]any{"name": req.Source.Name}
	if req.Source.Remote != "" {
		source["remote"] = req.Source.Remote
	}
	spec := map[string]any{"source": source}
	if len(req.Aliases) > 0 {
		aliases := make([]any, len(req.Aliases))
		for i, a := range req.Aliases {
			aliases[i] = a
		}
		spec["aliases"] = aliases
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": imagesGVR.GroupVersion().String(),
		"kind":       "Image",
		"metadata":   objectMeta(req.Name, NamespaceOf(tenant), req.CreatedBy),
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return compute.Image{}, mapError(err)
	}
	return imageOf(obj), nil
}

// Delete deletes an image in the foreground. An image that an instance uses is refused by an admission webhook of
// Crossplane ("in-use by 1 usage(s)"): that is a conflict, not a failure.
func (s *Images) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, imagesGVR, tenant)
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	err = r.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground})
	if err != nil && strings.Contains(err.Error(), "in-use") {
		return &compute.InUseError{}
	}
	return mapError(err)
}

func imageOf(obj *unstructured.Unstructured) compute.Image {
	remote, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "remote")
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "name")
	aliases, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "aliases")
	fingerprint, _, _ := unstructured.NestedString(obj.Object, "status", "fingerprint")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	return compute.Image{
		Name:        obj.GetName(),
		Remote:      remote,
		SourceName:  name,
		Aliases:     aliases,
		Fingerprint: fingerprint,
		CreatedAt:   obj.GetCreationTimestamp().Time,
		CreatedBy:   obj.GetAnnotations()[CreatedByAnnotation],
		Status:      status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
}

// ---- instances ----

func (s *Instances) List(ctx context.Context, tenant string) ([]compute.Instance, error) {
	r, err := namespaced(s.conn, instancesGVR, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]compute.Instance, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, instanceOf(&list.Items[i]))
	}
	return out, nil
}

func (s *Instances) Get(ctx context.Context, tenant, name string) (compute.Instance, error) {
	r, err := namespaced(s.conn, instancesGVR, tenant)
	if err != nil {
		return compute.Instance{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return compute.Instance{}, mapError(err)
	}
	return instanceOf(obj), nil
}

func (s *Instances) Create(ctx context.Context, tenant string, req compute.CreateInstance) (compute.Instance, error) {
	r, err := namespaced(s.conn, instancesGVR, tenant)
	if err != nil {
		return compute.Instance{}, err
	}
	// Only what was asked for: the rest is the defaults of the XRD.
	spec := map[string]any{"imageRef": map[string]any{"name": req.Image}}
	setInt := func(key string, v *int) {
		if v != nil {
			spec[key] = int64(*v)
		}
	}
	setBool := func(key string, v *bool) {
		if v != nil {
			spec[key] = *v
		}
	}
	setInt("cpu", req.CPU)
	setInt("memoryMiB", req.MemoryMiB)
	setInt("diskGiB", req.DiskGiB)
	setBool("running", req.Running)
	setBool("nesting", req.Nesting)
	setBool("secureBoot", req.SecureBoot)
	if len(req.SSHPublicKeys) > 0 {
		spec["sshPublicKeys"] = toAny(req.SSHPublicKeys)
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": instancesGVR.GroupVersion().String(),
		"kind":       "Instance",
		"metadata":   objectMeta(req.Name, NamespaceOf(tenant), req.CreatedBy),
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return compute.Instance{}, mapError(err)
	}
	return instanceOf(obj), nil
}

// Patch is a JSON merge patch of the fields that were given.
func (s *Instances) Patch(ctx context.Context, tenant, name string, req compute.PatchInstance) (compute.Instance, error) {
	r, err := namespaced(s.conn, instancesGVR, tenant)
	if err != nil {
		return compute.Instance{}, err
	}
	spec := map[string]any{}
	if req.Running != nil {
		spec["running"] = *req.Running
	}
	if req.CPU != nil {
		spec["cpu"] = *req.CPU
	}
	if req.MemoryMiB != nil {
		spec["memoryMiB"] = *req.MemoryMiB
	}
	if req.DiskGiB != nil {
		spec["diskGiB"] = *req.DiskGiB
	}
	if req.SSHPublicKeys != nil {
		spec["sshPublicKeys"] = *req.SSHPublicKeys
	}
	doc := map[string]any{"spec": spec}
	if req.MarkChange {
		doc["metadata"] = map[string]any{"annotations": map[string]any{RunningChangedAtAnnotation: time.Now().UTC().Format(time.RFC3339)}}
	}
	patch, err := json.Marshal(doc)
	if err != nil {
		return compute.Instance{}, err
	}
	obj, err := r.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return compute.Instance{}, mapError(err)
	}
	return instanceOf(obj), nil
}

func (s *Instances) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, instancesGVR, tenant)
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	return mapError(r.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground}))
}

func instanceOf(obj *unstructured.Unstructured) compute.Instance {
	image, _, _ := unstructured.NestedString(obj.Object, "spec", "imageRef", "name")
	running, _, _ := unstructured.NestedBool(obj.Object, "spec", "running")
	nesting, _, _ := unstructured.NestedBool(obj.Object, "spec", "nesting")
	secureBoot, _, _ := unstructured.NestedBool(obj.Object, "spec", "secureBoot")
	keys, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "sshPublicKeys")
	state, _, _ := unstructured.NestedString(obj.Object, "status", "state")
	ipv4, _, _ := unstructured.NestedString(obj.Object, "status", "ipv4")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	return compute.Instance{
		Name:             obj.GetName(),
		Image:            image,
		CPU:              intOf(obj, "cpu"),
		MemoryMiB:        intOf(obj, "memoryMiB"),
		DiskGiB:          intOf(obj, "diskGiB"),
		Running:          running,
		Nesting:          nesting,
		SecureBoot:       secureBoot,
		SSHPublicKeys:    keys,
		State:            state,
		IPv4:             ipv4,
		CreatedAt:        obj.GetCreationTimestamp().Time,
		CreatedBy:        obj.GetAnnotations()[CreatedByAnnotation],
		RunningChangedAt: timeOfAnnotation(obj, RunningChangedAtAnnotation),
		Status:           status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
}

// timeOfAnnotation reads a date written in an annotation, zero when it is absent or cannot be read.
func timeOfAnnotation(obj *unstructured.Unstructured, key string) time.Time {
	at, err := time.Parse(time.RFC3339, obj.GetAnnotations()[key])
	if err != nil {
		return time.Time{}
	}
	return at
}

// intOf reads a number of the spec: the API server decodes whole numbers as int64, the fake clients of the tests may not.
func intOf(obj *unstructured.Unstructured, field string) int {
	value, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", field)
	switch v := value.(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
