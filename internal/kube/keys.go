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

// Connection is the client of the cluster. It is built on first use, and again after a failure, so that the API starts and
// answers its probes without a cluster (as in the CI of the image): what needs the cluster is then an "unavailable",
// not a crash.
type Connection struct {
	connect func() (dynamic.Interface, error)

	mu     sync.Mutex
	client dynamic.Interface
}

// NewConnection returns a connection that builds its client with NewClient(kubeContext) when it is first needed.
func NewConnection(kubeContext string) *Connection {
	return &Connection{connect: func() (dynamic.Interface, error) { return NewClient(kubeContext) }}
}

// Static returns a connection to a client that already exists (tests).
func Static(client dynamic.Interface) *Connection {
	return &Connection{connect: func() (dynamic.Interface, error) { return client, nil }}
}

// Dynamic returns the client, building it if needed.
func (c *Connection) Dynamic() (dynamic.Interface, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		client, err := c.connect()
		if err != nil {
			return nil, err
		}
		c.client = client
	}
	return c.client, nil
}

// Keys reads the ApiKey objects. It implements auth.KeyStore.
type Keys struct {
	conn *Connection
}

// NewKeys returns a store that reads the keys through conn.
func NewKeys(conn *Connection) *Keys { return &Keys{conn: conn} }

// GetKey gets the ApiKey <name> of the namespace t-<tenant>, with one get. tenant and name must already be checked
// (auth.ParseKey does).
func (k *Keys) GetKey(ctx context.Context, tenant, name string) (auth.KeyRecord, error) {
	client, err := k.conn.Dynamic()
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
