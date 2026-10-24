// Package kube reads the phorge.fr objects from the Kubernetes API of the control cluster.
package kube

import (
	"context"
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/phorge-fr/phorge-api/internal/auth"
)

var apiKeys = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "apikeys"}

// NamespaceOf is the namespace of a tenant: t-<name>.
func NamespaceOf(tenant string) string { return "t-" + tenant }

// NewClient returns a client for the cluster: the service account of the pod when there is one, otherwise the kubeconfig
// (KUBECONFIG or ~/.kube/config), on kubeContext if it is not empty.
func NewClient(kubeContext string) (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes configuration: %w", err)
	}
	cfg.UserAgent = "phorge-api"
	return dynamic.NewForConfig(cfg)
}

// Keys reads the ApiKey objects. It implements auth.KeyStore.
type Keys struct {
	connect func() (dynamic.Interface, error)

	mu     sync.Mutex
	client dynamic.Interface
}

// NewKeys returns a store that reads the keys through client.
func NewKeys(client dynamic.Interface) *Keys {
	return &Keys{connect: func() (dynamic.Interface, error) { return client, nil }}
}

// NewLazyKeys returns a store that builds its client (see NewClient) on the first key it reads, and again after a
// failure. The API starts and answers its probes without a cluster, as in the CI of the image; a key is then an
// "unavailable", not a crash.
func NewLazyKeys(kubeContext string) *Keys {
	return &Keys{connect: func() (dynamic.Interface, error) { return NewClient(kubeContext) }}
}

func (k *Keys) dynamic() (dynamic.Interface, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.client == nil {
		c, err := k.connect()
		if err != nil {
			return nil, err
		}
		k.client = c
	}
	return k.client, nil
}

// GetKey gets the ApiKey <name> of the namespace t-<tenant>, with one get. tenant and name must already be checked
// (auth.ParseKey does).
func (k *Keys) GetKey(ctx context.Context, tenant, name string) (auth.KeyRecord, error) {
	client, err := k.dynamic()
	if err != nil {
		return auth.KeyRecord{}, err
	}
	obj, err := client.Resource(apiKeys).Namespace(NamespaceOf(tenant)).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return auth.KeyRecord{}, auth.ErrKeyNotFound
	}
	if err != nil {
		return auth.KeyRecord{}, err
	}
	return recordOf(obj), nil
}

func recordOf(obj *unstructured.Unstructured) auth.KeyRecord {
	// A field of the wrong type reads as empty, which fails closed: an empty hash matches nothing.
	hash, _, _ := unstructured.NestedString(obj.Object, "spec", "hash")
	scopes, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "scopes")
	expiresAt, _, _ := unstructured.NestedString(obj.Object, "spec", "expiresAt")
	createdBy, _, _ := unstructured.NestedString(obj.Object, "spec", "createdBy")
	expired, _, _ := unstructured.NestedBool(obj.Object, "status", "expired")
	return auth.KeyRecord{
		Hash:      hash,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
		Expired:   expired,
		Deleting:  obj.GetDeletionTimestamp() != nil,
		CreatedBy: createdBy,
	}
}
