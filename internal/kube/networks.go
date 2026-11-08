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

var networksGVR = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "networks"}

// Networks reads and writes the Network objects of the tenants. It implements compute.NetworkStore.
type Networks struct {
	conn *Connection
}

// NewNetworks returns a store that works through conn.
func NewNetworks(conn *Connection) *Networks { return &Networks{conn: conn} }

var _ compute.NetworkStore = (*Networks)(nil)

// List returns the networks of a tenant.
func (s *Networks) List(ctx context.Context, tenant string) ([]compute.Network, error) {
	r, err := namespaced(s.conn, networksGVR, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]compute.Network, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, networkOf(&list.Items[i]))
	}
	return out, nil
}

// Get returns one network.
func (s *Networks) Get(ctx context.Context, tenant, name string) (compute.Network, error) {
	r, err := namespaced(s.conn, networksGVR, tenant)
	if err != nil {
		return compute.Network{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return compute.Network{}, mapError(err)
	}
	return networkOf(obj), nil
}

// Create writes the Network. Its addresses and its NAT are not a choice: the kind has no field for them.
func (s *Networks) Create(ctx context.Context, tenant string, req compute.CreateNetwork) (compute.Network, error) {
	r, err := namespaced(s.conn, networksGVR, tenant)
	if err != nil {
		return compute.Network{}, err
	}
	spec := map[string]any{}
	if req.Description != "" {
		spec["description"] = req.Description
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": networksGVR.GroupVersion().String(),
		"kind":       "Network",
		"metadata":   objectMeta(req.Name, NamespaceOf(tenant), req.CreatedBy),
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return compute.Network{}, mapError(err)
	}
	return networkOf(obj), nil
}

// Delete deletes a network in the foreground. A network that an instance uses is refused by an admission webhook of Crossplane
// ("in-use by 1 usage(s)"): that is a conflict, not a failure.
func (s *Networks) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, networksGVR, tenant)
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

func networkOf(obj *unstructured.Unstructured) compute.Network {
	description, _, _ := unstructured.NestedString(obj.Object, "spec", "description")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	return compute.Network{
		Name:        obj.GetName(),
		Description: description,
		CreatedAt:   obj.GetCreationTimestamp().Time,
		CreatedBy:   obj.GetAnnotations()[CreatedByAnnotation],
		Status:      status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
}
