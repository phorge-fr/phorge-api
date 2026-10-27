package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/phorge-fr/phorge-api/internal/tenants"
)

var tenantsGVR = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "tenants"}

// TenantNamespace is where the Tenant objects live. The namespaces of the tenants themselves are t-<name>.
const TenantNamespace = "phorge-system"

// CreatedByAnnotation records who created a tenant through the API.
const CreatedByAnnotation = "phorge.fr/created-by"

// RunningChangedAtAnnotation records, on an Instance, when the API last changed running (RFC 3339). It is the time of the request,
// which neither Incus nor Crossplane report: the API refuses a stop while the VM is still booting (compute.SettleTime) and
// says how long a start or a stop has lasted.
const RunningChangedAtAnnotation = "phorge.fr/running-changed-at"

// Tenants reads and writes the Tenant objects. It implements tenants.Store.
type Tenants struct {
	conn *Connection
}

// NewTenants returns a store that works through conn.
func NewTenants(conn *Connection) *Tenants { return &Tenants{conn: conn} }

var _ tenants.Store = (*Tenants)(nil)

func (s *Tenants) resource() (dynamic.ResourceInterface, error) {
	client, err := s.conn.Dynamic()
	if err != nil {
		return nil, err
	}
	return client.Resource(tenantsGVR).Namespace(TenantNamespace), nil
}

// List returns every tenant.
func (s *Tenants) List(ctx context.Context) ([]tenants.Tenant, error) {
	r, err := s.resource()
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]tenants.Tenant, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, tenantOf(&list.Items[i]))
	}
	return out, nil
}

// Get returns one tenant.
func (s *Tenants) Get(ctx context.Context, name string) (tenants.Tenant, error) {
	r, err := s.resource()
	if err != nil {
		return tenants.Tenant{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return tenants.Tenant{}, mapError(err)
	}
	return tenantOf(obj), nil
}

// Create creates a tenant. Crossplane then composes it, which takes about a minute.
func (s *Tenants) Create(ctx context.Context, req tenants.CreateRequest) (tenants.Tenant, error) {
	r, err := s.resource()
	if err != nil {
		return tenants.Tenant{}, err
	}
	spec := map[string]any{"owner": req.Owner}
	if req.Plan != "" {
		spec["plan"] = string(req.Plan)
	}
	if req.DisplayName != "" {
		spec["displayName"] = req.DisplayName
	}
	meta := map[string]any{"name": req.Name, "namespace": TenantNamespace}
	if req.CreatedBy != "" {
		meta["annotations"] = map[string]any{CreatedByAnnotation: req.CreatedBy}
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": tenantsGVR.GroupVersion().String(),
		"kind":       "Tenant",
		"metadata":   meta,
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return tenants.Tenant{}, mapError(err)
	}
	return tenantOf(obj), nil
}

// Patch changes the plan or the display name, with a JSON merge patch on the fields that were given.
func (s *Tenants) Patch(ctx context.Context, name string, req tenants.PatchRequest) (tenants.Tenant, error) {
	r, err := s.resource()
	if err != nil {
		return tenants.Tenant{}, err
	}
	spec := map[string]any{}
	if req.Plan != nil {
		spec["plan"] = string(*req.Plan)
	}
	if req.DisplayName != nil {
		spec["displayName"] = *req.DisplayName
	}
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return tenants.Tenant{}, err
	}
	obj, err := r.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return tenants.Tenant{}, mapError(err)
	}
	return tenantOf(obj), nil
}

// Delete deletes a tenant in the foreground: the object stays, with a deletion timestamp, until every resource it
// composed is gone (a Tenant was gone after 1 s in the background mode, its resources after 58 s).
func (s *Tenants) Delete(ctx context.Context, name string) error {
	r, err := s.resource()
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	return mapError(r.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground}))
}

func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return tenants.ErrNotFound
	case apierrors.IsAlreadyExists(err):
		return tenants.ErrExists
	case apierrors.IsInvalid(err):
		// The message of the admission rule, for instance the CEL message of the XRD.
		return &tenants.InvalidError{Problems: []string{err.Error()}}
	default:
		return fmt.Errorf("kubernetes: %w", err)
	}
}

func tenantOf(obj *unstructured.Unstructured) tenants.Tenant {
	// A field of the wrong type reads as empty.
	owner, _, _ := unstructured.NestedString(obj.Object, "spec", "owner")
	plan, _, _ := unstructured.NestedString(obj.Object, "spec", "plan")
	displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")
	namespace, _, _ := unstructured.NestedString(obj.Object, "status", "namespace")
	project, _, _ := unstructured.NestedString(obj.Object, "status", "incusProject")

	t := tenants.Tenant{
		Name:         obj.GetName(),
		DisplayName:  displayName,
		Owner:        owner,
		Plan:         tenants.Plan(plan),
		CreatedAt:    obj.GetCreationTimestamp().Time,
		CreatedBy:    obj.GetAnnotations()[CreatedByAnnotation],
		Namespace:    namespace,
		IncusProject: project,
		Problems:     problems,
		Deleting:     obj.GetDeletionTimestamp() != nil,
	}
	t.Ready, t.NotReadySince = readyOf(obj)
	return t
}

// readyOf reads the Ready condition: whether it is true, and since when it is what it is (the creation time of the object
// when it has no such condition yet).
func readyOf(obj *unstructured.Unstructured) (ready bool, since time.Time) {
	since = obj.GetCreationTimestamp().Time
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["type"] != "Ready" {
			continue
		}
		ready = cond["status"] == "True"
		if at, err := time.Parse(time.RFC3339, fmt.Sprint(cond["lastTransitionTime"])); err == nil {
			since = at
		}
	}
	return ready, since
}
