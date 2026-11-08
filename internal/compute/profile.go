package compute

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/phorge-fr/phorge-api/internal/status"
)

// A profile name is an Incus profile name: letters, digits, dots, underscores and hyphens, starting with a letter or a digit.
// It is the one of the Instance kind's XRD.
var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidProfileName reports whether s can name a profile, to give to an instance. The name of a profile that is made is stricter,
// see ValidNewProfileName.
func ValidProfileName(s string) bool { return profileNameRe.MatchString(s) }

// newProfileNameRe is the name of a profile a user makes: 1 to 32 characters, lowercase letters, digits and hyphens, starting with a
// letter and not ending with a hyphen. It is the one of the Profile XRD.
var newProfileNameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidNewProfileName reports whether s can name a profile that is made.
func ValidNewProfileName(s string) bool { return newProfileNameRe.MatchString(s) }

// Taken says whether a name is one a tenant already has: the three profiles it is created with, and the project's own empty one.
func Taken(name string) bool {
	if name == "default" {
		return true
	}
	_, ok := ProfileNamed(name)
	return ok
}

// MaxProfileDescription is the longest description of a profile.
const MaxProfileDescription = 200

// Profile is a size an instance can take: CPU, memory and a root disk. A tenant has three of them, `s`, `m` and `l`, which its
// project is created with (phorge-fr/Hangar, composition-tenant.yml); a user's own profiles come with phorge-fr/phorge-api#73.
type Profile struct {
	Name        string
	Description string
	CPU         int
	MemoryMiB   int
	DiskGiB     int
	// Builtin is a profile the tenant is created with, which the user cannot change or delete.
	Builtin bool
	// CreatedAt, CreatedBy and Status are those of a profile a user made. A builtin profile has none: it is made with the tenant.
	CreatedAt time.Time
	CreatedBy string
	Status    status.Object
}

// CreateProfile is what making a profile asks for.
type CreateProfile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CPU         *int   `json:"cpu"`
	MemoryMiB   *int   `json:"memoryMiB"`
	DiskGiB     *int   `json:"diskGiB"`

	// CreatedBy is the subject of the caller, recorded on the object. Not part of the request body.
	CreatedBy string `json:"-"`
}

// Validate checks the request before anything is sent to the cluster.
func (r CreateProfile) Validate() error {
	var p problems
	switch {
	case !ValidNewProfileName(r.Name):
		p.add("name must be 1 to 32 characters: lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen")
	case Taken(r.Name):
		p.add("name %s is taken: s, m, l and default are the profiles every tenant has", r.Name)
	}
	if len(r.Description) > MaxProfileDescription {
		p.add("description must be at most %d characters", MaxProfileDescription)
	}
	for _, f := range []struct {
		name   string
		v      *int
		lo, hi int
	}{{"cpu", r.CPU, MinCPU, MaxCPU}, {"memoryMiB", r.MemoryMiB, MinMemory, MaxMemory}, {"diskGiB", r.DiskGiB, MinDisk, MaxDisk}} {
		if f.v == nil {
			p.add("%s is required: a profile is a size, give cpu, memoryMiB and diskGiB", f.name)
			continue
		}
		checkBounds(&p, f.name, f.v, f.lo, f.hi)
	}
	return p.err()
}

// ProfileStore reads and writes the profiles a user made in a tenant. The three every tenant is created with are not objects of
// the API: they are BuiltinProfiles. Delete is in the foreground, like every deletion.
type ProfileStore interface {
	List(ctx context.Context, tenant string) ([]Profile, error)
	Get(ctx context.Context, tenant, name string) (Profile, error)
	Create(ctx context.Context, tenant string, r CreateProfile) (Profile, error)
	// Delete returns an *InUseError when an instance still has the profile.
	Delete(ctx context.Context, tenant, name string) error
}

// All returns the profiles of a tenant: the three it is created with, then the ones the user made, by name.
func All(user []Profile) []Profile {
	out := slices.Clone(BuiltinProfiles)
	made := slices.Clone(user)
	slices.SortFunc(made, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return append(out, made...)
}

// Find returns the profile of that name among the profiles of a tenant, the user's included.
func Find(user []Profile, name string) (Profile, bool) {
	if p, ok := ProfileNamed(name); ok {
		return p, true
	}
	for _, p := range user {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// BuiltinProfiles are the profiles every tenant starts with, in the order of their size. They are written by the Tenant
// composition: the two lists are the same.
var BuiltinProfiles = []Profile{
	{Name: "s", Description: "1 vCPU, 2 GiB RAM, 64 GiB disk", CPU: 1, MemoryMiB: 2048, DiskGiB: 64, Builtin: true},
	{Name: "m", Description: "2 vCPU, 4 GiB RAM, 128 GiB disk", CPU: 2, MemoryMiB: 4096, DiskGiB: 128, Builtin: true},
	{Name: "l", Description: "4 vCPU, 8 GiB RAM, 256 GiB disk", CPU: 4, MemoryMiB: 8192, DiskGiB: 256, Builtin: true},
}

// ProfileNamed returns the profile of that name among the profiles of a tenant.
func ProfileNamed(name string) (Profile, bool) {
	for _, p := range BuiltinProfiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Limit is the bounds of a value, and the value it takes when a request leaves it out.
type Limit struct {
	Min     int `json:"min"`
	Max     int `json:"max"`
	Default int `json:"default"`
}

// Limits are what a client may need to know before it asks: the bounds of an instance, which are the ones of the XRD.
type Limits struct {
	CPU       Limit
	MemoryMiB Limit
	DiskGiB   Limit
	// MaxSSHKeys is how many public keys an instance takes.
	MaxSSHKeys int
	// MaxNetworkName and MaxNetworkDescription are the longest name and description of a network.
	MaxNetworkName, MaxNetworkDescription int
}

// CurrentLimits returns the limits as the API enforces them.
func CurrentLimits() Limits {
	return Limits{
		CPU:        Limit{Min: MinCPU, Max: MaxCPU, Default: DefaultCPU},
		MemoryMiB:  Limit{Min: MinMemory, Max: MaxMemory, Default: DefaultMemoryMiB},
		DiskGiB:    Limit{Min: MinDisk, Max: MaxDisk, Default: DefaultDiskGiB},
		MaxSSHKeys: MaxSSHKeys, MaxNetworkName: 15, MaxNetworkDescription: MaxNetworkDescription,
	}
}
