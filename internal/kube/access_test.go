package kube

import (
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
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

func newKeysClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		membershipsGVR: "MembershipList", apiKeys: "ApiKeyList", secretsResource: "SecretList",
	})
}

func grantsOf(tenant string, scopes ...string) []access.Grant {
	return []access.Grant{{Tenant: tenant, Scopes: scopes}}
}

func TestApiKeysAreWrittenWithTheirGrantsAndNoSecret(t *testing.T) {
	client := newKeysClient()
	s := NewApiKeys(Static(client))
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)

	got, err := s.Create(t.Context(), access.KeyRecord{
		CreateKey: access.CreateKey{DisplayName: "CI", Grants: grantsOf("demo", "tenants:read", "images:write"), ExpiresAt: &expires},
		ID:        "0123456789abcdef", Owner: "alice@example.org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "0123456789abcdef" || got.Owner != "alice@example.org" || got.DisplayName != "CI" || len(got.Grants) != 1 ||
		got.Grants[0].Tenant != "demo" || len(got.Grants[0].Scopes) != 2 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Errorf("unexpected key: %+v", got)
	}
	obj, _ := client.Resource(apiKeys).Namespace(KeysNamespace).Get(t.Context(), "0123456789abcdef", metav1.GetOptions{})
	if _, found, _ := unstructured.NestedString(obj.Object, "spec", "hash"); found {
		t.Error("a hash was written: the secret is the token of the service account, nothing is stored here")
	}
	if exp, _, _ := unstructured.NestedString(obj.Object, "spec", "expiresAt"); exp != "2027-01-02T03:04:05Z" {
		t.Errorf("expiresAt = %q: RFC 3339 in UTC", exp)
	}
	if label := obj.GetLabels()[ownerLabel]; label != access.OwnerLabel("alice@example.org") {
		t.Errorf("owner label = %q", label)
	}
}

func TestApiKeysOfAUserOnly(t *testing.T) {
	s := NewApiKeys(Static(newKeysClient()))
	later := time.Now().Add(time.Hour)
	for id, owner := range map[string]string{"00000000000000a1": "alice@example.org", "00000000000000a2": "Alice@Example.org", "00000000000000b1": "bob@example.org"} {
		if _, err := s.Create(t.Context(), access.KeyRecord{CreateKey: access.CreateKey{DisplayName: "k", Grants: grantsOf("demo", "tenants:read"), ExpiresAt: &later}, ID: id, Owner: owner}); err != nil {
			t.Fatal(err)
		}
	}
	// The fake cluster does not filter on labels: the owner field is what decides, whatever the selector let through.
	got, err := s.List(t.Context(), "alice@example.org")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, k := range got {
		ids = append(ids, k.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"00000000000000a1", "00000000000000a2"}) {
		t.Errorf("alice's keys = %v: bob's must not be in it, the case of an email does not matter", ids)
	}
}

func TestApiKeyPatchAndStatus(t *testing.T) {
	s := NewApiKeys(Static(newKeysClient()))
	const id = "0123456789abcdef"
	later := time.Now().Add(time.Hour)
	if _, err := s.Create(t.Context(), access.KeyRecord{CreateKey: access.CreateKey{DisplayName: "k", Grants: grantsOf("demo", "tenants:read"), ExpiresAt: &later}, ID: id, Owner: "a@b.fr"}); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	got, err := s.Patch(t.Context(), id, access.PatchKey{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "renamed" || len(got.Grants) != 1 {
		t.Errorf("after the patch: %+v: only the name changes", got)
	}
	if _, err := s.Patch(t.Context(), "ffffffffffffffff", access.PatchKey{DisplayName: &name}); !errors.Is(err, access.ErrNotFound) {
		t.Errorf("patch of a missing key: %v", err)
	}

	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "x"},
		"spec":     map[string]any{"grants": []any{map[string]any{"tenant": "demo", "scopes": []any{"tenants:read"}}, "garbage"}, "expiresAt": "garbage"},
		"status":   map[string]any{"expired": true, "problems": "p", "identity": "apikey-x"},
	}}
	k := apiKeyOf(obj)
	if !k.Expired || k.Status.Problems != "p" || k.ExpiresAt != nil || k.Identity != "apikey-x" || len(k.Grants) != 1 {
		t.Errorf("a date that cannot be read is no date, a grant that is not one is skipped, status is read: %+v", k)
	}
}

func secretOf(name string, data map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"namespace": KeysNamespace, "name": name},
		"data":     data,
	}}
}

func TestTakeKeyReadsTheSecretOnceAndDeletesIt(t *testing.T) {
	client := newKeysClient()
	s := NewApiKeys(Static(client))
	secrets := client.Resource(secretsResource).Namespace(KeysNamespace)
	// A Secret holds its values in base64.
	encoded := base64.StdEncoding.EncodeToString([]byte("key-of-the-account"))
	if _, err := secrets.Create(t.Context(), secretOf("0123456789abcdef-token", map[string]any{"token": encoded}), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	key, err := s.TakeKey(t.Context(), "0123456789abcdef")
	if err != nil || key != "key-of-the-account" {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	if _, err := secrets.Get(t.Context(), "0123456789abcdef-token", metav1.GetOptions{}); err == nil {
		t.Error("the Secret is still there: the key could be read again")
	}
	if _, err := s.TakeKey(t.Context(), "0123456789abcdef"); !errors.Is(err, access.ErrKeyNotIssued) {
		t.Errorf("the second read: %v, want ErrKeyNotIssued", err)
	}
}

func TestTakeKeyWaitsForAKeyThatIsNotThereYet(t *testing.T) {
	client := newKeysClient()
	s := NewApiKeys(Static(client))
	if _, err := s.TakeKey(t.Context(), "0123456789abcdef"); !errors.Is(err, access.ErrKeyNotIssued) {
		t.Errorf("no Secret: %v", err)
	}
	// provider-http recreates the Secret empty once the key was taken: that is "taken", and the Secret stays.
	secrets := client.Resource(secretsResource).Namespace(KeysNamespace)
	for name, data := range map[string]map[string]any{"empty-token": {}, "blank-token": {"token": ""}, "garbage-token": {"token": "%%%"}} {
		if _, err := secrets.Create(t.Context(), secretOf(name, data), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		id := name[:len(name)-len("-token")]
		if _, err := s.TakeKey(t.Context(), id); !errors.Is(err, access.ErrKeyNotIssued) {
			t.Errorf("%s: %v, want ErrKeyNotIssued", name, err)
		}
		if _, err := secrets.Get(t.Context(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("%s: a Secret that holds no key was deleted: %v", name, err)
		}
	}
}

func TestTakeKeyOnAnUnreachableClusterIsNotANotIssuedKey(t *testing.T) {
	boom := errors.New("no kubeconfig")
	s := NewApiKeys(&Connection{connect: func() (dynamic.Interface, error) { return nil, boom }})
	if _, err := s.TakeKey(t.Context(), "0123456789abcdef"); !errors.Is(err, boom) || errors.Is(err, access.ErrKeyNotIssued) {
		t.Errorf("err = %v: waiting would hide a cluster that is down", err)
	}
}
