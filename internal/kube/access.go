package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

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

// ApiKeys reads and writes the ApiKey objects and the Secret that holds the key of each, for the routes that manage them.
// They all live in KeysNamespace. It implements access.KeyStore.
type ApiKeys struct{ conn *Connection }

// NewApiKeys returns a store that works through conn.
func NewApiKeys(conn *Connection) *ApiKeys { return &ApiKeys{conn: conn} }

var _ access.KeyStore = (*ApiKeys)(nil)

// ownerLabel is the label that finds the keys of a user.
const ownerLabel = "phorge.fr/owner"

func (s *ApiKeys) resource() (dynamic.ResourceInterface, error) {
	client, err := s.conn.Dynamic()
	if err != nil {
		return nil, err
	}
	return client.Resource(apiKeys).Namespace(KeysNamespace), nil
}

func (s *ApiKeys) List(ctx context.Context, owner string) ([]access.ApiKey, error) {
	r, err := s.resource()
	if err != nil {
		return nil, err
	}
	list, err := r.List(ctx, metav1.ListOptions{LabelSelector: ownerLabel + "=" + access.OwnerLabel(owner)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]access.ApiKey, 0, len(list.Items))
	for i := range list.Items {
		// The label is a hash: the owner field is what decides.
		if k := apiKeyOf(&list.Items[i]); strings.EqualFold(k.Owner, owner) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (s *ApiKeys) Get(ctx context.Context, id string) (access.ApiKey, error) {
	r, err := s.resource()
	if err != nil {
		return access.ApiKey{}, err
	}
	obj, err := r.Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// Create writes the object of a key: who owns it and what it may do, never a secret. Crossplane makes the service account
// and its key, which TakeKey reads.
func (s *ApiKeys) Create(ctx context.Context, req access.KeyRecord) (access.ApiKey, error) {
	r, err := s.resource()
	if err != nil {
		return access.ApiKey{}, err
	}
	grants := make([]any, 0, len(req.Grants))
	for _, g := range req.Grants {
		grants = append(grants, map[string]any{"tenant": g.Tenant, "scopes": toAny(g.Scopes)})
	}
	spec := map[string]any{
		"owner": req.Owner, "grants": grants, "displayName": req.DisplayName,
		"expiresAt": req.ExpiresAt.UTC().Format(time.RFC3339),
	}
	obj, err := r.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiKeys.GroupVersion().String(),
		"kind":       "ApiKey",
		"metadata": map[string]any{
			"name": req.ID, "namespace": KeysNamespace,
			"labels": map[string]any{ownerLabel: access.OwnerLabel(req.Owner)},
		},
		"spec": spec,
	}}, metav1.CreateOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// Patch is a JSON merge patch of the display name, the only thing that can change.
func (s *ApiKeys) Patch(ctx context.Context, id string, req access.PatchKey) (access.ApiKey, error) {
	r, err := s.resource()
	if err != nil {
		return access.ApiKey{}, err
	}
	spec := map[string]any{}
	if req.DisplayName != nil {
		spec["displayName"] = *req.DisplayName
	}
	patch, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		return access.ApiKey{}, err
	}
	obj, err := r.Patch(ctx, id, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return access.ApiKey{}, mapError(err)
	}
	return apiKeyOf(obj), nil
}

// TakeKey reads the key from the Secret <id>-token that Crossplane fills, and deletes the Secret, so that the key is read
// once. When Crossplane has not written the key yet, or it was already taken (provider-http then recreates the Secret
// without it), the answer is access.ErrKeyNotIssued.
func (s *ApiKeys) TakeKey(ctx context.Context, id string) (string, error) {
	client, err := s.conn.Dynamic()
	if err != nil {
		return "", err
	}
	secrets := client.Resource(secretsResource).Namespace(KeysNamespace)
	obj, err := secrets.Get(ctx, id+"-token", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", access.ErrKeyNotIssued
	}
	if err != nil {
		return "", fmt.Errorf("kubernetes: %w", err)
	}
	// A Secret holds base64 in data.
	encoded, _, _ := unstructured.NestedString(obj.Object, "data", "token")
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 {
		return "", access.ErrKeyNotIssued
	}
	// The key is only shown if it could be removed from the cluster: otherwise it would stay readable there.
	uid := obj.GetUID()
	if err := secrets.Delete(ctx, id+"-token", metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("kubernetes: %w", err)
	}
	return string(key), nil
}

// Delete deletes a key in the foreground. The key stops working at once: the authentication refuses an object that is being
// deleted.
func (s *ApiKeys) Delete(ctx context.Context, id string) error {
	r, err := s.resource()
	if err != nil {
		return err
	}
	foreground := metav1.DeletePropagationForeground
	return mapError(r.Delete(ctx, id, metav1.DeleteOptions{PropagationPolicy: &foreground}))
}

func apiKeyOf(obj *unstructured.Unstructured) access.ApiKey {
	displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
	owner, _, _ := unstructured.NestedString(obj.Object, "spec", "owner")
	expiresAt, _, _ := unstructured.NestedString(obj.Object, "spec", "expiresAt")
	identity, _, _ := unstructured.NestedString(obj.Object, "status", "identity")
	expired, _, _ := unstructured.NestedBool(obj.Object, "status", "expired")
	problems, _, _ := unstructured.NestedString(obj.Object, "status", "problems")

	var grants []access.Grant
	raw, _, _ := unstructured.NestedSlice(obj.Object, "spec", "grants")
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tenant, _ := m["tenant"].(string)
		var scopes []string
		if list, ok := m["scopes"].([]any); ok {
			for _, v := range list {
				if sc, ok := v.(string); ok {
					scopes = append(scopes, sc)
				}
			}
		}
		grants = append(grants, access.Grant{Tenant: tenant, Scopes: scopes})
	}

	ready, since := readyOf(obj)
	k := access.ApiKey{
		ID:          obj.GetName(),
		Owner:       owner,
		DisplayName: displayName,
		Grants:      grants,
		Identity:    identity,
		Expired:     expired,
		CreatedAt:   obj.GetCreationTimestamp().Time,
		Status:      status.Object{Ready: ready, NotReadySince: since, Problems: problems, Deleting: obj.GetDeletionTimestamp() != nil},
	}
	if at, err := time.Parse(time.RFC3339, expiresAt); err == nil {
		k.ExpiresAt = &at
	}
	return k
}
