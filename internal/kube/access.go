package kube

import (
	"context"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/phorge-fr/phorge-api/internal/access"
	"github.com/phorge-fr/phorge-api/internal/status"
)

var membershipsGVR = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "memberships"}

// Memberships reads and writes the Membership objects of a tenant. It implements access.MembershipStore.
type Memberships struct{ conn *Connection }

// NewMemberships returns a store that works through conn.
func NewMemberships(conn *Connection) *Memberships { return &Memberships{conn: conn} }

var _ access.MembershipStore = (*Memberships)(nil)

func (s *Memberships) List(ctx context.Context, tenant string) ([]access.Membership, error) {
	r, err := namespaced(s.conn, membershipsGVR, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]access.Membership, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, membershipOf(&list.Items[i]))
	}
	return out, nil
}

func (s *Memberships) Get(ctx context.Context, tenant, name string) (access.Membership, error) {
	r, err := namespaced(s.conn, membershipsGVR, tenant)
	if err != nil {
		return access.Membership{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return access.Membership{}, mapError(err)
	}
	return membershipOf(obj), nil
}

// Create writes the membership under the name derived from its subject, so that a user has one per tenant.
func (s *Memberships) Create(ctx context.Context, tenant string, req access.CreateMembership) (access.Membership, error) {
	r, err := namespaced(s.conn, membershipsGVR, tenant)
	if err != nil {
		return access.Membership{}, err
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": membershipsGVR.GroupVersion().String(),
		"kind":       "Membership",
		"metadata":   objectMeta(access.MembershipName(req.Subject), NamespaceOf(tenant), req.CreatedBy),
		"spec": map[string]any{
			"subject": map[string]any{"kind": req.Subject.Kind, "name": req.Subject.Name},
			"role":    string(req.Role),
		},
	}}, metav1.CreateOptions{})
	if err != nil {
		return access.Membership{}, mapError(err)
	}
	return membershipOf(obj), nil
}

// Delete deletes a membership in the foreground. Its tuples go within about a minute after that.
func (s *Memberships) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, membershipsGVR, tenant)
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	return mapError(r.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground}))
}

func membershipOf(obj *unstructured.Unstructured) access.Membership {
	kind, _, _ := unstructured.NestedString(obj.Object, "spec", "subject", "kind")
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "subject", "name")
	role, _, _ := unstructured.NestedString(obj.Object, "spec", "role")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	return access.Membership{
		Name:      obj.GetName(),
		Subject:   access.Subject{Kind: kind, Name: name},
		Role:      access.Role(role),
		CreatedAt: obj.GetCreationTimestamp().Time,
		CreatedBy: obj.GetAnnotations()[CreatedByAnnotation],
		Status:    status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
}

// ApiKeys reads and writes the ApiKey objects of a tenant, for the routes that manage them (the authentication reads them
// with Keys). It implements access.KeyStore.
type ApiKeys struct{ conn *Connection }

// NewApiKeys returns a store that works through conn.
func NewApiKeys(conn *Connection) *ApiKeys { return &ApiKeys{conn: conn} }

var _ access.KeyStore = (*ApiKeys)(nil)

func (s *ApiKeys) List(ctx context.Context, tenant string) ([]access.ApiKey, error) {
	r, err := namespaced(s.conn, apiKeys, tenant)
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]access.ApiKey, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, apiKeyOf(&list.Items[i]))
	}
	return out, nil
}

func (s *ApiKeys) Get(ctx context.Context, tenant, name string) (access.ApiKey, error) {
	r, err := namespaced(s.conn, apiKeys, tenant)
	if err != nil {
		return access.ApiKey{}, err
	}
	obj, err := r.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// Create writes the object of a key: its hash, never the key.
func (s *ApiKeys) Create(ctx context.Context, tenant string, req access.KeyRecord) (access.ApiKey, error) {
	r, err := namespaced(s.conn, apiKeys, tenant)
	if err != nil {
		return access.ApiKey{}, err
	}
	spec := map[string]any{"hash": req.Hash, "scopes": toAny(req.Scopes)}
	if req.DisplayName != "" {
		spec["displayName"] = req.DisplayName
	}
	if req.ExpiresAt != nil {
		spec["expiresAt"] = req.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if req.CreatedBy != "" {
		spec["createdBy"] = req.CreatedBy
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiKeys.GroupVersion().String(),
		"kind":       "ApiKey",
		"metadata":   map[string]any{"name": req.Name, "namespace": NamespaceOf(tenant)},
		"spec":       spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// Patch is a JSON merge patch of what was given. Removing a scope removes its tuple at once.
func (s *ApiKeys) Patch(ctx context.Context, tenant, name string, req access.PatchKey) (access.ApiKey, error) {
	r, err := namespaced(s.conn, apiKeys, tenant)
	if err != nil {
		return access.ApiKey{}, err
	}
	spec := map[string]any{}
	if req.Scopes != nil {
		spec["scopes"] = *req.Scopes
	}
	if req.ExpiresAt != nil {
		spec["expiresAt"] = req.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if req.DisplayName != nil {
		spec["displayName"] = *req.DisplayName
	}
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return access.ApiKey{}, err
	}
	obj, err := r.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// Delete deletes a key in the foreground. The key stops working at once (the authentication refuses an object that is being
// deleted), its tuples go within about a minute.
func (s *ApiKeys) Delete(ctx context.Context, tenant, name string) error {
	r, err := namespaced(s.conn, apiKeys, tenant)
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	return mapError(r.Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &foreground}))
}

func apiKeyOf(obj *unstructured.Unstructured) access.ApiKey {
	displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
	scopes, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "scopes")
	createdBy, _, _ := unstructured.NestedString(obj.Object, "spec", "createdBy")
	expiresAt, _, _ := unstructured.NestedString(obj.Object, "spec", "expiresAt")
	expired, _, _ := unstructured.NestedBool(obj.Object, "status", "expired")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	ready, since := readyOf(obj)
	k := access.ApiKey{
		Name:        obj.GetName(),
		DisplayName: displayName,
		Scopes:      scopes,
		Expired:     expired,
		CreatedAt:   obj.GetCreationTimestamp().Time,
		CreatedBy:   createdBy,
		Status:      status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
	if at, err := time.Parse(time.RFC3339, expiresAt); err == nil {
		k.ExpiresAt = &at
	}
	return k
}
