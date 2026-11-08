package compute

import (
	"context"
	"regexp"
	"time"

	"github.com/phorge-fr/phorge-api/internal/status"
)

// A Network is an Incus network name: 15 characters at most, the limit of Incus, a letter first. It does not end with a hyphen.
var networkNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,13}[a-z0-9])?$`)

// MaxNetworkDescription is the longest description of a network.
const MaxNetworkDescription = 200

// ValidNetworkName reports whether s can name a Network.
func ValidNetworkName(s string) bool { return networkNameRe.MatchString(s) }

// Network is a Network object as the API presents it: an OVN network of the tenant's project, NATed on the uplink. A tenant has
// none until its user deploys one, and an instance is attached to one of them.
type Network struct {
	Name        string
	Description string
	CreatedAt   time.Time
	CreatedBy   string
	Status      status.Object
}

// CreateNetwork is what creating a network asks for. Its addresses are Incus's choice.
type CreateNetwork struct {
	Name        string `json:"name"`
	Description string `json:"description"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// Validate checks the request before anything is sent to the cluster.
func (r CreateNetwork) Validate() error {
	var p problems
	if !ValidNetworkName(r.Name) {
		p.add("name must be 1 to 15 characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen")
	}
	if len(r.Description) > MaxNetworkDescription {
		p.add("description must be at most %d characters", MaxNetworkDescription)
	}
	return p.err()
}

// NetworkStore reads and writes the networks of a tenant. Delete is in the foreground, like every deletion.
type NetworkStore interface {
	List(ctx context.Context, tenant string) ([]Network, error)
	Get(ctx context.Context, tenant, name string) (Network, error)
	Create(ctx context.Context, tenant string, r CreateNetwork) (Network, error)
	// Delete returns an *InUseError when an instance is still attached to the network.
	Delete(ctx context.Context, tenant, name string) error
}
