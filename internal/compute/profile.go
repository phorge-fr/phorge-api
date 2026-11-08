package compute

import "regexp"

// A profile name is an Incus profile name: letters, digits, dots, underscores and hyphens, starting with a letter or a digit.
// It is the one of the Instance kind's XRD.
var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidProfileName reports whether s can name a profile.
func ValidProfileName(s string) bool { return profileNameRe.MatchString(s) }

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
