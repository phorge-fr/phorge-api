package kube

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

func apiKey(namespace, name string, edit func(*unstructured.Unstructured)) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "phorge.fr/v1alpha1",
		"kind":       "ApiKey",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
		"spec": map[string]any{
			"hash":      auth.HashSecret("secret"),
			"owner":     "alice@example.org",
			"scopes":    []any{"tenants:read", "images:write"},
			"expiresAt": "2027-01-01T00:00:00Z",
		},
	}}
	if edit != nil {
		edit(o)
	}
	return o
}

// store puts the objects in a fake cluster through the real resource name: the fake would otherwise guess "apikeies"
// from the kind ApiKey.
func store(t *testing.T, objs ...*unstructured.Unstructured) *Keys {
	t.Helper()
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	for _, o := range objs {
		if _, err := client.Resource(apiKeys).Namespace(o.GetNamespace()).Create(t.Context(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return NewKeys(Static(client))
}

func TestGetKeyReadsTheObjectOfTheKeysNamespace(t *testing.T) {
	k := store(t, apiKey(KeysNamespace, "0123456789abcdef", nil), apiKey("t-demo", "0123456789abcdef", func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, "someone-else@example.org", "spec", "owner")
	}))

	rec, err := k.GetKey(t.Context(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Hash != auth.HashSecret("secret") || rec.ExpiresAt != "2027-01-01T00:00:00Z" || rec.Owner != "alice@example.org" {
		t.Errorf("unexpected record: %+v (a key of another namespace must not be read)", rec)
	}
	if len(rec.Scopes) != 2 || rec.Scopes[0] != "tenants:read" || rec.Scopes[1] != "images:write" {
		t.Errorf("scopes = %v", rec.Scopes)
	}
	if rec.Expired || rec.Deleting {
		t.Errorf("a fresh key is neither expired nor deleting: %+v", rec)
	}
}

func TestGetKeyReadsTheStatusAndTheDeletion(t *testing.T) {
	now := metav1.Now()
	k := store(t, apiKey(KeysNamespace, "0123456789abcdef", func(o *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(o.Object, true, "status", "expired")
		o.SetDeletionTimestamp(&now)
	}))
	rec, err := k.GetKey(t.Context(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Expired || !rec.Deleting {
		t.Errorf("status.expired and the deletion timestamp were not read: %+v", rec)
	}
}

func TestGetKeyNotFound(t *testing.T) {
	k := store(t, apiKey(KeysNamespace, "0123456789abcdef", nil), apiKey("t-demo", "fedcba9876543210", nil))
	// Another id, and a key that exists in a namespace that is not the keys one.
	for _, id := range []string{"ffffffffffffffff", "fedcba9876543210"} {
		if _, err := k.GetKey(t.Context(), id); !errors.Is(err, auth.ErrKeyNotFound) {
			t.Errorf("%v: err = %v, want ErrKeyNotFound", id, err)
		}
	}
}

func TestAMalformedObjectMatchesNoKey(t *testing.T) {
	k := store(t, apiKey(KeysNamespace, "0123456789abcdef", func(o *unstructured.Unstructured) { o.Object["spec"] = map[string]any{"hash": int64(3)} }))
	rec, err := k.GetKey(t.Context(), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Hash != "" {
		t.Errorf("hash = %q, want empty: nothing matches an empty hash", rec.Hash)
	}
}

func TestAnUnreachableClusterIsNotAMissingKey(t *testing.T) {
	boom := errors.New("no kubeconfig")
	k := NewKeys(&Connection{connect: func() (dynamic.Interface, error) { return nil, boom }})
	_, err := k.GetKey(t.Context(), "0123456789abcdef")
	if !errors.Is(err, boom) || errors.Is(err, auth.ErrKeyNotFound) {
		t.Errorf("err = %v: it would be reported as a 401 instead of a 503", err)
	}
}
