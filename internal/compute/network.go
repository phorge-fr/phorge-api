package compute

import (
	"context"
	"fmt"
	"net/netip"
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
	// IPv4 is the range of the network as a CIDR ("10.77.1.0/24") when it was asked for. Empty when Incus chose it: nothing reports that
	// choice back.
	IPv4      string
	CreatedAt time.Time
	CreatedBy string
	Status    status.Object
}

// CreateNetwork is what creating a network asks for. Its addresses are Incus's choice.
type CreateNetwork struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// IPv4 is the range the user asks for, as a CIDR of the network ("10.77.1.0/24"). Empty: Incus chooses.
	IPv4 string `json:"ipv4"`

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
	if r.IPv4 != "" {
		if _, err := ParseRange(r.IPv4); err != nil {
			p.add("ipv4 %s", err.Error())
		}
	}
	return p.err()
}

// The prefixes a network may have: a /16 is the biggest range a tenant gets, a /29 the smallest that holds a gateway and a few VMs.
const (
	MinNetworkPrefix = 16
	MaxNetworkPrefix = 29
)

// ParseRange reads the range a user asks for: the CIDR of a network ("10.77.1.0/24"), in a private range, with a prefix of 16 to 29, and
// the address of the network itself, not of a host in it. The error says what to change.
func ParseRange(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("must be an IPv4 range such as 10.77.1.0/24")
	}
	switch {
	case p.Bits() < MinNetworkPrefix || p.Bits() > MaxNetworkPrefix:
		return netip.Prefix{}, fmt.Errorf("must have a prefix from /%d to /%d", MinNetworkPrefix, MaxNetworkPrefix)
	case p.Masked() != p:
		return netip.Prefix{}, fmt.Errorf("must be the address of the network, %s", p.Masked())
	case !p.Addr().IsPrivate():
		return netip.Prefix{}, fmt.Errorf("must be in a private range: 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16")
	}
	return p, nil
}

// Reserved are the ranges the router, the clusters and the compute nodes use: a tenant network on one of them would shadow a machine of the
// platform for its VMs. They are the ranges of the Network repository and the cluster setups.
var Reserved = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("10.1.0.0/24"), netip.MustParsePrefix("10.2.0.0/24"),
	netip.MustParsePrefix("10.3.0.0/24"), netip.MustParsePrefix("10.4.0.0/24"), netip.MustParsePrefix("10.5.0.0/24"),
	netip.MustParsePrefix("10.6.0.0/24"), netip.MustParsePrefix("10.10.0.0/24"), netip.MustParsePrefix("10.11.0.0/24"),
	netip.MustParsePrefix("10.12.0.0/24"),
	// The pods and the services of the Kubernetes clusters.
	netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("10.96.0.0/12"),
	netip.MustParsePrefix("172.17.0.0/24"), netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("192.168.2.0/24"),
	netip.MustParsePrefix("192.168.88.0/24"),
}

// ReservedOverlap returns the reserved range that overlaps p, if there is one.
func ReservedOverlap(p netip.Prefix) (netip.Prefix, bool) {
	for _, r := range Reserved {
		if r.Overlaps(p) {
			return r, true
		}
	}
	return netip.Prefix{}, false
}

// Gateway is the address of the gateway Incus takes for a range: the first host of the network, with the prefix ("10.77.1.1/24").
func Gateway(p netip.Prefix) string {
	return netip.PrefixFrom(p.Addr().Next(), p.Bits()).String()
}

// RangeOfGateway is the other way: the range of a gateway address as Incus takes it ("10.77.1.1/24" is "10.77.1.0/24").
func RangeOfGateway(s string) (string, bool) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return "", false
	}
	return p.Masked().String(), true
}

// NetworkStore reads and writes the networks of a tenant. Delete is in the foreground, like every deletion.
type NetworkStore interface {
	// ListAll returns the networks of every tenant, to keep their ranges apart: Incus does not refuse two networks on the same range.
	ListAll(ctx context.Context) ([]Network, error)
	List(ctx context.Context, tenant string) ([]Network, error)
	Get(ctx context.Context, tenant, name string) (Network, error)
	Create(ctx context.Context, tenant string, r CreateNetwork) (Network, error)
	// Delete returns an *InUseError when an instance is still attached to the network.
	Delete(ctx context.Context, tenant, name string) error
}
