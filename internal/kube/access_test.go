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

	"github.com/phorge-fr/phorge-api/internal/access"
)

func newAccessClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		membershipsGVR: "MembershipList", apiKeys: "ApiKeyList",
	})
}

func TestMembershipsAreNamedAfterTheirSubject(t *testing.T) {
	client := newAccessClient()
	s := NewMemberships(Static(client))
	bob := access.Subject{Kind: "User", Name: "bob@example.org"}

	got, err := s.Create(t.Context(), "demo", access.CreateMembership{Subject: bob, Role: access.Operator, CreatedBy: "user:a@b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != access.MembershipName(bob) || got.Role != access.Operator || got.Subject != bob || got.CreatedBy != "user:a@b" {
		t.Errorf("unexpected membership: %+v", got)
	}
	obj, err := client.Resource(membershipsGVR).Namespace("t-demo").Get(t.Context(), got.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetKind() != "Membership" {
		t.Errorf("kind %s", obj.GetKind())
	}

	// A second one for the same user is refused, whatever the role: that is the point of the name.
	if _, err := s.Create(t.Context(), "demo", access.CreateMembership{Subject: bob, Role: access.Admin}); !errors.Is(err, access.ErrExists) {
		t.Errorf("a second membership of the same user: %v", err)
	}
	// Another tenant has its own.
	if _, err := s.Create(t.Context(), "other", access.CreateMembership{Subject: bob, Role: access.Viewer}); err != nil {
		t.Errorf("another tenant: %v", err)
	}
	if list, err := s.List(t.Context(), "demo"); err != nil || len(list) != 1 {
		t.Errorf("list: %v, %v", list, err)
	}
	if err := s.Delete(t.Context(), "demo", got.Name); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), "demo", got.Name); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("a second delete: %v", err)
	}
}

func TestApiKeysStoreTheHashNeverTheKey(t *testing.T) {
	client := newAccessClient()
	s := NewApiKeys(Static(client))
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)

	got, err := s.Create(t.Context(), "demo", access.KeyRecord{
		CreateKey: access.CreateKey{Name: "ci", DisplayName: "CI", Scopes: []string{"tenant:read", "images:write"}, ExpiresAt: &expires},
		Hash:      "abcd", CreatedBy: "user:a@b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ci" || got.DisplayName != "CI" || len(got.Scopes) != 2 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) || got.CreatedBy != "user:a@b" {
		t.Errorf("unexpected key: %+v", got)
	}
	obj, _ := client.Resource(apiKeys).Namespace("t-demo").Get(t.Context(), "ci", metav1.GetOptions{})
	if hash, _, _ := unstructured.NestedString(obj.Object, "spec", "hash"); hash != "abcd" {
		t.Errorf("hash = %q", hash)
	}
	if exp, _, _ := unstructured.NestedString(obj.Object, "spec", "expiresAt"); exp != "2027-01-02T03:04:05Z" {
		t.Errorf("expiresAt = %q: RFC 3339 in UTC", exp)
	}
}

func TestApiKeyPatchAndStatus(t *testing.T) {
	s := NewApiKeys(Static(newAccessClient()))
	if _, err := s.Create(t.Context(), "demo", access.KeyRecord{CreateKey: access.CreateKey{Name: "ci", Scopes: []string{"tenant:read", "images:write"}}, Hash: "ab"}); err != nil {
		t.Fatal(err)
	}
	scopes := []string{"tenant:read"}
	got, err := s.Patch(t.Context(), "demo", "ci", access.PatchKey{Scopes: &scopes})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "tenant:read" {
		t.Errorf("scopes after the patch: %v", got.Scopes)
	}
	if _, err := s.Patch(t.Context(), "demo", "ghost", access.PatchKey{Scopes: &scopes}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("patch of a missing key: %v", err)
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "x"},
		"spec":     map[string]any{"scopes": []any{"tenant:read"}, "expiresAt": "garbage"},
		"status":   map[string]any{"expired": true, "problems": "p"},
	}}
	k := apiKeyOf(obj)
	if !k.Expired || k.Status.Problems != "p" || k.ExpiresAt != nil {
		t.Errorf("a date that cannot be read is no date, status.expired is read: %+v", k)
	}
}
