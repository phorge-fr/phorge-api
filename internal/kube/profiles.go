package kube

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

var profilesGVR = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "profiles"}

// Profiles reads and writes the Profile objects the users make in their tenants. It implements compute.ProfileStore. The three
// profiles every tenant is created with are not objects of this kind: they are made by the Tenant composition.
type Profiles struct {
	conn *Connection
}

// NewProfiles returns a store that works through conn.
func NewProfiles(conn *Connection) *Profiles { return &Profiles{conn: conn} }

var _ compute.ProfileStore = (*Profiles)(nil)

// List returns the profiles a user made in a tenant.
func (s *Profiles) List(ctx context.Context, tenant string) ([]compute.Profile, error) {
	r, err := namespaced(s.conn, profilesGVR, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]compute.Profile, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, profileOf(&list.Items[i]))
	}
	return out, nil
}

// Get returns one profile.
func (s *Profiles) Get(ctx context.Context, tenant, name string) (compute.Profile, error) {
	r, err := namespaced(s.conn, profilesGVR, tenant)
	if err != nil {
		return compute.Profile{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return compute.Profile{}, mapError(err)
	}
	return profileOf(obj), nil
}

// Create writes the Profile.
func (s *Profiles) Create(ctx context.Context, tenant string, req compute.CreateProfile) (compute.Profile, error) {
	r, err := namespaced(s.conn, profilesGVR, tenant)
	if err != nil {
		return compute.Profile{}, err
	}
	spec := map[string]any{"cpu": int64(*req.CPU), "memoryMiB": int64(*req.MemoryMiB), "diskGiB": int64(*req.DiskGiB)}
	if req.Description != "" {
		spec["description"] = req.Description
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": profilesGVR.GroupVersion().String(),
		"kind":       "Profile",
		"metadata":   objectMeta(req.Name, NamespaceOf(tenant), req.CreatedBy),
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return compute.Profile{}, mapError(err)
	}
	return profileOf(obj), nil
}

// Delete deletes a profile in the foreground. A profile that an instance has is refused by an admission webhook of Crossplane
// ("in-use by 1 usage(s)"): that is a conflict, not a failure.
func (s *Profiles) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, profilesGVR, tenant)
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

func profileOf(obj *unstructured.Unstructured) compute.Profile {
	description, _, _ := unstructured.NestedString(obj.Object, "spec", "description")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	return compute.Profile{
		Name:        obj.GetName(),
		Description: description,
		CPU:         intOf(obj, "cpu"),
		MemoryMiB:   intOf(obj, "memoryMiB"),
		DiskGiB:     intOf(obj, "diskGiB"),
		CreatedAt:   obj.GetCreationTimestamp().Time,
		CreatedBy:   obj.GetAnnotations()[CreatedByAnnotation],
		Status:      status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
}
