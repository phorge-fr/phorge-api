// Package kube reads the phorge.fr objects from the Kubernetes API of the control cluster.
package kube

import (
	"errors"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var apiKeys = schema.GroupVersionResource{Group: "phorge.fr", Version: "v1alpha1", Resource: "apikeys"}

// secretsResource is where the key of an ApiKey waits to be read, see ApiKeys.TakeKey.
var secretsResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// NamespaceOf is the namespace of a tenant: t-<name>.
func NamespaceOf(tenant string) string { return "t-" + tenant }

// KeysNamespace is where the ApiKey objects live: one namespace for all of them, a key belongs to a user, not to a tenant.
const KeysNamespace = "phorge-keys"

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
